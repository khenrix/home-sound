/* Home Sound regression fixture. SPDX-License-Identifier: Apache-2.0 */
#include "ap2_ptp.c"
#include <assert.h>
static log_level quiet=lSILENCE;
log_level *loglevel=&quiet;
int main(void) {
 struct ap2_ptp_ctx ctx={0};
 inet_pton(AF_INET,"127.0.0.1",&ctx.bind_addr);
 inet_pton(AF_INET,"224.0.1.129",&ctx.mcast_addr);
 int first=ptp_open_socket(&ctx,0);assert(first>=0);
 struct sockaddr_in addr;socklen_t n=sizeof(addr);assert(getsockname(first,(struct sockaddr *)&addr,&n)==0);
 int second=ptp_open_socket(&ctx,ntohs(addr.sin_port));
 if(second>=0)close(second);close(first);
 if(second>=0){fputs("FAIL two timing engines can bind the same UDP port\n",stderr);return 1;}
 int foreign=socket(AF_INET,SOCK_DGRAM,0);assert(foreign>=0);
 int yes=1;assert(setsockopt(foreign,SOL_SOCKET,SO_REUSEADDR,&yes,sizeof(yes))==0);
 addr.sin_port=0;assert(bind(foreign,(struct sockaddr *)&addr,sizeof(addr))==0);
 n=sizeof(addr);assert(getsockname(foreign,(struct sockaddr *)&addr,&n)==0);
 second=ptp_open_socket(&ctx,ntohs(addr.sin_port));assert(second<0);close(foreign);
 puts("PASS timing port has exclusive owner and rejects foreign reusable binder");return 0;
}
