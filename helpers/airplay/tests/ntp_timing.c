/* Home Sound regression fixture. SPDX-License-Identifier: Apache-2.0 */
#include "ap2_ptp.c"
#include <assert.h>
/* Run the actual responder thread with its host clock supplied by the fixture. */
void timing_probe(uint32_t *sec, uint32_t *frac) {
    struct ap2_ptp_ctx ctx={0};
    ctx.timing_sock=socket(AF_INET,SOCK_DGRAM,0); assert(ctx.timing_sock>=0);
    struct sockaddr_in dst={.sin_family=AF_INET,.sin_addr.s_addr=htonl(INADDR_LOOPBACK)};
    assert(bind(ctx.timing_sock,(struct sockaddr *)&dst,sizeof(dst))==0);
    socklen_t n=sizeof(dst); assert(getsockname(ctx.timing_sock,(struct sockaddr *)&dst,&n)==0);
    ctx.running=true;
    pthread_t thread; assert(pthread_create(&thread,NULL,timing_thread_func,&ctx)==0);
    int fd=socket(AF_INET,SOCK_DGRAM,0); assert(fd>=0);
    struct timeval tv={.tv_sec=2}; assert(setsockopt(fd,SOL_SOCKET,SO_RCVTIMEO,&tv,sizeof(tv))==0);
    uint8_t request[32]={0},reply[32]; request[1]=0xd2; request[24]=0x12;
    assert(sendto(fd,request,32,0,(struct sockaddr *)&dst,sizeof(dst))==32);
    assert(recv(fd,reply,32,0)==32); assert(reply[1]==0xd3 && reply[8]==0x12);
    uint32_t s,f; memcpy(&s,reply+16,4); memcpy(&f,reply+20,4); *sec=ntohl(s); *frac=ntohl(f);
    /* Cancellation avoids introducing a fixture data race on upstream running. */
    assert(pthread_cancel(thread)==0); assert(pthread_join(thread,NULL)==0);
    close(fd);close(ctx.timing_sock);
}
