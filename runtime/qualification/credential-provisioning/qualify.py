"""Exercise real OS encryption with synthetic data in an isolated container."""

import errno
import os
from pathlib import Path
import pty
import select
import signal
import stat
import subprocess
import time


TOKEN = b"SYNTHETIC_OS_PROVISION_CANARY_20261010"
TARGET = Path("/etc/credstore.encrypted/mintclaw-credential/op-service-account")


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def invoke(input_value):
    pid, terminal = pty.fork()
    if pid == 0:
        os.execv("/qual/mintclaw-credential", ["/qual/mintclaw-credential", "provision"])
    output = bytearray()
    prompted = False
    status = None
    deadline = time.monotonic() + 40
    try:
        while time.monotonic() < deadline:
            ready, _, _ = select.select([terminal], [], [], 0.1)
            if ready:
                try:
                    chunk = os.read(terminal, 4096)
                except OSError as error:
                    if error.errno != errno.EIO:
                        raise
                    chunk = b""
                if not chunk:
                    if status is not None:
                        break
                    completed, result = os.waitpid(pid, os.WNOHANG)
                    if completed:
                        status = result
                        break
                    time.sleep(0.01)
                    continue
                output.extend(chunk)
                require(len(output) < 16384, "unbounded credential command output")
                if b"Token: " in output and not prompted:
                    prompted = True
                    os.write(terminal, input_value)
            if status is None:
                completed, result = os.waitpid(pid, os.WNOHANG)
                if completed:
                    status = result
        if status is None:
            completed, result = os.waitpid(pid, os.WNOHANG)
            if completed:
                status = result
        require(status is not None, "credential command did not terminate")
        require(TOKEN not in output, "terminal exposed synthetic private input")
        return os.waitstatus_to_exitcode(status), bytes(output), prompted
    finally:
        if status is None:
            os.kill(pid, signal.SIGKILL)
            os.waitpid(pid, 0)
        os.close(terminal)


def qualify():
    require(os.getuid() == 0, "qualification requires container root")
    require(Path("/.dockerenv").exists(), "qualification requires a disposable Docker container")
    require(os.environ.get("MINTCLAW_DISPOSABLE_CREDENTIAL_QUALIFICATION") == "1", "qualification not opted in")
    require(not TARGET.exists(), "qualification refuses existing credential storage")

    code, output, prompted = invoke(TOKEN + b"\x03")
    require(code != 0 and prompted and not TARGET.exists(), "interrupted input created a credential")

    code, output, prompted = invoke(TOKEN + b"\n")
    require(code == 0 and prompted, "real OS credential provisioning failed")
    require(b"Agent access remains disabled" in output, "provisioning implied agent activation")
    require(TARGET.stat().st_uid == 0 and stat.S_IMODE(TARGET.stat().st_mode) == 0o600, "credential ownership is unsafe")
    require(stat.S_IMODE(TARGET.parent.stat().st_mode) == 0o700, "credential directory is not private")
    require(TOKEN not in TARGET.read_bytes(), "synthetic plaintext persisted")
    result = subprocess.run(
        ["/usr/bin/systemd-creds", "decrypt", "--name=op-service-account", str(TARGET), "-"],
        check=False, capture_output=True, timeout=15,
    )
    require(result.returncode == 0 and result.stdout == TOKEN, "OS credential round trip failed")
    original = TARGET.read_bytes()
    code, output, prompted = invoke(b"UNUSED_SYNTHETIC_REPLACEMENT\n")
    require(code != 0 and not prompted and TARGET.read_bytes() == original, "existing credential was replaced")
    require(list(TARGET.parent.iterdir()) == [TARGET], "encrypted temporary file leaked")
    print("credential OS qualification passed: hidden input, interruption, encryption, ownership, no overwrite")


if __name__ == "__main__":
    try:
        qualify()
    except Exception:
        # Never print subprocess output or exception text from a private path.
        print("credential OS qualification failed", flush=True)
        raise SystemExit(1) from None
