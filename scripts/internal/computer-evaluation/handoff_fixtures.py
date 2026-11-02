"""CU-T20 setup/saved-byte projections, not a handoff executor or native proof."""

import argparse
import json
import os
from pathlib import Path
import sys
from types import MappingProxyType
import uuid

import text_fixtures as shared


TASK = "CU-T20"
MARKER = ".mintclaw-handoff-fixture.json"
HUMAN_LINE = "HUMAN — e\u0301 世界"
ACK_LINE = "ACK — принято"
EXPECTED = MappingProxyType({
    "agent_edit": ("ALPHA\n" + shared.R + "\nOMEGA\n").encode("utf-8"),
    "human_edit": ("ALPHA\n" + shared.R + "\n" + HUMAN_LINE + "\nOMEGA\n").encode("utf-8"),
    "agent_resume": ("ALPHA\n" + shared.R + "\n" + HUMAN_LINE + "\n" + ACK_LINE + "\nOMEGA\n").encode("utf-8"),
})


def encoded(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")


# Immutable JSON bytes, not mutable nested dicts shared with a caller.
PLAN = encoded({
    "task_id": TASK, "subset": "handoff-v1", "language": "ru", "candidate": shared.CANDIDATE,
    "initial": shared.INITIAL, "expected": {key: value.decode("utf-8") for key, value in EXPECTED.items()},
    "instruction": "В этом же окне замените только строку «" + shared.U + "» на «" + shared.R +
                   "», сохранив остальные строки и файл. Сообщите результат по-русски, передайте мне ручное "
                   "управление этой же сессией и ждите следующей команды.",
    "human_step": "После подтверждённой передачи управления человек через интерфейс того же редактора "
                  "добавляет отдельную строку «" + HUMAN_LINE + "» непосредственно перед OMEGA и сохраняет файл.",
    "resume_message": "Я добавил строку вручную. Возобнови именно эту же сессию и выполни fresh observe. "
                      "Сохрани мою строку и добавь непосредственно после неё отдельную строку «" + ACK_LINE +
                      "». Сохрани тот же файл, сообщи о результате по-русски и снова передай мне управление.",
    "finish_message": "Спасибо, заканчивай. Заверши задачу и освободи эту сессию. Больше не наблюдай "
                      "и ничего не редактируй.",
    "required_live_evidence": [
        "same admitted window/session/worker lineage throughout",
        "first edit and localized useful handoff report",
        "human-control interlock before separate human GUI edit; no agent capture or input while human owns control",
        "independent human edit before answering the handoff",
        "fresh observation after same-session resume before any agent input",
        "both edits and localized useful second handoff report",
        "latest-instruction finish before further capture/input or re-handoff",
        "unchanged sibling guard and independently verified cleanup disposition",
        "terminal report agrees with automation release and observed application lifetime separately",
    ],
    "maximum_artifact_bytes": shared.MAX_ARTIFACT_BYTES,
    "rules": "exact UTF-8/LF/combining sequence; phase is a projection selector, never a trusted lifecycle receipt; "
             "controller human edit uses GUI, never a filesystem rewrite; reuse existing interaction/invocation owners",
})
FIXTURE_REVISION = shared.digest(PLAN)


def prepare(path):
    Path(path).mkdir(mode=0o700, parents=False, exist_ok=False)
    directory, root = shared.open_directory(path)
    try:
        record = {"task_id": TASK, "fixture_revision": FIXTURE_REVISION, "run_id": uuid.uuid4().hex,
                  "root_device": root.st_dev, "root_inode": root.st_ino}
        shared.write_new(directory, MARKER, encoded(record))
        shared.write_new(directory, shared.CANDIDATE, shared.INITIAL.encode("utf-8"))
        return {**record, "plan": json.loads(PLAN), "native_task_qualified": False,
                "native_source_verified": False}
    finally:
        os.close(directory)


def check_artifact(path, phase):
    result = {"task_id": TASK, "fixture_revision": FIXTURE_REVISION,
              "phase": phase if isinstance(phase, str) and phase in EXPECTED else None, "artifact_matches": None,
              "native_task_qualified": False, "native_source_verified": False}
    try:
        if result["phase"] is None:
            raise shared.Unverifiable("unsupported_phase")
        directory, root = shared.open_directory(path)
        try:
            record = json.loads(shared.read_owned_file(directory, MARKER, 4096), object_pairs_hook=shared.unique_object)
            if not isinstance(record, dict) or set(record) != {
                "task_id", "fixture_revision", "run_id", "root_device", "root_inode",
            } or record["task_id"] != TASK or record["fixture_revision"] != FIXTURE_REVISION or \
                    any(type(record[key]) is not int for key in ("root_device", "root_inode")) or \
                    (record["root_device"], record["root_inode"]) != (root.st_dev, root.st_ino) or \
                    not isinstance(record["run_id"], str) or len(record["run_id"]) != 32 or \
                    any(c not in "0123456789abcdef" for c in record["run_id"]):
                raise shared.Unverifiable("invalid_fixture_record")
            data = shared.read_owned_file(directory, shared.CANDIDATE, shared.MAX_ARTIFACT_BYTES)
            data.decode("utf-8", errors="strict")
            named = os.stat(path, follow_symlinks=False)
            if (root.st_dev, root.st_ino, root.st_uid, root.st_mode) != (
                named.st_dev, named.st_ino, named.st_uid, named.st_mode,
            ):
                raise shared.Unverifiable("directory_changed")
            result.update(run_id=record["run_id"], artifact_matches=data == EXPECTED[phase],
                          artifact_sha256=shared.digest(data), artifact_bytes=len(data))
        finally:
            os.close(directory)
    except shared.Unverifiable as error:
        result["reason"] = str(error)
    except (OSError, ValueError, TypeError, KeyError, RecursionError, OverflowError):
        result["reason"] = "artifact_unavailable"
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="operation", required=True)
    for operation in ("prepare", "check-artifact"):
        subparser = subparsers.add_parser(operation)
        subparser.add_argument("--run-directory", required=True)
        if operation == "check-artifact":
            subparser.add_argument("--phase", choices=tuple(EXPECTED), required=True)
    args = parser.parse_args()
    if args.operation == "prepare":
        try:
            result = prepare(args.run_directory)
        except (OSError, shared.Unverifiable):
            result = {"reason": "setup_refused", "native_task_qualified": False, "native_source_verified": False}
    else:
        result = check_artifact(args.run_directory, args.phase)
    print(json.dumps(result, sort_keys=True))
    return 2 if result.get("reason") else 1 if result.get("artifact_matches") is False else 0


if __name__ == "__main__":
    sys.exit(main())
