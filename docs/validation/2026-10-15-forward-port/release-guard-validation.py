from pathlib import Path
import tempfile, subprocess, os
source = Path(__file__).resolve().parents[3]
with tempfile.TemporaryDirectory(prefix='task1-release-check-') as root:
    root=Path(root)
    (root/'Makefile').write_bytes((source/'Makefile').read_bytes())
    (root/'configs/coins').mkdir(parents=True)
    (root/'configs/coins/bgold.json').write_text('{}')
    (root/'build/tools').mkdir(parents=True)
    (root/'build/bb-build-var-prefixes.txt').write_bytes((source/'build/bb-build-var-prefixes.txt').read_bytes())
    image=root/'build/tools/image_status.sh'
    image.write_text('#!/bin/sh\nprintf "ok\\n"\n'); image.chmod(0o755)
    binpath=root/'fake-bin'; binpath.mkdir()
    docker=binpath/'docker'; docker.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$TASK1_CAPTURE"\n'); docker.chmod(0o755)
    capture=root/'capture'
    env=dict(os.environ,PATH=str(binpath)+':'+os.environ['PATH'],TASK1_CAPTURE=str(capture),BB_BUILD_ENV='testenv',BB_DEV_RPC_URL_HTTP_testcoin='http://invalid.local',BB_STAGING='1')
    def cmd(args,code=0):
        r=subprocess.run(args,cwd=root,env=env,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
        print('$ '+' '.join(args)); print(r.stdout,end='')
        assert r.returncode==code,(r.returncode,r.stdout)
        return r.stdout
    cmd(['git','init','-q']); cmd(['git','add','.']); cmd(['git','-c','user.name=Task1','-c','user.email=task1@example.invalid','commit','-qm','fixture'])
    sha=cmd(['git','rev-parse','HEAD']).strip()
    # Keep captured Docker calls out of the fixture's source status.
    env['TASK1_CAPTURE']=str(root.parent/(root.name+'-capture.log'))
    capture=Path(env['TASK1_CAPTURE'])
    try:
        cmd(['make','release-build','GITCOMMIT=incorrect','DOCKER_VERSION=test'])
        cmd(['make','release-deb-blockbook-bgold','GITCOMMIT=incorrect','DOCKER_VERSION=test'])
        calls=capture.read_text()
        assert calls.count('GITCOMMIT='+sha)==2,calls
        assert 'incorrect' not in calls,calls
        assert '-e BB_BUILD_ENV=testenv' in calls,calls
        assert '-e BB_DEV_RPC_URL_HTTP_testcoin' in calls,calls
        assert '-e BB_STAGING' in calls,calls
        assert '/build/build-deb.sh blockbook bgold' in calls,calls
        print('PASS clean binary/Debian release forwards exact HEAD, overriding supplied metadata')
        capture.unlink()
        (root/'untracked.txt').write_text('untracked\n')
        cmd(['make','release-build','DOCKER_VERSION=test'],2)
        assert not capture.exists()
        print('PASS untracked source rejected before Docker invocation')
        (root/'untracked.txt').unlink()
        (root/'Makefile').write_text((root/'Makefile').read_text()+'\n# tracked dirty fixture\n')
        cmd(['make','release-deb-blockbook-bgold','DOCKER_VERSION=test'],2)
        assert not capture.exists()
        print('PASS tracked source rejected before Docker invocation')
        dry=cmd(['make','-n','build','DOCKER_VERSION=test'])
        assert 'GITCOMMIT='+sha+'-dirty' in dry
        print('PASS ordinary development build marks dirty source')
        # An error obtaining status must fail closed before Docker.
        git_stub=binpath/'git'
        git_stub.write_text('#!/bin/sh\nexit 1\n'); git_stub.chmod(0o755)
        cmd(['make','release-build','DOCKER_VERSION=test'],2)
        assert not capture.exists()
        git_stub.unlink()
        print('PASS git status errors reject release before Docker invocation')
        # Direct package helper preserves explicit metadata or computes clean/dirty HEAD.
        helper=(source/'build/docker/deb/build-deb.sh').read_text()
        snippet=helper[helper.index('    if [ -z "${GITCOMMIT:-}" ]; then'):helper.index('    export VERSION=')]
        snippet=snippet.replace('/src',str(root))
        def package_metadata(value):
            direct_env=dict(env)
            direct_env.pop('GITCOMMIT',None)
            if value is not None: direct_env['GITCOMMIT']=value
            proc=subprocess.run(['bash','-c',snippet+'\nprintf "%s" "$GITCOMMIT"'],cwd=root,env=direct_env,text=True,capture_output=True,check=True)
            return proc.stdout
        assert package_metadata('explicit-assertion')=='explicit-assertion'
        assert package_metadata(None)==sha+'-dirty'
        cmd(['git','checkout','--','Makefile'])
        assert package_metadata(None)==sha
        print('PASS direct Debian metadata preserves explicit override and full clean/dirty revision')
        inner=(source/'build/docker/bin/Makefile').read_text().replace('/src',str(root))
        inner_path=root.parent/(root.name+'-inner.mk')
        try:
            inner_path.write_text(inner+'\nmetadata:\n\t@echo $(GITCOMMIT)\n')
            args=['make','-s','-f',str(inner_path),'metadata','JEMALLOC=0','BUILDTIME=fixed']
            assert cmd(args).strip()==sha
            inner_env=dict(env,GITCOMMIT='explicit-inner')
            result=subprocess.run(args,cwd=root,env=inner_env,text=True,capture_output=True,check=True)
            assert result.stdout.strip()=='explicit-inner'
            print('PASS inner Makefile full clean SHA default and environment override')
        finally: inner_path.unlink(missing_ok=True)
    finally:
        capture.unlink(missing_ok=True)
