/* cca_ct_helpers.h — Conntrack kfunc declarations for tc BPF programs.
 * Provides forward declarations for kernel conntrack kfuncs (6.1+) and
 * the minimal type definitions they require. These types must match the
 * kernel's BTF exactly for kfunc resolution at load time. */
#ifndef CCA_CT_HELPERS_H
#define CCA_CT_HELPERS_H

#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>

/* nf_conn is a conntrack entry handle. Defined as a minimal struct (not a
 * forward declaration) so clang emits a BTF Struct kind that matches the
 * kernel's BTF. BPF programs only pass it by pointer to kfuncs — the
 * internal fields are never accessed. */
struct nf_conn {
	unsigned int __opaque;
};

/* nf_inet_addr holds an IP address for NAT target specification.
 * Only the IPv4 member is used by CCattler. */
union nf_inet_addr {
	__u32 all[4]; /* Generic access to all 16 bytes */
	__be32 ip;    /* IPv4 address in network byte order */
};

/* bpf_ct_opts carries options for conntrack kfunc calls. Layout must
 * match the kernel's struct bpf_ct_opts exactly (12 bytes). */
struct bpf_ct_opts {
	__s32 netns_id;    /* Network namespace ID (-1 = current netns) */
	__s32 error;       /* Error code set by kfunc on failure */
	__u8  l4proto;     /* L4 protocol: IPPROTO_TCP (6) or IPPROTO_UDP (17) */
	__u8  dir;         /* Conntrack direction (0 = original, 1 = reply) */
	__u8  reserved[2]; /* Padding to 12 bytes */
};

/* nf_nat_manip_type selects SNAT or DNAT in conntrack NAT setup.
 * NF_NAT_MANIP_DST tells the kernel to rewrite the destination. */
enum nf_nat_manip_type {
	NF_NAT_MANIP_SRC = 0, /* Source NAT (rewrite source IP/port) */
	NF_NAT_MANIP_DST = 1, /* Destination NAT (rewrite dest IP/port) */
};

/* BPF_F_CURRENT_NETNS selects the current network namespace when passed
 * as bpf_ct_opts.netns_id. */
#ifndef BPF_F_CURRENT_NETNS
#define BPF_F_CURRENT_NETNS (-1)
#endif

/* bpf_skb_ct_lookup finds an existing conntrack entry matching the given
 * tuple. Returns a locked reference that must be released with
 * bpf_ct_release(). Returns NULL if no entry found. Kernel 5.18+. */
extern struct nf_conn *bpf_skb_ct_lookup(
	struct __sk_buff *skb_ctx,
	struct bpf_sock_tuple *bpf_tuple,
	__u32 tuple_len,
	struct bpf_ct_opts *opts,
	__u32 opts_len) __ksym;

/* bpf_skb_ct_alloc allocates a new conntrack entry for the given tuple.
 * The entry is not yet committed to the conntrack table — call
 * bpf_ct_insert_entry() to commit. Returns NULL on allocation failure.
 * Kernel 5.18+. */
extern struct nf_conn *bpf_skb_ct_alloc(
	struct __sk_buff *skb_ctx,
	struct bpf_sock_tuple *bpf_tuple,
	__u32 tuple_len,
	struct bpf_ct_opts *opts,
	__u32 opts_len) __ksym;

/* bpf_ct_set_nat_info records NAT translation info on an allocated (not
 * yet inserted) conntrack entry. For DNAT: addr is the backend IP, port
 * is the backend port in host byte order, manip is NF_NAT_MANIP_DST.
 * The kernel's nf_nat module uses this info to rewrite packets when the
 * conntrack entry is found in PREROUTING. Kernel 6.1+. */
extern int bpf_ct_set_nat_info(
	struct nf_conn *nfct,
	union nf_inet_addr *addr,
	int port,
	enum nf_nat_manip_type manip) __ksym;

/* bpf_ct_insert_entry commits an allocated conntrack entry to the hash
 * table. On success returns the entry (caller must bpf_ct_release it).
 * On failure (e.g. duplicate tuple) returns NULL and frees the entry
 * internally — do NOT release a NULL return. Kernel 5.18+. */
extern struct nf_conn *bpf_ct_insert_entry(
	struct nf_conn *nfct) __ksym;

/* bpf_ct_release releases a reference to a conntrack entry obtained from
 * bpf_skb_ct_lookup or bpf_ct_insert_entry. Must be called exactly once
 * per non-NULL return from those functions. Kernel 5.18+. */
extern void bpf_ct_release(
	struct nf_conn *nfct) __ksym;

/* bpf_ct_set_timeout sets a custom timeout in milliseconds on a
 * conntrack entry. Optional — kernel defaults apply if not called. */
extern void bpf_ct_set_timeout(
	struct nf_conn *nfct,
	__u32 timeout_ms) __ksym;

#endif /* CCA_CT_HELPERS_H */
