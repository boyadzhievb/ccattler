/* cca_maps.h — Shared BPF map definitions and struct types for CCattler
 * socket-level VIP load balancing. All IP addresses and ports are stored
 * in network byte order for direct comparison with socket context values. */
#ifndef CCA_MAPS_H
#define CCA_MAPS_H

#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>

/* CCA_SVC_FLAG_STICKY_SOURCE_IP enables source-IP session affinity. */
#define CCA_SVC_FLAG_STICKY_SOURCE_IP 0x01

/* service_key identifies a service by VIP address, port, and protocol. */
struct service_key {
	__u32 vip_addr; /* Virtual IP address (network byte order) */
	__u16 port;     /* Service port (network byte order) */
	__u8 protocol;  /* IP protocol (IPPROTO_TCP=6, IPPROTO_UDP=17) */
	__u8 pad;       /* Alignment padding to 8 bytes */
};

/* service_value holds service metadata returned from a VIP lookup. */
struct service_value {
	__u32 service_id;               /* Unique numeric service identifier */
	__u16 backend_count;            /* Number of active backend slots (0..N-1) */
	__u16 flags;                    /* Bitmask of CCA_SVC_FLAG_* constants */
	__u32 affinity_timeout_seconds; /* Session affinity TTL (0 = no affinity) */
};

/* backend_key selects a specific backend slot within a service. */
struct backend_key {
	__u32 service_id; /* Service identifier from service_value */
	__u16 slot;       /* Backend slot index (0 to backend_count-1) */
	__u16 pad;        /* Alignment padding to 8 bytes */
};

/* backend_value holds the real endpoint address for one backend slot. */
struct backend_value {
	__u32 backend_addr; /* Backend IP address (network byte order) */
	__u16 port;         /* Backend port (network byte order) */
	__u16 pad;          /* Alignment padding to 8 bytes */
};
/* affinity_key identifies a client-to-service affinity binding. */
struct affinity_key {
	__u32 client_addr; /* Client source IP (network byte order) */
	__u32 service_id;  /* Service identifier */
};

/* affinity_value caches the backend selected for an affinity binding. */
struct affinity_value {
	__u32 backend_addr; /* Cached backend IP (network byte order) */
	__u16 port;         /* Cached backend port (network byte order) */
	__u16 pad;          /* Alignment padding */
	__u64 timestamp;    /* Last-used time from bpf_ktime_get_ns() */
};
/* reverse_nat_key identifies a translated connection for reverse VIP lookup.
 * Keyed by socket cookie + backend to avoid collisions when services share
 * backends (same pattern as Cilium's ipv4_revnat_tuple). */
struct reverse_nat_key {
	__u64 socket_cookie; /* bpf_get_socket_cookie() unique socket ID */
	__u32 backend_addr;  /* Backend IP address (network byte order) */
	__u16 backend_port;  /* Backend port (network byte order) */
	__u16 pad;           /* Alignment padding to 16 bytes */
};
/* reverse_nat_value maps a backend endpoint back to its service VIP. */
struct reverse_nat_value {
	__u32 vip_addr; /* Original VIP address (network byte order) */
	__u16 vip_port; /* Original VIP port (network byte order) */
	__u16 pad;      /* Alignment padding to 8 bytes */
};

/* cca_services maps {VIP, port, protocol} to service metadata. */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 4096);
	__type(key, struct service_key);
	__type(value, struct service_value);
} cca_services SEC(".maps");

/* cca_backends maps {service_id, slot} to a backend endpoint. */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 65536);
	__type(key, struct backend_key);
	__type(value, struct backend_value);
} cca_backends SEC(".maps");

/* cca_affinity caches client-to-backend bindings (LRU eviction). */
struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 131072);
	__type(key, struct affinity_key);
	__type(value, struct affinity_value);
} cca_affinity SEC(".maps");

/* cca_reverse_nat maps backend endpoints back to service VIPs. */
struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 131072);
	__type(key, struct reverse_nat_key);
	__type(value, struct reverse_nat_value);
} cca_reverse_nat SEC(".maps");

#endif /* CCA_MAPS_H */
