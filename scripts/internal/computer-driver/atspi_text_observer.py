#!/usr/bin/python3
"""Private fixture: retain original AT-SPI object identity, capture fresh state only."""
import argparse
import json
import sys
import time
import gi

gi.require_version("Atspi", "2.0")
from gi.repository import Atspi, Gio, GLib

parser = argparse.ArgumentParser()
parser.add_argument("--pid", required=True, type=int)
parser.add_argument("--bind", action="store_true")
args = parser.parse_args()

def call(connection, owner, path, interface, method, parameters, signature):
    return connection.call_sync(owner, path, interface, method, parameters,
                                GLib.VariantType.new(signature), Gio.DBusCallFlags.NONE, 250, None).unpack()

if args.bind:
    Atspi.set_timeout(250, 500)
    Atspi.init()
    deadline = time.monotonic() + 6
    desktop = Atspi.get_desktop(0)
    applications = [desktop.get_child_at_index(i) for i in range(min(desktop.get_child_count(), 32))]
    applications = [app for app in applications if app.get_process_id() == args.pid]
    assert len(applications) == 1, "original fixture app absent"
    pending = [(applications[0], 0)]
    fields = []
    visited = 0
    while pending and visited < 512 and time.monotonic() < deadline:
        item, depth = pending.pop()
        item.clear_cache_single()
        visited += 1
        if item.get_role_name() == "text" and "Text" in item.get_interfaces():
            fields.append(item)
        count = item.get_child_count()
        assert count <= 128 and depth < 48
        pending.extend((item.get_child_at_index(i), depth + 1) for i in range(count))
    assert not pending and len(fields) == 1, "original fixture text ambiguous or discovery incomplete"
    session = Gio.bus_get_sync(Gio.BusType.SESSION, None)
    address = call(session, "org.a11y.Bus", "/org/a11y/bus", "org.a11y.Bus",
                   "GetAddress", None, "(s)")[0]
    binding = {"address": address, "owner": fields[0].app.bus_name, "path": fields[0].path, "pid": args.pid}
else:
    raw = sys.stdin.buffer.read(8193)
    assert len(raw) <= 8192
    binding = json.loads(raw)
    assert set(binding) == {"address", "owner", "path", "pid"}
    assert binding["pid"] == args.pid

assert binding["address"].startswith("unix:")
assert binding["owner"].startswith(":") and binding["path"].startswith("/")
connection = Gio.DBusConnection.new_for_address_sync(
    binding["address"], Gio.DBusConnectionFlags.AUTHENTICATION_CLIENT
    | Gio.DBusConnectionFlags.MESSAGE_BUS_CONNECTION, None, None)
try:
    peer_pid = call(connection, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus",
                    "GetConnectionUnixProcessID", GLib.Variant("(s)", (binding["owner"],)), "(u)")[0]
    assert peer_pid == args.pid
    role = call(connection, binding["owner"], binding["path"], "org.a11y.atspi.Accessible",
                "GetRole", None, "(u)")[0]
    assert role == int(Atspi.Role.TEXT)
    text = call(connection, binding["owner"], binding["path"], "org.a11y.atspi.Text",
                "GetText", GLib.Variant("(ii)", (0, -1)), "(s)")[0]
    state = call(connection, binding["owner"], binding["path"], "org.a11y.atspi.Accessible",
                 "GetState", None, "(au)")[0]
    assert len(state) == 2 and len(text.encode()) <= 32768
    def has(flag):
        value = int(flag)
        return bool(state[value // 32] & (1 << (value % 32)))
    snapshot = {"text": text, "editable": has(Atspi.StateType.EDITABLE),
                "sensitive": has(Atspi.StateType.SENSITIVE)}
    print(json.dumps({"binding": binding, "initial": snapshot} if args.bind else snapshot, ensure_ascii=False))
finally:
    connection.close_sync(None)
