/* cca_dataplane.c — Unified multi-program BPF data plane for CCattler.
 *
 * All BPF programs compiled into a single ELF object, embedded in the Go
 * binary via go:embed. Eliminates separate /usr/lib/cca/ install step
 * and MapReplacements — all programs share maps from one collection.
 *
 * Programs:
 *   cca_sock4_connect     (cgroup/connect4)    — TCP VIP interception
 *   cca_sock4_sendmsg     (cgroup/sendmsg4)    — UDP VIP interception
 *   cca_sock4_getpeername  (cgroup/getpeername4) — reverse VIP lookup
 *   cca_tc_dnat           (tc)                  — bridge ingress DNAT
 *   cca_tc_snat           (tc)                  — physical NIC egress SNAT
 *
 * Compiled to network/ebpf/cca_dataplane.o via scripts/build-ebpf.sh. */

#include <linux/bpf.h>
#include <linux/pkt_cls.h>
#include <linux/if_ether.h>
#include <linux/in.h>
#include <linux/ip.h>
#include <linux/tcp.h>
#include <linux/udp.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_endian.h>
#include "cca_maps.h"
#include "cca_helpers.h"
#include "cca_ct_helpers.h"

/* ====================================================================
 * Socket LB programs — attached to the root cgroup for host-level VIP
 * interception of connect(), sendmsg(), and getpeername() syscalls.
 * ==================================================================== */

/* cca_sock4_connect intercepts IPv4 connect() calls. If the destination
 * matches a VIP, selects a backend and rewrites the socket destination.
 * Records reverse NAT keyed by socket cookie for getpeername transparency.
 * Note: msg_src_ip4 is not readable in connect4 (kernel restriction), so
 * we use the truncated socket cookie as the affinity client identifier. */
SEC("cgroup/connect4")
int cca_sock4_connect(struct bpf_sock_addr *socket_ctx)
{
	struct service_key service_lookup_key = {};
	struct service_value *matched_service;
	struct backend_value chosen_backend = {};

	service_lookup_key.vip_addr = socket_ctx->user_ip4;
	service_lookup_key.port = (__u16)socket_ctx->user_port;
	service_lookup_key.protocol = socket_ctx->protocol;

	matched_service = bpf_map_lookup_elem(&cca_services,
					      &service_lookup_key);
	if (!matched_service)
		return 1;

	__u32 client_identity = (__u32)bpf_get_socket_cookie(socket_ctx);
	if (select_backend(matched_service, client_identity,
			   &chosen_backend) < 0)
		return 1;

	socket_ctx->user_ip4 = chosen_backend.backend_addr;
	socket_ctx->user_port = (__u32)chosen_backend.port;

	struct reverse_nat_key reverse_key = {
		.socket_cookie = bpf_get_socket_cookie(socket_ctx),
		.backend_addr = chosen_backend.backend_addr,
		.backend_port = chosen_backend.port,
	};
	struct reverse_nat_value reverse_value = {
		.vip_addr = service_lookup_key.vip_addr,
		.vip_port = service_lookup_key.port,
	};
	bpf_map_update_elem(&cca_reverse_nat, &reverse_key,
			    &reverse_value, BPF_ANY);

	return 1;
}

/* cca_sock4_sendmsg intercepts IPv4 UDP sendto/sendmsg calls. Same VIP
 * translation as connect4 but for connectionless datagrams. */
SEC("cgroup/sendmsg4")
int cca_sock4_sendmsg(struct bpf_sock_addr *socket_ctx)
{
	struct service_key service_lookup_key = {};
	struct service_value *matched_service;
	struct backend_value chosen_backend = {};

	service_lookup_key.vip_addr = socket_ctx->user_ip4;
	service_lookup_key.port = (__u16)socket_ctx->user_port;
	service_lookup_key.protocol = socket_ctx->protocol;

	matched_service = bpf_map_lookup_elem(&cca_services,
					      &service_lookup_key);
	if (!matched_service)
		return 1;

	if (select_backend(matched_service, socket_ctx->msg_src_ip4,
			   &chosen_backend) < 0)
		return 1;

	socket_ctx->user_ip4 = chosen_backend.backend_addr;
	socket_ctx->user_port = (__u32)chosen_backend.port;

	struct reverse_nat_key reverse_key = {
		.socket_cookie = bpf_get_socket_cookie(socket_ctx),
		.backend_addr = chosen_backend.backend_addr,
		.backend_port = chosen_backend.port,
	};
	struct reverse_nat_value reverse_value = {
		.vip_addr = service_lookup_key.vip_addr,
		.vip_port = service_lookup_key.port,
	};
	bpf_map_update_elem(&cca_reverse_nat, &reverse_key,
			    &reverse_value, BPF_ANY);

	return 1;
}

