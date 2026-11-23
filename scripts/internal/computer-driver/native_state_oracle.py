"""Independent public Xlib/libXi ABI checks on a fixture-owned connection."""
import ctypes as C
import os
from pathlib import Path
import socket
import struct


def bind(library, name, result, *arguments):
    function = getattr(library, name)
    function.restype, function.argtypes = result, list(arguments)
    return function


class DeviceInfo(C.Structure):
    _fields_ = [("deviceid", C.c_int), ("name", C.c_char_p), ("use", C.c_int),
                ("attachment", C.c_int), ("enabled", C.c_int),
                ("num_classes", C.c_int), ("classes", C.c_void_p)]


def device_snapshot(xi, display):
    count = C.c_int()
    values = bind(xi, "XIQueryDevice", C.POINTER(DeviceInfo), C.c_void_p, C.c_int, C.POINTER(C.c_int))(
        display, 0, C.byref(count))
    if not values:
        raise RuntimeError("independent native device query failed")
    try:
        if not 0 < count.value < 128:
            raise RuntimeError("invalid native device count")
        return {value.deviceid: (value.name.decode(), value.use, value.attachment, bool(value.enabled))
                for value in values[:count.value]}
    finally:
        bind(xi, "XIFreeDeviceInfo", None, C.POINTER(DeviceInfo))(values)


class FixtureOracle:
    """Independent post-test observer, authenticated to the owned server only."""

    def __init__(self, environment):
        self.x, self.xi = C.CDLL("libX11.so.6"), C.CDLL("libXi.so.6")
        previous = os.environ.get("XAUTHORITY")
        os.environ["XAUTHORITY"] = environment["XAUTHORITY"]
        try:
            self.display = bind(self.x, "XOpenDisplay", C.c_void_p, C.c_char_p)(
                environment["DISPLAY"].encode("ascii"))
        finally:
            if previous is None:
                os.environ.pop("XAUTHORITY", None)
            else:
                os.environ["XAUTHORITY"] = previous
        if not self.display:
            raise RuntimeError("independent fixture connection unavailable")
        try:
            fd = bind(self.x, "XConnectionNumber", C.c_int, C.c_void_p)(self.display)
            with socket.fromfd(fd, socket.AF_UNIX, socket.SOCK_STREAM) as peer:
                pid, uid, _ = struct.unpack("3i", peer.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            if ((pid, uid) != (int(environment["MINTCLAW_PRIVATE_XVFB_PID"]), os.getuid())
                    or Path(f"/proc/{pid}/exe").resolve()
                    != Path(environment["MINTCLAW_PRIVATE_XVFB_EXECUTABLE"]).resolve()):
                raise RuntimeError("independent observer reached a different server")
            self.original = device_snapshot(self.xi, self.display)
        except BaseException:
            self.close()
            raise

    def verify_retained_quiescence(self, expected):
        current = device_snapshot(self.xi, self.display)
        if not all(current.get(device) == value for device, value in self.original.items()):
            raise RuntimeError("original native devices changed")
        retained = {device: value for device, value in current.items() if device not in self.original}
        keyboards = [device for device, value in retained.items() if value[1] in (2, 4)]
        roles = {value[1]: device for device, value in retained.items()}
        if (len(retained) != expected or any(not value[3] for value in retained.values())
                or (expected and (set(roles) != {1, 2, 3, 4}
                                  or retained[roles[1]][2] != roles[2] or retained[roles[2]][2] != roles[1]
                                  or retained[roles[3]][2] != roles[1] or retained[roles[4]][2] != roles[2]))):
            raise RuntimeError("retained native pair identity or topology mismatch")
        if any(any(key_state(self.xi, self.display, device)) for device in keyboards):
            raise RuntimeError("retained native keyboard is still held")
        return retained

    def close(self):
        if self.display:
            bind(self.x, "XCloseDisplay", C.c_int, C.c_void_p)(self.display)
            self.display = None


class WindowAttributes(C.Structure):
    _fields_ = [(name, C.c_int) for name in ("x", "y", "width", "height", "border_width", "depth")] + [
        ("visual", C.c_void_p), ("root", C.c_ulong)] + [
        (name, C.c_int) for name in ("kind", "bit_gravity", "win_gravity", "backing_store")] + [
        ("backing_planes", C.c_ulong), ("backing_pixel", C.c_ulong), ("save_under", C.c_int),
        ("colormap", C.c_ulong), ("map_installed", C.c_int), ("map_state", C.c_int),
        ("all_event_masks", C.c_long), ("your_event_mask", C.c_long), ("do_not_propagate_mask", C.c_long),
        ("override_redirect", C.c_int), ("screen", C.c_void_p)]


class LegacyDevice(C.Structure):
    _fields_ = [("device_id", C.c_ulong), ("num_classes", C.c_int), ("classes", C.c_void_p)]


class LegacyDeviceState(C.Structure):
    _fields_ = [("device_id", C.c_ulong), ("num_classes", C.c_int), ("data", C.c_void_p)]


class LegacyKeyState(C.Structure):
    _fields_ = [("kind", C.c_ubyte), ("length", C.c_ubyte), ("num_keys", C.c_short),
                ("keys", C.c_ubyte * 32)]


def key_state(xi, display, device_id):
    device = LegacyDevice(device_id, 0, None)
    state = bind(xi, "XQueryDeviceState", C.POINTER(LegacyDeviceState), C.c_void_p, C.POINTER(LegacyDevice))(
        display, C.byref(device))
    if not state:
        raise RuntimeError("independent native key-state query failed")
    try:
        if state.contents.device_id != device_id or not 0 < state.contents.num_classes <= 16 or not state.contents.data:
            raise RuntimeError("invalid native key-state identity or classes")
        offset, result = 0, None
        for _ in range(state.contents.num_classes):
            kind, length = C.cast(state.contents.data + offset, C.POINTER(C.c_ubyte * 2)).contents
            if length < 2 or offset + length > 4096:
                raise RuntimeError("invalid native class length")
            if kind == 0:
                if result is not None or length != C.sizeof(LegacyKeyState):
                    raise RuntimeError("native key-state ABI mismatch")
                keys = C.cast(state.contents.data + offset, C.POINTER(LegacyKeyState)).contents
                if not 0 < keys.num_keys <= 256:
                    raise RuntimeError("invalid native key count")
                result = bytes(keys.keys)
            offset += length
        if result is None:
            raise RuntimeError("native key state missing")
        return result
    finally:
        bind(xi, "XFreeDeviceState", None, C.POINTER(LegacyDeviceState))(state)


def window_viewable(x, display, window, root):
    attributes = WindowAttributes()
    status = bind(x, "XGetWindowAttributes", C.c_int, C.c_void_p, C.c_ulong, C.POINTER(WindowAttributes))(
        display, window, C.byref(attributes))
    return bool(status and attributes.root == root and attributes.kind == 1 and attributes.map_state == 2
                and attributes.width > 0 and attributes.height > 0)
