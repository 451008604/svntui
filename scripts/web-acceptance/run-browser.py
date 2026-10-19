#!/usr/bin/env python3
"""POSIX PTY startup bridge: secret link goes only into child-process memory."""
import argparse
import hashlib
import platform
import json
import os
from pathlib import Path
import re
import select
import signal
import subprocess
import time


def main():
    if os.name == 'nt':
        raise SystemExit('Windows requires the existing ConPTY controller; this bridge is POSIX only')
    import pty
    import fcntl
    import struct
    import termios
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--fixture', required=True, type=Path)
    parser.add_argument('--evidence', required=True, type=Path)
    parser.add_argument('--script', type=Path, default=Path(__file__).with_name('browser-probe.cjs'))
    parser.add_argument('--node', default=os.environ.get('SVNTUI_NODE','node'))
    args = parser.parse_args()
    manifest = json.loads(args.fixture.read_text(encoding="utf-8"))
    args.evidence.mkdir(parents=True,exist_ok=True)
    env = os.environ.copy()
    env.update(manifest['env'])
    binary_hash=hashlib.sha256(args.binary.read_bytes()).hexdigest()
    script_hash=hashlib.sha256(args.script.read_bytes()).hexdigest()
    master, slave = pty.openpty()
    fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack("HHHH",60,320,0,0))
    server = subprocess.Popen([str(args.binary.resolve()),'web','--no-open',manifest['wc']],
                              cwd=manifest['wc'],env=env,stdin=slave,stdout=slave,stderr=slave,start_new_session=True)
    os.close(slave)
    buffer = b''
    startup_url = None
    # Never print raw PTY output: it contains the single-use credential.
    try:
        deadline = time.monotonic()+30
        while time.monotonic()<deadline:
            if server.poll() is not None:
                raise RuntimeError('Web server exited before startup; raw output withheld to protect credential')
            if select.select([master],[],[],0.2)[0]:
                buffer += os.read(master,65536)
                match = re.search(rb'http://127\.0\.0\.1:\d+/[^\s]*#[^\s]+(?=\r?\n)',buffer)
                if match:
                    startup_url = match.group().decode('utf-8')
                    break
        if not startup_url:
            raise RuntimeError('No secret startup link received from PTY within 30 seconds')
        env['SVNTUI_WEB_URL'] = startup_url
        env['SVNTUI_BROWSER_EVIDENCE'] = str(args.evidence.resolve())
        env['SVNTUI_FIXTURE_MANIFEST'] = str(args.fixture.resolve())
        # Child helper redacts errors and writes no session/storageState/trace files.
        browser = subprocess.run([args.node,str(args.script.resolve())],env=env,capture_output=True,timeout=int(os.environ.get("SVNTUI_BROWSER_TIMEOUT","600")))
        for name,data in [('browser-stdout.txt',browser.stdout),('browser-stderr.txt',browser.stderr)]:
            safe = data.replace(startup_url.encode(),b'[startup-link-redacted]')
            fragment = startup_url.split('#',1)[1]
            safe = safe.replace(fragment.encode(),b'[startup-fragment-redacted]')
            if '=' in fragment:
                safe = safe.replace(fragment.split('=',1)[1].encode(),b'[startup-token-redacted]')
            (args.evidence/name).write_bytes(safe)
        summary = {'bridge':'POSIX PTY','server_pid':server.pid,'browser_exit_code':browser.returncode,
                   'startup_link_retained':False,'probe_only':args.script.name=='browser-probe.cjs',
                   'binary':str(args.binary.resolve()),'binary_sha256':binary_hash,
                   'script':str(args.script.resolve()),'script_sha256':script_hash,
                   'platform':platform.platform(),'fixture':str(args.fixture.resolve())}
        (args.evidence/'bridge-summary.json').write_text(json.dumps(summary,indent=2))
        print(json.dumps(summary,indent=2))
        if browser.returncode:
            raise SystemExit(browser.returncode)
    finally:
        # Only terminate the exact task-owned server, allowing its graceful shutdown.
        if server.poll() is None:
            server.send_signal(signal.SIGINT)
            try:
                server.wait(timeout=30)
            except subprocess.TimeoutExpired:
                server.terminate()
                server.wait(timeout=10)
        os.close(master)


if __name__ == '__main__':
    main()
