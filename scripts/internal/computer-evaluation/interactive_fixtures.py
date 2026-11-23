"""Private CU-T18–19 recipes and state projections, never native-source proof."""

import argparse
import base64
import json
import math
import os
from pathlib import Path
import sys
from types import MappingProxyType
import uuid

import text_fixtures as shared


MARKER = ".mintclaw-interactive-fixture.json"
SPEC_FILE = "fixture.json"
MAX_SNAPSHOT_BYTES = 32 * 1024
MAX_LAUNCH_BYTES = 16 * 1024
GUARD_TEXT = "GUARD_UNCHANGED"


def encoded(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False).encode("utf-8")


SPECS = MappingProxyType({
    "CU-T18": encoded({
        "surface": {"width": 640, "height": 480},
        "cards": [{"id": "alpha", "label": "Card Alpha", "x": 40, "y": 60, "width": 120, "height": 70},
                  {"id": "beta", "label": "Card Beta", "x": 260, "y": 60, "width": 120, "height": 70}],
        "destination": {"x": 32, "y": 252, "width": 136, "height": 86},
        "instruction": "Drag Card Alpha into the outlined destination below it. Keep Card Beta and the sibling "
                       "guard window unchanged. Complete the drag by releasing its button.",
    }),
    "CU-T19": encoded({
        "surface": {"width": 640, "height": 480},
        "fields": {"name": "", "choice": "alpha", "confirmed": False, "period": "daily"},
        "decoys": {"name_preview": "KEEP", "choice_preview": "gamma", "confirmed_preview": False},
        "choices": ["alpha", "beta", "gamma"], "periods": ["daily", "monthly", "yearly"],
        "instruction": "Fill the Name field with exactly " + shared.U + ". Select beta in Choice, check Confirm, "
                       "and select monthly in Period. Leave all preview controls and the sibling guard unchanged.",
    }),
})
FIXTURE_REVISION = shared.digest(encoded({
    "subset": "interactive-v1", "specs": {key: json.loads(value) for key, value in SPECS.items()},
    "guard": {"text": GUARD_TEXT, "count": 0, "visible": True},
    "form_expected": {"name": shared.U, "choice": "beta", "confirmed": True, "period": "monthly"},
    "maximum_snapshot_bytes": MAX_SNAPSHOT_BYTES,
    "maximum_launch_bytes": MAX_LAUNCH_BYTES,
    "rules": "logical client coordinates; alpha contained by destination; beta unchanged; completed alpha drag "
             "from original position with actual motion callbacks; exact form and decoys; bounded typed state; "
             "native source/freshness/execution and lifecycle remain independently required",
}))


def seed(task_id):
    return json.loads(SPECS[task_id])


def prepare(task_id, path):
    spec = SPECS[task_id]
    Path(path).mkdir(mode=0o700, parents=False, exist_ok=False)
    directory, root = shared.open_directory(path)
    try:
        record = {"task_id": task_id, "fixture_revision": FIXTURE_REVISION, "run_id": uuid.uuid4().hex,
                  "root_device": root.st_dev, "root_inode": root.st_ino}
        shared.write_new(directory, MARKER, encoded(record))
        shared.write_new(directory, SPEC_FILE, spec)
        return {**record, "instruction": seed(task_id)["instruction"], "native_task_qualified": False,
                "native_source_verified": False}
    finally:
        os.close(directory)


def load_fixture(task_id, path):
    directory, root = shared.open_directory(path)
    try:
        record = json.loads(shared.read_owned_file(directory, MARKER, 4096), object_pairs_hook=shared.unique_object)
        if not isinstance(record, dict) or set(record) != {
            "task_id", "fixture_revision", "run_id", "root_device", "root_inode",
        } or record["task_id"] != task_id or record["fixture_revision"] != FIXTURE_REVISION or \
                (record["root_device"], record["root_inode"]) != (root.st_dev, root.st_ino) or \
                not isinstance(record["run_id"], str) or len(record["run_id"]) != 32 or \
                any(c not in "0123456789abcdef" for c in record["run_id"]):
            raise shared.Unverifiable("invalid_fixture_record")
        if shared.read_owned_file(directory, SPEC_FILE, 8192) != SPECS[task_id]:
            raise shared.Unverifiable("fixture_spec_changed")
        named = os.stat(path, follow_symlinks=False)
        if (root.st_dev, root.st_ino, root.st_uid, root.st_mode) != (
            named.st_dev, named.st_ino, named.st_uid, named.st_mode,
        ):
            raise shared.Unverifiable("directory_changed")
        return record, seed(task_id)
    finally:
        os.close(directory)


