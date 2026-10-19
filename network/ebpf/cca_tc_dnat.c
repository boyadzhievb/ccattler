/* cca_tc_dnat.c — tc ingress DNAT for container-to-VIP bridge traffic.
 * Attached to the cca-net bridge. For new connections destined to a VIP,
 * creates a conntrack DNAT entry via kfuncs so the kernel's nf_nat applies
 * the actual packet rewriting and handles return-path reverse NAT. For
 * established connections, passes through (kernel conntrack handles them).
 * Compiled to /usr/lib/cca/cca_tc_dnat.o via build-ebpf.sh. */

#include <linux/bpf.h>
#include <linux/pkt_cls.h>
#include <linux/if_ether.h>
#include <linux/ip.h>
#include <linux/tcp.h>
#include <linux/udp.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_endian.h>
#include "cca_maps.h"
#include "cca_helpers.h"
#include "cca_ct_helpers.h"

/* extract_l4_ports reads source and destination ports from a TCP or UDP
 * header at the given offset. Returns 0 on success, -1 if the header
 * extends past data_end (bounds check for the BPF verifier). */
static __always_inline int extract_l4_ports(
	void *transport_header,
	void *data_end,
	__u8 ip_protocol,
	__u16 *source_port,
	__u16 *destination_port)
{
	if (ip_protocol == IPPROTO_TCP) {
		struct tcphdr *tcp_header = transport_header;

		if ((void *)(tcp_header + 1) > data_end)
			return -1;
		*source_port = tcp_header->source;
		*destination_port = tcp_header->dest;
		return 0;
	}

	if (ip_protocol == IPPROTO_UDP) {
		struct udphdr *udp_header = transport_header;

		if ((void *)(udp_header + 1) > data_end)
			return -1;
		*source_port = udp_header->source;
		*destination_port = udp_header->dest;
		return 0;
	}

	return -1;
}

/* cca_tc_dnat is the tc ingress classifier for the container bridge.
 * It intercepts new connections destined to VIPs, selects a backend,
 * and creates a conntrack DNAT entry so that nf_nat rewrites the packet
 * in PREROUTING. Established connections are passed through. */
SEC("tc")
int cca_tc_dnat(struct __sk_buff *skb_ctx)
{
	void *data_start = (void *)(long)skb_ctx->data;
	void *data_end = (void *)(long)skb_ctx->data_end;

	/* Parse Ethernet header — only handle IPv4. */
	struct ethhdr *ethernet_header = data_start;

	if ((void *)(ethernet_header + 1) > data_end)
		return TC_ACT_OK;
	if (ethernet_header->h_proto != bpf_htons(ETH_P_IP))
		return TC_ACT_OK;

	/* Parse IP header. */
	struct iphdr *ip_header = (void *)(ethernet_header + 1);

	if ((void *)(ip_header + 1) > data_end)
		return TC_ACT_OK;

	/* Only handle TCP and UDP. */
	if (ip_header->protocol != IPPROTO_TCP &&
	    ip_header->protocol != IPPROTO_UDP)
		return TC_ACT_OK;

	/* Extract L4 ports. ihl is in 32-bit words. */
	void *transport_header = (void *)ip_header + (ip_header->ihl * 4);
	__u16 source_port = 0;
	__u16 destination_port = 0;

	if (extract_l4_ports(transport_header, data_end,
			     ip_header->protocol,
			     &source_port, &destination_port) < 0)
		return TC_ACT_OK;

	/* Build the 5-tuple for conntrack lookup. All fields are in
	 * network byte order, matching the packet header layout. */
	struct bpf_sock_tuple connection_tuple = {};

	connection_tuple.ipv4.saddr = ip_header->saddr;
	connection_tuple.ipv4.daddr = ip_header->daddr;
	connection_tuple.ipv4.sport = source_port;
	connection_tuple.ipv4.dport = destination_port;

	/* Fast path: check if a conntrack entry already exists for this
	 * flow. Established connections are handled by kernel nf_nat. */
	struct bpf_ct_opts ct_lookup_options = {
		.netns_id = BPF_F_CURRENT_NETNS,
		.l4proto = ip_header->protocol,
	};
	struct nf_conn *existing_entry = bpf_skb_ct_lookup(
		skb_ctx, &connection_tuple,
		sizeof(connection_tuple.ipv4),
		&ct_lookup_options, sizeof(ct_lookup_options));

	if (existing_entry) {
		bpf_ct_release(existing_entry);
		return TC_ACT_OK;
	}

	/* New connection — check if the destination is a service VIP.
	 * Packet ports (__be16 loaded as __u16) match the map's format
	 * because both are big-endian bytes read as a native integer. */
	struct service_key service_lookup_key = {};

	service_lookup_key.vip_addr = ip_header->daddr;
	service_lookup_key.port = destination_port;
	service_lookup_key.protocol = ip_header->protocol;

	struct service_value *matched_service = bpf_map_lookup_elem(
		&cca_services, &service_lookup_key);

	if (!matched_service)
		return TC_ACT_OK;

	/* Select a backend. Use source IP as client identity for
	 * session affinity (source-IP sticky). */
	struct backend_value chosen_backend = {};
	__u32 client_identity = ip_header->saddr;

	if (select_backend(matched_service, client_identity,
			   &chosen_backend) < 0)
		return TC_ACT_OK;

	/* Allocate a new conntrack entry for the original tuple. */
	struct bpf_ct_opts ct_alloc_options = {
		.netns_id = BPF_F_CURRENT_NETNS,
		.l4proto = ip_header->protocol,
	};
	struct nf_conn *new_entry = bpf_skb_ct_alloc(
		skb_ctx, &connection_tuple,
		sizeof(connection_tuple.ipv4),
		&ct_alloc_options, sizeof(ct_alloc_options));

	if (!new_entry)
		return TC_ACT_OK;

	/* Set DNAT target: backend IP and port. bpf_ct_set_nat_info
	 * expects the port in host byte order. Our map stores ports in
	 * the "native load of big-endian" format, so bpf_ntohs converts
	 * to host order. The IP address is __be32 and can be passed
	 * directly to nf_inet_addr.ip which is also __be32. */
	union nf_inet_addr nat_destination_addr = {};

	nat_destination_addr.ip = chosen_backend.backend_addr;

	int backend_port_host_order = bpf_ntohs(chosen_backend.port);

	bpf_ct_set_nat_info(new_entry, &nat_destination_addr,
			    backend_port_host_order, NF_NAT_MANIP_DST);

	/* Commit the entry to the conntrack table. On success, nf_nat
	 * will find this entry in PREROUTING and apply the DNAT.
	 * On failure (duplicate tuple), the entry is freed internally. */
	struct nf_conn *inserted_entry = bpf_ct_insert_entry(new_entry);

	if (inserted_entry)
		bpf_ct_release(inserted_entry);

	return TC_ACT_OK;
}

char _license[] SEC("license") = "GPL";
