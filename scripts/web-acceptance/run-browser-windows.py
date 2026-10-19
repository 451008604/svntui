#!/usr/bin/env python3
"""Windows-only ConPTY bridge; startup credentials never enter a file or CLI argument.

Derived from the independently exercised windows-conpty4.py API/console-handle
patterns. This Web adaptation requires its own native Windows execution check.
"""
import argparse
import ctypes
from ctypes import wintypes
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys
import threading
import time
import uuid


def save_json(path, value):
    pending = Path(str(path)+'.tmp')
    pending.write_text(json.dumps(value,ensure_ascii=False,indent=2),encoding='utf-8')
    pending.replace(path)


class COORD(ctypes.Structure):
    _fields_ = [("X", ctypes.c_short), ("Y", ctypes.c_short)]


class STARTUPINFOW(ctypes.Structure):
    _fields_ = [
        ("cb", wintypes.DWORD), ("lpReserved", wintypes.LPWSTR),
        ("lpDesktop", wintypes.LPWSTR), ("lpTitle", wintypes.LPWSTR),
        ("dwX", wintypes.DWORD), ("dwY", wintypes.DWORD),
        ("dwXSize", wintypes.DWORD), ("dwYSize", wintypes.DWORD),
        ("dwXCountChars", wintypes.DWORD), ("dwYCountChars", wintypes.DWORD),
        ("dwFillAttribute", wintypes.DWORD), ("dwFlags", wintypes.DWORD),
        ("wShowWindow", wintypes.WORD), ("cbReserved2", wintypes.WORD),
        ("lpReserved2", ctypes.POINTER(ctypes.c_byte)),
        ("hStdInput", wintypes.HANDLE), ("hStdOutput", wintypes.HANDLE),
        ("hStdError", wintypes.HANDLE),
    ]


class STARTUPINFOEXW(ctypes.Structure):
    _fields_ = [("StartupInfo", STARTUPINFOW), ("lpAttributeList", ctypes.c_void_p)]


class PROCESS_INFORMATION(ctypes.Structure):
    _fields_ = [("hProcess", wintypes.HANDLE), ("hThread", wintypes.HANDLE),
                ("dwProcessId", wintypes.DWORD), ("dwThreadId", wintypes.DWORD)]


