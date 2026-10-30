/* Home Sound regression fixture. SPDX-License-Identifier: Apache-2.0 */
#define bind required_forbidden_bind
#include "ap2_ptp.c"
#undef bind
#include <assert.h>
static log_level quiet=lSILENCE;
log_level *loglevel=&quiet;
void get_mac(uint8_t mac[]) {memset(mac,0,6);}
static unsigned bind_attempts;
int required_forbidden_bind(int fd,const struct sockaddr *addr,socklen_t len) {
 (void)fd;(void)addr;(void)len;bind_attempts++;errno=EPERM;return -1;
}
int main(void) {
#ifdef HOME_SOUND_OWNED_PTP
 ap2_ptp_require_shared();
#endif
 struct ap2_ptp_ctx *ctx=ap2_ptp_create();assert(ctx);
 bool fallback=ap2_ptp_start(ctx,"127.0.0.1");
 struct in_addr local={.s_addr=htonl(INADDR_LOOPBACK)};
 bool engine=ap2_ptp_engine_start(ctx,local,NULL);
 ap2_ptp_destroy(ctx);
 if(fallback || engine || bind_attempts) {fputs("FAIL required shared route attempted independent NTP/PTP binding\n",stderr);return 1;}
 puts("PASS required shared route cannot start independent NTP/PTP engines");return 0;
}