def finite(value):
    return type(value) in (int, float) and abs(value) <= 10000 and math.isfinite(value)


def launch_config(task_id, path):
    record, _ = load_fixture(task_id, path)
    return {"record": record, "spec_base64": base64.b64encode(SPECS[task_id]).decode("ascii")}


def parse_launch(task_id, data):
    """Validate fixture data only; a launch message cannot grant native authority."""
    if not isinstance(data, bytes) or len(data) > MAX_LAUNCH_BYTES:
        raise shared.Unverifiable("launch_bounds")
    value = json.loads(data, object_pairs_hook=shared.unique_object)
    if not isinstance(value, dict) or set(value) != {"record", "spec_base64"}:
        raise shared.Unverifiable("invalid_launch")
    record = value["record"]
    if not isinstance(record, dict) or set(record) != {
        "task_id", "fixture_revision", "run_id", "root_device", "root_inode",
    } or record["task_id"] != task_id or record["fixture_revision"] != FIXTURE_REVISION or \
            not isinstance(record["run_id"], str) or len(record["run_id"]) != 32 or \
            any(c not in "0123456789abcdef" for c in record["run_id"]) or any(
                type(record[k]) is not int or record[k] < 0 for k in ("root_device", "root_inode")):
        raise shared.Unverifiable("invalid_launch")
    if not isinstance(value["spec_base64"], str) or \
            base64.b64decode(value["spec_base64"], validate=True) != SPECS[task_id]:
        raise shared.Unverifiable("fixture_spec_changed")
    return record, seed(task_id)


def point(value):
    return isinstance(value, list) and len(value) == 2 and all(finite(v) for v in value)


def canvas_matches(spec, state):
    if not isinstance(state, dict) or set(state) != {"cards", "drag"} or not isinstance(state["cards"], list) or \
            len(state["cards"]) != 2:
        raise shared.Unverifiable("invalid_canvas_state")
    for actual, original in zip(state["cards"], spec["cards"]):
        if not isinstance(actual, dict) or set(actual) != set(original) or any(
                not finite(actual[key]) for key in ("x", "y", "width", "height")):
            raise shared.Unverifiable("invalid_canvas_state")
        if any(actual[key] != original[key] for key in ("id", "label", "width", "height")):
            return False
    first, second = state["cards"]
    if second != spec["cards"][1]:
        return False
    zone = spec["destination"]
    if not (zone["x"] <= first["x"] <= zone["x"] + zone["width"] - first["width"] and
            zone["y"] <= first["y"] <= zone["y"] + zone["height"] - first["height"]):
        return False
    drag = state["drag"]
    if not isinstance(drag, dict) or set(drag) != {"card", "from", "to", "updates", "completed"} or \
            not point(drag["from"]) or not point(drag["to"]) or type(drag["updates"]) is not int:
        raise shared.Unverifiable("invalid_drag_state")
    return (drag["card"] == "alpha" and drag["from"] == [40, 60] and
            drag["to"] == [first["x"], first["y"]] and 1 <= drag["updates"] <= 100000 and
            drag["completed"] is True)


def form_matches(spec, state):
    if not isinstance(state, dict) or set(state) != {"fields", "decoys"} or \
            not isinstance(state["fields"], dict) or not isinstance(state["decoys"], dict) or \
            set(state["fields"]) != set(spec["fields"]) or set(state["decoys"]) != set(spec["decoys"]):
        raise shared.Unverifiable("invalid_form_state")
    fields, decoys = state["fields"], state["decoys"]
    return (fields == {"name": shared.U, "choice": "beta", "confirmed": True, "period": "monthly"}
            and fields["confirmed"] is True and decoys == spec["decoys"] and decoys["confirmed_preview"] is False)


