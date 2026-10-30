/* Home Sound receiver-clock reporting regression. SPDX-License-Identifier: Apache-2.0 */
#define main upstream_cli_main
#include "cliairplay.c"
#undef main
#include <assert.h>

/* Run the real reporting, readiness and peer-expiry code. Only host time,
 * receiver exchange timestamps and the status sink are controlled. */
static unsigned queries, lines;
static uint64_t fixture_ms = 1000000;
struct ap2cl_s *fixture_client(void);
void fixture_set_ptp(bool enabled);
void fixture_peer_exchange(uint64_t now_ns);
bool fixture_peer_fresh(void);
void fixture_actual_readiness(struct ap2cl_s *, ap2_clock_readiness_t *);
uint64_t host_clock_now_ns(void) { return fixture_ms * 1000000ULL; }
uint64_t raopcl_get_ntp(struct ntp_s *unused) {
    (void)unused;
    return ((fixture_ms / 1000) << 32) + (((fixture_ms % 1000) << 32) / 1000);
}
void ap2cl_clock_readiness(struct ap2cl_s *client, ap2_clock_readiness_t *r) {
    queries++; fixture_actual_readiness(client, r);
}
void ap2_io_status_vline(const char *fmt, va_list args) {
    lines++; vprintf(fmt, args); putchar('\n');
}
int main(int argc, char **argv) {
    main_log = lSILENCE; setvbuf(stdout, NULL, _IONBF, 0);
    g_ap2cl = fixture_client(); fixture_peer_exchange(host_clock_now_ns());
    if (argc == 2 && !strcmp(argv[1], "stream")) {
        char command[32];
        while (fgets(command, sizeof(command), stdin)) {
            if (!strcmp(command, "lost\n")) {
                fixture_ms += 6000; assert(!fixture_peer_fresh());
            } else if (!strcmp(command, "ready\n")) {
                fixture_ms += 1000; fixture_peer_exchange(host_clock_now_ns());
            } else return 2;
            clock_ready_tick();
            printf("[FIXTURE] queries=%u lines=%u\n", queries, lines);
        }
        return 0;
    }
    clock_ready_tick();
    for (unsigned i = 0; i < 100; i++) clock_ready_tick();
    assert(queries == 1 && lines == 1);
    fixture_ms += 500; fixture_peer_exchange(host_clock_now_ns()); clock_ready_tick();
    fixture_ms += 6000; assert(!fixture_peer_fresh()); clock_ready_tick();
    if (queries != 3 || lines != 2) {
        fprintf(stderr, "FAIL receiver loss hidden: queries=%u lines=%u\n", queries, lines);
        return 1;
    }
    for (unsigned i = 0; i < 100; i++) clock_ready_tick();
    fixture_ms += 500; clock_ready_tick();
    assert(queries == 4 && lines == 2);
    fixture_ms += 500; fixture_peer_exchange(host_clock_now_ns()); clock_ready_tick();
    assert(queries == 5 && lines == 3);
    fixture_set_ptp(false);
    fixture_ms += 500; clock_ready_tick();
    for (unsigned i = 0; i < 100; i++) { fixture_ms += 50; clock_ready_tick(); }
    assert(queries == 26 && lines == 4);
    puts("PASS continuous receiver readiness, bounded polling and change-only reports");
    return 0;
}
