"""Cold-copy a dedicated, versioned SeaweedFS instance on Windows."""
import argparse
import contextlib
import ctypes
from ctypes import wintypes
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import stat
import subprocess
import sys
import time

PIN = "38c92cf30b098ffcad2b7b6d2702c317ef20b8571d252fd44d07e83517c1353b"
HERE = Path(__file__).resolve().parent
REPO = next(p for p in HERE.parents if (p / "go.mod").is_file() and (p / "db" / "migrations").is_dir())
PORT_FLAGS = ("master", "master_grpc", "volume", "volume_grpc", "filer", "filer_grpc", "s3", "s3_grpc")


def read(path):
    return json.loads(path.read_text(encoding="utf-8"))


def write(path, data):
    temp = path.with_suffix(".new-" + secrets.token_hex(6))
    with temp.open("x", encoding="utf-8") as out:
        json.dump(data, out, indent=2)
        out.flush()
        os.fsync(out.fileno())
    temp.replace(path)


def sha(path):
    with path.open("rb") as src:
        return hashlib.file_digest(src, "sha256").hexdigest()


def plain(path):
    for p in (path, *path.parents):
        if p.exists() and getattr(p.lstat(), "st_file_attributes", 0) & stat.FILE_ATTRIBUTE_REPARSE_POINT:
            raise ValueError("drill path contains a reparse point")
    return path


def manifest(tree):
    result = {}
    for folder, dirs, files in os.walk(tree):
        for name in sorted(dirs):
            plain(Path(folder) / name)
        for name in sorted(files):
            p = plain(Path(folder) / name)
            if not p.is_file():
                raise ValueError("unexpected service file type")
            result[p.relative_to(tree).as_posix()] = {"size": p.stat().st_size, "sha256": sha(p)}
    if not result:
        raise ValueError("no service files in cold copy")
    return result


def reserve_ports(n, exclude=()):
    with contextlib.ExitStack() as stack:
        numbers = []
        while len(numbers) < n:
            s = stack.enter_context(socket.socket())
            s.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
            if port not in exclude:
                numbers.append(port)
        return numbers


def kernel():
    k = ctypes.WinDLL("kernel32", use_last_error=True)
    k.OpenProcess.argtypes = (wintypes.DWORD, wintypes.BOOL, wintypes.DWORD)
    k.OpenProcess.restype = wintypes.HANDLE
    k.CloseHandle.argtypes = (wintypes.HANDLE,)
    k.GetProcessTimes.argtypes = (wintypes.HANDLE,) + (ctypes.POINTER(wintypes.FILETIME),) * 4
    k.QueryFullProcessImageNameW.argtypes = (wintypes.HANDLE, wintypes.DWORD, wintypes.LPWSTR, ctypes.POINTER(wintypes.DWORD))
    k.WaitForSingleObject.argtypes = (wintypes.HANDLE, wintypes.DWORD)
    k.WaitForSingleObject.restype = wintypes.DWORD
    return k


@contextlib.contextmanager
def identity(pid):
    k = kernel()
    handle = k.OpenProcess(0x00100000 | 0x1000, False, pid)
    if not handle:
        if ctypes.get_last_error() == 87:
            yield None
            return
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        buf = ctypes.create_unicode_buffer(32768)
        size = wintypes.DWORD(len(buf))
        stamps = [wintypes.FILETIME() for _ in range(4)]
        if not k.QueryFullProcessImageNameW(handle, 0, buf, ctypes.byref(size)) or not k.GetProcessTimes(handle, *(ctypes.byref(x) for x in stamps)):
            raise ctypes.WinError(ctypes.get_last_error())
        birth = (stamps[0].dwHighDateTime << 32) | stamps[0].dwLowDateTime
        yield handle, str(Path(buf.value).resolve()), birth
    finally:
        k.CloseHandle(handle)


def verify_listener(pid, ports):
    found = set()
    netstat = Path(os.environ["SystemRoot"]) / "System32" / "netstat.exe"
    for line in subprocess.check_output([str(netstat), "-ano", "-p", "TCP"], encoding="ascii", errors="replace").splitlines():
        fields = line.split()
        if len(fields) == 5 and fields[0] == "TCP" and fields[-1] == str(pid) and fields[2] in ("0.0.0.0:0", "[::]:0"):
            host, port = fields[1].rsplit(":", 1)
            if host != "127.0.0.1":
                raise ValueError("local drill listener escaped loopback")
            found.add(int(port))
    if found != set(ports.values()):
        raise ValueError(f"listener check expected {len(ports)} loopback ports, found {len(found)}")