/* cca_sock4_getpeername intercepts getpeername() and restores the original
 * VIP address using the socket cookie for per-connection reverse NAT. */
SEC("cgroup/getpeername4")
int cca_sock4_getpeername(struct bpf_sock_addr *socket_ctx)
{
	struct reverse_nat_key reverse_lookup_key = {
		.socket_cookie = bpf_get_socket_cookie(socket_ctx),
		.backend_addr = socket_ctx->user_ip4,
		.backend_port = (__u16)socket_ctx->user_port,
	};

	struct reverse_nat_value *original_vip;
	original_vip = bpf_map_lookup_elem(&cca_reverse_nat,
					   &reverse_lookup_key);
	if (!original_vip)
		return 1;

	socket_ctx->user_ip4 = original_vip->vip_addr;
	socket_ctx->user_port = (__u32)original_vip->vip_port;

	return 1;
}

/* ====================================================================
 * TC DNAT program — attached to the container bridge ingress. Creates
 * conntrack DNAT entries for new container-to-VIP connections so the
 * kernel's nf_nat applies packet rewriting and return-path reverse NAT.
 * ==================================================================== */

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

SEC("tc")
int cca_tc_dnat(struct __sk_buff *skb_ctx)
{
	void *data_start = (void *)(long)skb_ctx->data;
	void *data_end = (void *)(long)skb_ctx->data_end;

	struct ethhdr *ethernet_header = data_start;

	if ((void *)(ethernet_header + 1) > data_end)
		return TC_ACT_OK;
	if (ethernet_header->h_proto != bpf_htons(ETH_P_IP))
		return TC_ACT_OK;

	struct iphdr *ip_header = (void *)(ethernet_header + 1);

	if ((void *)(ip_header + 1) > data_end)
		return TC_ACT_OK;

	if (ip_header->protocol != IPPROTO_TCP &&
	    ip_header->protocol != IPPROTO_UDP)
		return TC_ACT_OK;

	void *transport_header = (void *)ip_header + (ip_header->ihl * 4);
	__u16 source_port = 0;
	__u16 destination_port = 0;

	if (extract_l4_ports(transport_header, data_end,
			     ip_header->protocol,
			     &source_port, &destination_port) < 0)
		return TC_ACT_OK;

	struct bpf_sock_tuple connection_tuple = {};

	connection_tuple.ipv4.saddr = ip_header->saddr;
	connection_tuple.ipv4.daddr = ip_header->daddr;
	connection_tuple.ipv4.sport = source_port;
	connection_tuple.ipv4.dport = destination_port;

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

	struct service_key service_lookup_key = {};

	service_lookup_key.vip_addr = ip_header->daddr;
	service_lookup_key.port = destination_port;
	service_lookup_key.protocol = ip_header->protocol;

	struct service_value *matched_service = bpf_map_lookup_elem(
		&cca_services, &service_lookup_key);

	if (!matched_service)
		return TC_ACT_OK;

	struct backend_value chosen_backend = {};
	__u32 client_identity = ip_header->saddr;

	if (select_backend(matched_service, client_identity,
			   &chosen_backend) < 0)
		return TC_ACT_OK;

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

	union nf_inet_addr nat_destination_addr = {};

	nat_destination_addr.ip = chosen_backend.backend_addr;

	int backend_port_host_order = bpf_ntohs(chosen_backend.port);

	bpf_ct_set_nat_info(new_entry, &nat_destination_addr,
			    backend_port_host_order, NF_NAT_MANIP_DST);

	struct nf_conn *inserted_entry = bpf_ct_insert_entry(new_entry);

	if (inserted_entry)
		bpf_ct_release(inserted_entry);

	return TC_ACT_OK;
}

