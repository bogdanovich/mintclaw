"""Owned GNOME/IBus fixture on an already attested virtual server; no host session."""

from contextlib import contextmanager
import ctypes as C
import json
import os
from pathlib import Path
import secrets
import select
import signal
import subprocess
import time

from native_state_oracle import FixtureOracle, bind


def _terminate_owned(process, descriptor):
    # The pidfd was opened before polling/reaping our original Popen child.
    # Numeric PIDs, process names and an assumed surviving group never signal.
    if not select.select([descriptor], [], [], 0)[0]:
        try:
            signal.pidfd_send_signal(descriptor, signal.SIGTERM)
        except ProcessLookupError:
            pass
    try:
        process.wait(timeout=3)
    except subprocess.TimeoutExpired:
        try:
            signal.pidfd_send_signal(descriptor, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=3)


def _owned_descendants(marker):
    result = []
    for path in Path("/proc").iterdir():
        if not path.name.isdecimal():
            continue
        descriptor = None
        try:
            if path.stat().st_uid != os.getuid():
                continue
            values = dict(item.split(b"=", 1) for item in (path / "environ").read_bytes().split(b"\0") if b"=" in item)
            if values.get(b"MINTCLAW_PRIVATE_GNOME_MARKER") != marker.encode():
                continue
            descriptor = os.pidfd_open(int(path.name))
            if select.select([descriptor], [], [], 0)[0]:
                continue
            current = dict(item.split(b"=", 1) for item in (path / "environ").read_bytes().split(b"\0") if b"=" in item)
            if current.get(b"MINTCLAW_PRIVATE_GNOME_MARKER") != marker.encode():
                continue
            result.append((descriptor, int(path.name), (path / "exe").resolve().name))
            descriptor = None
        except (FileNotFoundError, PermissionError, ProcessLookupError):
            pass
        finally:
            if descriptor is not None:
                os.close(descriptor)
    return result


