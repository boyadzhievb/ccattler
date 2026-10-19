/* cca_socket_lb.c — CCattler socket-level VIP load balancer.
 * Three cgroup BPF programs (connect4, sendmsg4, getpeername4) share maps
 * in a single ELF. Compiled to /usr/lib/cca/cca_ebpf.o via build-ebpf.sh. */

#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_endian.h>
#include "cca_maps.h"
#include "cca_helpers.h"

/* cca_sock4_connect intercepts IPv4 connect() calls. If the destination
 * matches a VIP, selects a backend and rewrites the socket destination.
 * Records reverse NAT keyed by socket cookie for getpeername transparency. */
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

char _license[] SEC("license") = "GPL";
