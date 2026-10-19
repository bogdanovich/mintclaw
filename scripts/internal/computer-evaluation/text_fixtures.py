"""Private controller fixtures/oracles, not a GUI executor or task-pass verifier."""

import argparse
from dataclasses import asdict, dataclass
import hashlib
import json
import os
from pathlib import Path
import stat
import sys
from types import MappingProxyType
from typing import Optional
import uuid


U = "MintClaw — Привет 世界 café 👩‍💻"
R = "Обновлено — naïve 🌿"
INITIAL = "ALPHA\n" + U + "\nOMEGA\n"
BLOCK = "FIRST\t" + U + "\nSECOND e\u0301\nTHIRD " + R + "\n"
MAX_ARTIFACT_BYTES = 64 * 1024
CANDIDATE = "document.txt"
MARKER = ".mintclaw-text-fixture.json"


@dataclass(frozen=True)
class Task:
    task_id: str
    initial: str
    expected: str
    instruction: str
    caret_scalars: Optional[int] = None
    seed_file: bool = True


TASKS = MappingProxyType({
    "CU-T01": Task(
        "CU-T01", "", U + "\n",
        "Create a new plain-text document containing exactly " + U
        + " followed by LF. Save as document.txt in the admitted task directory, then reopen that same file.",
        seed_file=False,
    ),
    "CU-T02": Task(
        "CU-T02", INITIAL, "ALPHA\n" + R + U + "\nOMEGA\n",
        "Insert " + R + " at Unicode-scalar offset 6, immediately after ALPHA and its LF. "
        "Preserve every existing character; do not replace the whole document. Save the same file.",
        caret_scalars=6,
    ),
    "CU-T03": Task(
        "CU-T03", INITIAL, "ALPHA\n" + R + "\nOMEGA\n",
        "Replace only the middle line " + U + " with " + R
        + ". Preserve both sentinel lines and LF endings. Save the same file.",
    ),
    "CU-T04": Task(
        "CU-T04", INITIAL, INITIAL + BLOCK,
        "Append exactly these three LF-terminated lines, preserving the original document: "
        + repr(BLOCK) + ". Interpret the shown tab/LF escapes literally as tab/LF; preserve combining e + U+0301. "
        "Save the same file.",
    ),
    "CU-T05": Task(
        "CU-T05", "needle A\nkeep\nneedle B\n", "needle A\nkeep\nedited B\n",
        "Find the second occurrence of needle and replace only that word with edited. "
        "Keep the first occurrence, keep line and B unchanged. Save the same file.",
    ),
})


def digest(data):
    return hashlib.sha256(data).hexdigest()


# This subset revision is not an executable revision or score for the full pack.
FIXTURE_REVISION = digest(json.dumps({
    "subset": "native-editor-v1", "maximum_bytes": MAX_ARTIFACT_BYTES,
    "candidate": CANDIDATE, "encoding": "utf-8", "normalization": "none",
    "tasks": [asdict(task) for task in TASKS.values()],
}, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))


class Unverifiable(Exception):
    """A fixed public reason, never OS errors or candidate contents."""


def file_identity(info):
    return info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns


def valid_file(info):
    return stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and info.st_nlink == 1


def read_owned_file(directory, name, maximum):
    descriptor = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
    try:
        before = os.fstat(descriptor)
        if not valid_file(before):
            raise Unverifiable("unsafe_file")
        if before.st_size > maximum:
            raise Unverifiable("artifact_too_large")
        chunks, size = [], 0
        while True:
            chunk = os.read(descriptor, min(8192, maximum + 1 - size))
            if not chunk:
                break
            chunks.append(chunk)
            size += len(chunk)
            if size > maximum:
                raise Unverifiable("artifact_too_large")
        after = os.fstat(descriptor)
        named = os.stat(name, dir_fd=directory, follow_symlinks=False)
        if not valid_file(after) or not valid_file(named) or \
                file_identity(before) != file_identity(after) or file_identity(after) != file_identity(named):
            raise Unverifiable("artifact_changed")
        return b"".join(chunks)
    finally:
        os.close(descriptor)


