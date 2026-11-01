#!/usr/bin/env python3
"""Run only attested, owned Xvfb servers; never an ambient personal desktop."""
import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import select
import struct
import subprocess

from native_state_oracle import FixtureOracle


HOST_CASES = (
    "TestPrivateNativeKeyboardOriginalEpochDeniesFreshNumericReplacement",
    "TestPrivateNativeKeyboardDispatchRetainsHostOwnership",
    "TestPrivateNativeKeyboardGenerationIsRecheckedBeforeEachEdge",
    "TestPrivateNativeKeyboardCancellationDoesNotReleaseHostLease",
    "TestPrivateNativeKeyboardFinishCancellationDoesNotPoisonNextAction",
    "TestPrivateNativeKeyboardReusesChannelAfterOldTargetCloses",
    "TestPrivateNativeKeyboardFreshOwnerRefusesRetainedDevices",
    "TestPrivateNativeKeyboardLeaseLossStopsHeldInput",
    "TestPrivateNativeKeyboardWireLossRetainsUnknownHostOwnership",
    "TestPrivateNativeKeyboardKeeperLossRetainsUnknownHostOwnership",
    "TestPrivateNativeKeyboardCancellationInterruptsBlockedChildAcknowledgement",
)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


@contextmanager
def owned_server(executable, work):
    work.mkdir(mode=0o700)
    cookie, auth = secrets.token_bytes(16), work / "fixture.auth"

    def write_auth(number, mode):
        with auth.open(mode) as handle:
            os.chmod(auth, 0o600)
            handle.write(struct.pack(">H", 65535))
            for value in [b"", number, b"MIT-MAGIC-COOKIE-1", cookie]:
                handle.write(struct.pack(">H", len(value)) + value)

    # Wildcard applies only to this owned server's random cookie. No -ac,
    # personal Xauthority, production display, or permission changes.
    write_auth(b"", "xb")
    environment = {"PATH": "/usr/bin:/bin", "TMPDIR": str(work)}
    with (work / "xvfb.log").open("xb") as log:
        server = subprocess.Popen([str(executable), "-displayfd", "1", "-screen", "0", "640x480x24",
                                   "-nolisten", "tcp", "-noreset", "-auth", str(auth)],
                                  stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=log,
                                  env=environment)
        try:
            if not select.select([server.stdout], [], [], 10)[0]:
                raise RuntimeError("owned Xvfb startup deadline")
            number = os.read(server.stdout.fileno(), 32).strip().decode("ascii")
            if (not number.isdecimal() or server.poll() is not None
                    or Path(f"/proc/{server.pid}/exe").resolve() != executable.resolve()):
                raise RuntimeError("owned Xvfb did not attest startup")
            write_auth(number.encode("ascii"), "wb")
            environment.update(MINTCLAW_NATIVE_KEYBOARD_NATIVE="1",
                               MINTCLAW_PRIVATE_X_DISPLAY_NUMBER=number,
                               MINTCLAW_PRIVATE_X_COOKIE=cookie.hex(),
                               MINTCLAW_PRIVATE_XVFB_PID=str(server.pid),
                               MINTCLAW_PRIVATE_XVFB_EXECUTABLE=str(executable.resolve()),
                               DISPLAY=f":{number}", XAUTHORITY=str(auth), NO_AT_BRIDGE="1",
                               GDK_BACKEND="x11", GSETTINGS_BACKEND="memory",
                               DBUS_SESSION_BUS_ADDRESS=f"unix:path={work}/no-session-bus",
                               XDG_CONFIG_HOME=str(work / "config"), XDG_CACHE_HOME=str(work / "cache"),
                               XDG_DATA_HOME=str(work / "data"))
            yield environment
        finally:
            server.stdout.close()
            if server.poll() is None:
                server.terminate()
                try:
                    server.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    server.kill()
                    server.wait(timeout=3)


