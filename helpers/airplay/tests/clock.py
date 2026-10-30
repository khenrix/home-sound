#!/usr/bin/env python3
"""Compile and exercise the actual helper's receiver-readiness reporter."""
import argparse, pathlib, subprocess, tempfile
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('source', type=pathlib.Path)
parser.add_argument('--output', type=pathlib.Path)
args = parser.parse_args()
source = args.source.resolve()
here = pathlib.Path(__file__).resolve().parent
with tempfile.TemporaryDirectory(prefix='home-sound-clock-') as tmp:
    exe = args.output.resolve() if args.output else pathlib.Path(tmp) / 'clock-report'
    includes = ['src', 'libraop/src', 'libraop/src/inc', 'libraop/crosstools/src',
                'libraop/dmap-parser', 'libraop/libmdns/targets/include/mdnssd',
                'libraop/libmdns/targets/include/mdnssvc',
                'libraop/libopenssl/targets/linux/x86_64/include',
                'libraop/libcodecs/targets/include/alac', 'libraop/libcodecs/targets/include/addons']
    command = ['gcc', '-O1', '-ffunction-sections', '-fdata-sections', '-Wl,--gc-sections', '-pthread']
    command += ['-I'+str(source/p) for p in includes]
    command += [str(here/'clock_report.c'), str(here/'clock_readiness.c'), str(here/'clock_peer.c'), str(source/'src/ap2_ptp_shm.c'), str(source/'libraop/crosstools/src/cross_log.c'), '-o', str(exe)]
    subprocess.run(command, check=True)
    subprocess.run([str(exe)], check=True)
