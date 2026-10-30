#!/usr/bin/env python3
import pathlib, subprocess, sys, tempfile
source=pathlib.Path(sys.argv[1]).resolve();here=pathlib.Path(__file__).resolve().parent
with tempfile.TemporaryDirectory(prefix='home-sound-ptp-required-') as tmp:
 exe=pathlib.Path(tmp)/'required'
 cmd=['gcc','-O1','-ffunction-sections','-fdata-sections','-Wl,--gc-sections','-pthread']
 if len(sys.argv)>2:cmd+=['-DHOME_SOUND_OWNED_PTP']
 cmd+=['-I'+str(source/p) for p in ['src','libraop/crosstools/src','libraop/src','libraop/src/inc']]
 cmd+=[str(here/('ptp_ports.c' if 'ports' in sys.argv else 'ptp_required.c'))]+[str(source/p) for p in ['src/ap2_ptp_shm.c','src/ap2_ptp_peers.c','src/host_clock.c','libraop/crosstools/src/cross_log.c']]+['-lrt','-o',str(exe)]
 subprocess.run(cmd,check=True);sys.exit(subprocess.run([str(exe)]).returncode)