def notify_stop(root):
    # Attach a helper to this instance's private, hidden console. CTRL_C is
    # delivered only to that console, with no signal sent to the parent shell.
    state = read(plain(root / "state.json"))
    run = state["running"]
    with identity(run["pid"]) as item:
        if state["root"] != str(root) or item is None or item[1:] != (state["weed"], run["birth"]) or sha(Path(item[1])) != PIN:
            raise ValueError("signal target is not the named drill instance")
        k = ctypes.WinDLL("kernel32", use_last_error=True)
        k.FreeConsole()
        if not k.AttachConsole(run["pid"]) or not k.SetConsoleCtrlHandler(None, True) or not k.GenerateConsoleCtrlEvent(0, 0):
            raise ctypes.WinError(ctypes.get_last_error())
        time.sleep(0.3)
        k.FreeConsole()


def stop(root, state):
    run = state.get("running")
    if not run:
        return
    with identity(run["pid"]) as item:
        if item is None or item[1:] != (state["weed"], run["birth"]):
            raise ValueError("owned instance disappeared or PID changed; cold snapshot refused")
        handle = item[0]
        if kernel().WaitForSingleObject(handle, 0) == 0:
            raise ValueError("owned instance exited before clean shutdown")
        subprocess.run([sys.executable, str(Path(__file__).resolve()), "_signal", str(root)], check=True, creationflags=subprocess.CREATE_NO_WINDOW)
        if kernel().WaitForSingleObject(handle, 60000) != 0:
            raise ValueError("owned instance did not stop gracefully; data preserved")
    state["stopped"] = state.pop("running")
    state.setdefault("shutdown_history", []).append(state["stopped"])
    write(root / "state.json", state)


def probe(root, command, versions=None):
    cfg = root / "s3.json"
    args = [str(root / "s3probe.exe"), command, str(cfg), str(versions or root / "versions.json")]
    result = subprocess.run(args, capture_output=True, timeout=85)
    if result.returncode:
        # Credentials and private object identities never enter terminal output.
        (root / "probe-error.log").write_bytes(result.stderr)
        raise ValueError("local S3 probe failed; inspect the private probe-error.log")


def launch(root, state, name, initialize):
    tree = root / name
    binary = Path(state["weed"])
    if sha(binary) != PIN:
        raise ValueError("unexpected SeaweedFS binary digest")
    ports = dict(state["ports"])
    if not initialize:
        # The workbench binds persisted object receipts to the original
        # endpoint. Keep the service address and raft identities unchanged.
        with contextlib.ExitStack() as stack:
            for port in ports.values():
                s = stack.enter_context(socket.socket())
                s.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
                s.bind(("127.0.0.1", port))
    args = [str(binary), "-logtostderr=true", "-config_dir=.", "server", "-ip=127.0.0.1", "-ip.bind=127.0.0.1",
            "-s3.ip.bind=127.0.0.1", "-s3", "-s3.config=identities.json", "-dir=volumes", "-master.dir=master", "-master.volumeSizeLimitMB=8",
            "-volume.max=24", "-volume.minFreeSpace=1MiB", "-s3.port.iceberg=0", "-s3.port.lance=0", "-master.volumePreallocate=false"]
    for item, value in ports.items():
        role, *grpc = item.split("_")
        args.append(f"-{role}.port{'.grpc' if grpc else ''}={value}")
    cfg = read(root / "s3.json")
    cfg["TEST_S3_ENDPOINT"] = f"http://127.0.0.1:{ports['s3']}"
    write(root / "s3.json", cfg)
    si = subprocess.STARTUPINFO()
    si.dwFlags |= subprocess.STARTF_USESHOWWINDOW
    si.wShowWindow = subprocess.SW_HIDE
    env = {key: value for key, value in os.environ.items() if not key.upper().startswith(("WEED_", "SEAWEED", "AWS_"))}
    with (root / (name + "-server.log")).open("ab") as log:
        child = subprocess.Popen(args, cwd=tree, env=env, stdin=subprocess.DEVNULL, stdout=log, stderr=log, creationflags=subprocess.CREATE_NEW_CONSOLE, startupinfo=si)
    with identity(child.pid) as item:
        if item is None or item[1] != str(binary):
            raise ValueError("unexpected service process identity")
        state["running"] = {"pid": child.pid, "birth": item[2], "tree": name, "ports": ports}
    write(root / "state.json", state)
    try:
        probe(root, "init" if initialize else "ready")
        verify_listener(child.pid, ports)
    except BaseException:
        if child.poll() is None:
            stop(root, state)
        raise
    print(f"{name} running on loopback; {len(ports)} listeners, settings in {root / 's3.json'}")


