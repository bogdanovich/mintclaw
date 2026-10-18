#!/usr/bin/env python3
"""Disposable native GTK fixture; never attaches to an existing application."""
import json
import os
import sys
import threading

import gi

gi.require_version("Gtk", "3.0")
gi.require_version("GdkX11", "3.0")
from gi.repository import Gtk, Gdk, GdkX11, GLib

windows = []
headings = []
entries = []
counts = [0, 0]


def state():
    print(json.dumps({"kind": "state", "values": [e.get_text() for e in entries],
                      "counts": counts}, ensure_ascii=False), flush=True)


def clicked(button, index):
    counts[index] += 1
    state()


def window_destroyed(window, index):
    if index == 0 and os.environ.get("MINTCLAW_FIXTURE_KEEP_GUARD") == "1":
        return
    Gtk.main_quit()


for index in range(2):
    window = Gtk.Window(title="MintClaw disposable fixture " + str(index))
    window.set_default_size(440, 240)
    window.move(100 + 460 * index, 100)
    if index == 1 and os.environ.get("MINTCLAW_FIXTURE_TRANSIENT_OVERLAY") == "1":
        window.set_type_hint(Gdk.WindowTypeHint.DIALOG)
        window.set_transient_for(windows[0])
        window.set_modal(True)
        window.move(120, 120)
    box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=20)
    box.set_border_width(25)
    window.add(box)
    heading = Gtk.Label(label="MintClaw qualification fixture")
    headings.append(heading)
    box.pack_start(heading, False, False, 0)
    entry = Gtk.Entry()
    entry.set_text("" if index == 0 else "GUARD_UNCHANGED")
    entry.get_accessible().set_name("Qualification text")
    entries.append(entry)
    box.pack_start(entry, False, False, 0)
    button = Gtk.Button(label="Apply fixture")
    button.connect("clicked", clicked, index)
    box.pack_start(button, False, False, 0)
    window.connect("destroy", window_destroyed, index)
    windows.append(window)
    window.show_all()

for entry in entries:
    entry.connect("changed", lambda _: state())
windows[1].present()
print(json.dumps({"kind": "ready", "pid": os.getpid(),
                  "windows": [GdkX11.X11Window.get_xid(w.get_window()) for w in windows]}), flush=True)
state()
last_values = []


def poll():
    global last_values
    values = [e.get_text() for e in entries]
    if values != last_values:
        last_values = values
        state()
    return True


GLib.timeout_add(200, poll)


def emit_owned_windows(inspect_id):
    print(json.dumps({"kind": "lifetime", "inspect_id": inspect_id, "pid": os.getpid(),
                      "windows": [GdkX11.X11Window.get_xid(w.get_window()) for w in windows
                                  if w.get_visible() and w.get_window() is not None]}), flush=True)


def inspect_owned_windows(inspect_id, close_target=False, advance_visual=False):
    if close_target and os.environ.get("MINTCLAW_FIXTURE_KEEP_GUARD") == "1":
        windows[0].destroy()
        Gdk.Display.get_default().sync()
    if advance_visual:
        clock = windows[0].get_frame_clock()
        handler = [None]

        def painted(frame_clock):
            frame_clock.disconnect(handler[0])
            Gdk.Display.get_default().sync()
            emit_owned_windows(inspect_id)

        handler[0] = clock.connect_after("after-paint", painted)
        headings[0].set_text("MintClaw fresh resumed frame")
        windows[0].queue_draw()
    else:
        emit_owned_windows(inspect_id)
    return False


def read_inspections():
    for line in sys.stdin:
        try:
            request = json.loads(line)
            inspect_id = request.get("inspect_id")
            if isinstance(inspect_id, str) and 0 < len(inspect_id) <= 128:
                prepared = request.get("prepare_text")
                if isinstance(prepared, dict):
                    value = prepared.get("value")
                    caret = prepared.get("caret")
                    if (isinstance(value, str) and len(value.encode("utf-8")) <= 4096
                            and type(caret) is int and 0 <= caret <= len(value)):
                        GLib.idle_add(prepare_owned_text, inspect_id, value, caret)
                    continue
                GLib.idle_add(inspect_owned_windows, inspect_id, request.get("close_target") is True,
                              request.get("advance_visual") is True)
        except (ValueError, AttributeError):
            pass


def prepare_owned_text(inspect_id, value, caret):
    entries[0].set_text(value)
    entries[0].select_region(caret, caret)
    entries[0].set_position(caret)
    print(json.dumps({"kind": "prepared_text", "inspect_id": inspect_id,
                      "values": [e.get_text() for e in entries], "counts": counts},
                     ensure_ascii=False), flush=True)
    return False


threading.Thread(target=read_inspections, daemon=True).start()
Gtk.main()
