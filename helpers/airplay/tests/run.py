#!/usr/bin/env python3
"""Compile actual pinned C emitter/responder; all sockets are ephemeral loopback."""
import pathlib, subprocess, sys, tempfile
source = pathlib.Path(sys.argv[1]).resolve()
case = sys.argv[2]
here = pathlib.Path(__file__).resolve().parent
with tempfile.TemporaryDirectory(prefix='home-sound-ntp-') as tmp:
    exe = pathlib.Path(tmp) / 'ntp-test'
    includes = ['src', 'libraop/libopenssl/targets/linux/x86_64/include', 'libraop/src', 'libraop/src/inc', 'libraop/crosstools/src', 'libraop/libcodecs/targets/include/alac', 'libraop/libcodecs/targets/include/addons']
    command = ['gcc', '-O1', '-ffunction-sections', '-fdata-sections', '-Wl,--gc-sections', '-pthread']
    command += ['-I'+str(source / p) for p in includes]
    command += [str(here/'ntp_media.c'), str(here/'ntp_timing.c'), str(source/'src/ap2_io.c'), str(source/'libraop/crosstools/src/cross_log.c'), '-o', str(exe)]
    subprocess.run(command, check=True)
    sys.exit(subprocess.run([str(exe), case]).returncode)
