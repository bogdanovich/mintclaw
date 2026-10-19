"""Private GTK parent's framed client; never attaches to an existing desktop."""
import json
import os
from pathlib import Path
import secrets
import select
import signal
import socket
import struct
import subprocess
import time


def signal_owned_input(keeper, executable, signum):
    children = Path(f"/proc/{keeper.pid}/task/{keeper.pid}/children").read_text().split()
    if len(children) != 1:
        raise RuntimeError("expected one original private input child")
    pid = int(children[0])
    descriptor = os.pidfd_open(pid)
    try:
        status = Path(f"/proc/{pid}/status").read_text().splitlines()
        parent = next(value for value in status if value.startswith("PPid:")).split()[1]
        if (int(parent) != keeper.pid or Path(f"/proc/{pid}/exe").resolve() != executable.resolve()
                or select.select([descriptor], [], [], 0)[0]):
            raise RuntimeError("private child identity changed")
        signal.pidfd_send_signal(descriptor, signum)
        if signum == signal.SIGSTOP:
            until = time.monotonic() + 0.25
            while True:
                state = next(value for value in Path(f"/proc/{pid}/status").read_text().splitlines()
                             if value.startswith("State:")).split()[1]
                if state == "T" and not select.select([descriptor], [], [], 0)[0]:
                    break
                if time.monotonic() >= until:
                    raise RuntimeError("owned input child did not enter kernel stopped state")
                time.sleep(0.001)
        return descriptor
    except BaseException:
        os.close(descriptor)
        raise


def run(executable, mode, bootstrap, binding, chords, pump, ready, observed):
    allowed = {"normal", "cancel_held", "crash_held", "blocked_stop", "foreign_stop", "duplicate_permit", "wire_loss"}
    if mode not in allowed:
        raise RuntimeError("unknown private qualification mode")
    operation = list(secrets.token_bytes(32))
    parent, child = socket.socketpair()
    keeper = subprocess.Popen([str(executable), "--keeper"], stdin=child, stdout=subprocess.DEVNULL,
                              stderr=subprocess.DEVNULL, close_fds=True)
    child.close()
    buffer = bytearray()
    result, keyboard, pending_ordinal = None, None, None
    sent_ready, sent_finish, injected = False, False, False
    rejected = 0
    cancelled_at = None
    lost_wire = False
    fault_descriptor = None

    def send(kind, **data):
        value = {"kind": kind, "data": data}
        raw = json.dumps(value, separators=(",", ":")).encode()
        parent.sendall(struct.pack(">I", len(raw)) + raw)

    try:
        parent.settimeout(1)
        send("start", operation=operation, bootstrap=bootstrap, binding=binding, chords=chords, budget_ms=10000)
        until = time.monotonic() + 20
        while time.monotonic() < until:
            pump(0.0001)
            if not lost_wire and select.select([parent], [], [], 0.001)[0]:
                chunk = parent.recv(65536)
                if not chunk:
                    if result is None:
                        raise RuntimeError("private keeper closed before result")
                    break
                buffer.extend(chunk)
            while len(buffer) >= 4:
                length = struct.unpack(">I", buffer[:4])[0]
                if not 0 < length <= 65536:
                    raise RuntimeError("private response frame bound")
                if len(buffer) < length + 4:
                    break
                packet = json.loads(buffer[4:length + 4])
                del buffer[:length + 4]
                kind, data = packet["kind"], packet.get("data", {})
                if kind != "rejected" and data.get("operation") != operation:
                    raise RuntimeError("foreign private result")
                if kind == "prepared":
                    # The real host bridge must check its original WindowGuard
                    # and lease epoch here. This parent owns the fixture only.
                    send("grant", operation=operation)
                elif kind == "admitted":
                    keyboard = data["keyboard"]
                elif kind == "edge":
                    ordinal, held = data["ordinal"], data["held"]
                    if not injected and mode in {"cancel_held", "crash_held", "blocked_stop", "wire_loss"} and held:
                        injected = True
                        if mode == "crash_held":
                            fault_descriptor = signal_owned_input(keeper, executable, signal.SIGKILL)
                            send("continue", operation=operation, ordinal=ordinal)
                        elif mode == "blocked_stop":
                            fault_descriptor = signal_owned_input(keeper, executable, signal.SIGSTOP)
                            send("continue", operation=operation, ordinal=ordinal)
                            cancelled_at = time.monotonic()
                            send("stop", operation=operation)
                        elif mode == "wire_loss":
                            parent.shutdown(socket.SHUT_RDWR)
                            parent.close()
                            lost_wire = True
                        else:
                            cancelled_at = time.monotonic()
                            send("stop", operation=operation)
                    elif not injected and mode == "foreign_stop":
                        injected = True
                        pending_ordinal = ordinal
                        foreign = operation.copy()
                        foreign[0] ^= 1
                        send("stop", operation=foreign)
                    elif not injected and mode == "duplicate_permit" and ordinal > 0:
                        injected = True
                        pending_ordinal = ordinal
                        send("continue", operation=operation, ordinal=ordinal - 1)
                    else:
                        send("continue", operation=operation, ordinal=ordinal)
                elif kind == "rejected":
                    rejected += 1
                    if pending_ordinal is None:
                        raise RuntimeError("unexpected private control rejection")
                    send("continue", operation=operation, ordinal=pending_ordinal)
                    pending_ordinal = None
                elif kind == "dispatched":
                    sent_finish = True
                elif kind == "complete":
                    result = data
                    break
                else:
                    raise RuntimeError("unexpected private phase")
            if result is not None:
                break
            if keyboard is not None and not sent_ready and ready(keyboard):
                send("ready", operation=operation)
                sent_ready = True
            if sent_finish and observed():
                send("finish", operation=operation)
                sent_finish = False
            if lost_wire and keeper.poll() is not None:
                result = {"wire_loss": True}
                break
        if result is None:
            raise RuntimeError("private process qualification deadline")
        keeper.wait(timeout=2)
        if lost_wire and keeper.returncode != 1:
            raise RuntimeError("lost-wire outcome must remain unconfirmed to its caller")
        if not lost_wire:
            if keeper.returncode != 0 or not result["cleanup_confirmed"]:
                raise RuntimeError("private keeper did not independently confirm cleanup")
            normal = mode in {"normal", "foreign_stop", "duplicate_permit"}
            if result["acknowledged"] != normal:
                raise RuntimeError("native acknowledgement outcome mismatch")
            if mode in {"cancel_held", "blocked_stop"}:
                if not result["canceled"] or not result["cancellation_delivered"]:
                    raise RuntimeError("scoped stop was not delivered")
                if time.monotonic() - cancelled_at > 1:
                    raise RuntimeError("blocked child concealed emergency stop")
            if mode in {"foreign_stop", "duplicate_permit"} and rejected != 1:
                raise RuntimeError("stale/foreign control was not rejected exactly once")
        if mode != "normal" and not injected:
            raise RuntimeError("private adversarial case did not run")
        return {"mode": mode, "cancel_latency_seconds": None if cancelled_at is None else time.monotonic() - cancelled_at,
                "metadata": result, "full_native_driver_qualified": False}
    finally:
        if fault_descriptor is not None:
            try:
                signal.pidfd_send_signal(fault_descriptor, signal.SIGKILL)
            except ProcessLookupError:
                pass
            finally:
                os.close(fault_descriptor)
        if not lost_wire:
            parent.close()
        if keeper.poll() is None:
            keeper.kill()
            keeper.wait(timeout=3)
