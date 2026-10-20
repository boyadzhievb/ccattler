/* cca_tc_snat.c — tc egress SNAT for outbound container traffic.
 * Attached to the physical NIC tc egress. For packets with a source IP in the
 * overlay CIDR range, rewrites the source to the node's advertise address and
 * recomputes IP + L4 checksums. This replaces the nftables POSTROUTING
 * masquerade rule, eliminating the last nftables dependency when
 * --dataplane ebpf is active.
 * Compiled to /usr/lib/cca/cca_tc_snat.o via build-ebpf.sh. */

#include <linux/bpf.h>
#include <linux/pkt_cls.h>
#include <linux/if_ether.h>
#include <linux/in.h>
#include <linux/ip.h>
#include <linux/tcp.h>
#include <linux/udp.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_endian.h>

/* cca_config stores node-level configuration values.
 * Index 0: node_ip       — node's advertise address (network byte order)
 * Index 1: reserved
 * Index 2: overlay_prefix — overlay CIDR network address (network byte order)
 * Index 3: overlay_mask   — overlay CIDR netmask (network byte order) */
struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(max_entries, 8);
	__type(key, __u32);
	__type(value, __u32);
} cca_config SEC(".maps");

/* Config map indices. */
#define CCA_CONFIG_NODE_IP        0
#define CCA_CONFIG_OVERLAY_PREFIX 2
#define CCA_CONFIG_OVERLAY_MASK   3

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

/* cca_tc_snat is the tc egress classifier for the physical NIC. It rewrites
 * the source IP of outbound packets originating from the overlay network
 * (container IPs) to the node's advertise address, replacing nftables
 * POSTROUTING masquerade. */
SEC("tc")
int cca_tc_snat(struct __sk_buff *skb_ctx)
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

	/* Only rewrite overlay-sourced packets. */
	if (!is_overlay_source(ip_header->saddr))
		return TC_ACT_OK;

	__u32 node_address = get_node_ip();

	if (node_address == 0)
		return TC_ACT_OK;

	/* Already rewritten (idempotent). */
	if (ip_header->saddr == node_address)
		return TC_ACT_OK;

	__u32 original_source = ip_header->saddr;

	/* Rewrite IP source address and fix the IP header checksum.
	 * bpf_l3_csum_replace computes the incremental checksum diff. */
	bpf_l3_csum_replace(skb_ctx,
		ETH_HLEN + offsetof(struct iphdr, check),
		original_source, node_address, sizeof(__u32));

	bpf_skb_store_bytes(skb_ctx,
		ETH_HLEN + offsetof(struct iphdr, saddr),
		&node_address, sizeof(__u32), 0);

	/* Reparse after store — verifier requires fresh bounds check. */
	data_start = (void *)(long)skb_ctx->data;
	data_end = (void *)(long)skb_ctx->data_end;
	ethernet_header = data_start;
	if ((void *)(ethernet_header + 1) > data_end)
		return TC_ACT_OK;
	ip_header = (void *)(ethernet_header + 1);
	if ((void *)(ip_header + 1) > data_end)
		return TC_ACT_OK;

	/* Fix L4 checksum for TCP and UDP. Both store the checksum at the
	 * same relative offset (bytes 16-17 from the L4 header start). The
	 * L4 pseudo-header includes the source IP, so changing it requires
	 * an incremental checksum update. */
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
