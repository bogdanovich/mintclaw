#!/usr/bin/env python3
"""Independent GTK state oracle for the actual private native keyboard engine."""
import argparse
import ctypes as C
import hashlib
import json
import os
from pathlib import Path
import re
import select
import socket
import struct
import subprocess
import time


def bind(library, name, result, *arguments):
    function = getattr(library, name)
    function.restype, function.argtypes = result, list(arguments)
    return function


class DeviceInfo(C.Structure):
    _fields_ = [("deviceid", C.c_int), ("name", C.c_char_p), ("use", C.c_int),
                ("attachment", C.c_int), ("enabled", C.c_int),
                ("num_classes", C.c_int), ("classes", C.c_void_p)]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--probe", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--process-worker", type=Path)
    parser.add_argument("--process-worker-sha256")
    parser.add_argument("--process-mode", default="normal")
    args = parser.parse_args()
    args.output.mkdir(mode=0o700, exist_ok=False)
    x = C.CDLL("libX11.so.6")
    xi = C.CDLL("libXi.so.6")
    ptr, integer, window = C.c_void_p, C.c_int, C.c_ulong
    opened = bind(x, "XOpenDisplay", ptr, C.c_char_p)(None)
    if not opened:
        raise RuntimeError("explicit private display unavailable")
    close = bind(x, "XCloseDisplay", integer, ptr)
    try:
        number = bind(x, "XConnectionNumber", integer, ptr)(opened)
        with socket.fromfd(number, socket.AF_UNIX, socket.SOCK_STREAM) as peer:
            pid, uid, _ = struct.unpack("3i", peer.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
        if ((pid, uid) != (int(os.environ["MINTCLAW_PRIVATE_XVFB_PID"]), os.getuid())
                or Path(f"/proc/{pid}/exe").resolve() != Path(os.environ["MINTCLAW_PRIVATE_XVFB_EXECUTABLE"]).resolve()):
            raise RuntimeError("not the attested owned virtual server")
        # Never import/initialize GTK before the native server identity check.
        import gi
        gi.require_version("Gtk", "3.0")
        gi.require_version("GdkX11", "3.0")
        from gi.repository import Gtk, Gdk, GdkX11, GLib
        if not Gtk.init_check()[0]:
            raise RuntimeError("private GTK fixture unavailable")
        sync = bind(x, "XSync", integer, ptr, integer)
        root = bind(x, "XDefaultRootWindow", window, ptr)(opened)
        atom = bind(x, "XInternAtom", window, ptr, C.c_char_p, integer)
        set_owner = bind(x, "XSetSelectionOwner", integer, ptr, window, window, window)
        get_owner = bind(x, "XGetSelectionOwner", window, ptr, window)
        create = bind(x, "XCreateWindow", window, ptr, window, integer, integer, C.c_uint, C.c_uint,
                      C.c_uint, integer, C.c_uint, ptr, window, ptr)
        set_focus = bind(x, "XSetInputFocus", integer, ptr, window, integer, window)
        get_focus = bind(x, "XGetInputFocus", integer, ptr, C.POINTER(window), C.POINTER(integer))
        code = bind(x, "XKeysymToKeycode", C.c_ubyte, ptr, window)
        query_version = bind(xi, "XIQueryVersion", integer, ptr, C.POINTER(integer), C.POINTER(integer))
        query = bind(xi, "XIQueryDevice", C.POINTER(DeviceInfo), ptr, integer, C.POINTER(integer))
        free = bind(xi, "XIFreeDeviceInfo", None, C.POINTER(DeviceInfo))
        major, minor = integer(2), integer(0)
        if query_version(opened, C.byref(major), C.byref(minor)) != 0:
            raise RuntimeError("private XI2 unavailable")

        def devices():
            count = integer()
            values = query(opened, 0, C.byref(count))
            if not values:
                raise RuntimeError("private device query unavailable")
            try:
                return sorted((value.deviceid, value.name.decode(), getattr(value, "use"), value.attachment)
                              for value in values[:count.value])
            finally:
                free(values)

        def pump(seconds=0.1):
            context = GLib.MainContext.default()
            until = time.monotonic() + seconds
            while time.monotonic() < until:
                while context.pending():
                    context.iteration(False)
                time.sleep(0.001)

        windows, entries = [], []
        try:
            prefix, suffix = "PREFIX|", "|SUFFIX"
            text = "Привет 世界 café 👩‍💻 e\u0301"
            for index in range(2):
                app = Gtk.Window(title="MintClaw same-title native keyboard fixture")
                app.set_default_size(300, 140)
                app.move(80, 80)
                entry = Gtk.Entry()
                entry.set_text(prefix + suffix if index == 0 else "GUARD_UNCHANGED")
                entry.set_position(len(prefix))
                app.add(entry)
                app.show_all()
                entry.grab_focus()
                windows.append(app)
                entries.append(entry)
            pump()
            target, guard = [GdkX11.X11Window.get_xid(app.get_window()) for app in windows]
            token = create(opened, root, 0, 0, 1, 1, 0, 0, 2, None, 0, None)
            selection = atom(opened, b"_MINTCLAW_COMPUTER_INPUT_CONTROL", 0)
            if not token or get_owner(opened, selection) != 0:
                raise RuntimeError("private fixture lease occupied")
            set_owner(opened, selection, token, 0)
            set_focus(opened, guard, 0, 0)
            sync(opened, 0)
            if get_owner(opened, selection) != token:
                raise RuntimeError("fixture lease was not established")
            pump()
            # Prepare the pre-action caret before native admission, not after
            # it. Resetting it after private focus would hide a regression.
            entries[0].set_text(prefix + suffix)
            entries[0].select_region(len(prefix), len(prefix))
            entries[0].set_position(len(prefix))
            original = devices()
            chords = []

            def keys(*symbols):
                values = [code(opened, symbol) for symbol in symbols]
                if not all(values):
                    raise RuntimeError("existing fixture map lacks explicit qualified chord")
                chords.append(values)

            # ASCII first, then delete it. No clipboard, keymap mutation or
            # guessed app-name fallback: explicitly qualify GTK IMContextSimple.
            keys(ord("a"))
            keys(0xff08)
            for character in text:
                keys(0xffe3, 0xffe1, ord("u"))
                for digit in format(ord(character), "x"):
                    keys(ord(digit))
                keys(0xff0d)
            if args.process_worker:
                executable = args.process_worker
                if (not executable.is_absolute() or executable.is_symlink() or not executable.is_file()
                        or hashlib.sha256(executable.read_bytes()).hexdigest() != args.process_worker_sha256):
                    raise RuntimeError("explicit private process artifact identity mismatch")
                from native_keyboard_process_fixture import run

                def process_ready(keyboard):
                    known = Gdk.Display.get_default().get_device_manager().list_devices(Gdk.DeviceType.MASTER)
                    if not any(device.get_property("device-id") == keyboard for device in known):
                        return False
                    if (entries[0].get_position() != len(prefix) or entries[0].get_selection_bounds()
                            or entries[0].get_text() != prefix + suffix):
                        raise RuntimeError("process admission changed the original fixture caret")
                    return True

                def process_observed():
                    return (entries[0].get_text().encode() == (prefix + text + suffix).encode()
                            and entries[1].get_text() == "GUARD_UNCHANGED")

                outcome = run(executable, args.process_mode,
                              {"socket": f"/tmp/.X11-unix/X{os.environ['MINTCLAW_PRIVATE_X_DISPLAY_NUMBER']}",
                               "screen": 0, "auth": list(bytes.fromhex(os.environ["MINTCLAW_PRIVATE_X_COOKIE"])),
                               "server": {"pid": int(os.environ["MINTCLAW_PRIVATE_XVFB_PID"]),
                                          "uid": os.getuid(), "root": root}},
                              {"window": target, "pid": os.getpid(), "lease_window": token,
                               "lease_pid": os.getpid()}, chords, pump, process_ready, process_observed)
                pump()
                focus, revert = window(), integer()
                get_focus(opened, C.byref(focus), C.byref(revert))
                normal = args.process_mode in {"normal", "foreign_stop", "duplicate_permit"}
                expected = prefix + (text if normal else "a") + suffix
                checks = {"original_caret_effect": entries[0].get_text().encode() == expected.encode(),
                          "guard_unchanged": entries[1].get_text() == "GUARD_UNCHANGED",
                          "core_focus_unchanged": focus.value == guard,
                          "original_windows_live": all(app.get_visible() for app in windows),
                          "original_devices_preserved_and_pair_absent": devices() == original}
                with (args.output / "process-result.json").open("x") as handle:
                    json.dump({"checks": checks, "outcome": outcome}, handle, sort_keys=True)
                if not all(checks.values()):
                    raise RuntimeError("independent process GTK state/cleanup check failed")
                print(f"Native process {args.process_mode}: PASS", flush=True)
                return
            job = args.output / "fixture-job.json"
            with job.open("x") as handle:
                os.chmod(job, 0o600)
                json.dump({"window": target, "pid": os.getpid(), "lease_window": token,
                           "lease_pid": os.getpid(), "chords": chords}, handle)
            environment = dict(os.environ, MINTCLAW_PRIVATE_NATIVE_JOB=str(job))
            with (args.output / "worker.log").open("xb") as log:
                worker = subprocess.Popen([str(args.probe), "qualification::owned_gtk_parent_keyboard_child",
                                           "--ignored", "--test-threads=1", "--nocapture"], env=environment,
                                          stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
                try:
                    until = time.monotonic() + 10
                    output = bytearray()
                    released = False
                    admitted_ready = False
                    while worker.poll() is None and time.monotonic() < until:
                        pump(0.002)
                        if select.select([worker.stdout], [], [], 0)[0]:
                            chunk = os.read(worker.stdout.fileno(), 8192)
                            log.write(chunk)
                            output.extend(chunk)
                        admitted = re.search(rb"MINTCLAW_NATIVE_KEYBOARD_ADMITTED=(\d+)", output)
                        if admitted and not admitted_ready:
                            expected_id = int(admitted.group(1))
                            devices_known = Gdk.Display.get_default().get_device_manager().list_devices(Gdk.DeviceType.MASTER)
                            if any(device.get_property("device-id") == expected_id for device in devices_known):
                                if (entries[0].get_position() != len(prefix)
                                        or entries[0].get_selection_bounds()
                                        or entries[0].get_text() != prefix + suffix):
                                    raise RuntimeError("native admission changed the original fixture caret")
                                worker.stdin.write(bytes([1]))
                                worker.stdin.flush()
                                admitted_ready = True
                        if (not released and b"MINTCLAW_NATIVE_KEYBOARD_DISPATCH_ACKNOWLEDGED" in output
                                and entries[0].get_text().encode() == (prefix + text + suffix).encode()
                                and entries[1].get_text() == "GUARD_UNCHANGED"):
                            worker.stdin.write(bytes([2]))
                            worker.stdin.flush()
                            worker.stdin.close()
                            released = True
                    if worker.poll() is None:
                        raise RuntimeError("native qualification child deadline")
                    if worker.returncode != 0:
                        with (args.output / "fixture-values.json").open("x") as diagnostic:
                            json.dump({"fixture_target": entries[0].get_text(), "fixture_guard": entries[1].get_text(),
                                       "admitted_ready": admitted_ready, "released": released}, diagnostic)
                        raise RuntimeError("native qualification child failed")
                finally:
                    if worker.poll() is None:
                        worker.kill()
                        worker.wait(timeout=3)
                    worker.stdin.close()
                    log.write(worker.stdout.read())
                    worker.stdout.close()
            pump()
            focus, revert = window(), integer()
            get_focus(opened, C.byref(focus), C.byref(revert))
            result = {"native_ascii_unicode_caret": entries[0].get_text().encode() == (prefix + text + suffix).encode(),
                      "guard_unchanged": entries[1].get_text() == "GUARD_UNCHANGED",
                      "core_focus_unchanged": focus.value == guard,
                      "original_windows_live": all(app.get_visible() for app in windows),
                      "original_devices_preserved_and_pair_absent": devices() == original,
                      "full_native_driver_qualified": False}
            with (args.output / "result.json").open("x") as handle:
                json.dump(result, handle, sort_keys=True)
            if not all(value for name, value in result.items() if name != "full_native_driver_qualified"):
                raise RuntimeError("independent native GTK state check failed")
            print("Native GTK caret, guards, windows and owned cleanup: PASS", flush=True)
        finally:
            for app in windows:
                app.destroy()
    finally:
        close(opened)


if __name__ == "__main__":
    main()
