#!/usr/bin/python3
"""Supplementary actual Go-host GTK4 probe on an owned display; not desktop admission."""
import argparse
import ctypes as C
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from native_state_oracle import FixtureOracle, bind, window_viewable
from gnome_session_fixture import private_gnome

spec = importlib.util.spec_from_file_location("owned_server_fixture", HERE / "native-keyboard-fixture.py")
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


def observe_text(observer, pid, environment, work, label, binding=None):
    arguments = [sys.executable, str(observer), "--pid", str(pid)]
    if binding is None:
        arguments.append("--bind")
    result = subprocess.run(
        arguments, env=environment, input=json.dumps(binding) if binding is not None else None,
        capture_output=True, text=True, timeout=3 if binding is not None else 8,
    )
    (work / (label + ".log")).write_text(result.stderr)
    if result.returncode != 0 or len(result.stdout.encode()) > 40 * 1024:
        raise RuntimeError("owned fixture text observation refused")
    return json.loads(result.stdout)


def run_case(work, args, control):
    work.mkdir(mode=0o700, exist_ok=False)
    worker, xvfb = args.worker, args.xvfb
    report = {"full_driver_qualified": False, "attached_desktop_qualified": False,
              "worker_sha256": hashlib.sha256(worker.read_bytes()).hexdigest(),
              "host_probe_sha256": args.host_probe_sha256,
              "application_sha256": args.application_sha256, "model_tasks": 0}
    text_effect = args.effect == "observed-text"
    report["effect_under_test"] = args.effect
    with fixture.owned_server(xvfb, work / "server") as env:
        env.update(HOME=str(work), LANG="C.UTF-8", GSK_RENDERER="cairo", GTK_A11Y="none",
                   PULSE_SERVER=f"unix:{work}/no-audio", PIPEWIRE_REMOTE="mintclaw-no-audio",
                   XDG_RUNTIME_DIR=str(work / "runtime"))
        Path(env["XDG_RUNTIME_DIR"]).mkdir(mode=0o700)
        for key in ("XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"):
            Path(env[key]).mkdir(mode=0o700)
        env["MINTCLAW_PRIVATE_DIAGNOSTIC_DIR"] = str(work)
        with private_gnome(env, work, accessibility=text_effect):
            os.environ.update(env)
            oracle = FixtureOracle(env)
            x, d = oracle.x, oracle.display
            ptr, win, integer, ulong = C.c_void_p, C.c_ulong, C.c_int, C.c_ulong
            root = bind(x, "XDefaultRootWindow", win, ptr)(d)
            sync = bind(x, "XSync", integer, ptr, integer)
            atom = bind(x, "XInternAtom", win, ptr, C.c_char_p, integer)
            pid_atom = atom(d, b"_NET_WM_PID", 0)
            prop = bind(x, "XGetWindowProperty", integer, ptr, win, win, C.c_long, C.c_long, integer, win,
                        C.POINTER(win), C.POINTER(integer), C.POINTER(ulong), C.POINTER(ulong),
                        C.POINTER(C.POINTER(C.c_ubyte)))
            free = bind(x, "XFree", integer, ptr)

            def window_property(window, name):
                property_atom=atom(d,name.encode(),0)
                actual,fmt,count,after,data=win(),integer(),ulong(),ulong(),C.POINTER(C.c_ubyte)()
                assert prop(d,window,property_atom,0,512,0,0,C.byref(actual),C.byref(fmt),
                            C.byref(count),C.byref(after),C.byref(data))==0
                try:
                    assert after.value==0 and count.value<=512
                    return list(C.cast(data,C.POINTER(ulong))[:count.value]) if fmt.value==32 else []
                finally:
                    if data:free(data)

            def managed_windows():
                return window_property(root,"_NET_CLIENT_LIST")

            class MessageData(C.Union):
                _fields_=[("longs",C.c_long*5),("bytes",C.c_char*20)]

            class ClientMessage(C.Structure):
                _fields_=[("type",integer),("serial",ulong),("send_event",integer),("display",ptr),
                          ("window",win),("message_type",win),("format",integer),("data",MessageData)]

            class Event(C.Union):
                _fields_=[("client",ClientMessage),("pad",C.c_long*24)]

            def activate_fixture_window(window, exact_focus=False):
                event=Event()
                event.client.type=33
                event.client.display=d
                event.client.window=window
                event.client.message_type=atom(d,b"_NET_ACTIVE_WINDOW",0)
                event.client.format=32
                event.client.data.longs[0]=2
                assert bind(x,"XSendEvent",integer,ptr,win,integer,C.c_long,C.POINTER(Event))(
                    d,root,0,(1<<19)|(1<<20),C.byref(event))
                sync(d,0)
                until=time.monotonic()+3
                stable_at=None
                last_focus=None
                while time.monotonic()<until:
                    focused=sample_focus("fixture_activation")
                    active=window_property(root,"_NET_ACTIVE_WINDOW")
                    valid=active==[window] and focused>1 and (not exact_focus or focused==window)
                    if not valid:
                        stable_at=None
                    elif stable_at is None or focused!=last_focus:
                        stable_at=time.monotonic()
                    elif time.monotonic()-stable_at>=0.2:
                        return focused
                    last_focus=focused
                    time.sleep(0.002)
                (work / ("activation-failure-" + str(window) + ".json")).write_text(json.dumps({
                    "requested": window, "active": active, "focused": focused, "managed": managed_windows(),
                }))
                raise RuntimeError("fixture WM activation did not settle")

            def property_pid(window):
                actual, fmt, count, after, data = win(), integer(), ulong(), ulong(), C.POINTER(C.c_ubyte)()
                assert prop(d, window, pid_atom, 0, 1, 0, 0, C.byref(actual), C.byref(fmt),
                            C.byref(count), C.byref(after), C.byref(data)) == 0
                try:
                    return C.cast(data, C.POINTER(ulong))[0] if fmt.value == 32 and count.value == 1 else None
                finally:
                    if data:
                        free(data)

            original = oracle.original
            original_pointer = integer()
            query_pointer = bind(oracle.xi, "XIGetClientPointer", integer, ptr, win, C.POINTER(integer))
            report["explicit_query_client_pointer"] = bool(query_pointer(d, 0, C.byref(original_pointer)))
            original_pointers = [i for i, v in original.items() if v[1] == 1]
            assert len(original_pointers) == 1
            original_pointer.value = original_pointers[0]
            original_keyboard = original[original_pointer.value][2]
            report["original_pointer"] = original_pointer.value
            report["original_keyboard"] = original_keyboard
            focus_trace = []
            started = time.monotonic()

            def sample_focus(label):
                native_focus, query_focus, revert = win(), win(), integer()
                assert bind(oracle.xi, "XIGetFocus", integer, ptr, integer, C.POINTER(win))(
                    d, original_keyboard, C.byref(native_focus)) == 0
                bind(x, "XGetInputFocus", integer, ptr, C.POINTER(win), C.POINTER(integer))(
                    d, C.byref(query_focus), C.byref(revert))
                row = [label, native_focus.value, query_focus.value]
                if not focus_trace or row[1:] != focus_trace[-1][2:]:
                    focus_trace.append([time.monotonic() - started] + row)
                return native_focus.value

            create = bind(x, "XCreateSimpleWindow", win, ptr, win, integer, integer,
                          C.c_uint, C.c_uint, C.c_uint, ulong, ulong)
            guard = create(d, root, 0, 0, 80, 80, 0, 0, 0)
            lease = bind(x, "XCreateWindow", win, ptr, win, integer, integer, C.c_uint, C.c_uint,
                         C.c_uint, integer, C.c_uint, ptr, ulong, ptr)(
                             d, root, 0, 0, 1, 1, 0, 0, 2, None, 0, None)
            assert guard and lease
            class WMHints(C.Structure):
                _fields_=[("flags",C.c_long),("input",integer),("initial_state",integer),
                          ("icon_pixmap",ulong),("icon_window",win),("icon_x",integer),("icon_y",integer),
                          ("icon_mask",ulong),("window_group",win)]
            hints=WMHints()
            hints.flags=3
            hints.input=1
            hints.initial_state=1
            assert bind(x,"XSetWMHints",integer,ptr,win,C.POINTER(WMHints))(d,guard,C.byref(hints))
            class ClassHint(C.Structure):
                _fields_=[("res_name",C.c_char_p),("res_class",C.c_char_p)]
            klass=ClassHint(b"mintclaw-fixture",b"MintClawFixture")
            assert bind(x,"XSetClassHint",integer,ptr,win,C.POINTER(ClassHint))(d,guard,C.byref(klass))
            bind(x,"XStoreName",integer,ptr,win,C.c_char_p)(d,guard,b"MintClaw unrelated fixture guard")
            bind(x, "XMapWindow", integer, ptr, win)(d, guard)
            selection = atom(d, b"_MINTCLAW_COMPUTER_INPUT_CONTROL", 0)
            # The actual Go InputLease alone owns the desktop selection.
            sync(d, 0)
            sample = work / "sample.txt"
            inserted = "MintClaw — Привет 世界 café 👩‍💻 e\u0301 مرحبا"
            original_text, expected = "PREFIX||SUFFIX\n", "PREFIX|" + inserted + "|SUFFIX\n"
            sample.write_text(original_text)
            app = None
            try:
                with (work / "application.log").open("xb") as log:
                    env["GTK_IM_MODULE"] = "ibus"
                    report["fixture_ime"] = "owned_IBus_xkb_us_engine"
                    app = subprocess.Popen([str(args.application), "--standalone", str(sample)],
                                           env=env, stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT)
                    until = time.monotonic() + 10
                    target = None
                    while time.monotonic() < until and app.poll() is None:
                        matches = [w for w in managed_windows() if property_pid(w) == app.pid
                                   and window_viewable(x, d, w, root)]
                        if len(matches) == 1:
                            target = matches[0]
                            break
                        time.sleep(0.01)
                    assert target is not None, "real GTK4 application did not map its original window"
                    assert Path(f"/proc/{app.pid}/exe").resolve() == args.application.resolve()
                    running_hash = hashlib.sha256(Path(f"/proc/{app.pid}/exe").read_bytes()).hexdigest()
                    assert running_hash == args.application_sha256
                    report.update(application_pid=app.pid, target_window=target,
                                  application_executable=str(Path(f"/proc/{app.pid}/exe").resolve()))
                    # All fixture activation happens before native admission.
                    # The backend never receives a caller-managed focus restore.
                    report["initial_app_focus"] = activate_fixture_window(target)
                    report["application_initial_focus_observed"] = True
                    report["fixture_guard_focus"] = activate_fixture_window(guard, exact_focus=True)
                    report["guard_window"] = guard
                    report["focus_after_guard"] = sample_focus("after_guard")
                    bound = None
                    if text_effect:
                        observer = HERE / "atspi_text_observer.py"
                        bound = observe_text(observer, app.pid, env, work, "initial-text")
                        initial = bound["initial"]
                        assert initial["text"] in (original_text, original_text[:-1])
                        expected_observed = initial["text"].replace(
                            "PREFIX||SUFFIX", "PREFIX|" + inserted + "|SUFFIX", 1,
                        )
                        report["original_observed_text"] = initial["text"]
                        report["expected_observed_text"] = expected_observed
                    keycode = bind(x, "XKeysymToKeycode", C.c_ubyte, ptr, win)
                    symbols = [(0xffe3, 0xff50)] + [(0xff53,)] * len("PREFIX|")
                    for character in inserted:
                        symbols.append((0xffe3, 0xffe1, ord("u")))
                        symbols.extend((ord(digit),) for digit in format(ord(character), "x"))
                        symbols.append((0xff0d,))
                    if not text_effect:
                        symbols.append((0xffe3, ord("s")))
                    assert len(symbols) <= 256
                    report["text_mechanism_under_test"] = "explicit_owned_IBus_unicode_compose_at_existing_caret"
                    report["native_chord_count"] = len(symbols)
                    chords = [[keycode(d, s) for s in chord] for chord in symbols]
                    assert all(all(c) for c in chords)
                    start = time.monotonic()
                    def pump(seconds):
                        time.sleep(seconds)
                        sample_focus("pump")

                    def overview_state():
                        v = subprocess.run(["/usr/bin/gdbus","call","--session","--dest","org.gnome.Shell",
                            "--object-path","/org/gnome/Shell","--method","org.freedesktop.DBus.Properties.Get",
                            "org.gnome.Shell","OverviewActive"],env=env,capture_output=True,text=True,timeout=2)
                        return {"exit":v.returncode,"value":v.stdout.strip(),"error":v.stderr.strip(),
                                "shell_startup_logged":"GNOME Shell started at" in (work/"shell.log").read_text()}

                    try:
                        if control:
                            while time.monotonic() - start < 2:
                                pump(0.002)
                            raise ValueError("intentional_no_input_control")
                        probe = args.host_probe
                        assert hashlib.sha256(probe.read_bytes()).hexdigest() == args.host_probe_sha256
                        config = work / "go-application-fixture.json"
                        configuration = {
                            "window": {"PID": app.pid, "WindowID": target,
                                       "Display": env["DISPLAY"], "XAuthority": env["XAUTHORITY"]},
                            "server_pid": int(env["MINTCLAW_PRIVATE_XVFB_PID"]),
                            "chords": chords, "artifact": str(sample),
                            "expected_hash": hashlib.sha256(
                                (original_text if text_effect else expected).encode("utf-8"),
                            ).hexdigest(),
                        }
                        if text_effect:
                            configuration.update(
                                observer=str(observer), observer_sha=args.text_observer_sha256,
                                observer_binding=bound["binding"], expected_text=expected_observed,
                                original_text=initial["text"],
                            )
                        config.write_text(json.dumps(configuration))
                        config.chmod(0o600)
                        go_env = dict(env, MINTCLAW_PRIVATE_NATIVE_APP_TEST="1",
                            MINTCLAW_PRIVATE_NATIVE_APP_CONFIG=str(config),
                            MINTCLAW_PRIVATE_NATIVE_HOST_WORKER=str(worker),
                            MINTCLAW_PRIVATE_NATIVE_HOST_WORKER_SHA256=args.worker_sha256)
                        with (work / "actual-go-host.log").open("xb") as go_log:
                            test_name = ("TestPrivateNativeTextObservationQualification" if text_effect
                                         else "TestPrivateNativeApplicationHostQualification")
                            host_process = subprocess.Popen([str(probe),
                                "-test.run=^" + test_name + "$",
                                "-test.v", "-test.count=1", "-test.timeout=35s"],env=go_env,
                                stdin=subprocess.DEVNULL,stdout=go_log,stderr=subprocess.STDOUT)
                            until = time.monotonic() + 40
                            try:
                                while host_process.poll() is None and time.monotonic() < until:
                                    pump(0.002)
                                if host_process.poll() is None:
                                    host_process.terminate()
                                    try: host_process.wait(timeout=3)
                                    except subprocess.TimeoutExpired:
                                        host_process.kill();host_process.wait(timeout=3)
                                    raise RuntimeError("actual Go application fixture deadline")
                                assert host_process.returncode == 0, "actual Go application fixture refused"
                            finally:
                                if host_process.poll() is None:
                                    host_process.terminate();host_process.wait(timeout=3)
                        marker = (
                            "actual Go Unicode text effect independently observed; "
                            "original file/guard/lease preserved; quiescent retained resources"
                            if text_effect else
                            "actual Go application effect verified; original guard/lease retained; "
                            "quiescent retained resources"
                        )
                        report["actual_go_host_verified"] = marker in (work / "actual-go-host.log").read_text()
                        assert report["actual_go_host_verified"]
                    except Exception as error:
                        report["input_error"] = str(error)
                    if text_effect:
                        final = observe_text(observer, app.pid, env, work, "final-text", bound["binding"])
                        report["final_observed_text"] = final["text"]
                        report["independent_text_matches"] = final["text"] == (
                            initial["text"] if control else expected_observed
                        )
                    report["overview_after_action"] = overview_state()
                    report.update(
                        file_text=sample.read_text(), expected=expected,
                        independently_saved=sample.read_text() == expected, app_alive=app.poll() is None,
                    )
                    focus, revert = win(), integer()
                    bind(x, "XGetInputFocus", integer, ptr, C.POINTER(win), C.POINTER(integer))(
                        d, C.byref(focus), C.byref(revert))
                    report["ambient_focus_preserved"] = sample_focus("final") == guard
                    report["core_query_focus"] = focus.value
                    report["focus_trace"] = focus_trace
                    report["no_input_control"] = control
                    try:
                        report["retained_pair"] = oracle.verify_retained_quiescence(0 if control else 4)
                        report["native_keys_clear"] = True
                    except Exception as error:
                        report["quiescence_error"] = str(error)
                    report["target_still_viewable"] = window_viewable(x, d, target, root)
            finally:
                if app is not None and app.poll() is None:
                    app.terminate()
                    try:
                        app.wait(timeout=3)
                    except subprocess.TimeoutExpired:
                        app.kill()
                        app.wait(timeout=3)
                oracle.close()
    (work / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"case":str(work),"control":control,"saved":report.get("independently_saved"),
                            "go_host":report.get("actual_go_host_verified",False)}),flush=True)
    required = ("native_keys_clear", "ambient_focus_preserved", "app_alive", "target_still_viewable")
    assert all(report.get(key) for key in required), "original application/native state was not preserved"
    if control:
        assert report["file_text"] == original_text and report["retained_pair"] == {}
        assert report.get("input_error") == "intentional_no_input_control"
        if text_effect:
            assert report["independent_text_matches"]
    else:
        assert report.get("actual_go_host_verified")
        if text_effect:
            assert report["independent_text_matches"] and report["file_text"] == original_text
        else:
            assert report["independently_saved"]
        assert "input_error" not in report, "actual Go application effect gate failed"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for kind in ("xvfb", "worker", "host-probe", "application"):
        parser.add_argument("--" + kind, type=Path, required=True)
        parser.add_argument("--" + kind + "-sha256", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--repetitions", type=int, choices=range(1, 21), default=20)
    parser.add_argument("--effect", choices=("saved-file", "observed-text"), default="saved-file")
    parser.add_argument("--text-observer-sha256")
    args = parser.parse_args()
    if sys.platform != "linux" or not hasattr(os, "pidfd_open"):
        parser.error("Linux with original process-handle support is required")
    for kind in ("xvfb", "worker", "host_probe", "application"):
        path, expected = getattr(args, kind), getattr(args, kind + "_sha256")
        if (not path.is_absolute() or path.is_symlink() or not path.is_file()
                or hashlib.sha256(path.read_bytes()).hexdigest() != expected):
            parser.error("pinned private fixture executable identity mismatch")
    if args.effect == "observed-text":
        observer = HERE / "atspi_text_observer.py"
        if (observer.is_symlink() or not observer.is_file()
                or hashlib.sha256(observer.read_bytes()).hexdigest() != args.text_observer_sha256):
            parser.error("explicit pinned text-observer source identity required")
    if not args.output.is_absolute() or args.output.is_symlink():
        parser.error("an explicit new private output directory is required")
    args.output.mkdir(mode=0o700, exist_ok=False)
    for attempt in range(1, args.repetitions + 1):
        work = args.output / str(attempt)
        work.mkdir(mode=0o700)
        run_case(work / "input", args, False)
        run_case(work / "no-input-control", args, True)
    print("Owned GNOME/IBus/actual-Go keyboard fixture passed; no full driver, attached-host or model qualification.")


if __name__ == "__main__":
    main()
