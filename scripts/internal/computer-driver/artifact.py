#!/usr/bin/env python3
"""Record/verify a private candidate-driver artifact; this grants no authority."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys

SOURCE_COMMIT = "0c69d9a2c8abbb5051a2df4a23f0fa166d5385dc"
DRIVER_VERSION = "0.34.1"
PATCH_ID = "mintclaw-atspi-utf8-v1"
RUST_VERSION = "1.97.1"
TARGET = "x86_64-unknown-linux-gnu"
FEATURES = ["cua-driver/portal-input"]


def digest(path):
    path = Path(path)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o022:
        raise ValueError("artifact input must be a regular non-writable-by-others file")
    result = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            result.update(chunk)
    return result.hexdigest()


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate artifact metadata key")
        result[key] = value
    return result


def expected(patch):
    return {
        "schema_version": 1,
        "upstream": "https://github.com/trycua/cua",
        "source_commit": SOURCE_COMMIT,
        "driver_version": DRIVER_VERSION,
        "patch_id": PATCH_ID,
        "patch_sha256": digest(patch),
        "rust_toolchain": RUST_VERSION,
        "target": TARGET,
        "features": FEATURES,
    }


def emit(source, patch, binary, output):
    source = Path(source)
    actual = subprocess.check_output(
        ["git", "-C", str(source), "rev-parse", "HEAD"], text=True, stderr=subprocess.DEVNULL
    ).strip()
    if actual != SOURCE_COMMIT:
        raise ValueError("wrong pinned source commit")
    metadata = expected(patch)
    metadata.update({
        "binary_sha256": digest(binary),
        "cargo_lock_sha256": digest(source / "libs/cua-driver/rust/Cargo.lock"),
        "patched_native_sha256": digest(
            source / "libs/cua-driver/rust/crates/platform-linux/src/atspi/native.rs"
        ),
    })
    # Never overwrite a prior receipt or follow an output symlink.
    descriptor = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        json.dump(metadata, stream, sort_keys=True, indent=2)
        stream.write("\n")


def verify(manifest, patch, binary):
    manifest = Path(manifest)
    digest(manifest)  # same regular-file/private-writer checks as the binary
    if manifest.stat().st_size > 16 * 1024:
        raise ValueError("artifact metadata exceeds bound")
    with manifest.open(encoding="utf-8") as stream:
        metadata = json.load(stream, object_pairs_hook=unique_object)
    if not isinstance(metadata, dict):
        raise ValueError("artifact metadata is not an object")
    fixed = expected(patch)
    keys = set(fixed) | {"binary_sha256", "cargo_lock_sha256", "patched_native_sha256"}
    if set(metadata) != keys or any(
        type(metadata.get(key)) is not type(value) or metadata.get(key) != value
        for key, value in fixed.items()
    ):
        raise ValueError("artifact provenance does not match pinned source and patch")
    for key in ("binary_sha256", "cargo_lock_sha256", "patched_native_sha256"):
        value = metadata[key]
        if not isinstance(value, str) or len(value) != 64 or any(c not in "0123456789abcdef" for c in value):
            raise ValueError("invalid artifact digest")
    if metadata["binary_sha256"] != digest(binary):
        raise ValueError("artifact executable checksum mismatch")
    return metadata["binary_sha256"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    writer = commands.add_parser("emit")
    writer.add_argument("--source", required=True)
    writer.add_argument("--patch", required=True)
    writer.add_argument("--binary", required=True)
    writer.add_argument("--output", required=True)
    reader = commands.add_parser("verify")
    reader.add_argument("--manifest", required=True)
    reader.add_argument("--patch", required=True)
    reader.add_argument("--binary", required=True)
    args = parser.parse_args()
    try:
        if args.command == "emit":
            emit(args.source, args.patch, args.binary, args.output)
        else:
            print(verify(args.manifest, args.patch, args.binary))
    except (ValueError, OSError, subprocess.SubprocessError):
        # Paths, subprocess output and metadata are not safe diagnostic content.
        print("private computer-driver artifact verification failed", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
