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


class GenericCookie(C.Structure):
    _fields_ = [("type", C.c_int), ("serial", C.c_ulong), ("send_event", C.c_int),
                ("display", C.c_void_p), ("extension", C.c_int), ("evtype", C.c_int),
                ("cookie", C.c_uint), ("data", C.c_void_p)]


class NativeEvent(C.Union):
    _fields_ = [("cookie", GenericCookie), ("pad", C.c_long * 24)]


class XIKeyPrefix(C.Structure):
    _fields_ = [("type", C.c_int), ("serial", C.c_ulong), ("send_event", C.c_int),
                ("display", C.c_void_p), ("extension", C.c_int), ("evtype", C.c_int),
                ("time", C.c_ulong), ("deviceid", C.c_int), ("sourceid", C.c_int), ("detail", C.c_int),
                ("root", C.c_ulong), ("event", C.c_ulong), ("child", C.c_ulong)]


class XIEventMask(C.Structure):
    _fields_ = [("deviceid", C.c_int), ("mask_len", C.c_int), ("mask", C.POINTER(C.c_ubyte))]


class XIGrabModifiers(C.Structure):
    _fields_ = [("modifiers", C.c_int), ("status", C.c_int)]


# Public XKBstr.h layouts. A separate original Xlib client observes controls;
# neither the input worker's ACK nor its claimed settings are the oracle.
class XkbMods(C.Structure):
    _fields_ = [("mask", C.c_ubyte), ("real_mods", C.c_ubyte), ("vmods", C.c_ushort)]


class XkbControls(C.Structure):
    _fields_ = [("mk_dflt_btn", C.c_ubyte), ("num_groups", C.c_ubyte), ("groups_wrap", C.c_ubyte),
                ("internal", XkbMods), ("ignore_lock", XkbMods), ("enabled_ctrls", C.c_uint)] + [
                    (name, C.c_short if name == "mk_curve" else C.c_ushort) for name in (
                        "repeat_delay", "repeat_interval", "slow_keys_delay", "debounce_delay", "mk_delay",
                        "mk_interval", "mk_time_to_max", "mk_max_speed", "mk_curve", "ax_options", "ax_timeout",
                        "axt_opts_mask", "axt_opts_values")] + [
                ("axt_ctrls_mask", C.c_uint), ("axt_ctrls_values", C.c_uint),
                ("per_key_repeat", C.c_ubyte * 32)]