@contextmanager
def private_gnome(environment, work):
    marker = secrets.token_hex(24)
    environment.update(
        DBUS_SYSTEM_BUS_ADDRESS=f"unix:path={work}/no-system-bus",
        GNOME_SHELL_DISABLE_EXTENSIONS="1", GNOME_SHELL_SESSION_MODE="user",
        LIBGL_ALWAYS_SOFTWARE="1", IBUS_ADDRESS=f"unix:path={work}/no-ibus",
        XDG_SESSION_ID="mintclaw-private-disposable", XDG_SESSION_TYPE="x11",
        MINTCLAW_PRIVATE_GNOME_MARKER=marker,
    )
    config = work / "bus.conf"
    # No activation directories, inherited host bus or user service discovery.
    config.write_text(
        '<busconfig><type>session</type><listen>unix:tmpdir=' + str(work)
        + '</listen><auth>EXTERNAL</auth><policy context="default">'
        '<allow send_destination="*" eavesdrop="true"/><allow eavesdrop="true"/>'
        '<allow own="*"/></policy></busconfig>'
    )
    processes = []

    def launch(arguments, log_name, pipe=False):
        with (work / log_name).open("xb") as log:
            process = subprocess.Popen(
                arguments, env=environment, stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE if pipe else log, stderr=log if pipe else subprocess.STDOUT,
                start_new_session=True,
            )
        try:
            descriptor = os.pidfd_open(process.pid)
        except OSError:
            # Still our unreaped original Popen child; never search by name.
            process.terminate()
            process.wait(timeout=3)
            raise
        processes.append((process, descriptor))
        return process

    def bus(log_name):
        process = launch(
            ["/usr/bin/dbus-daemon", "--nofork", "--config-file=" + str(config), "--print-address=1"],
            log_name, pipe=True,
        )
        if not select.select([process.stdout], [], [], 5)[0]:
            raise RuntimeError("private bus transport deadline")
        address = process.stdout.readline().strip().decode()
        if not address.startswith("unix:path=" + str(work)) or process.poll() is not None:
            raise RuntimeError("not the original owned bus")
        return address

    try:
        environment["DBUS_SYSTEM_BUS_ADDRESS"] = bus("system-bus.log")
        environment["DBUS_SESSION_BUS_ADDRESS"] = bus("bus.log")
        environment["IBUS_ADDRESS"] = f"unix:path={work}/ibus.sock"
        environment["GTK_IM_MODULE"] = "ibus"
        ibus = launch(
            ["/usr/bin/ibus-daemon", "--single", "--xim", "--address=" + environment["IBUS_ADDRESS"], "--cache=none"],
            "ibus.log",
        )
        until, current = time.monotonic() + 8, None
        while time.monotonic() < until and ibus.poll() is None:
            selected = subprocess.run(
                ["/usr/bin/ibus", "engine", "xkb:us::eng"], env=environment,
                capture_output=True, text=True, timeout=2,
            )
            if selected.returncode == 0:
                current = subprocess.run(
                    ["/usr/bin/ibus", "engine"], env=environment, capture_output=True, text=True, timeout=2,
                )
                if current.returncode == 0 and current.stdout.strip() == "xkb:us::eng":
                    break
            time.sleep(0.02)
        else:
            raise RuntimeError("owned IBus engine readiness failed")
        (work / "ibus-ready.json").write_text(json.dumps({
            "pid": ibus.pid, "engine": current.stdout.strip(), "address_owned": True,
            "host_bus_inaccessible": True,
        }) + "\n")
        shell = launch(["/usr/bin/gnome-shell", "--x11", "--sm-disable", "--mode=user"], "shell.log")
        oracle = FixtureOracle(environment)
        try:
            x, display = oracle.x, oracle.display
            pointer, window, integer, ulong = C.c_void_p, C.c_ulong, C.c_int, C.c_ulong
            root = bind(x, "XDefaultRootWindow", window, pointer)(display)
            atom = bind(x, "XInternAtom", window, pointer, C.c_char_p, integer)(
                display, b"_NET_SUPPORTING_WM_CHECK", 0,
            )
            get = bind(x, "XGetWindowProperty", integer, pointer, window, window, C.c_long, C.c_long, integer,
                       window, C.POINTER(window), C.POINTER(integer), C.POINTER(ulong), C.POINTER(ulong),
                       C.POINTER(C.POINTER(C.c_ubyte)))

            def wm_window():
                actual, form, count, after = window(), integer(), ulong(), ulong()
                data = C.POINTER(C.c_ubyte)()
                status = get(display, root, atom, 0, 1, 0, 0, C.byref(actual), C.byref(form),
                             C.byref(count), C.byref(after), C.byref(data))
                if status != 0:
                    raise RuntimeError("owned WM metadata unavailable")
                try:
                    return C.cast(data, C.POINTER(ulong))[0] if form.value == 32 and count.value == 1 else None
                finally:
                    if data:
                        bind(x, "XFree", integer, pointer)(data)

            until, readiness_timeouts = time.monotonic() + 15, 0
            while time.monotonic() < until and shell.poll() is None:
                # Source-backed startup-complete signal plus native/bus state;
                # elapsed time alone never declares the desktop ready.
                if wm_window() and b"GNOME Shell started at " in (work / "shell.log").read_bytes():
                    remaining = until - time.monotonic()
                    if remaining <= 0:
                        break
                    try:
                        overview = subprocess.run(
                            ["/usr/bin/gdbus", "call", "--session", "--dest", "org.gnome.Shell",
                             "--object-path", "/org/gnome/Shell", "--method", "org.freedesktop.DBus.Properties.Get",
                             "org.gnome.Shell", "OverviewActive"], env=environment,
                            capture_output=True, text=True, timeout=min(1, remaining),
                        )
                    except subprocess.TimeoutExpired:
                        readiness_timeouts += 1
                        continue
                    if overview.returncode == 0 and overview.stdout.strip() in ("(<true>,)", "(<false>,)"):
                        break
                time.sleep(0.02)
            else:
                raise RuntimeError("private GNOME readiness failed")
            if time.monotonic() >= until or shell.poll() is not None:
                raise RuntimeError("private GNOME readiness deadline")
            if "<true>" in overview.stdout:
                hidden = subprocess.run(
                    ["/usr/bin/gdbus", "call", "--session", "--dest", "org.gnome.Shell",
                     "--object-path", "/org/gnome/Shell", "--method", "org.freedesktop.DBus.Properties.Set",
                     "org.gnome.Shell", "OverviewActive", "<false>"], env=environment,
                    capture_output=True, text=True, timeout=2,
                )
                if hidden.returncode != 0:
                    raise RuntimeError("private overview did not close")
            until = time.monotonic() + 5
            while time.monotonic() < until and shell.poll() is None:
                status = bind(x, "XGrabKeyboard", integer, pointer, window, integer, integer, integer, window)(
                    display, root, 0, 1, 1, 0,
                )
                if status == 0:
                    bind(x, "XUngrabKeyboard", integer, pointer, window)(display, 0)
                    bind(x, "XSync", integer, pointer, integer)(display, 0)
                    break
                time.sleep(0.01)
            else:
                raise RuntimeError("private GNOME retained a startup core grab")
            original_wm = wm_window()
            (work / "wm-ready.json").write_text(json.dumps({
                "shell_pid": shell.pid, "wm_window": original_wm, "private_empty_system_bus": True,
                "private_session_bus": True, "overview_initial": overview.stdout.strip(),
                "startup_complete_observed": True, "readiness_timeout_count": readiness_timeouts,
                "startup_core_slot_available": True, "attached_host_qualified": False,
            }) + "\n")
            yield environment
            finish = {
                "original_shell_alive": shell.poll() is None, "original_wm_window_retained": wm_window() == original_wm,
                "original_buses_alive": all(process.poll() is None for process, _ in processes),
            }
            (work / "wm-finish.json").write_text(json.dumps(finish) + "\n")
            if not all(finish.values()):
                raise RuntimeError("original GNOME did not survive the input case")
        finally:
            oracle.close()
    finally:
        for process, descriptor in reversed(processes):
            try:
                _terminate_owned(process, descriptor)
            finally:
                os.close(descriptor)
                if process.stdout is not None:
                    process.stdout.close()
        descendants = _owned_descendants(marker)
        try:
            (work / "fixture-owned-descendant-cleanup.json").write_text(json.dumps([
                {"pid": pid, "executable": executable} for _, pid, executable in descendants
            ]) + "\n")
            for descriptor, _, _ in descendants:
                try:
                    signal.pidfd_send_signal(descriptor, signal.SIGTERM)
                except ProcessLookupError:
                    pass
            until = time.monotonic() + 3
            for descriptor, _, _ in descendants:
                if not select.select([descriptor], [], [], max(0, until - time.monotonic()))[0]:
                    signal.pidfd_send_signal(descriptor, signal.SIGKILL)
                if not select.select([descriptor], [], [], 3)[0]:
                    raise RuntimeError("owned fixture process did not stop")
            remaining = _owned_descendants(marker)
            try:
                (work / "fixture-residuals.json").write_text(json.dumps([
                    {"pid": pid, "executable": executable} for _, pid, executable in remaining
                ]) + "\n")
                if remaining:
                    for descriptor, _, _ in remaining:
                        try:
                            signal.pidfd_send_signal(descriptor, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                    for descriptor, _, _ in remaining:
                        if not select.select([descriptor], [], [], 3)[0]:
                            raise RuntimeError("owned fixture descendant did not stop")
                    raise RuntimeError("owned fixture descendants survived cleanup")
            finally:
                for descriptor, _, _ in remaining:
                    os.close(descriptor)
        finally:
            for descriptor, _, _ in descendants:
                os.close(descriptor)
