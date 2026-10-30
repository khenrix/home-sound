#!/usr/bin/env python3
"""Export pinned Git objects and build separately named experimental helpers.
No installation, capabilities, device traffic, or changes to the supplied checkout.
"""
import argparse, hashlib, json, os, pathlib, shutil, subprocess, tarfile, tempfile
PIN = '0a58c239d5fb3cf8927b9707a71c6fe897266b6a'
HERE = pathlib.Path(__file__).resolve().parent

def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)

def git(repo, *args):
    return subprocess.check_output(['git', '-C', str(repo), *args])

def export(repo, pin, dest, pins):
    dest.mkdir(parents=True, exist_ok=True)
    # Archive immutable objects, never the possibly dirty working tree.
    with tempfile.TemporaryFile() as archive:
        run(['git', '-C', str(repo), 'archive', pin], stdout=archive)
        archive.seek(0)
        with tarfile.open(fileobj=archive) as tar:
            tar.extractall(dest, filter='data')
    for entry in git(repo, 'ls-tree', '-rz', pin).split(b'\0'):
        if not entry: continue
        meta, name = entry.split(b'\t', 1)
        mode, kind, sha = meta.decode().split()
        if mode == '160000':
            relative = name.decode()
            pins.append({'path': str(dest / relative), 'commit': sha})
            export(repo / relative, sha, dest / relative, pins)

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('canonical_checkout', type=pathlib.Path)
    parser.add_argument('new_output_directory', type=pathlib.Path)
    args=parser.parse_args();os.umask(0o022)
    source=args.canonical_checkout.resolve();out=args.new_output_directory.resolve()
    if out.exists(): parser.error('output directory must be new; candidates never overwrite a baseline')
    if git(source,'rev-parse','v0.5.9^{commit}').decode().strip()!=PIN: parser.error('tag does not match the pinned release')
    out.mkdir(parents=True);pins=[];baseline=out/'source'
    export(source,PIN,baseline,pins)
    patches={name:hashlib.sha256((HERE/'patches'/f'{name}.patch').read_bytes()).hexdigest() for name in ('anchor','epoch')}
    manifest={'pin':PIN,'submodules':[dict(path=str(pathlib.Path(p['path']).relative_to(baseline)),commit=p['commit']) for p in pins], 'patch_sha256':patches,'artifacts':{},'compiler':subprocess.check_output(['gcc','--version']).decode().splitlines()[0]}
    for name in ('anchor','epoch','combined'):
        dest=out/name
        shutil.copytree(baseline,dest,ignore=lambda path,names: ['libraop'] if pathlib.Path(path)==baseline else [])
        (dest/'libraop').symlink_to('../source/libraop',target_is_directory=True)
        selected=('anchor','epoch') if name=='combined' else (name,)
        for patch in selected:
            with (HERE/'patches'/f'{patch}.patch').open('rb') as f: run(['patch','--batch','--fuzz=0','-p1'],cwd=dest,stdin=f)
        run(['python3',str(HERE/'tests/run.py'),str(dest),name])
        if name=='combined': run(['python3',str(HERE/'tests/run.py'),str(dest),'epoch'])
        version='0.5.9-homesound-ntp-'+name
        command=['make','HOST=linux','PLATFORM=x86_64','STATIC=1','CC=gcc','CXX=g++','VERSION='+version,'LIBCODECS_PATCHED='+str(dest/'build/libcodecs_patched.a'),'EXTRA_CFLAGS=-ffile-prefix-map='+str(out)+'=.', '-j2']
        (dest/'build').mkdir(exist_ok=True)
        # Upstream libcodecs is a thin archive; materialize it before moving.
        archive=dest/'build/libcodecs_patched.a'
        run(['ar','-M'], input=('CREATE '+str(archive)+'\nADDLIB '+str(dest/'libraop/libcodecs/targets/linux/x86_64/libcodecs.a')+'\nSAVE\nEND\n').encode())
        run(['ar','d',str(archive),'alac_wrapper.o'])
        with (out/(name+'-build.log')).open('w') as log: run(command,cwd=dest,stdout=log,stderr=subprocess.STDOUT)
        artifact=out/('cliairplay-'+version+'-linux-x86_64')
        shutil.copy2(dest/'bin/cliairplay-linux-x86_64',artifact)
        manifest['artifacts'][name]={'file':artifact.name,'sha256':hashlib.sha256(artifact.read_bytes()).hexdigest(),'version':version,'build_command':command}
    for filename in ('LICENSE','THIRD_PARTY_NOTICES.md'): shutil.copy2(baseline/filename,out/filename)
    (out/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
    print(json.dumps(manifest['artifacts'],indent=2))
if __name__=='__main__':main()