def check_state(task_id, path, data):
    result = {"task_id": task_id, "fixture_revision": FIXTURE_REVISION, "state_matches": None,
              "native_task_qualified": False, "native_source_verified": False}
    try:
        record, spec = load_fixture(task_id, path)
        if not isinstance(data, bytes) or len(data) > MAX_SNAPSHOT_BYTES:
            raise shared.Unverifiable("snapshot_bounds")
        value = json.loads(data, object_pairs_hook=shared.unique_object)
        if not isinstance(value, dict) or set(value) != {"task_id", "run_id", "fixture_revision", "state", "guard"} or \
                any(value[key] != record[key] for key in ("task_id", "run_id", "fixture_revision")):
            raise shared.Unverifiable("invalid_snapshot_binding")
        guard = value["guard"]
        if not isinstance(guard, dict) or set(guard) != {"text", "count", "visible"} or type(guard["count"]) is not int:
            raise shared.Unverifiable("invalid_guard_state")
        unchanged = guard == {"text": GUARD_TEXT, "count": 0, "visible": True} and guard["visible"] is True
        matched = canvas_matches(spec, value["state"]) if task_id == "CU-T18" else form_matches(spec, value["state"])
        result.update(run_id=record["run_id"], state_matches=unchanged and matched,
                      snapshot_sha256=shared.digest(data), snapshot_bytes=len(data))
    except shared.Unverifiable as error:
        result["reason"] = str(error)
    except (OSError, ValueError, TypeError, KeyError, RecursionError, OverflowError):
        result["reason"] = "snapshot_unavailable"
    return result


class CanvasModel:
    """Owned fixture layout only. GUI callbacks supply all input; no native claim."""

    def __init__(self, spec):
        self.spec = spec
        self.cards = json.loads(encoded(spec["cards"]))
        self.active = None
        self.drag = None

    def begin(self, x, y):
        if not all(finite(v) for v in (x, y)):
            return False
        for index in reversed(range(len(self.cards))):
            card = self.cards[index]
            if card["x"] <= x < card["x"] + card["width"] and card["y"] <= y < card["y"] + card["height"]:
                self.active = (index, x - card["x"], y - card["y"])
                self.drag = {"card": card["id"], "from": [card["x"], card["y"]],
                             "to": [card["x"], card["y"]], "updates": 0, "completed": False}
                return True
        return False

    def move(self, x, y, count=True):
        if self.active is None or not all(finite(v) for v in (x, y)):
            return False
        index, dx, dy = self.active
        card = self.cards[index]
        card["x"] = min(max(x - dx, 0), self.spec["surface"]["width"] - card["width"])
        card["y"] = min(max(y - dy, 0), self.spec["surface"]["height"] - card["height"])
        self.drag["to"] = [card["x"], card["y"]]
        if count:
            self.drag["updates"] = min(self.drag["updates"] + 1, 100000)
        return True

    def finish(self, x, y):
        if not self.move(x, y, count=False):
            return False
        self.drag["completed"] = True
        self.active = None
        return True

    def cancel(self):
        self.active = None

    def snapshot(self):
        return json.loads(encoded({"cards": self.cards, "drag": self.drag}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("prepare", "launch-config", "check-state"))
    parser.add_argument("--task", choices=tuple(SPECS), required=True)
    parser.add_argument("--run-directory", required=True)
    args = parser.parse_args()
    if args.operation == "prepare":
        try:
            result = prepare(args.task, args.run_directory)
        except (OSError, shared.Unverifiable):
            result = {"reason": "setup_refused", "native_task_qualified": False, "native_source_verified": False}
    elif args.operation == "launch-config":
        try:
            result = launch_config(args.task, args.run_directory)
        except (OSError, shared.Unverifiable, ValueError, TypeError):
            result = {"reason": "launch_refused", "native_task_qualified": False, "native_source_verified": False}
    else:
        result = check_state(args.task, args.run_directory, sys.stdin.buffer.read(MAX_SNAPSHOT_BYTES + 1))
    print(json.dumps(result, sort_keys=True))
    return 2 if result.get("reason") else 1 if result.get("state_matches") is False else 0


if __name__ == "__main__":
    sys.exit(main())