def host_case(args, name, environment, work):
    environment.update(MINTCLAW_NATIVE_X11_GUARD_TEST="1", MINTCLAW_NATIVE_X11_LEASE_TEST="1",
                       MINTCLAW_PRIVATE_NATIVE_HOST_TEST="1",
                       MINTCLAW_PRIVATE_NATIVE_HOST_WORKER=str(args.process_worker),
                       MINTCLAW_PRIVATE_NATIVE_HOST_WORKER_SHA256=args.process_worker_sha256)
    oracle = FixtureOracle(environment)
    try:
        with (work / "host.log").open("xb") as log:
            subprocess.run([str(args.host_probe), f"-test.run=^{name}$", "-test.v",
                            "-test.count=1", "-test.timeout=30s"], env=environment,
                           stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT,
                           timeout=35, check=True)
        report = (work / "host.log").read_text()
        if (len(re.findall(r"^--- PASS: " + re.escape(name) + r" \(", report, re.M)) != 1
                or any(marker in report for marker in ("SKIP", "FAIL", "DATA RACE"))):
            raise RuntimeError("original Go host generation case missing")
        # This check occurs after the Go fixture exits. Its retained-window,
        # same-channel and caller-lease assertions run inside the Go case.
        expected = 0 if name == HOST_CASES[0] else 4
        retained = oracle.verify_retained_quiescence(expected)
        with (work / "result.json").open("x") as handle:
            json.dump({"case": name, "actual_go_race_test_passed": True,
                       "original_devices_preserved": True, "retained_devices": retained,
                       "independent_native_master_and_slave_clear_after_test": True,
                       "full_driver_qualified": False}, handle, sort_keys=True)
    finally:
        oracle.close()


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
    parser.add_argument("--process-modes", nargs="+", default=["normal"], choices=(
        "normal", "cancel_held", "blocked_stop", "crash_held", "foreign_stop", "duplicate_permit", "wire_loss",
        "keeper_crash_held", "group_term_held", "late_grab_repeat", "deadline_io"))
    parser.add_argument("--host-probe", type=Path)
    parser.add_argument("--host-probe-sha256")
    args = parser.parse_args()
    if args.process_worker and not args.gtk:
        parser.error("process qualification requires the explicit GTK fixture environment")
    if args.host_probe and (not args.gtk or not args.process_worker):
        parser.error("host qualification requires the explicit private process worker and fixture environment")
    executables = [(args.xvfb, args.xvfb_sha256), (args.probe, args.probe_sha256)]
    if args.process_worker:
        executables.append((args.process_worker, args.process_worker_sha256))
    if args.host_probe:
        executables.append((args.host_probe, args.host_probe_sha256))
    for path, expected in executables:
        if not path.is_absolute() or path.is_symlink() or not path.is_file() or digest(path) != expected:
            raise RuntimeError("private fixture executable identity mismatch")
    if args.host_probe:
        listed = subprocess.run([str(args.host_probe), "-test.list=^TestPrivateNativeKeyboard"],
                                env={"PATH": "/usr/bin:/bin"}, stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                timeout=10, check=True, text=True).stdout
        names = [line for line in listed.splitlines() if line.startswith("TestPrivateNativeKeyboard")]
        if len(names) != len(HOST_CASES) or set(names) != set(HOST_CASES):
            raise RuntimeError("original Go host case inventory changed")
    if not args.output.is_absolute() or args.output.is_symlink():
        raise RuntimeError("explicit new private output directory required")
    args.output.mkdir(mode=0o700, exist_ok=False)
    gtk_script = str(Path(__file__).with_name("native-keyboard-gtk-fixture.py"))
    for attempt in range(1, args.repetitions + 1):
        work = args.output / str(attempt)
        work.mkdir(mode=0o700)
        with owned_server(args.xvfb, work / "native") as environment:
            with (work / "probe.log").open("xb") as log:
                subprocess.run([str(args.probe), "mintclaw_native_keyboard::native::", "--ignored",
                                "--test-threads=1", "--nocapture"], env=environment,
                               stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT,
                               timeout=20, check=True)
            report = (work / "probe.log").read_text()
            if len(re.findall(r"^test result: ok\. 34 passed; 0 failed; 0 ignored;", report, re.M)) != 1:
                raise RuntimeError("native keyboard cases missing")
            if args.gtk:
                subprocess.run(["/usr/bin/python3", gtk_script, "--probe", str(args.probe),
                                "--output", str(work / "gtk")], env=environment,
                               stdin=subprocess.DEVNULL, timeout=25, check=True)
        if args.process_worker:
            # Ending a resource owner leaves a quarantined native pair. A new
            # independent owner must not adopt it. Every fault case therefore
            # owns its own server; do not delete devices to make tests pass.
            for mode in args.process_modes:
                with owned_server(args.xvfb, work / ("server-" + mode)) as environment:
                    subprocess.run(["/usr/bin/python3", gtk_script, "--probe", str(args.probe),
                                    "--output", str(work / mode), "--process-worker", str(args.process_worker),
                                    "--process-mode", mode, "--process-worker-sha256", args.process_worker_sha256],
                                   env=environment, stdin=subprocess.DEVNULL, timeout=25, check=True)
        if args.host_probe:
            for name in HOST_CASES:
                with owned_server(args.xvfb, work / name) as environment:
                    host_case(args, name, environment, work / name)
            print(f"original Go host fixture round {attempt}: eleven cases PASS", flush=True)
        print(f"native-keyboard fixture round {attempt}: all required cases PASS", flush=True)
    print("Mechanism fixture only; full driver/host/model qualification remains required.", flush=True)


if __name__ == "__main__":
    main()
