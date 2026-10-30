/* Home Sound regression fixture. SPDX-License-Identifier: Apache-2.0 */
/* Include the pinned implementation so the actual static emitter runs. */
#include "ap2_client.c"
#include <assert.h>

static log_level quiet = lSILENCE;
log_level *loglevel = &quiet;
static uint64_t now_ntp;
uint64_t raopcl_get_ntp(struct ntp_s *unused) { (void)unused; return now_ntp; }
void timing_probe(uint32_t *sec, uint32_t *frac);
static uint64_t now_ns = 1000250000000ULL;
uint64_t host_clock_now_ns(void) { return now_ns; }

static uint32_t word(const uint8_t *p) { uint32_t v; memcpy(&v,p,4); return ntohl(v); }
static int socket_at(struct sockaddr_in *addr) {
    int fd = socket(AF_INET, SOCK_DGRAM, 0); assert(fd >= 0);
    *addr = (struct sockaddr_in){.sin_family=AF_INET,.sin_addr.s_addr=htonl(INADDR_LOOPBACK)};
    assert(bind(fd,(struct sockaddr *)addr,sizeof(*addr))==0);
    socklen_t n=sizeof(*addr); assert(getsockname(fd,(struct sockaddr *)addr,&n)==0);
    struct timeval tv={.tv_sec=1}; assert(setsockopt(fd,SOL_SOCKET,SO_RCVTIMEO,&tv,sizeof(tv))==0);
    return fd;
}
int main(int argc, char **argv) {
    assert(argc == 2);
    struct ap2cl_s p={0};
    struct sockaddr_in receiver;
    int rx=socket_at(&receiver);
    p.ctrl_sock=socket(AF_INET,SOCK_DGRAM,0); assert(p.ctrl_sock>=0);
    p.ctrl_addr=receiver; p.format.sample_rate=48000; p.lead_ms=2000;
    p.start_ntp=(1000ULL<<32)+(1ULL<<30); // 1000.25 s, internal Unix epoch
    uint64_t start_frame=48012000;
    now_ntp=p.start_ntp;
    if (!strcmp(argv[1],"epoch")) {
        const uint32_t starts[]={1000U,1700000000U,2208989000U};
        const uint32_t wire_seconds[]={2208989800U,3908988800U,123010504U};
        for(unsigned era=0;era<3;era++) {
        p.start_ntp=((uint64_t)starts[era]<<32)+(1ULL<<30);
        now_ntp=p.start_ntp;now_ns=(uint64_t)starts[era]*1000000000ULL+250000000ULL;
        p.head_ts=(uint64_t)starts[era]*48000+12000+96000;
        p.rtp_timestamp=(uint32_t)p.head_ts;
        uint8_t pkt[20];
        assert(ap2_send_sync_packet(&p,true)==AP2_SEND_SENT);
        assert(recv(rx,pkt,sizeof(pkt),0)==20);
        uint32_t sec,frac; timing_probe(&sec,&frac);
        if(word(pkt+8)!=sec || word(pkt+12)!=frac) {
            fprintf(stderr,"FAIL wire epoch: media=%u/%u responder=%u/%u\n",word(pkt+8),word(pkt+12),sec,frac); return 1;
        }
        assert(sec==wire_seconds[era] && frac==1073741824U);
        }
    } else {
        /* Different queue heads and wall clocks must announce one content line. */
        const uint64_t ahead[]={24000,57600,28800,144000};
        const uint32_t want_sec[]={998,999,998,1001};
        const uint32_t want_frac[]={3221225472U,1932735283U,3650722201U,1073741824U};
        const uint32_t want_rtp[]={4294871396U,4294904996U,4294876196U,24100U};
        for(unsigned i=0;i<4;i++) {
            p.head_ts=start_frame+ahead[i];
            p.rtp_timestamp=100+(uint32_t)(ahead[i]-24000); // one fixed RTP origin
            now_ntp=(999ULL+i*11)<<32;
            uint8_t pkt[20];
            assert(ap2_send_sync_packet(&p,i==0)==AP2_SEND_SENT);
            assert(recv(rx,pkt,sizeof(pkt),0)==20);
            uint32_t sec=word(pkt+8);
            if(!strcmp(argv[1],"combined")) sec-=2208988800U;
            /* Fixed-point subtraction may round one tick upward (<0.24ns). */
            int64_t fraction_error=(int64_t)word(pkt+12)-want_frac[i];
            if(word(pkt+4)!=want_rtp[i] || sec!=want_sec[i] || fraction_error<0 || fraction_error>1) {
                fprintf(stderr,"FAIL frozen line at queue=%llu: RTP=%u time=%u/%u\n",(unsigned long long)ahead[i],word(pkt+4),sec,word(pkt+12)); return 1;
            }
            assert(word(pkt+16)==p.rtp_timestamp);
            assert(pkt[0]==(i?0x80:0x90) && pkt[1]==0xd4);
        }
    }
    close(rx);close(p.ctrl_sock); puts("PASS actual C NTP loopback");return 0;
}
