#!/usr/bin/env python3
"""POSIX-only two-server cookie usability proof. Startup links remain RAM-only."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import select
import signal
import struct
import subprocess
import time


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary',required=True,type=Path)
    parser.add_argument('--fixture-a',required=True,type=Path)
    parser.add_argument('--fixture-b',required=True,type=Path)
    parser.add_argument('--evidence',required=True,type=Path)
    parser.add_argument('--node',default=os.environ.get('SVNTUI_NODE','node'))
    args=parser.parse_args()
    if os.name != "posix":
        parser.error("this two-server controller requires POSIX PTYs")
    import fcntl
    import pty
    import termios
    args.evidence.mkdir(parents=True,exist_ok=True)
    owned=[]
    urls=[]
    report={'server_pids':[],'fixture_paths':[str(args.fixture_a),str(args.fixture_b)],
            'binary_sha256':hashlib.sha256(args.binary.read_bytes()).hexdigest(),'startup_links_retained':False}
    try:
        for fixture in (args.fixture_a,args.fixture_b):
            manifest=json.loads(fixture.read_text(encoding='utf-8'))
            env=os.environ.copy();env.update(manifest['env'])
            master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',60,320,0,0))
            process=subprocess.Popen([str(args.binary.resolve()),'web','--no-open',manifest['wc']],cwd=manifest['wc'],env=env,
                                     stdin=slave,stdout=slave,stderr=slave,start_new_session=True)
            os.close(slave);owned.append((process,master));report['server_pids'].append(process.pid)
            data=b'';deadline=time.monotonic()+30
            while time.monotonic()<deadline:
                if process.poll() is not None:raise RuntimeError('owned server exited; raw startup output withheld')
                if select.select([master],[],[],0.2)[0]:
                    data+=os.read(master,65536)
                    match=re.search(rb'http://127\.0\.0\.1:\d+/[^\s]*#[^\s]+(?=\r?\n)',data)
                    if match:
                        urls.append(match.group().decode('utf-8'));break
            if len(urls)!=len(owned):raise TimeoutError('startup URL missing; raw output withheld')
        env=os.environ.copy();env.update(SVNTUI_WEB_URL=urls[0],SVNTUI_OTHER_WEB_URL=urls[1],SVNTUI_BROWSER_EVIDENCE=str(args.evidence.resolve()))
        result=subprocess.run([args.node,str(Path(__file__).with_name('two-server-cookies.cjs').resolve())],env=env,capture_output=True,timeout=120)
        for name,data in [('stdout',result.stdout),('stderr',result.stderr)]:
            for url in urls:
                for secret in (url,url.split('#',1)[1],url.split('token=',1)[-1]):data=data.replace(secret.encode(),b'[startup-credential-redacted]')
            (args.evidence/('browser-'+name+'.txt')).write_bytes(data)
        report['browser_exit_code']=result.returncode
    finally:
        for process,_ in owned:
            if process.poll() is None:process.send_signal(signal.SIGINT)
        for process,master in owned:
            try:process.wait(timeout=30)
            except subprocess.TimeoutExpired:report.setdefault('cleanup_required_pids',[]).append(process.pid)
            os.close(master)
        (args.evidence/'two-server-bridge.json').write_text(json.dumps(report,indent=2),encoding='utf-8')
    print(json.dumps(report,indent=2))
    return report.get('browser_exit_code',1)


if __name__=='__main__':
    raise SystemExit(main())