/* ====================================================================
 * TC SNAT program — attached to the physical NIC tc egress. Rewrites
 * overlay source IPs to the node's advertise address, replacing the
 * nftables POSTROUTING masquerade rule.
 * ==================================================================== */

/* is_overlay_source returns true if the given source IP falls within the
 * overlay CIDR configured in the cca_config map. */
static __always_inline int is_overlay_source(__u32 source_address)
{
	__u32 prefix_index = CCA_CONFIG_OVERLAY_PREFIX;
	__u32 mask_index = CCA_CONFIG_OVERLAY_MASK;
	__u32 *overlay_prefix = bpf_map_lookup_elem(&cca_config, &prefix_index);
	__u32 *overlay_mask = bpf_map_lookup_elem(&cca_config, &mask_index);

	if (!overlay_prefix || !overlay_mask)
		return 0;

	return (source_address & *overlay_mask) == *overlay_prefix;
}

/* get_node_ip returns the node's advertise address from the config map.
 * Returns 0 if the config entry is missing. */
static __always_inline __u32 get_node_ip(void)
{
	__u32 node_ip_index = CCA_CONFIG_NODE_IP;
	__u32 *node_ip = bpf_map_lookup_elem(&cca_config, &node_ip_index);

	if (!node_ip)
		return 0;

	return *node_ip;
}

SEC("tc")
int cca_tc_snat(struct __sk_buff *skb_ctx)
{
	void *data_start = (void *)(long)skb_ctx->data;
	void *data_end = (void *)(long)skb_ctx->data_end;

	struct ethhdr *ethernet_header = data_start;

	if ((void *)(ethernet_header + 1) > data_end)
		return TC_ACT_OK;
	if (ethernet_header->h_proto != bpf_htons(ETH_P_IP))
		return TC_ACT_OK;

	struct iphdr *ip_header = (void *)(ethernet_header + 1);

	if ((void *)(ip_header + 1) > data_end)
		return TC_ACT_OK;

	if (!is_overlay_source(ip_header->saddr))
		return TC_ACT_OK;

	__u32 node_address = get_node_ip();

	if (node_address == 0)
		return TC_ACT_OK;

	if (ip_header->saddr == node_address)
		return TC_ACT_OK;

	__u32 original_source = ip_header->saddr;

	bpf_l3_csum_replace(skb_ctx,
		ETH_HLEN + offsetof(struct iphdr, check),
		original_source, node_address, sizeof(__u32));

	bpf_skb_store_bytes(skb_ctx,
		ETH_HLEN + offsetof(struct iphdr, saddr),
		&node_address, sizeof(__u32), 0);

	data_start = (void *)(long)skb_ctx->data;
	data_end = (void *)(long)skb_ctx->data_end;
	ethernet_header = data_start;
	if ((void *)(ethernet_header + 1) > data_end)
		return TC_ACT_OK;
	ip_header = (void *)(ethernet_header + 1);
	if ((void *)(ip_header + 1) > data_end)
		return TC_ACT_OK;

	__u8 ip_protocol = ip_header->protocol;
	__u32 l4_offset = ETH_HLEN + (ip_header->ihl * 4);

	if (ip_protocol == IPPROTO_TCP) {
		bpf_l4_csum_replace(skb_ctx,
			l4_offset + offsetof(struct tcphdr, check),
			original_source, node_address,
			BPF_F_PSEUDO_HDR | sizeof(__u32));
	} else if (ip_protocol == IPPROTO_UDP) {
		bpf_l4_csum_replace(skb_ctx,
			l4_offset + offsetof(struct udphdr, check),
			original_source, node_address,
			BPF_F_PSEUDO_HDR | sizeof(__u32));
	}

	return TC_ACT_OK;
}

char _license[] SEC("license") = "GPL";
