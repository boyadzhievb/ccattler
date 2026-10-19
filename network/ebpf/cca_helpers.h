/* cca_helpers.h — Shared helper functions for CCattler BPF programs.
 * Provides backend selection with random slot distribution and optional
 * source-IP session affinity via the cca_affinity LRU map. */

#ifndef CCA_HELPERS_H
#define CCA_HELPERS_H

#include "cca_maps.h"
#include <bpf/bpf_endian.h>

/* NANOSECONDS_PER_SECOND converts affinity_timeout_seconds to nanoseconds. */
#define NANOSECONDS_PER_SECOND 1000000000ULL

/* select_backend picks a backend for the given service. When the sticky
 * source-IP flag is set and a non-expired affinity binding exists, that
 * cached backend is returned. Otherwise a random slot is chosen and the
 * affinity cache is updated. Returns 0 on success, -1 if no backend found. */
static __always_inline int select_backend(
	struct service_value *service_entry,
	__u32 client_address,
	struct backend_value *selected_backend)
{
	struct backend_key backend_lookup_key = {};
	struct backend_value *found_backend;

	backend_lookup_key.service_id = service_entry->service_id;

	/* Check session affinity if the sticky-source-ip flag is set. */
	if (service_entry->flags & CCA_SVC_FLAG_STICKY_SOURCE_IP) {
		struct affinity_key affinity_lookup_key = {
			.client_addr = client_address,
			.service_id = service_entry->service_id,
		};
		struct affinity_value *cached_affinity;

		cached_affinity = bpf_map_lookup_elem(&cca_affinity,
						      &affinity_lookup_key);
		if (cached_affinity) {
			__u64 current_time_ns = bpf_ktime_get_ns();
			__u64 expiry_ns = cached_affinity->timestamp +
				((__u64)service_entry->affinity_timeout_seconds *
				 NANOSECONDS_PER_SECOND);

			if (current_time_ns < expiry_ns) {
				selected_backend->backend_addr =
					cached_affinity->backend_addr;
				selected_backend->port = cached_affinity->port;
				return 0;
			}
		}
	}

	/* No valid affinity binding; select a backend via random slot. */
	if (service_entry->backend_count == 0)
		return -1;

	backend_lookup_key.slot =
		bpf_get_prandom_u32() % service_entry->backend_count;

	found_backend = bpf_map_lookup_elem(&cca_backends,
					    &backend_lookup_key);
	if (!found_backend)
		return -1;

	selected_backend->backend_addr = found_backend->backend_addr;
	selected_backend->port = found_backend->port;

	/* Update the affinity cache if sticky sessions are enabled. */
	if (service_entry->flags & CCA_SVC_FLAG_STICKY_SOURCE_IP) {
		struct affinity_key affinity_update_key = {
			.client_addr = client_address,
			.service_id = service_entry->service_id,
		};
		struct affinity_value new_affinity = {
			.backend_addr = found_backend->backend_addr,
			.port = found_backend->port,
			.timestamp = bpf_ktime_get_ns(),
		};
		bpf_map_update_elem(&cca_affinity, &affinity_update_key,
				    &new_affinity, BPF_ANY);
	}

	return 0;
}

#endif /* CCA_HELPERS_H */