def open_directory(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    info = os.fstat(descriptor)
    if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
        os.close(descriptor)
        raise Unverifiable("unsafe_directory")
    return descriptor, info


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise Unverifiable("invalid_fixture_record")
        result[key] = value
    return result


def write_new(directory, name, data):
    descriptor = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory)
    try:
        with os.fdopen(descriptor, "wb", closefd=False) as output:
            output.write(data)
    finally:
        os.close(descriptor)


def prepare(task_id, path):
    task = TASKS[task_id]
    # Never adopt, overwrite or clean an existing directory, including on error.
    Path(path).mkdir(mode=0o700, parents=False, exist_ok=False)
    directory, info = open_directory(path)
    try:
        record = {
            "task_id": task_id, "fixture_revision": FIXTURE_REVISION, "run_id": uuid.uuid4().hex,
            "root_device": info.st_dev, "root_inode": info.st_ino,
        }
        write_new(directory, MARKER, json.dumps(record, sort_keys=True).encode("utf-8"))
        if task.seed_file:
            write_new(directory, CANDIDATE, task.initial.encode("utf-8"))
        return {
            "task_id": task_id, "fixture_revision": FIXTURE_REVISION, "run_id": record["run_id"],
            "candidate": CANDIDATE, "seeded": task.seed_file, "instruction": task.instruction,
            "caret_scalars": task.caret_scalars, "native_task_qualified": False,
        }
    finally:
        os.close(directory)


def check_artifact(task_id, path):
    result = {
        "task_id": task_id, "fixture_revision": FIXTURE_REVISION,
        "artifact_matches": None, "native_task_qualified": False,
    }
    try:
        directory, root = open_directory(path)
        try:
            record = json.loads(read_owned_file(directory, MARKER, 4096), object_pairs_hook=unique_object)
            if not isinstance(record, dict) or set(record) != {
                "task_id", "fixture_revision", "run_id", "root_device", "root_inode",
            } or record["task_id"] != task_id or record["fixture_revision"] != FIXTURE_REVISION or \
                    record["root_device"] != root.st_dev or record["root_inode"] != root.st_ino or \
                    not isinstance(record["run_id"], str) or len(record["run_id"]) != 32 or \
                    any(char not in "0123456789abcdef" for char in record["run_id"]):
                raise Unverifiable("invalid_fixture_record")
            data = read_owned_file(directory, CANDIDATE, MAX_ARTIFACT_BYTES)
            data.decode("utf-8", errors="strict")
            named_root = os.stat(path, follow_symlinks=False)
            if (root.st_dev, root.st_ino, root.st_uid, root.st_mode) != (
                named_root.st_dev, named_root.st_ino, named_root.st_uid, named_root.st_mode,
            ):
                raise Unverifiable("directory_changed")
            result.update({
                "run_id": record["run_id"], "artifact_matches": data == TASKS[task_id].expected.encode("utf-8"),
                "artifact_sha256": digest(data), "artifact_bytes": len(data),
            })
        finally:
            os.close(directory)
    except Unverifiable as error:
        result["reason"] = str(error)
    except (OSError, ValueError, UnicodeError, TypeError, KeyError, RecursionError):
        result["reason"] = "artifact_unavailable"
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("prepare", "check-artifact"))
    parser.add_argument("--task", choices=tuple(TASKS), required=True)
    parser.add_argument("--run-directory", required=True)
    arguments = parser.parse_args()
    if arguments.operation == "prepare":
        try:
            result = prepare(arguments.task, arguments.run_directory)
        except (OSError, Unverifiable):
            print(json.dumps({"reason": "setup_refused", "native_task_qualified": False}))
            return 2
    else:
        result = check_artifact(arguments.task, arguments.run_directory)
    print(json.dumps(result, sort_keys=True, ensure_ascii=False))
    if result.get("artifact_matches") is False:
        return 1
    return 2 if result.get("reason") else 0


if __name__ == "__main__":
    sys.exit(main())