class WinAPI:
    def __init__(self):
        if os.name != "nt":
            raise RuntimeError("This harness requires Windows 10 1809 or later / Windows Server 2019 or later")
        self.dll = ctypes.WinDLL("kernel32", use_last_error=True)
        H, D, P, B = wintypes.HANDLE, wintypes.DWORD, ctypes.c_void_p, wintypes.BOOL
        self.Handler = ctypes.WINFUNCTYPE(B, D)
        signatures = {
            "CreatePipe": ([ctypes.POINTER(H), ctypes.POINTER(H), P, D], B),
            "CreatePseudoConsole": ([COORD, H, H, D, ctypes.POINTER(H)], ctypes.c_long),
            "ClosePseudoConsole": ([H], None),
            "InitializeProcThreadAttributeList": ([P, D, D, ctypes.POINTER(ctypes.c_size_t)], B),
            "UpdateProcThreadAttribute": ([P, D, ctypes.c_size_t, P, ctypes.c_size_t, P, P], B),
            "DeleteProcThreadAttributeList": ([P], None),
            "CreateProcessW": ([wintypes.LPCWSTR, wintypes.LPWSTR, P, P, B, D, P,
                                wintypes.LPCWSTR, ctypes.POINTER(STARTUPINFOEXW),
                                ctypes.POINTER(PROCESS_INFORMATION)], B),
            "CloseHandle": ([H], B),
            "GenerateConsoleCtrlEvent": ([D, D], B),
            "ReadFile": ([H, P, D, ctypes.POINTER(D), P], B),
            "WriteFile": ([H, P, D, ctypes.POINTER(D), P], B),
            "WaitForSingleObject": ([H, D], D),
            "GetExitCodeProcess": ([H, ctypes.POINTER(D)], B),
            "TerminateProcess": ([H, wintypes.UINT], B),
            "GetStdHandle": ([D], H),
            "GetConsoleMode": ([H, ctypes.POINTER(D)], B),
            "SetConsoleMode": ([H, D], B),
            "GetConsoleCP": ([], wintypes.UINT),
            "GetConsoleOutputCP": ([], wintypes.UINT),
            "SetConsoleCP": ([wintypes.UINT], B),
            "SetConsoleOutputCP": ([wintypes.UINT], B),
            "SetConsoleCtrlHandler": ([self.Handler, B], B),
            "OpenThread": ([D, B, D], H),
            "CancelSynchronousIo": ([H], B),
        }
        for name, (argtypes, restype) in signatures.items():
            try:
                fn = getattr(self.dll, name)
            except AttributeError as error:
                raise RuntimeError("Required Windows API is unavailable: " + name) from error
            fn.argtypes, fn.restype = argtypes, restype
            setattr(self, name, fn)

    @staticmethod
    def check(ok, operation):
        if not ok:
            raise ctypes.WinError(ctypes.get_last_error(), operation)

    def snapshot(self):
        result = {}
        for name, number in (("stdin_mode", -10), ("stdout_mode", -11), ("stderr_mode", -12)):
            mode = wintypes.DWORD()
            handle = self.GetStdHandle(number & 0xFFFFFFFF)
            if self.GetConsoleMode(handle, ctypes.byref(mode)):
                result[name] = mode.value
            else:
                result[name] = {"error": ctypes.get_last_error()}
        result["input_code_page"] = self.GetConsoleCP()
        result["output_code_page"] = self.GetConsoleOutputCP()
        return result

    def cancel_thread_io(self, thread):
        if thread is None or not thread.is_alive():
            return
        handle = self.OpenThread(0x0001, False, thread.native_id)  # THREAD_TERMINATE
        if handle:
            try:
                self.CancelSynchronousIo(handle)
            finally:
                self.CloseHandle(handle)


def worker(args):
    """Bind actual console handles, then let only the owned server receive Ctrl-C."""
    import msvcrt
    api = WinAPI()
    api.dll.SetStdHandle.argtypes = [wintypes.DWORD,wintypes.HANDLE]
    api.dll.SetStdHandle.restype = wintypes.BOOL
    for fd, number, device in ((0,-10,'CONIN$'),(1,-11,'CONOUT$'),(2,-12,'CONOUT$')):
        console_fd = os.open(device,os.O_RDWR|os.O_BINARY)
        os.dup2(console_fd,fd)
        os.close(console_fd)
        api.check(api.dll.SetStdHandle(number & 0xFFFFFFFF,msvcrt.get_osfhandle(fd)),'SetStdHandle worker')
    @api.Handler
    def protect_worker(event):
        return event in (0,1)
    api.check(api.SetConsoleCtrlHandler(protect_worker,True),'SetConsoleCtrlHandler')
    child = None
    report = {'worker_pid':os.getpid(),'server_pid':None,'state':'starting','exit_code':None}
    try:
        api.check(api.SetConsoleCP(65001),'SetConsoleCP')
        api.check(api.SetConsoleOutputCP(65001),'SetConsoleOutputCP')
        report['console'] = api.snapshot()
        if not all(isinstance(v,int) for v in report['console'].values()):
            raise RuntimeError('worker does not have real console handles')
        child = subprocess.Popen([str(args.binary.resolve()),'web','--no-open',str(args.wc)],cwd=args.wc)
        report.update(server_pid=child.pid,state='running')
        save_json(args.worker_result,report)
        # The controller sends Ctrl-C through this private console and waits.
        report['exit_code'] = child.wait()
        report['state'] = 'exited'
    except BaseException as exc:
        report.update(state='failed',error_type=type(exc).__name__)
        if child is not None and child.poll() is None:
            api.GenerateConsoleCtrlEvent(0,0)
            try:
                report['exit_code'] = child.wait(timeout=45)
            except subprocess.TimeoutExpired:
                report.update(state='shutdown_timeout',cleanup_required=True)
    finally:
        save_json(args.worker_result,report)
        api.SetConsoleCtrlHandler(protect_worker,False)
    return 0 if report['state']=='exited' and report['exit_code']==0 else 1


