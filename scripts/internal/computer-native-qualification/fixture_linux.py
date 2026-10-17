#!/usr/bin/env python3
"""Disposable native GTK fixture; never attaches to an existing application."""
import json
import os

import gi

gi.require_version("Gtk", "3.0")
gi.require_version("GdkX11", "3.0")
from gi.repository import Gtk, Gdk, GdkX11, GLib

windows = []
entries = []
counts = [0, 0]


def state():
    print(json.dumps({"kind": "state", "values": [e.get_text() for e in entries],
                      "counts": counts}, ensure_ascii=False), flush=True)


def clicked(button, index):
    counts[index] += 1
    state()


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
    box.pack_start(Gtk.Label(label="MintClaw qualification fixture"), False, False, 0)
    entry = Gtk.Entry()
    entry.set_text("" if index == 0 else "GUARD_UNCHANGED")
    entry.get_accessible().set_name("Qualification text")
    entries.append(entry)
    box.pack_start(entry, False, False, 0)
    button = Gtk.Button(label="Apply fixture")
    button.connect("clicked", clicked, index)
    box.pack_start(button, False, False, 0)
    window.connect("destroy", Gtk.main_quit)
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
Gtk.main()