def begin(root, weed):
    if not weed or not weed.is_file() or sha(weed) != PIN:
        raise ValueError("supply pinned SeaweedFS Windows 4.53 executable via --weed")
    if root.exists() and any(p.name != "drill.lock" for p in root.iterdir()):
        raise ValueError("start requires a new empty drill directory")
    root.mkdir(parents=True, exist_ok=True)
    source = root / "source"
    source.mkdir()
    for d in ("volumes", "master", "filer-meta"):
        (source / d).mkdir()
    (source / "filer.toml").write_text('[leveldb2]\nenabled = true\ndir = "./filer-meta"\n', encoding="utf-8")
    key, secret = secrets.token_hex(12), secrets.token_hex(32)
    write(source / "identities.json", {"identities": [{"name": "isolated-drill", "credentials": [{"accessKey": key, "secretKey": secret}], "actions": ["Admin", "Read", "Write", "List", "Tagging"]}]})
    write(root / "s3.json", {"TEST_S3_BUCKET": "drill-" + secrets.token_hex(8), "TEST_S3_REGION": "us-east-1",
                              "TEST_S3_ACCESS_KEY_ID": key, "TEST_S3_SECRET_ACCESS_KEY": secret})
    ports = dict(zip(PORT_FLAGS, reserve_ports(len(PORT_FLAGS))))
    state = {"format": 1, "root": str(root), "weed": str(weed), "ports": ports}
    write(root / "state.json", state)
    buildenv = dict(os.environ, GOPROXY="off")
    subprocess.run(["go", "build", "-o", str(root / "s3probe.exe"), str(HERE / "s3probe" / "main.go")], cwd=REPO, env=buildenv, check=True)
    launch(root, state, "source", True)


def cold_copy(root, source, target, expected):
    src, dest = root / source, root / target
    if dest.exists():
        raise ValueError(f"{target} already exists; preserving it")
    before = manifest(src)
    if expected is not None and expected != before:
        raise ValueError("saved snapshot differs from the recorded cold backup")
    shutil.copytree(src, dest)
    if manifest(dest) != before or manifest(src) != before:
        raise ValueError("service files changed or the cold copy differs")
    return before


def action(root, args):
    if args.action == "start":
        begin(root, args.weed.resolve() if args.weed else None)
        return
    state = read(plain(root / "state.json"))
    if state.get("root") != str(root) or state.get("format") != 1:
        raise ValueError("directory does not belong to this drill")
    running = state.get("running")
    if args.action == "backup":
        if running and running["tree"] != "source":
            raise ValueError("backup requires the original drill source")
        stop(root, state)
        if state.get("stopped", {}).get("tree") != "source":
            raise ValueError("no confirmed clean source shutdown")
        files = cold_copy(root, "source", "backup", None)
        write(root / "backup-manifest.json", files)
        print(f"cold backup verified, {len(files)} files and {sum(v['size'] for v in files.values())} bytes")
    elif args.action == "restore":
        if running or state.get("stopped", {}).get("tree") != "source":
            raise ValueError("restore requires an offline source")
        files = read(root / "backup-manifest.json")
        cold_copy(root, "backup", "restored", files)
        launch(root, state, "restored", False)
    elif args.action == "stop":
        stop(root, state)
        print("owned service stopped")
    else:
        if not running or (args.action == "seed" and running["tree"] != "source"):
            raise ValueError("operation requires its own active drill instance")
        with identity(running["pid"]) as item:
            if item is None or item[1:] != (state["weed"], running["birth"]):
                raise ValueError("service identity changed")
            verify_listener(running["pid"], running["ports"])
            if args.action == "seed" and args.versions and not plain(args.versions.absolute()).resolve().is_relative_to(root):
                raise ValueError("seed manifest must be inside the owned drill directory")
            probe(root, args.action, args.versions)
        print("original version IDs and SHA-256 verified")


if __name__ == "__main__":
    if sys.platform != "win32":
        sys.exit("the pinned local drill requires Windows")
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "_signal":
            notify_stop(plain(Path(sys.argv[2]).absolute()).resolve())
        else:
            parser = argparse.ArgumentParser(description=__doc__)
            parser.add_argument("action", choices=("start", "seed", "backup", "restore", "verify", "stop"))
            parser.add_argument("--root", required=True, type=Path)
            parser.add_argument("--weed", type=Path, help="pinned weed.exe used only when creating a new drill")
            parser.add_argument("--versions", type=Path, help="version manifest for verify, or a new output for seed")
            args = parser.parse_args()
            root = plain(Path(os.path.abspath(args.root))).resolve()
            if args.action == "start":
                if root.exists() and any(root.iterdir()):
                    raise ValueError("start requires a new empty drill directory")
                root.mkdir(parents=True, exist_ok=True)
            elif not (root / "state.json").is_file():
                raise ValueError("no existing drill at the requested root")
            import msvcrt
            with (root / "drill.lock").open("a+b") as lock:
                lock.seek(0)
                msvcrt.locking(lock.fileno(), msvcrt.LK_NBLCK, 1)
                action(root, args)
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        sys.exit(str(exc))
