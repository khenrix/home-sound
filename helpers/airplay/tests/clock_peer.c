/* Actual peer freshness query, without sockets or threads.
 * SPDX-License-Identifier: Apache-2.0 */
#include "ap2_ptp.c"

struct ap2_ptp_ctx *fixture_peer(void) {
    static struct ap2_ptp_ctx peer;
    static bool initialized;
    if (!initialized) {
        pthread_mutex_init(&peer.lock, NULL);
        inet_pton(AF_INET, "127.0.0.1", &peer.exchange[0].addr);
        peer.exchange[0].first_ns = 995000000000ULL;
        peer.exchange[0].third_ns = 995250000000ULL;
        peer.exchange[0].count = 18;
        initialized = true;
    }
    return &peer;
}
void fixture_peer_exchange(uint64_t now_ns) { fixture_peer()->exchange[0].last_ns = now_ns; }
bool fixture_peer_fresh(void) {
    struct ap2_ptp_exchange snapshot;
    return ap2_ptp_peer_exchange(fixture_peer(), "127.0.0.1", &snapshot);
}
