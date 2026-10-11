import os
import pathlib
import struct

ROOT = pathlib.Path("/qual-credentials")
UID = GID = 65532
CANARY = b"MINTCLAW_SYNTHETIC_LOADER_TOKEN_v1"

if os.geteuid() != 0 or not pathlib.Path("/.dockerenv").exists():
    raise SystemExit("synthetic loader setup requires its disposable root container")
if os.environ.get("MINTCLAW_SYNTHETIC_LOADER_SETUP") != "1":
    raise SystemExit("synthetic loader setup is not enabled")
if any(ROOT.iterdir()):
    raise SystemExit("synthetic loader volume must be empty")


def acl(permission, *, extra_user=False, group_read=False, other_read=False):
    entries = [(1, permission, 0xFFFFFFFF), (2, permission, UID)]
    if extra_user:
        entries.append((2, permission, UID + 1))
    entries.extend([
        (4, 4 if group_read else 0, 0xFFFFFFFF),
        (16, permission, 0xFFFFFFFF),
        (32, 4 if other_read else 0, 0xFFFFFFFF),
    ])
    return struct.pack("<I", 2) + b"".join(struct.pack("<HHI", *entry) for entry in entries)


def source(name, value=CANARY, **options):
    directory = ROOT / name
    directory.mkdir(mode=0o700)
    file = directory / "op-service-account"
    file.write_bytes(value)
    os.chmod(file, 0o400)
    os.chmod(directory, 0o500)
    os.setxattr(file, "system.posix_acl_access", acl(4, **options))
    os.setxattr(directory, "system.posix_acl_access", acl(5))
    return directory, file


os.chmod(ROOT, 0o755)
(ROOT / "synthetic-only").write_text("mintclaw-synthetic-loader-v1")
os.chmod(ROOT / "synthetic-only", 0o444)
source("canonical")
source("extra-user", extra_user=True)
source("group-readable", group_read=True)
source("other-readable", other_read=True)
_, file = source("owner-writable")
os.chmod(file, 0o640)
_, file = source("multiple-links")
os.link(file, ROOT / "second-link")
directory, file = source("symlink")
os.chmod(directory, 0o750)
file.unlink()
file.symlink_to(ROOT / "canonical" / "op-service-account")
os.chmod(directory, 0o550)
directory, file = source("fifo")
os.chmod(directory, 0o750)
file.unlink()
os.mkfifo(file, mode=0o400)
os.setxattr(file, "system.posix_acl_access", acl(4))
os.chmod(directory, 0o550)
source("empty", value=b"")
source("oversize", value=b"x" * 32769)
source("newline", value=CANARY + b"\n")
source("non-ascii", value=CANARY + b"\xff")
unsafe_parent = ROOT / "unsafe-parent"
unsafe_parent.mkdir()
os.chmod(unsafe_parent, 0o777)
source("unsafe-parent/source")

fallback = ROOT / "fallback"
fallback.mkdir(mode=0o700)
file = fallback / "op-service-account"
file.write_bytes(CANARY)
os.chmod(file, 0o400)
os.chmod(fallback, 0o500)
os.chown(file, UID, GID)
os.chown(fallback, UID, GID)
print("synthetic credential loader fixtures prepared")
