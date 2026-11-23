#!/usr/bin/env python3
"""Owned GTK canvas/form windows. Controller protocol queries state only."""

import argparse
import json
import os
import sys
import threading

import interactive_fixtures as recipes


def run(task_id):
    import gi
    gi.require_version("Gtk", "3.0")
    gi.require_version("GdkX11", "3.0")
    gi.require_foreign("cairo")
    from gi.repository import Gtk, Gdk, GdkX11, GLib

    record, spec = recipes.parse_launch(task_id, sys.stdin.buffer.readline(recipes.MAX_LAUNCH_BYTES + 1))
    target = Gtk.Window(title="MintClaw " + task_id + " disposable fixture")
    target.set_default_size(spec["surface"]["width"], spec["surface"]["height"])
    target.set_resizable(False)
    target.move(40, 40)
    guard = Gtk.Window(title="MintClaw disposable guard")
    guard.set_default_size(280, 180)
    guard.move(720, 40)
    guard_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=12)
    guard_box.set_border_width(20)
    guard.add(guard_box)
    guard_entry = Gtk.Entry()
    guard_entry.set_text(recipes.GUARD_TEXT)
    guard_entry.set_max_length(4096)
    guard_box.pack_start(guard_entry, False, False, 0)
    guard_button = Gtk.Button(label="Guard apply")
    guard_box.pack_start(guard_button, False, False, 0)
    guard_count = [0]
    guard_button.connect("clicked", lambda _: guard_count.__setitem__(0, guard_count[0] + 1))
    guard.connect("destroy", lambda _: Gtk.main_quit())

    model = recipes.CanvasModel(spec) if task_id == "CU-T18" else None
    entries, choices, checks, periods = {}, {}, {}, {}
    if model is not None:
        canvas = Gtk.DrawingArea()
        canvas.set_size_request(640, 480)
        canvas.add_events(Gdk.EventMask.BUTTON_PRESS_MASK | Gdk.EventMask.BUTTON_RELEASE_MASK |
                          Gdk.EventMask.POINTER_MOTION_MASK)

        def draw(_, context):
            context.set_source_rgb(1, 1, 1)
            context.paint()
            context.set_source_rgb(0.15, 0.15, 0.15)
            context.set_font_size(16)
            context.move_to(20, 28)
            context.show_text("Move Card Alpha into the outlined destination")
            zone = spec["destination"]
            context.set_source_rgb(0.3, 0.45, 0.3)
            context.set_line_width(2)
            context.rectangle(zone["x"], zone["y"], zone["width"], zone["height"])
            context.stroke()
            for card in model.cards:
                context.set_source_rgb(*(0.2, 0.45, 0.8) if card["id"] == "alpha" else (0.25, 0.6, 0.4))
                context.rectangle(card["x"], card["y"], card["width"], card["height"])
                context.fill()
                context.set_source_rgb(1, 1, 1)
                context.move_to(card["x"] + 12, card["y"] + 40)
                context.show_text(card["label"])
            return False

        def press(_, event):
            return event.button == 1 and model.begin(event.x, event.y)

        def motion(_, event):
            if event.state & Gdk.ModifierType.BUTTON1_MASK and model.move(event.x, event.y):
                canvas.queue_draw()
                return True
            return False

        def release(_, event):
            if event.button == 1 and model.finish(event.x, event.y):
                canvas.queue_draw()
                return True
            return False

        canvas.connect("draw", draw)
        canvas.connect("button-press-event", press)
        canvas.connect("motion-notify-event", motion)
        canvas.connect("button-release-event", release)
        canvas.connect("grab-broken-event", lambda *_: model.cancel())
        target.add(canvas)
    else:
        grid = Gtk.Grid(column_spacing=12, row_spacing=12)
        grid.set_border_width(20)
        target.add(grid)
        row = [0]

        def label(text):
            item = Gtk.Label(label=text)
            item.set_xalign(0)
            grid.attach(item, 0, row[0], 1, 1)

        for key, title, initial in [('name_preview', 'Name preview', spec['decoys']['name_preview']),
                                    ('name', 'Name', spec['fields']['name'])]:
            label(title)
            entry = Gtk.Entry()
            entry.set_text(initial)
            entry.set_max_length(4096)
            entry.set_size_request(390, 30)
            entry.get_accessible().set_name(title)
            entries[key] = entry
            grid.attach(entry, 1, row[0], 1, 1)
            row[0] += 1
        for key, title, initial in [('choice_preview', 'Choice preview', spec['decoys']['choice_preview']),
                                    ('choice', 'Choice', spec['fields']['choice'])]:
            label(title)
            choice = Gtk.ComboBoxText()
            for option in spec['choices']:
                choice.append_text(option)
            choice.set_active(spec['choices'].index(initial))
            choice.get_accessible().set_name(title)
            choices[key] = choice
            grid.attach(choice, 1, row[0], 1, 1)
            row[0] += 1
        for key, title in [('confirmed_preview', 'Confirm preview'), ('confirmed', 'Confirm')]:
            check = Gtk.CheckButton(label=title)
            check.set_active(False)
            checks[key] = check
            grid.attach(check, 1, row[0], 1, 1)
            row[0] += 1
        label('Period')
        group = None
        box = Gtk.Box(spacing=12)
        for name in spec['periods']:
            button = Gtk.RadioButton.new_with_label_from_widget(group, name)
            group = group or button
            button.set_active(name == spec['fields']['period'])
            periods[name] = button
            box.pack_start(button, False, False, 0)
        grid.attach(box, 1, row[0], 1, 1)

    target.show_all()
    guard.show_all()
    original = [GdkX11.X11Window.get_xid(window.get_window()) for window in (target, guard)]

    def emit(value):
        print(json.dumps(value, ensure_ascii=False, sort_keys=True), flush=True)

    def inspect(inspect_id):
        if model is not None:
            state = model.snapshot()
        else:
            selected = [name for name, button in periods.items() if button.get_active()]
            state = {'fields': {'name': entries['name'].get_text(), 'choice': choices['choice'].get_active_text(),
                                'confirmed': checks['confirmed'].get_active(),
                                'period': selected[0] if len(selected) == 1 else ''},
                     'decoys': {'name_preview': entries['name_preview'].get_text(),
                                'choice_preview': choices['choice_preview'].get_active_text(),
                                'confirmed_preview': checks['confirmed_preview'].get_active()}}
        value = {key: record[key] for key in ('task_id', 'run_id', 'fixture_revision')}
        value.update(state=state, guard={'text': guard_entry.get_text(), 'count': guard_count[0],
                                        'visible': guard.get_visible()})
        emit({'kind': 'snapshot', 'inspect_id': inspect_id, 'pid': os.getpid(), 'windows': original,
              'visible_windows': [identity for window, identity in zip((target, guard), original)
                                  if window.get_visible() and window.get_window() is not None], 'value': value})
        return False

    def read_queries():
        while True:
            line = sys.stdin.buffer.readline(4097)
            if not line:
                return
            if len(line) > 4096:
                while line and not line.endswith(b'\n'):
                    line = sys.stdin.buffer.readline(4097)
                continue
            try:
                request = json.loads(line, object_pairs_hook=recipes.shared.unique_object)
                if isinstance(request, dict) and set(request) == {'inspect_id'} and \
                        isinstance(request['inspect_id'], str) and 0 < len(request['inspect_id'].encode('utf-8')) <= 128:
                    GLib.idle_add(inspect, request['inspect_id'])
            except (ValueError, recipes.shared.Unverifiable):
                continue

    emit({'kind': 'ready', 'pid': os.getpid(), 'windows': original, 'task_id': task_id,
          'fixture_revision': recipes.FIXTURE_REVISION})
    threading.Thread(target=read_queries, daemon=True).start()
    Gtk.main()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--task', choices=tuple(recipes.SPECS), required=True)
    args = parser.parse_args()
    if sys.platform != 'linux':
        parser.error('Linux GTK fixture only')
    run(args.task)


if __name__ == '__main__':
    main()