class NativeSession:
    def __init__(self, args, env, wc, worker_result):
        self.api = WinAPI()
        self.input_read, self.input_write = wintypes.HANDLE(),wintypes.HANDLE()
        self.output_read, self.output_write = wintypes.HANDLE(),wintypes.HANDLE()
        self.hpc = wintypes.HANDLE()
        self.process = PROCESS_INFORMATION()
        self.data, self.lock = bytearray(),threading.Lock()
        self.reader, self.errors = None,[]
        self.closed = False
        attributes, initialized = None,False
        try:
            self.api.check(self.api.CreatePipe(ctypes.byref(self.input_read),ctypes.byref(self.input_write),None,0),'CreatePipe input')
            self.api.check(self.api.CreatePipe(ctypes.byref(self.output_read),ctypes.byref(self.output_write),None,0),'CreatePipe output')
            hr = self.api.CreatePseudoConsole(COORD(args.columns,args.rows),self.input_read,self.output_write,0,ctypes.byref(self.hpc))
            if hr < 0:
                raise OSError('CreatePseudoConsole failed: 0x%08X' % (hr & 0xFFFFFFFF))
            # Drain before process creation and through final ConPTY shutdown.
            self.reader = threading.Thread(target=self.read_output,name='svntui-web-conpty-reader',daemon=True)
            self.reader.start()
            required = ctypes.c_size_t()
            self.api.InitializeProcThreadAttributeList(None,1,0,ctypes.byref(required))
            if not required.value:
                raise ctypes.WinError(ctypes.get_last_error(),'attribute-list size')
            attributes = ctypes.create_string_buffer(required.value)
            self.api.check(self.api.InitializeProcThreadAttributeList(attributes,1,0,ctypes.byref(required)),'InitializeProcThreadAttributeList')
            initialized = True
            # PSEUDOCONSOLE takes the opaque handle itself, never its address.
            self.api.check(self.api.UpdateProcThreadAttribute(attributes,0,0x00020016,self.hpc,ctypes.sizeof(self.hpc),None,None),'UpdateProcThreadAttribute')
            startup = STARTUPINFOEXW()
            startup.StartupInfo.cb = ctypes.sizeof(STARTUPINFOEXW)
            startup.lpAttributeList = ctypes.cast(attributes,ctypes.c_void_p)
            argv = [sys.executable,'-u',str(Path(__file__).resolve()),'--worker',
                    '--binary',str(args.binary.resolve()),'--wc',str(wc),'--worker-result',str(worker_result)]
            command = ctypes.create_unicode_buffer(subprocess.list2cmdline(argv))
            environment = ctypes.create_unicode_buffer('\0'.join(k+'='+v for k,v in sorted(env.items(),key=lambda p:p[0].upper()))+'\0\0')
            self.api.check(self.api.CreateProcessW(sys.executable,command,None,None,False,0x00080000|0x00000400,
                          environment,str(wc),ctypes.byref(startup),ctypes.byref(self.process)),'CreateProcessW worker')
        except BaseException:
            self.close()
            raise
        finally:
            if initialized:
                self.api.DeleteProcThreadAttributeList(attributes)
            self.close_handle('input_read')
            self.close_handle('output_write')
            if self.process.hThread:
                self.api.CloseHandle(self.process.hThread)
                self.process.hThread = None

    def close_handle(self,name):
        handle = getattr(self,name)
        if handle and handle.value:
            self.api.CloseHandle(handle)
            setattr(self,name,wintypes.HANDLE())

    def read_output(self):
        try:
            buffer = ctypes.create_string_buffer(65536)
            while True:
                count = wintypes.DWORD()
                if not self.api.ReadFile(self.output_read,buffer,len(buffer),ctypes.byref(count),None):
                    code = ctypes.get_last_error()
                    if code not in (109,232,995):
                        self.errors.append('ReadFile:'+str(code))
                    break
                if not count.value:
                    break
                # The private credential-bearing stream remains in RAM only.
                with self.lock:
                    self.data.extend(buffer.raw[:count.value])
                    if len(self.data)>131072:
                        del self.data[:-131072]
        except BaseException as exc:
            self.errors.append('reader:'+type(exc).__name__)

    def startup_url(self,timeout=30):
        deadline = time.monotonic()+timeout
        while time.monotonic()<deadline:
            with self.lock:
                text = bytes(self.data).decode('utf-8','replace')
            text = re.sub(r'\x1b\][^\x07]*(?:\x07|\x1b\\)','',text)
            text = re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]','',text)
            # Require a complete line, so split pipe reads cannot truncate token.
            match = re.search(r'http://127\.0\.0\.1:\d+/[^\s]*#[^\s]+(?=\r?\n)',text)
            if match:
                return match.group()
            if self.exited():
                raise RuntimeError('server worker exited before startup; raw stream withheld')
            if self.errors:
                raise RuntimeError('ConPTY read failed; raw stream withheld')
            time.sleep(0.05)
        raise TimeoutError('startup URL not received; raw stream withheld')

    def exited(self):
        return bool(self.process.hProcess) and self.api.WaitForSingleObject(self.process.hProcess,0)==0

    def interrupt_and_wait(self,timeout=45):
        if not self.process.hProcess or self.exited():
            return True
        data = ctypes.create_string_buffer(b'\x03')
        count = wintypes.DWORD()
        self.api.check(self.api.WriteFile(self.input_write,data,1,ctypes.byref(count),None),'ConPTY Ctrl-C')
        if count.value!=1:
            raise OSError('short ConPTY Ctrl-C write')
        return self.api.WaitForSingleObject(self.process.hProcess,int(timeout*1000))==0

    def close(self):
        if self.closed:
            return
        self.closed = True
        # Closing a running console could terminate its child: only close after exit.
        if self.process.hProcess and not self.exited():
            self.errors.append('cleanup_required:owned_worker_still_running')
            return
        if self.hpc.value:
            handle,self.hpc = self.hpc,wintypes.HANDLE()
            closer = threading.Thread(target=self.api.ClosePseudoConsole,args=(handle,),daemon=True)
            closer.start()
            closer.join(10)
            if closer.is_alive():
                self.errors.append('ClosePseudoConsole:timeout')
        self.close_handle('input_read')
        self.close_handle('output_write')
        if self.reader:
            self.reader.join(2)
            if self.reader.is_alive():
                self.api.cancel_thread_io(self.reader)
                self.reader.join(2)
        self.close_handle('input_write')
        if self.reader is None or not self.reader.is_alive():
            self.close_handle('output_read')
        else:
            self.errors.append('reader:shutdown_timeout')
        if self.process.hProcess:
            self.api.CloseHandle(self.process.hProcess)
            self.process.hProcess = None
        with self.lock:
            self.data.clear()