class XkbDesc(C.Structure):
    _fields_ = [("dpy", C.c_void_p), ("flags", C.c_ushort), ("device_spec", C.c_ushort),
                ("min_key_code", C.c_ubyte), ("max_key_code", C.c_ubyte),
                ("ctrls", C.POINTER(XkbControls))] + [
                    (name, C.c_void_p) for name in ("server", "map", "indicators", "names", "compat", "geom")]


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

        def keyboard_controls(device):
            description = bind(x, "XkbAllocKeyboard", C.POINTER(XkbDesc))()
            if not description:
                raise RuntimeError("independent keyboard description allocation failed")
            try:
                description.contents.device_spec = device
                status = bind(x, "XkbGetControls", integer, ptr, C.c_ulong, C.POINTER(XkbDesc))(
                    opened, 0xf8001fff, description)
                if status != 0 or not description.contents.ctrls:
                    raise RuntimeError("independent device controls query failed")
                controls = description.contents.ctrls.contents
                result = {}
                for name, _ in XkbControls._fields_:
                    value = getattr(controls, name)
                    result[name] = ([value.mask, value.real_mods, value.vmods] if isinstance(value, XkbMods)
                                    else list(value) if name == "per_key_repeat" else value)
                return result
            finally:
                bind(x, "XkbFreeKeyboard", None, C.POINTER(XkbDesc), C.c_uint, integer)(description, 0, 1)

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
            original_controls = {device: keyboard_controls(device) for device in
                                 [256] + [item[0] for item in original if item[2] in {2, 4}]}
            if not any(value["enabled_ctrls"] & 1 for value in original_controls.values()):
                raise RuntimeError("fixture must retain an originally repeating keyboard")
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

                admitted_controls, grab_events = {}, []
                late_grab_installed = False

                def late_grab():
                    nonlocal late_grab_installed
                    bits = (C.c_ubyte * 1)((1 << 2) | (1 << 3))
                    mask = XIEventMask(1, 1, bits)
                    modifiers = XIGrabModifiers(C.c_int(1 << 31).value, 0)
                    result = bind(xi, "XIGrabKeycode", integer, ptr, integer, integer, window, integer, integer,
                                  integer, C.POINTER(XIEventMask), integer, C.POINTER(XIGrabModifiers))(
                                      opened, 1, code(opened, ord("a")), root, 1, 1, 0,
                                      C.byref(mask), 1, C.byref(modifiers))
                    sync(opened, 0)
                    if result != 0 or modifiers.status != 0:
                        raise RuntimeError("owned all-master passive XI2 grab refused")
                    late_grab_installed = True

                def process_ready(keyboard):
                    known = Gdk.Display.get_default().get_device_manager().list_devices(Gdk.DeviceType.MASTER)
                    if not any(device.get_property("device-id") == keyboard for device in known):
                        return False
                    owned = [item[0] for item in devices() if item[0] == keyboard
                             or (item[2] == 4 and item[3] == keyboard)]
                    if len(owned) != 2:
                        raise RuntimeError("expected exact original private master and slave")
                    admitted_controls.update({device: keyboard_controls(device) for device in owned})
                    if any(value["enabled_ctrls"] & 1 for value in admitted_controls.values()):
                        raise RuntimeError("private keyboard still admits autonomous repeat")
                    if original_controls != {device: keyboard_controls(device) for device in original_controls}:
                        raise RuntimeError("private setup changed original keyboard controls")
                    if (entries[0].get_position() != len(prefix) or entries[0].get_selection_bounds()
                            or entries[0].get_text() != prefix + suffix):
                        raise RuntimeError("process admission changed the original fixture caret")
                    return True

                def process_observed():
                    return (entries[0].get_text().encode() == (prefix + text + suffix).encode()
                            and entries[1].get_text() == "GUARD_UNCHANGED")

                def cleanup_observed(cleaned):
                    current_devices = devices()
                    return (entries[0].get_text() == prefix + "a" + suffix
                            and entries[1].get_text() == "GUARD_UNCHANGED"
                            and (current_devices == original if cleaned else len(current_devices) == len(original) + 4))

                outcome = run(executable, args.process_mode,
                              {"socket": f"/tmp/.X11-unix/X{os.environ['MINTCLAW_PRIVATE_X_DISPLAY_NUMBER']}",
                               "screen": 0, "auth": list(bytes.fromhex(os.environ["MINTCLAW_PRIVATE_X_COOKIE"])),
                               "server": {"pid": int(os.environ["MINTCLAW_PRIVATE_XVFB_PID"]),
                                          "uid": os.getuid(), "root": root}},
                              {"window": target, "pid": os.getpid(), "lease_window": token,
                               "lease_pid": os.getpid()}, chords, pump, process_ready, process_observed, cleanup_observed,
                              late_grab if args.process_mode == "late_grab_repeat" else None)
                if args.process_mode == "late_grab_repeat":
                    pending = bind(x, "XPending", integer, ptr)
                    next_event = bind(x, "XNextEvent", integer, ptr, C.POINTER(NativeEvent))
                    sync(opened, 0)
                    while pending(opened):
                        packet = NativeEvent()
                        next_event(opened, C.byref(packet))
                        if packet.cookie.type != 35 or packet.cookie.evtype not in {2, 3}:
                            continue
                        loaded = bind(x, "XGetEventData", integer, ptr, C.POINTER(GenericCookie))(
                            opened, C.byref(packet.cookie))
                        if not loaded or not packet.cookie.data:
                            raise RuntimeError("grabbed XI2 event cookie unavailable")
                        try:
                            value = C.cast(packet.cookie.data, C.POINTER(XIKeyPrefix)).contents
                            grab_events.append({"type": value.evtype, "window": value.event,
                                                "native": not bool(value.send_event), "keycode": value.detail,
                                                "device": value.deviceid, "source": value.sourceid})
                        finally:
                            bind(x, "XFreeEventData", None, ptr, C.POINTER(GenericCookie))(
                                opened, C.byref(packet.cookie))
                    with (args.output / "grab-measurement.json").open("x") as handle:
                        json.dump({"root": root, "original_target": target, "grab_events": grab_events,
                                   "target_text": entries[0].get_text(), "outcome": outcome}, handle, sort_keys=True)
                if args.process_mode in {"keeper_crash_held", "group_term_held"}:
                    # Wait past several server autorepeat periods. A removed
                    # process/closed socket alone did not stop this regression.
                    pump(0.75)
                pump()
                focus, revert = window(), integer()
                get_focus(opened, C.byref(focus), C.byref(revert))
                normal = args.process_mode in {"normal", "foreign_stop", "duplicate_permit"}
                expected = prefix + (text if normal else "a") + suffix
                checks = {"original_caret_effect": entries[0].get_text().encode() == expected.encode(),
                          "guard_unchanged": entries[1].get_text() == "GUARD_UNCHANGED",
                          "core_focus_unchanged": focus.value == guard,
                          "original_windows_live": all(app.get_visible() for app in windows),
                          "original_devices_preserved_and_pair_absent": devices() == original,
                          "original_keyboard_controls_preserved": original_controls == {
                              device: keyboard_controls(device) for device in original_controls},
                          "private_repeat_disabled_at_admission": len(admitted_controls) == 2 and all(
                              not (value["enabled_ctrls"] & 1) for value in admitted_controls.values())}
                if args.process_mode == "late_grab_repeat":
                    checks["late_grab_actually_installed"] = late_grab_installed
                    checks["no_key_to_grabbing_client"] = not grab_events
                with (args.output / "controls-measurement.json").open("x") as handle:
                    json.dump({"original": original_controls, "admitted_owned": admitted_controls}, handle, sort_keys=True)
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
