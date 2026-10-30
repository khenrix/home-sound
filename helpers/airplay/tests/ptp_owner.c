/* Home Sound regression fixture. SPDX-License-Identifier: Apache-2.0 */
#include "ap2_ptp_shm.c"
#include <assert.h>
#include <sys/wait.h>
static log_level quiet=lSILENCE;
log_level *loglevel=&quiet;
int main(void) {
 assert(sizeof(struct ap2_ptp_shm)==3176);
#ifdef HOME_SOUND_OWNED_PTP
 assert(!ap2_ptp_set_endpoint("/foreign",12345));
 assert(!ap2_ptp_set_endpoint("/hs-test/escape",12345));
 assert(!ap2_ptp_set_endpoint("/hs-test",0));
 assert(!ap2_ptp_set_endpoint("/hs-test",65536));
 assert(ap2_ptp_set_endpoint(AP2_PTP_SHM_NAME,12345));
#endif
 int fd=shm_open(AP2_PTP_SHM_NAME,O_CREAT|O_EXCL|O_RDWR,0600);assert(fd>=0);
 assert(ftruncate(fd,1)==0);assert(write(fd,"X",1)==1);
 struct ap2_ptp_shm_writer w;
 bool accepted=ap2_ptp_shm_writer_open(&w);
 if(accepted) ap2_ptp_shm_writer_close(&w);
 char c=0;assert(pread(fd,&c,1,0)==1);
 close(fd);shm_unlink(AP2_PTP_SHM_NAME);
 if(accepted || c!='X') {fputs("FAIL reused unknown shared memory\n",stderr);return 1;}
 assert(ap2_ptp_shm_writer_open(&w));ap2_ptp_shm_writer_close(&w);
 assert(shm_open(AP2_PTP_SHM_NAME,O_RDONLY,0)<0 && errno==ENOENT);
 pid_t pid=fork();assert(pid>=0);
 if(pid==0) {struct ap2_ptp_shm_writer orphan;_exit(ap2_ptp_shm_writer_open(&orphan)?0:2);}
 int status;assert(waitpid(pid,&status,0)==pid&&WEXITSTATUS(status)==0);
 assert(!ap2_ptp_shm_writer_open(&w));
 fd=shm_open(AP2_PTP_SHM_NAME,O_RDONLY,0);assert(fd>=0);close(fd);
#ifdef HOME_SOUND_OWNED_PTP
 char next[31];snprintf(next,sizeof(next),"/hs-next-%d",getpid());
 assert(ap2_ptp_set_endpoint(next,12346));
 assert(ap2_ptp_shm_writer_open(&w));ap2_ptp_shm_writer_close(&w);
 /* A new generation runs without deleting the crashed generation. */
 fd=shm_open(AP2_PTP_SHM_NAME,O_RDONLY,0);assert(fd>=0);close(fd);
#endif
 /* Fixture alone knows it owns this orphan. Production must not delete it. */
 shm_unlink(AP2_PTP_SHM_NAME);
 puts("PASS exclusive ownership, graceful restart and preserved crash orphan");
}
