"""Supervise one pinned Windows executable in its own hidden console."""
import ctypes
from ctypes import wintypes
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys


def respond(**fields):
    print(json.dumps(fields), flush=True)


def same_process(child, binary, digest):
    # Popen keeps this native handle until this supervisor exits. The process
    # object stays alive across the signal and wait even after its PID exits.
    if child.poll() is not None:
        raise ValueError("owned process already exited")
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    query = kernel.QueryFullProcessImageNameW
    query.argtypes = (wintypes.HANDLE, wintypes.DWORD, wintypes.LPWSTR, ctypes.POINTER(wintypes.DWORD))
    image = ctypes.create_unicode_buffer(32768)
    size = wintypes.DWORD(len(image))
    if not query(int(child._handle), 0, image, ctypes.byref(size)) or Path(image.value).resolve() != binary:
        raise ValueError("running executable identity differs")
    with binary.open("rb") as stream:
        if hashlib.file_digest(stream, "sha256").hexdigest() != digest:
            raise ValueError("pinned executable digest differs")


def graceful_stop(child, binary, digest):
    same_process(child, binary, digest)
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.FreeConsole()
    if not kernel.AttachConsole(child.pid):
        raise ValueError("cannot attach to owned console")
    try:
        members = (wintypes.DWORD * 8)()
        count = kernel.GetConsoleProcessList(members, 8)
        if count != 2 or set(members[:count]) != {child.pid, os.getpid()}:
            raise ValueError("console has unexpected occupants")
        if not kernel.SetConsoleCtrlHandler(None, True) or not kernel.GenerateConsoleCtrlEvent(0, 0):
            raise ValueError("cannot notify owned console")
        if child.wait(timeout=45) != 0:
            raise ValueError("owned process exited unsuccessfully")
    finally:
        kernel.FreeConsole()


def listener_ready(child, port):
    if child.poll() is not None:
        raise ValueError("owned process exited before handoff")
    netstat = Path(os.environ["SystemRoot"]) / "System32" / "netstat.exe"
    out = subprocess.check_output([str(netstat), "-ano", "-p", "TCP"], timeout=8,
                                  creationflags=subprocess.CREATE_NO_WINDOW)
    owned = set()
    for line in out.decode("ascii", errors="replace").splitlines():
        fields = line.split()
        if len(fields) == 5 and fields[0] == "TCP" and fields[-1] == str(child.pid) and fields[2] in ("0.0.0.0:0", "[::]:0"):
            owned.add(fields[1])
    if owned and owned != {"127.0.0.1:" + str(port)}:
        raise ValueError("owned process opened unexpected listeners")
    return bool(owned)


def main():
    binary = Path(sys.argv[1]).resolve(strict=True)
    digest = sys.argv[2]
    port = int(os.environ["APP_ADDR"].removeprefix("127.0.0.1:"))
    with binary.open("rb") as stream:
        if hashlib.file_digest(stream, "sha256").hexdigest() != digest:
            raise ValueError("executable changed before launch")
    startup = subprocess.STARTUPINFO()
    startup.dwFlags |= subprocess.STARTF_USESHOWWINDOW
    startup.wShowWindow = subprocess.SW_HIDE
    with Path(sys.argv[3]).open("xb") as log:
        child = subprocess.Popen([str(binary)], cwd=Path(sys.argv[3]).parent,
                                 stdin=subprocess.DEVNULL, stdout=log, stderr=log,
                                 creationflags=subprocess.CREATE_NEW_CONSOLE, startupinfo=startup)
    stop_attempted = False
    try:
        same_process(child, binary, digest)
        respond(launched=True)
        for command in sys.stdin:
            if command.strip() == "listener":
                respond(listening=listener_ready(child, port))
            elif command.strip() == "stop":
                stop_attempted = True
                graceful_stop(child, binary, digest)
                respond(stopped=True, exitCode=child.returncode)
                return
            else:
                raise ValueError("unsupported control command")
    finally:
        if not stop_attempted and child.poll() is None:
            graceful_stop(child, binary, digest)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # Do not echo command lines, environment, URLs or raw service logs.
        respond(error=type(error).__name__)
        sys.exit(1)
