/* Actual receiver-readiness calculation with a controlled peer clock.
 * SPDX-License-Identifier: Apache-2.0 */
#define ap2cl_clock_readiness fixture_actual_readiness
#include "ap2_client.c"
#undef ap2cl_clock_readiness

struct ap2_ptp_ctx *fixture_peer(void);
struct ap2cl_s *fixture_client(void) {
    static struct ap2cl_s client;
    static bool initialized;
    if (!initialized) {
        pthread_mutex_init(&client.clock_verify_lock, NULL);
        client.flow = FLOW_NATIVE_AP2;
        client.use_ptp = true;
        client.state = AP2_STREAMING;
        client.device.address = "127.0.0.1";
        client.ptp = fixture_peer();
        client.clock_connected_unix_ms = 990000;
        initialized = true;
    }
    return &client;
}
void fixture_set_ptp(bool enabled) { fixture_client()->use_ptp = enabled; }