def redact(data,startup_url):
    if startup_url:
        fragment = startup_url.split('#',1)[1]
        secrets = [startup_url,fragment]
        if '=' in fragment:
            secrets.append(fragment.split('=',1)[1])
        for secret in secrets:
            data = data.replace(secret.encode(),b'[startup-credential-redacted]')
    return data


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--worker',action='store_true',help=argparse.SUPPRESS)
    parser.add_argument('--binary',required=True,type=Path)
    parser.add_argument('--fixture',type=Path)
    parser.add_argument('--evidence',type=Path)
    parser.add_argument('--node',default=os.environ.get('SVNTUI_NODE','node'))
    parser.add_argument('--script',type=Path,default=Path(__file__).with_name('browser-probe.cjs'))
    parser.add_argument('--columns',type=int,default=320)
    parser.add_argument('--rows',type=int,default=60)
    parser.add_argument('--wc',type=Path,help=argparse.SUPPRESS)
    parser.add_argument('--worker-result',type=Path,help=argparse.SUPPRESS)
    args = parser.parse_args()
    if os.name!='nt':
        parser.error('requires native Windows 10 1809+/Server 2019+; no remote operation is performed')
    if args.worker:
        return worker(args)
    if not args.fixture or not args.evidence:
        parser.error('--fixture and --evidence are required')
    if args.columns<240 or args.columns>32767 or args.rows<24 or args.rows>32767:
        parser.error('columns must be 240..32767, rows 24..32767')
    manifest = json.loads(args.fixture.read_text(encoding='utf-8'))
    binary = args.binary.resolve(strict=True)
    args.evidence = args.evidence.resolve()
    args.evidence.mkdir(parents=True,exist_ok=True)
    run_id = 'windows-web-'+time.strftime('%Y%m%d-%H%M%S-')+uuid.uuid4().hex[:8]
    worker_result = args.evidence/(run_id+'-worker.json')
    summary_path = args.evidence/(run_id+'-bridge.json')
    env = os.environ.copy()
    env.update(manifest['env'])
    env.update(PYTHONUTF8='1',PYTHONIOENCODING='utf-8')
    for key in ('SVNTUI_WEB_URL','SVNTUI_STARTUP_TOKEN'):
        env.pop(key,None)
    report = {'run_id':run_id,'bridge':'Windows ConPTY','controller_pid':os.getpid(),
              'platform':platform.platform(),'architecture':platform.machine(),'python':sys.version,
              'binary':str(binary),'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),
              'fixture':str(args.fixture.resolve()),'fixture_root':manifest['root'],'wc':manifest['wc'],
              'evidence':str(args.evidence),'worker_result':str(worker_result),
              'columns':args.columns,'rows':args.rows,'startup_link_retained':False,
              'raw_console_retained':False,'probe_only':args.script.name=='browser-probe.cjs',
              'state':'starting','worker_pid':None,'server_pid':None,'cleanup_required':True}
    save_json(summary_path,report)
    session,startup_url = None,None
    try:
        session = NativeSession(args,env,Path(manifest['wc']),worker_result)
        report['worker_pid'] = session.process.dwProcessId
        save_json(summary_path,report)
        startup_url = session.startup_url()
        env['SVNTUI_WEB_URL'] = startup_url
        env['SVNTUI_BROWSER_EVIDENCE'] = str(args.evidence)
        env['SVNTUI_FIXTURE_MANIFEST'] = str(args.fixture.resolve())
        report['state'] = 'browser_running'
        save_json(summary_path,report)
        browser = subprocess.run([args.node,str(args.script.resolve())],env=env,capture_output=True,
                                 timeout=int(os.environ.get("SVNTUI_BROWSER_TIMEOUT","600")),creationflags=subprocess.CREATE_NO_WINDOW)
        for name,data in (('stdout',browser.stdout),('stderr',browser.stderr)):
            (args.evidence/(run_id+'-browser-'+name+'.txt')).write_bytes(redact(data,startup_url))
        report.update(browser_exit_code=browser.returncode,state='browser_finished')
    except BaseException as exc:
        # Exception messages can include child arguments: retain only the type.
        report.update(state='failed',error_type=type(exc).__name__)
    finally:
        if session is not None:
            try:
                report['ctrl_c_exit_observed'] = session.interrupt_and_wait()
            except BaseException as exc:
                report['shutdown_error_type'] = type(exc).__name__
            report['cleanup_required'] = not session.exited()
            session.close()
            report['conpty_errors'] = session.errors
        if worker_result.exists():
            observed = json.loads(worker_result.read_text(encoding='utf-8'))
            report['worker'] = observed
            report['server_pid'] = observed.get('server_pid')
            report['cleanup_required'] = report['cleanup_required'] or observed.get('cleanup_required',False)
        save_json(summary_path,report)
    print(json.dumps(report,ensure_ascii=True,indent=2))
    return 0 if report.get('browser_exit_code')==0 and not report['cleanup_required'] and not report.get('conpty_errors') else 1


if __name__=='__main__':
    raise SystemExit(main())
