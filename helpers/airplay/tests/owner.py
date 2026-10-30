#!/usr/bin/env python3
import os, pathlib, subprocess, sys, tempfile
source=pathlib.Path(sys.argv[1]).resolve();here=pathlib.Path(__file__).resolve().parent
with tempfile.TemporaryDirectory(prefix='home-sound-ptp-test-') as tmp:
 exe=pathlib.Path(tmp)/'owner'
 subprocess.run((['gcc']+(['-DHOME_SOUND_OWNED_PTP'] if len(sys.argv)>2 else []))+['-O1','-ffunction-sections','-fdata-sections','-Wl,--gc-sections','-pthread','-DAP2_PTP_SHM_NAME="/hs-test-'+str(os.getpid())+'"','-I'+str(source/'src'),'-I'+str(source/'libraop/crosstools/src'),str(here/'ptp_owner.c'),str(source/'libraop/crosstools/src/cross_log.c'),'-lrt','-o',str(exe)],check=True)
 sys.exit(subprocess.run([str(exe)]).returncode)
