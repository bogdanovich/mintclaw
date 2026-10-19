#!/usr/bin/env python3
"""Run only an attested, owned Xvfb. No existing display or ambient Xauthority."""
import argparse
import hashlib
import os
from pathlib import Path
import re
import secrets
import select
import struct
import subprocess


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--xvfb", type=Path, required=True)
    parser.add_argument("--xvfb-sha256", required=True)
    parser.add_argument("--probe", type=Path, required=True)
    parser.add_argument("--probe-sha256", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--repetitions", type=int, choices=range(1, 21), default=20)
    parser.add_argument("--gtk", action="store_true")
    parser.add_argument("--process-worker", type=Path)
    parser.add_argument("--process-worker-sha256")
    parser.add_argument("--process-modes", nargs="+", default=["normal"])
    parser.add_argument("--host-probe", type=Path)
    parser.add_argument("--host-probe-sha256")
    args = parser.parse_args()
    if args.host_probe and (not args.gtk or not args.process_worker):
        parser.error("host qualification requires the explicit private process worker and fixture environment")
    executables = [(args.xvfb, args.xvfb_sha256), (args.probe, args.probe_sha256)]
    if args.host_probe:
        executables.append((args.host_probe, args.host_probe_sha256))
    for path, expected in executables:
        if not path.is_absolute() or path.is_symlink() or not path.is_file() or digest(path) != expected:
            raise RuntimeError("private fixture executable identity mismatch")
    if not args.output.is_absolute() or args.output.is_symlink():
        raise RuntimeError("explicit new private output directory required")
    args.output.mkdir(mode=0o700, exist_ok=False)
    for attempt in range(1, args.repetitions + 1):
        work = args.output / str(attempt)
        work.mkdir(mode=0o700)
        cookie = secrets.token_bytes(16)
        auth = work / "fixture.auth"
        fields = [b"", b"", b"MIT-MAGIC-COOKIE-1", cookie]
        # Wildcard record on this owned server only; it still requires its
        # random cookie. No -ac or system/private-desktop permission changes.
        with auth.open("xb") as handle:
            os.chmod(auth, 0o600)
            handle.write(struct.pack(">H", 65535))
            for value in fields:
                handle.write(struct.pack(">H", len(value)) + value)
        environment = {"PATH": "/usr/bin:/bin", "TMPDIR": str(work)}
        with (work / "xvfb.log").open("xb") as log:
            server = subprocess.Popen([str(args.xvfb), "-displayfd", "1", "-screen", "0", "640x480x24",
                                       "-nolisten", "tcp", "-noreset", "-auth", str(auth)],
                                      stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=log,
                                      env=environment)
            try:
                if not select.select([server.stdout], [], [], 10)[0]:
                    raise RuntimeError("owned Xvfb startup deadline")
                # Xvfb writes one newline-terminated display number once ready.
                number = os.read(server.stdout.fileno(), 32).strip().decode("ascii")
                if not number.isdecimal() or server.poll() is not None:
                    raise RuntimeError("owned Xvfb did not attest startup")
                # The server has loaded the same cookie already. Its display
                # number was assigned by -displayfd; now bind Xlib/GTK's explicit
                # client-side record to that number before launching any client.
                with auth.open("wb") as handle:
                    handle.write(struct.pack(">H", 65535))
                    for value in [b"", number.encode("ascii"), b"MIT-MAGIC-COOKIE-1", cookie]:
                        handle.write(struct.pack(">H", len(value)) + value)
                environment.update(MINTCLAW_NATIVE_KEYBOARD_NATIVE="1",
                                   MINTCLAW_PRIVATE_X_DISPLAY_NUMBER=number,
                                   MINTCLAW_PRIVATE_X_COOKIE=cookie.hex(),
                                   MINTCLAW_PRIVATE_XVFB_PID=str(server.pid),
                                   MINTCLAW_PRIVATE_XVFB_EXECUTABLE=str(args.xvfb.resolve()))
                with (work / "probe.log").open("xb") as probe_log:
                    subprocess.run([str(args.probe), "mintclaw_native_keyboard::native::", "--ignored",
                                    "--test-threads=1", "--nocapture"], env=environment,
                                   stdin=subprocess.DEVNULL, stdout=probe_log, stderr=subprocess.STDOUT,
                                   timeout=20, check=True)
                report = (work / "probe.log").read_text()
                if len(re.findall(r"^test result: ok\. 10 passed; 0 failed; 0 ignored;", report, re.M)) != 1:
                    raise RuntimeError("native keyboard cases missing")
                if args.gtk:
                    environment.update(DISPLAY=f":{number}", XAUTHORITY=str(auth), NO_AT_BRIDGE="1",
                                       GDK_BACKEND="x11", GSETTINGS_BACKEND="memory",
                                       DBUS_SESSION_BUS_ADDRESS=f"unix:path={work}/no-session-bus",
                                       XDG_CONFIG_HOME=str(work / "config"), XDG_CACHE_HOME=str(work / "cache"),
                                       XDG_DATA_HOME=str(work / "data"))
                    modes = [None] + args.process_modes if args.process_worker else [None]
                    for mode in modes:
                        command = ["/usr/bin/python3", str(Path(__file__).with_name("native-keyboard-gtk-fixture.py")),
                                   "--probe", str(args.probe), "--output", str(work / ("gtk" if mode is None else mode))]
                        if mode is not None:
                            command.extend(["--process-worker", str(args.process_worker), "--process-mode", mode,
                                            "--process-worker-sha256", args.process_worker_sha256])
                        subprocess.run(command, env=environment, stdin=subprocess.DEVNULL, timeout=25, check=True)
                if args.host_probe:
                    environment.update(MINTCLAW_NATIVE_X11_GUARD_TEST="1", MINTCLAW_NATIVE_X11_LEASE_TEST="1",
                                       MINTCLAW_PRIVATE_NATIVE_HOST_TEST="1",
                                       MINTCLAW_PRIVATE_NATIVE_HOST_WORKER=str(args.process_worker),
                                       MINTCLAW_PRIVATE_NATIVE_HOST_WORKER_SHA256=args.process_worker_sha256)
                    with (work / "host.log").open("xb") as host_log:
                        subprocess.run([str(args.host_probe), "-test.run=^TestPrivateNativeKeyboard", "-test.v",
                                        "-test.count=1", "-test.timeout=30s"], env=environment,
                                       stdin=subprocess.DEVNULL, stdout=host_log, stderr=subprocess.STDOUT,
                                       timeout=35, check=True)
                    host_report = (work / "host.log").read_text()
                    if (len(re.findall(r"^--- PASS: TestPrivateNativeKeyboard", host_report, re.M)) != 7
                            or "SKIP" in host_report or "FAIL" in host_report):
                        raise RuntimeError("original Go host generation cases missing")
                    print(f"original Go host fixture round {attempt}: seven cases PASS", flush=True)
                print(f"native-keyboard fixture round {attempt}: ten cases PASS", flush=True)
            finally:
                server.stdout.close()
                if server.poll() is None:
                    server.terminate()
                    try:
                        server.wait(timeout=3)
                    except subprocess.TimeoutExpired:
                        server.kill()
                        server.wait(timeout=3)
    print("Mechanism fixture only; full driver/host/model qualification remains required.", flush=True)


if __name__ == "__main__":
    main()
