//! Private native keyboard engine under construction; not an admitted host.
//!
//! The original desktop channel owns its MPX devices independently of each
//! target/input-child action. Ending an action fences its exact input client
//! and proves native key quiescence, without deleting GTK-visible resources.
//! A dormant original death-fenced transport can perform release-only cleanup.
//! The caller retains its action lease until input quiescence is confirmed.
//! Native retention, disposal and an application effect are distinct outcomes.

use std::io::{self, ErrorKind};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::os::unix::fs::MetadataExt;
use std::os::unix::net::UnixStream;
use std::sync::Mutex;
use std::time::{Duration, Instant};
use zeroize::Zeroize;

use x11rb::connection::{Connection, RequestConnection};
use x11rb::protocol::res::ConnectionExt as _;
use x11rb::protocol::xfixes::ConnectionExt as _;
use x11rb::protocol::xinput::ConnectionExt as _;
use x11rb::protocol::xkb::{self, ConnectionExt as _};
use x11rb::protocol::xproto::{self, ConnectionExt as _};
use x11rb::protocol::xtest::ConnectionExt as _;
use x11rb::protocol::{Event, res, xfixes, xinput};
use x11rb::rust_connection::{DefaultStream, RustConnection};
use x11rb::x11_utils::Serialize;

use super::mintclaw_native_x11_io::{IoBoundary, NativeStream};

#[path = "mintclaw_native_x11_history.rs"]
mod history;

type Result<T> = io::Result<T>;
const PREFIX: &[u8] = b"MintClaw private input ";
const SELECTION: &[u8] = b"_MINTCLAW_COMPUTER_INPUT_CONTROL";
const TRANSACTION: Duration = Duration::from_millis(250);
#[cfg(test)]
const MAX_CHORDS: usize = 256;

fn unavailable() -> io::Error {
    io::Error::new(ErrorKind::ConnectionAborted, "native keyboard authority unavailable")
}

#[derive(Debug)]
struct NativeQuarantined;

impl std::fmt::Display for NativeQuarantined {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("native resources quarantined")
    }
}

impl std::error::Error for NativeQuarantined {}

pub(super) fn is_quarantined(error: &io::Error) -> bool {
    error.get_ref().is_some_and(|cause| cause.is::<NativeQuarantined>())
}

fn checked<T, E>(result: std::result::Result<T, E>) -> Result<T> {
    result.map_err(|_| unavailable())
}

fn at<T>(stage: &'static str, result: Result<T>) -> Result<T> {
    result.map_err(|error| io::Error::new(error.kind(), format!("{stage}: {error}")))
}

/// Already-connected literal Unix socket, explicit authentication and screen.
/// No DISPLAY scan, ambient cookie discovery, TCP, or reconnect exists here.
/// The supervisor separately bounds connecting and attests the server peer.
pub(super) struct NativeConnection {
    connection: RustConnection<NativeStream>,
    boundary: IoBoundary,
    root: u32,
    peer: (i32, u32),
}

impl NativeConnection {
    fn from_socket(
        socket: UnixStream,
        screen: usize,
        auth_name: Vec<u8>,
        auth_data: Vec<u8>,
        deadline: Instant,
    ) -> Result<Self> {
        let mut peer: libc::ucred = unsafe { std::mem::zeroed() };
        let mut length = std::mem::size_of_val(&peer) as libc::socklen_t;
        let status = unsafe {
            libc::getsockopt(
                socket.as_raw_fd(),
                libc::SOL_SOCKET,
                libc::SO_PEERCRED,
                (&mut peer as *mut libc::ucred).cast(),
                &mut length,
            )
        };
        if status != 0 || length as usize != std::mem::size_of_val(&peer) || peer.pid <= 0 {
            return Err(unavailable());
        }
        let (stream, _) = DefaultStream::from_unix_stream(socket)?;
        let (stream, boundary) = NativeStream::new(stream);
        let phase = boundary.begin(deadline.min(Instant::now() + TRANSACTION))?;
        let connection =
            checked(RustConnection::connect_to_stream_with_auth_info(stream, screen, auth_name, auth_data))?;
        let root = connection.setup().roots[screen].root;
        phase.finish()?;
        Ok(Self { connection, boundary, root, peer: (peer.pid, peer.uid) })
    }

    fn transaction<T>(&self, deadline: Instant, operation: impl FnOnce() -> Result<T>) -> Result<T> {
        let phase = self.boundary.begin(deadline.min(Instant::now() + TRANSACTION))?;
        checked(checked(self.connection.grab_server())?.check())?;
        let result = operation();
        // Even an ordinary authority refusal must release the server grab.
        // Failure/timeout shuts down this retained socket, never a new client.
        checked(checked(self.connection.ungrab_server())?.check())?;
        phase.finish()?;
        result
    }
}

/// One original control/input connection and its sealed metadata stream.
/// The witness is established before any target, private device or host grant.
pub(super) struct Endpoint {
    native: NativeConnection,
    history: Mutex<history::MutationHistory>,
}

impl std::ops::Deref for Endpoint {
    type Target = NativeConnection;

    fn deref(&self) -> &Self::Target { &self.native }
}

impl Endpoint {
    pub(super) fn from_sockets(
        socket: UnixStream,
        metadata: UnixStream,
        screen: usize,
        auth_name: Vec<u8>,
        auth_data: Vec<u8>,
        deadline: Instant,
    ) -> Result<Self> {
        let native = NativeConnection::from_socket(
            socket, screen, auth_name.clone(), auth_data.clone(), deadline,
        )?;
        let metadata = NativeConnection::from_socket(metadata, screen, auth_name, auth_data, deadline)?;
        if native.server() != metadata.server() {
            return Err(unavailable());
        }
        let history = history::MutationHistory::new(&native, metadata, deadline)?;
        Ok(Self { native, history: Mutex::new(history) })
    }

    /// Called only inside this connection's already-held server transaction.
    /// Read-only state and focus clearing remain possible after history loss.
    fn check_history(&self) -> Result<()> {
        self.history.lock().map_err(|_| unavailable())?.fence(&self.native)
    }
}

struct Process { reference: OwnedFd, pid: u32 }

impl Process {
    fn retain(pid: u32) -> Result<Self> {
        let pid = i32::try_from(pid).map_err(|_| unavailable())?;
        if pid <= 0 {
            return Err(unavailable());
        }
        let fd = unsafe { libc::syscall(libc::SYS_pidfd_open, pid, 0) };
        if fd < 0 {
            return Err(unavailable());
        }
        let process = Self { reference: unsafe { OwnedFd::from_raw_fd(fd as i32) }, pid: pid as u32 };
        let metadata = std::fs::metadata(format!("/proc/{pid}"))?;
        if metadata.uid() != unsafe { libc::getuid() } || !process.alive() {
            return Err(unavailable());
        }
        Ok(process)
    }

    fn alive(&self) -> bool {
        let mut fd = libc::pollfd { fd: self.reference.as_raw_fd(), events: libc::POLLIN, revents: 0 };
        unsafe { libc::poll(&mut fd, 1, 0) == 0 }
    }
}

/// Observe the original server's client table, not an observer-window event.
/// Caller first stops the original writer (or proves the controller pidfd dead).
/// Never KillClient by an ID that may already belong to a replacement client.
/// No server grab is held while waiting: it would prevent EOF/reaping progress.
fn wait_client_gone(endpoint: &Endpoint, token: u32, expected_pid: u32, deadline: Instant) -> Result<()> {
    let base = token & !endpoint.connection.setup().resource_id_mask;
    if base == 0 || base == endpoint.connection.setup().resource_id_base || expected_pid == 0 {
        return Err(unavailable());
    }
    while Instant::now() < deadline {
        let phase = endpoint.boundary.begin(deadline.min(Instant::now() + TRANSACTION))?;
        let reply = checked(checked(endpoint.connection.res_query_client_ids(&[res::ClientIdSpec {
            client: base, mask: res::ClientIdMask::CLIENT_XID | res::ClientIdMask::LOCAL_CLIENT_PID,
        }]))?.reply())?;
        let gone = if reply.ids.is_empty() {
            true
        } else {
            // PID-only absence is insufficient: a live client without PID
            // metadata must remain unknown. CLIENT_XID explicitly attests life.
            let clients: Vec<_> = reply.ids.iter().filter(|id| id.spec.mask == res::ClientIdMask::CLIENT_XID).collect();
            let pids: Vec<_> = reply.ids.iter().filter(|id| id.spec.mask == res::ClientIdMask::LOCAL_CLIENT_PID).collect();
            if reply.ids.len() != 2 || clients.len() != 1 || pids.len() != 1
                || clients[0].spec.client != base || pids[0].spec.client != base
                || !clients[0].value.is_empty() || pids[0].value.len() != 1
            {
                return Err(unavailable());
            }
            // A different kernel PID proves the old client incarnation ended.
            // Do not mutate or adopt the replacement client.
            pids[0].value[0] != expected_pid
        };
        phase.finish()?;
        if gone { return Ok(()) }
        let remaining = deadline.saturating_duration_since(Instant::now()).min(Duration::from_millis(5));
        if !remaining.is_zero() {
            unsafe { libc::poll(std::ptr::null_mut(), 0, remaining.as_millis().max(1) as i32); }
        }
    }
    Err(unavailable())
}

/// Native witnesses, not a model-chosen title, geometry, PID label or alias.
/// The selected host's existing binding/admission remains a separate gate.
#[derive(Clone, Copy, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct Binding {
    pub(super) window: u32,
    pub(super) pid: u32,
    pub(super) lease_window: u32,
    pub(super) lease_pid: u32,
}

struct Authority {
    binding: Binding,
    process: Process,
    lease_process: Process,
    selection: u32,
}

impl Authority {
    fn subscribe(endpoint: &Endpoint, binding: Binding, observer: u32) -> Result<Self> {
        let connection = &endpoint.connection;
        if binding.window <= 1
            || binding.lease_window <= 1
            || binding.window == binding.lease_window
            || connection
                .setup()
                .roots
                .iter()
                .any(|screen| [binding.window, binding.lease_window].contains(&screen.root))
        {
            return Err(unavailable());
        }
        let process = Process::retain(binding.pid)?;
        let lease_process = Process::retain(binding.lease_pid)?;
        let selection = checked(checked(connection.intern_atom(true, SELECTION))?.reply())?.atom;
        if selection == 0 {
            return Err(unavailable());
        }
        for window in [binding.window, binding.lease_window] {
            checked(
                checked(connection.change_window_attributes(
                    window,
                    &xproto::ChangeWindowAttributesAux::new().event_mask(xproto::EventMask::STRUCTURE_NOTIFY),
                ))?
                .check(),
            )?;
        }
        checked(checked(connection.xfixes_query_version(2, 0))?.reply())?;
        checked(
            checked(connection.xfixes_select_selection_input(
                observer,
                selection,
                xfixes::SelectionEventMask::SET_SELECTION_OWNER
                    | xfixes::SelectionEventMask::SELECTION_WINDOW_DESTROY
                    | xfixes::SelectionEventMask::SELECTION_CLIENT_CLOSE,
            ))?
            .check(),
        )?;
        let authority = Self { binding, process, lease_process, selection };
        authority.check(endpoint)?;
        Ok(authority)
    }

    fn check(&self, endpoint: &Endpoint) -> Result<()> {
        let connection = &endpoint.connection;
        if !self.process.alive() || !self.lease_process.alive() {
            return Err(unavailable());
        }
        let version = checked(checked(connection.res_query_version(1, 2))?.reply())?;
        if (version.server_major, version.server_minor) < (1, 2) {
            return Err(unavailable());
        }
        // Query our own actual client PID as a local-namespace proof, then the
        // exact native resources. Never consult the forgeable _NET_WM_PID.
        for (window, pid) in [
            (connection.setup().resource_id_base, std::process::id()),
            (self.binding.window, self.binding.pid),
            (self.binding.lease_window, self.binding.lease_pid),
        ] {
            let values = checked(
                checked(connection.res_query_client_ids(&[res::ClientIdSpec {
                    client: window,
                    mask: res::ClientIdMask::LOCAL_CLIENT_PID,
                }]))?
                .reply(),
            )?;
            if values.ids.len() != 1
                || values.ids[0].spec.mask != res::ClientIdMask::LOCAL_CLIENT_PID
                || values.ids[0].value != [pid]
            {
                return Err(io::Error::new(ErrorKind::ConnectionAborted, "native XRes owner witness refused"));
            }
        }
        let geometry = checked(checked(connection.get_geometry(self.binding.window))?.reply())?;
        let attributes = checked(checked(connection.get_window_attributes(self.binding.window))?.reply())?;
        let lease_attributes = checked(checked(connection.get_window_attributes(self.binding.lease_window))?.reply())?;
        let owner = checked(checked(connection.get_selection_owner(self.selection))?.reply())?.owner;
        if geometry.root != endpoint.root
            || attributes.map_state != xproto::MapState::VIEWABLE
            || attributes.class != xproto::WindowClass::INPUT_OUTPUT
            || lease_attributes.class != xproto::WindowClass::INPUT_ONLY
            || lease_attributes.map_state != xproto::MapState::UNMAPPED
            || owner != self.binding.lease_window
        {
            return Err(unavailable());
        }
        Ok(())
    }
}

#[derive(Clone)]
struct Pair {
    name: Vec<u8>,
    devices: Vec<xinput::XIDeviceInfo>,
    keyboard: u8,
    slave: u8,
    pointer: u16,
}

impl Pair {
    /// Only the master delivers application events. An attached XTEST slave
    /// otherwise retains PointerRoot focus and can deliver a duplicate stream
    /// outside the target. Keep that original injection source unfocused.
    fn set_focus(&self, endpoint: &Endpoint, window: u32) -> Result<()> {
        for (device, desired) in [(self.keyboard, window), (self.slave, x11rb::NONE)] {
            checked(checked(endpoint.connection.xinput_set_device_focus(
                desired, x11rb::CURRENT_TIME, xproto::InputFocus::NONE, device,
            ))?.check())?;
        }
        self.check_focus(endpoint, window)
    }

    fn check_focus(&self, endpoint: &Endpoint, window: u32) -> Result<()> {
        for (device, desired) in [(self.keyboard, window), (self.slave, x11rb::NONE)] {
            let focus = checked(checked(endpoint.connection.xinput_get_device_focus(device))?.reply())?;
            if focus.focus != desired || focus.revert_to != xproto::InputFocus::NONE {
                return Err(unavailable());
            }
        }
        Ok(())
    }

    fn create(endpoint: &Endpoint) -> Result<Self> {
        let connection = &endpoint.connection;
        checked(checked(connection.xinput_xi_query_version(2, 0))?.reply())?;
        checked(checked(connection.xtest_get_version(2, 2))?.reply())?;
        let before = checked(checked(connection.xinput_xi_query_device(0u16))?.reply())?.infos;
        // Refuse orphaned resources. A name is only a busy fence, NEVER
        // authority to delete a device from an old/unknown invocation.
        if before.iter().any(|device| device.name.starts_with(PREFIX)) {
            return Err(io::Error::new(ErrorKind::WouldBlock, NativeQuarantined));
        }
        let mut nonce = [0u8; 16];
        getrandom::fill(&mut nonce).map_err(|_| unavailable())?;
        let mut name = PREFIX.to_vec();
        for byte in nonce {
            name.extend_from_slice(format!("{byte:02x}").as_bytes());
        }
        let change = xinput::HierarchyChange {
            len: (2 + name.len().div_ceil(4)) as u16,
            data: xinput::HierarchyChangeData::AddMaster(xinput::HierarchyChangeDataAddMaster {
                send_core: true,
                enable: true,
                name: name.clone(),
            }),
        };
        checked(checked(connection.xinput_xi_change_hierarchy(&[change]))?.check())?;
        let after = checked(checked(connection.xinput_xi_query_device(0u16))?.reply())?.infos;
        let devices: Vec<_> = after.into_iter().filter(|device| device.name.starts_with(&name)).collect();
        // If creation is uncertain, leave the orphan busy fence in place. Do
        // not invent a cleanup identity from a partial query or retry AddMaster.
        let exact = |suffix: &[u8], kind| -> Result<&xinput::XIDeviceInfo> {
            let full = [name.as_slice(), suffix].concat();
            let mut matches = devices.iter().filter(|device| device.name == full && device.type_ == kind);
            let device = matches.next().ok_or_else(unavailable)?;
            if matches.next().is_some() || !device.enabled || before.iter().any(|old| old.deviceid == device.deviceid) {
                return Err(unavailable());
            }
            Ok(device)
        };
        let keyboard = exact(b" keyboard", xinput::DeviceType::MASTER_KEYBOARD)?;
        let pointer = exact(b" pointer", xinput::DeviceType::MASTER_POINTER)?;
        let slave = exact(b" XTEST keyboard", xinput::DeviceType::SLAVE_KEYBOARD)?;
        let slave_pointer = exact(b" XTEST pointer", xinput::DeviceType::SLAVE_POINTER)?;
        if devices.len() != 4
            || keyboard.attachment != pointer.deviceid
            || pointer.attachment != keyboard.deviceid
            || slave.attachment != keyboard.deviceid
            || slave_pointer.attachment != pointer.deviceid
            || !(2..128).contains(&keyboard.deviceid)
            || !(2..128).contains(&slave.deviceid)
        {
            return Err(unavailable());
        }
        Ok(Self {
            name: name.clone(),
            keyboard: keyboard.deviceid as u8,
            slave: slave.deviceid as u8,
            pointer: pointer.deviceid,
            devices,
        })
    }

    fn check(&self, endpoint: &Endpoint) -> Result<()> {
        let current = checked(checked(endpoint.connection.xinput_xi_query_device(0u16))?.reply())?.infos;
        for original in &self.devices {
            let matches: Vec<_> = current.iter().filter(|device| device.deviceid == original.deviceid).collect();
            if matches.len() != 1
                || matches[0].name != original.name
                || matches[0].type_ != original.type_
                || matches[0].attachment != original.attachment
                || !matches[0].enabled
            {
                return Err(unavailable());
            }
        }
        if current.iter().any(|device| {
            !self.devices.iter().any(|own| own.deviceid == device.deviceid)
                && (device.name.starts_with(&self.name)
                    || device.attachment == u16::from(self.keyboard)
                    || device.attachment == self.pointer)
        }) {
            return Err(unavailable());
        }
        Ok(())
    }
}

/// Repeat is a device control, not a per-edge host permission. This guard
/// changes only the newly owned master and its sole original XTEST slave.
/// It does not establish grab routing or a complete window-isolation profile.
struct NativeRepeatGuard;

impl NativeRepeatGuard {
    fn configure(endpoint: &Endpoint, pair: &Pair) -> Result<()> {
        let connection = &endpoint.connection;
        if !checked(checked(connection.xkb_use_extension(1, 0))?.reply())?.supported {
            return Err(unavailable());
        }
        for device in [pair.keyboard, pair.slave] {
            checked(checked(connection.xkb_set_controls(
                u16::from(device),
                0u8.into(), 0u8.into(), 0u8.into(), 0u8.into(),
                0u16.into(), 0u16.into(), 0u16.into(), 0u16.into(), 0, 0, 0u16.into(),
                xkb::BoolCtrl::REPEAT_KEYS, 0u32.into(), xkb::Control::CONTROLS_ENABLED,
                0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
                0u32.into(), 0u32.into(), 0u16.into(), 0u16.into(), &[0; 32],
            ))?.check())?;
        }
        Self::check(endpoint, pair)
    }

    fn subscribe(endpoint: &Endpoint, pair: &Pair) -> Result<()> {
        let connection = &endpoint.connection;
        if !checked(checked(connection.xkb_use_extension(1, 0))?.reply())?.supported {
            return Err(unavailable());
        }
        for device in [pair.keyboard, pair.slave] {
            checked(checked(connection.xkb_select_events(
                u16::from(device), 0u16.into(), xkb::EventType::CONTROLS_NOTIFY,
                0u16.into(), 0u16.into(), &xkb::SelectEventsAux::default(),
            ))?.check())?;
        }
        Self::check(endpoint, pair)
    }

    fn check(endpoint: &Endpoint, pair: &Pair) -> Result<()> {
        for device in [pair.keyboard, pair.slave] {
            let controls = checked(checked(endpoint.connection.xkb_get_controls(u16::from(device)))?.reply())?;
            let repeat = u32::from(xkb::BoolCtrl::REPEAT_KEYS);
            if controls.device_id != device || u32::from(controls.enabled_controls) & repeat != 0
                || u32::from(controls.access_x_timeout_mask) & u32::from(controls.access_x_timeout_values) & repeat != 0
            {
                return Err(unavailable());
            }
        }
        Ok(())
    }
}

/// Bounded semantic profile for original private-device maps and releases.
/// Matching snapshots alone do not establish continuous native map history;
/// that remains an independent qualification gate before this backend is ready.
struct SafeKeymap {
    safe: [bool; 256],
    source_map: Vec<u8>,
}

impl SafeKeymap {
    fn map(endpoint: &Endpoint, device: u8) -> Result<xkb::GetMapReply> {
        checked(checked(endpoint.connection.xkb_get_map(
            u16::from(device), 255u16.into(), 0u16.into(),
            0, 0, 0, 0, 0, 0, 0, 0, 0u16.into(), 0, 0, 0, 0, 0, 0,
        ))?.reply())
    }

    fn map_bytes(map: &xkb::GetMapReply) -> Vec<u8> {
        let mut bytes = map.serialize();
        // Device ID and reply sequence identify the query, not keyboard data.
        bytes[1..4].fill(0);
        bytes
    }

    fn maps_match(&self, endpoint: &Endpoint, pair: &Pair) -> Result<bool> {
        for device in [pair.keyboard, pair.slave] {
            Self::controls(endpoint, device)?;
            let map = Self::map(endpoint, device)?;
            if map.device_id != device || Self::map_bytes(&map) != self.source_map {
                return Ok(false);
            }
        }
        Ok(true)
    }

    fn controls(endpoint: &Endpoint, device: u8) -> Result<()> {
        let controls = checked(checked(endpoint.connection.xkb_get_controls(u16::from(device)))?.reply())?;
        // Native accessibility remapping, timers and overlay behavior are not
        // covered by ordinary balanced key edges. Refuse rather than changing
        // an existing user's configuration or interpreting shortcut names.
        let unsafe_controls = u32::from(xkb::BoolCtrl::REPEAT_KEYS | xkb::BoolCtrl::SLOW_KEYS
            | xkb::BoolCtrl::BOUNCE_KEYS | xkb::BoolCtrl::STICKY_KEYS | xkb::BoolCtrl::MOUSE_KEYS
            | xkb::BoolCtrl::ACCESS_X_KEYS | xkb::BoolCtrl::OVERLAY1_MASK | xkb::BoolCtrl::OVERLAY2_MASK);
        if controls.device_id != device || u32::from(controls.enabled_controls) & unsafe_controls != 0
            || u32::from(controls.access_x_timeout_mask) & u32::from(controls.access_x_timeout_values)
                & unsafe_controls != 0
        {
            return Err(unavailable());
        }
        Ok(())
    }

    fn admit(endpoint: &Endpoint, pair: &Pair) -> Result<Self> {
        endpoint.check_history()?;
        let connection = &endpoint.connection;
        if !checked(checked(connection.xkb_use_extension(1, 0))?.reply())?.supported {
            return Err(unavailable());
        }
        let mut safe = [true; 256];
        let mut source_map = None;
        for device in [pair.keyboard, pair.slave] {
            Self::controls(endpoint, device)?;
            let map = Self::map(endpoint, device)?;
            let bytes = Self::map_bytes(&map);
            if let Some(original) = &source_map {
                if *original != bytes {
                    return Err(unavailable());
                }
            } else {
                source_map = Some(bytes);
            }
            if map.device_id != device || map.min_key_code != connection.setup().min_keycode
                || map.max_key_code != connection.setup().max_keycode
                || map.first_key_action != map.min_key_code
                || u16::from(map.n_key_actions) != u16::from(map.max_key_code) - u16::from(map.min_key_code) + 1
            {
                return Err(unavailable());
            }
            let actions = map.map.key_actions.ok_or_else(unavailable)?;
            if actions.acts_rtrn_count.len() != usize::from(map.n_key_actions)
                || actions.acts_rtrn_acts.len() != usize::from(map.total_actions)
            {
                return Err(unavailable());
            }
            let mut offset = 0usize;
            for (index, count) in actions.acts_rtrn_count.iter().enumerate() {
                let end = offset.checked_add(usize::from(*count)).ok_or_else(unavailable)?;
                let values = actions.acts_rtrn_acts.get(offset..end).ok_or_else(unavailable)?;
                // NoAction and non-locking per-keyboard modifier/group actions.
                // RedirectKey, pointer/device actions, controls, messages and
                // server/VT actions are structurally outside this profile.
                safe[usize::from(map.first_key_action) + index] &= values.iter().all(|action| {
                    matches!(action.as_type(), xkb::SAType::NO_ACTION | xkb::SAType::SET_MODS | xkb::SAType::SET_GROUP)
                });
                offset = end;
            }
            if offset != actions.acts_rtrn_acts.len() {
                return Err(unavailable());
            }
            for behavior in map.map.behaviors_rtrn.ok_or_else(unavailable)? {
                if behavior.behavior.as_type() != 0 {
                    safe[usize::from(behavior.keycode)] = false;
                }
            }
        }
        Ok(Self { safe, source_map: source_map.ok_or_else(unavailable)? })
    }

    fn check(&self, endpoint: &Endpoint, pair: &Pair) -> Result<()> {
        endpoint.check_history()?;
        if !self.maps_match(endpoint, pair)? {
            return Err(unavailable());
        }
        Ok(())
    }
}

/// Native resource history outlives any individual action. A fresh target or
/// unchanged numeric device IDs cannot restore either lost epoch.
struct ResourceEpoch {
    pair_good: bool,
    input_safe: bool,
}

impl ResourceEpoch {
    fn new() -> Self {
        Self { pair_good: true, input_safe: true }
    }

    fn drain(
        &mut self,
        endpoint: &Endpoint,
        pair: &Pair,
        mut scope: Option<(&Authority, u32, &mut ActionWatch)>,
        allow_creation: bool,
    ) -> Result<()> {
        let stop_at = Instant::now() + Duration::from_millis(100);
        for _ in 0..4096 {
            if Instant::now() >= stop_at {
                return Err(unavailable());
            }
            let Some(event) = checked(endpoint.connection.poll_for_event())? else {
                return Ok(());
            };
            // response_type() masks SendEvent's flag. A forged DestroyNotify
            // must never attest that an original client has been fenced.
            if event.raw_response_type() & 0x80 != 0 {
                if let Some((_, _, action)) = &mut scope {
                    action.target_good = false;
                }
                self.pair_good = false;
                self.input_safe = false;
                continue;
            }
            match event {
                Event::DestroyNotify(event) => {
                    if let Some((authority, input_token, action)) = &mut scope {
                        if [authority.binding.window, authority.binding.lease_window].contains(&event.window) {
                            action.target_good = false;
                        }
                        if event.window == *input_token {
                            action.input_gone = true;
                        }
                        if let Some((token, gone)) = &mut action.controller {
                            if event.window == *token {
                                *gone = true;
                            }
                        }
                    }
                }
                Event::UnmapNotify(event) => {
                    if let Some((authority, _, action)) = &mut scope {
                        if event.window == authority.binding.window {
                            action.target_good = false;
                        }
                    }
                }
                Event::XfixesSelectionNotify(event) => {
                    if let Some((authority, _, action)) = &mut scope {
                        if event.selection == authority.selection {
                            action.target_good = false;
                        }
                    }
                }
                Event::XinputHierarchy(event) => {
                    for info in &event.infos {
                        let own = pair.devices.iter().any(|device| device.deviceid == info.deviceid);
                        let foreign_member = !own
                            && [u16::from(pair.keyboard), pair.pointer].contains(&info.attachment);
                        let allowed = xinput::HierarchyMask::MASTER_ADDED
                            | xinput::HierarchyMask::SLAVE_ADDED
                            | xinput::HierarchyMask::DEVICE_ENABLED
                            | xinput::HierarchyMask::SLAVE_ATTACHED;
                        if u32::from(info.flags) != 0
                            && (foreign_member
                                || (own && (!allow_creation || u32::from(info.flags) & !u32::from(allowed) != 0)))
                        {
                            self.pair_good = false;
                        }
                    }
                }
                Event::XkbControlsNotify(event) if [pair.keyboard, pair.slave].contains(&event.device_id) => {
                    // Controls changing back cannot restore input permission.
                    // Original pair identity remains available for cleanup.
                    self.input_safe = false;
                }
                _ => (),
            }
        }
        Err(unavailable())
    }
}

struct ActionWatch {
    target_good: bool,
    input_gone: bool,
    controller: Option<(u32, bool)>,
}

fn observer(endpoint: &Endpoint) -> Result<u32> {
    checked(checked(endpoint.connection.xinput_xi_query_version(2, 0))?.reply())?;
    let window = checked(endpoint.connection.generate_id())?;
    checked(
        checked(endpoint.connection.create_window(
            0,
            window,
            endpoint.root,
            0,
            0,
            1,
            1,
            0,
            xproto::WindowClass::INPUT_ONLY,
            0,
            &xproto::CreateWindowAux::new().event_mask(xproto::EventMask::STRUCTURE_NOTIFY),
        ))?
        .check(),
    )?;
    checked(
        checked(endpoint.connection.xinput_xi_select_events(
            endpoint.root,
            &[xinput::EventMask { deviceid: 0, mask: vec![xinput::XIEventMask::HIERARCHY] }],
        ))?
        .check(),
    )?;
    Ok(window)
}

/// No pair or key authority exists during preparation. The host must fence
/// its already-retained original WindowGuard/lease generation after these
/// subscriptions, and before calling grant(). Fresh numeric IDs are not that
/// original-host epoch proof.
pub(super) struct PreparedKeeper {
    control: Endpoint,
    authority: Authority,
    observer: u32,
}

pub(super) struct PreparedInput {
    input: Endpoint,
    authority: Authority,
    input_token: u32,
}

#[derive(Clone, Copy, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct ServerIdentity {
    pid: i32,
    uid: u32,
    root: u32,
}

impl ServerIdentity {
    /// Attest the original server peer before sending authentication bytes.
    pub(super) fn matches_peer(&self, socket: &UnixStream) -> bool {
        let mut peer: libc::ucred = unsafe { std::mem::zeroed() };
        let mut length = std::mem::size_of_val(&peer) as libc::socklen_t;
        let status = unsafe {
            libc::getsockopt(
                socket.as_raw_fd(),
                libc::SOL_SOCKET,
                libc::SO_PEERCRED,
                (&mut peer as *mut libc::ucred).cast(),
                &mut length,
            )
        };
        status == 0 && length as usize == std::mem::size_of_val(&peer) && (peer.pid, peer.uid) == (self.pid, self.uid)
    }
}

#[derive(Clone, Copy, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct InputIdentity {
    server: ServerIdentity,
    pid: u32,
    input_token: u32,
    client_base: u32,
}

#[derive(Clone, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct PairWitness {
    name: Vec<u8>,
    keyboard: u8,
    slave: u8,
    pointer: u16,
    devices: Vec<DeviceWitness>,
}

#[derive(Clone, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct DeviceWitness {
    id: u16,
    kind: u16,
    attachment: u16,
    name: Vec<u8>,
}

impl NativeConnection {
    pub(super) fn server(&self) -> ServerIdentity {
        ServerIdentity { pid: self.peer.0, uid: self.peer.1, root: self.root }
    }
}

impl Pair {
    fn witness(&self) -> PairWitness {
        PairWitness {
            name: self.name.clone(),
            keyboard: self.keyboard,
            slave: self.slave,
            pointer: self.pointer,
            devices: self
                .devices
                .iter()
                .map(|device| DeviceWitness {
                    id: device.deviceid,
                    kind: device.type_.into(),
                    attachment: device.attachment,
                    name: device.name.clone(),
                })
                .collect(),
        }
    }

    fn from_witness(witness: PairWitness) -> Result<Self> {
        if witness.name.len() != PREFIX.len() + 32
            || !witness.name.starts_with(PREFIX)
            || !witness.name[PREFIX.len()..].iter().all(u8::is_ascii_hexdigit)
            || witness.devices.len() != 4
            || !(2..128).contains(&witness.keyboard)
            || !(2..128).contains(&witness.slave)
            || witness.pointer <= 1
        {
            return Err(unavailable());
        }
        let expected = [
            (witness.pointer, 1u16, u16::from(witness.keyboard), b" pointer".as_slice()),
            (u16::from(witness.keyboard), 2, witness.pointer, b" keyboard".as_slice()),
            (u16::from(witness.slave), 4, u16::from(witness.keyboard), b" XTEST keyboard".as_slice()),
        ];
        for (id, kind, attachment, suffix) in expected {
            if witness
                .devices
                .iter()
                .filter(|device| {
                    device.id == id
                        && device.kind == kind
                        && device.attachment == attachment
                        && device.name == [witness.name.as_slice(), suffix].concat()
                })
                .count()
                != 1
            {
                return Err(unavailable());
            }
        }
        let pointer_name = [witness.name.as_slice(), b" XTEST pointer"].concat();
        if witness
            .devices
            .iter()
            .filter(|device| device.kind == 3 && device.attachment == witness.pointer && device.name == pointer_name)
            .count()
            != 1
            || witness
                .devices
                .iter()
                .enumerate()
                .any(|(index, device)| device.id <= 1 || witness.devices[..index].iter().any(|old| old.id == device.id))
        {
            return Err(unavailable());
        }
        Ok(Self {
            name: witness.name,
            keyboard: witness.keyboard,
            slave: witness.slave,
            pointer: witness.pointer,
            devices: witness
                .devices
                .into_iter()
                .map(|device| xinput::XIDeviceInfo {
                    deviceid: device.id,
                    type_: device.kind.into(),
                    attachment: device.attachment,
                    enabled: true,
                    name: device.name,
                    classes: Vec::new(),
                })
                .collect(),
        })
    }
}

impl PreparedKeeper {
    pub(super) fn prepare(control: Endpoint, binding: Binding, deadline: Instant) -> Result<Self> {
        let (observer, authority) = at(
            "native control preparation",
            control.transaction(deadline, || {
                let observer = observer(&control)?;
                Ok((observer, Authority::subscribe(&control, binding, observer)?))
            }),
        )?;
        Ok(Self { control, authority, observer })
    }

    pub(super) fn controller_token(&self) -> u32 {
        self.observer
    }

    /// The trusted host must have checked its original epoch after prepare().
    /// expected_worker_pid comes from the retained spawned child, not its wire.
    /// Uncertain admission errors NEVER attest cleanup or authorize a replay.
    pub(super) fn grant(self, identity: InputIdentity, expected_worker_pid: u32, deadline: Instant) -> Result<Keeper> {
        if self.control.server() != identity.server
            || identity.pid != expected_worker_pid
            || identity.input_token <= 1
            || identity.client_base == 0
            || identity.client_base == self.control.connection.setup().resource_id_base
            || identity.input_token & !self.control.connection.setup().resource_id_mask != identity.client_base
        {
            return Err(unavailable());
        }
        let input_process = Process::retain(expected_worker_pid)?;
        let pair = self.control.transaction(deadline, || {
            self.authority.check(&self.control)?;
            let owner = checked(
                checked(self.control.connection.res_query_client_ids(&[res::ClientIdSpec {
                    client: identity.input_token,
                    mask: res::ClientIdMask::LOCAL_CLIENT_PID,
                }]))?
                .reply(),
            )?;
            if owner.ids.len() != 1
                || owner.ids[0].spec.mask != res::ClientIdMask::LOCAL_CLIENT_PID
                || owner.ids[0].value != [expected_worker_pid]
                || !input_process.alive()
            {
                return Err(unavailable());
            }
            checked(
                checked(self.control.connection.change_window_attributes(
                    identity.input_token,
                    &xproto::ChangeWindowAttributesAux::new().event_mask(xproto::EventMask::STRUCTURE_NOTIFY),
                ))?
                .check(),
            )?;
            Pair::create(&self.control)
        })?;
        // Construct the owner before focus: any later failure uses the same
        // independent cleanup path instead of losing an allocated pair.
        let mut keeper = Keeper {
            channel: NativeChannel {
                control: self.control, pair, epoch: ResourceEpoch::new(),
                observer: self.observer, profile: None, previous_input: None,
            },
            phase: ChannelPhase::Active(NativeAction {
                authority: self.authority,
                input_token: identity.input_token,
                input_process,
                watch: ActionWatch { target_good: true, input_gone: false, controller: None },
            }),
        };
        keeper.channel.control.transaction(deadline, || {
            let ChannelPhase::Active(action) = &mut keeper.phase else { return Err(unavailable()) };
            action.authority.check(&keeper.channel.control)?;
            keeper.channel.pair.check(&keeper.channel.control)?;
            // Construct the owner first: an uncertain/refused device-control
            // write must still use the original retained cleanup identity.
            NativeRepeatGuard::configure(&keeper.channel.control, &keeper.channel.pair)?;
            NativeRepeatGuard::subscribe(&keeper.channel.control, &keeper.channel.pair)?;
            keeper.channel.profile = Some(SafeKeymap::admit(&keeper.channel.control, &keeper.channel.pair)?);
            keeper.channel.epoch.drain(
                &keeper.channel.control, &keeper.channel.pair,
                Some((&action.authority, action.input_token, &mut action.watch)), true,
            )?;
            if !action.watch.target_good
                || !keeper.channel.epoch.pair_good
                || !keeper.channel.epoch.input_safe
            {
                return Err(unavailable());
            }
            keeper.channel.pair.set_focus(&keeper.channel.control, action.authority.binding.window)
        })?;
        Ok(keeper)
    }
}

/// A dormant cleanup connection, retained in the input process before a pair
/// exists. It never types and cannot take over while the original controller
/// is alive. Its native epoch observations are not reconstructed after death.
pub(super) struct BackupCleanup {
    prepared: PreparedKeeper,
    controller: Process,
    input_process: Process,
    input_token: u32,
    pair: Option<Pair>,
    profile: Option<SafeKeymap>,
    epoch: ResourceEpoch,
    watch: ActionWatch,
}

impl BackupCleanup {
    pub(super) fn prepare(
        control: Endpoint,
        binding: Binding,
        controller_pid: u32,
        controller_token: u32,
        input: InputIdentity,
        deadline: Instant,
    ) -> Result<Self> {
        let controller = Process::retain(controller_pid)?;
        let input_process = Process::retain(std::process::id())?;
        let prepared = PreparedKeeper::prepare(control, binding, deadline)?;
        prepared.control.transaction(deadline, || {
            if prepared.control.server() != input.server
                || input.pid != std::process::id()
                || controller_pid == std::process::id()
                || controller_token <= 1
                || controller_token == input.input_token
                || !controller.alive()
            {
                return Err(unavailable());
            }
            let connection = &prepared.control.connection;
            for (token, pid) in [(controller_token, controller_pid), (input.input_token, input.pid)] {
                checked(
                    checked(connection.change_window_attributes(
                        token,
                        &xproto::ChangeWindowAttributesAux::new().event_mask(xproto::EventMask::STRUCTURE_NOTIFY),
                    ))?
                    .check(),
                )?;
                let owner = checked(
                    checked(connection.res_query_client_ids(&[res::ClientIdSpec {
                        client: token,
                        mask: res::ClientIdMask::LOCAL_CLIENT_PID,
                    }]))?
                    .reply(),
                )?;
                let attributes = checked(checked(connection.get_window_attributes(token))?.reply())?;
                let geometry = checked(checked(connection.get_geometry(token))?.reply())?;
                if owner.ids.len() != 1
                    || owner.ids[0].spec.mask != res::ClientIdMask::LOCAL_CLIENT_PID
                    || owner.ids[0].value != [pid]
                    || attributes.class != xproto::WindowClass::INPUT_ONLY
                    || attributes.map_state != xproto::MapState::UNMAPPED
                    || geometry.root != prepared.control.root
                {
                    return Err(unavailable());
                }
            }
            Ok(())
        })?;
        Ok(Self {
            prepared,
            controller,
            input_process,
            input_token: input.input_token,
            pair: None,
            profile: None,
            epoch: ResourceEpoch::new(),
            watch: ActionWatch {
                target_good: true,
                input_gone: false,
                controller: Some((controller_token, false)),
            },
        })
    }

    pub(super) fn bind(&mut self, witness: PairWitness, deadline: Instant) -> Result<()> {
        let pair = Pair::from_witness(witness)?;
        self.prepared.control.transaction(deadline, || {
            self.prepared.authority.check(&self.prepared.control)?;
            pair.check(&self.prepared.control)?;
            NativeRepeatGuard::subscribe(&self.prepared.control, &pair)?;
            self.profile = Some(SafeKeymap::admit(&self.prepared.control, &pair)?);
            self.epoch.drain(
                &self.prepared.control, &pair, Some((&self.prepared.authority, self.input_token, &mut self.watch)), true,
            )?;
            if !self.controller.alive()
                || !self.watch.target_good
                || !self.epoch.pair_good
                || !self.epoch.input_safe
                || self.watch.input_gone
                || !matches!(self.watch.controller, Some((_, false)))
            {
                return Err(unavailable());
            }
            Ok(())
        })?;
        self.pair = Some(pair);
        Ok(())
    }

    /// Closing the input transport alone does not release global MPX keys.
    /// Wait only for the original pidfd, never infer death from a heartbeat or
    /// wire timeout. The live primary remains the sole cleanup owner otherwise.
    pub(super) fn after_input_failure(mut self, deadline: Instant) -> bool {
        while self.controller.alive() && Instant::now() < deadline {
            unsafe {
                libc::poll(std::ptr::null_mut(), 0, 5);
            }
        }
        if self.controller.alive() {
            return false;
        }
        let Some(pair) = self.pair.take() else {
            return false;
        };
        let Some((controller_token, _)) = self.watch.controller else { return false };
        let fenced = wait_client_gone(
            &self.prepared.control, controller_token, self.controller.pid, Instant::now() + TRANSACTION,
        ).and_then(|()| self.prepared.control.transaction(Instant::now() + TRANSACTION, || {
            pair.check(&self.prepared.control)?;
            self.epoch.drain(
                &self.prepared.control, &pair,
                Some((&self.prepared.authority, self.input_token, &mut self.watch)), false,
            )?;
            if !self.epoch.pair_good { return Err(unavailable()) }
            Ok(())
        }));
        // No release owner exists until the original controller's queued
        // requests are positively gone. Unknown stays quarantined.
        if fenced.is_err() {
            return false;
        }
        let mut keeper = Keeper {
            channel: NativeChannel {
                control: self.prepared.control, pair, epoch: self.epoch,
                observer: self.prepared.observer, profile: self.profile, previous_input: None,
            },
            phase: ChannelPhase::Active(NativeAction {
                authority: self.prepared.authority,
                input_token: self.input_token,
                input_process: self.input_process,
                watch: self.watch,
            }),
        };
        keeper.quiesce().is_ok()
    }
}

impl PreparedInput {
    pub(super) fn prepare(input: Endpoint, binding: Binding, deadline: Instant) -> Result<Self> {
        let (input_token, authority) = input.transaction(deadline, || {
            let token = observer(&input)?;
            Ok((token, Authority::subscribe(&input, binding, token)?))
        })?;
        Ok(Self { input, authority, input_token })
    }

    pub(super) fn identity(&self) -> InputIdentity {
        InputIdentity {
            server: self.input.server(),
            pid: std::process::id(),
            input_token: self.input_token,
            client_base: self.input.connection.setup().resource_id_base,
        }
    }

    pub(super) fn bind(self, witness: PairWitness, deadline: Instant) -> Result<InputWorker> {
        let pair = Pair::from_witness(witness)?;
        let mut epoch = ResourceEpoch::new();
        let mut watch = ActionWatch { target_good: true, input_gone: false, controller: None };
        let (event_base, profile) = self.input.transaction(deadline, || {
            self.authority.check(&self.input)?;
            pair.check(&self.input)?;
            NativeRepeatGuard::subscribe(&self.input, &pair)?;
            epoch.drain(&self.input, &pair, Some((&self.authority, self.input_token, &mut watch)), true)?;
            if !watch.target_good || !epoch.pair_good || !epoch.input_safe {
                return Err(unavailable());
            }
            let extension = checked(self.input.connection.extension_information(xinput::X11_EXTENSION_NAME))?
                .ok_or_else(unavailable)?;
            extension.first_event.checked_add(xinput::DEVICE_KEY_RELEASE_EVENT).ok_or_else(unavailable)?;
            pair.check_focus(&self.input, self.authority.binding.window)?;
            Ok((extension.first_event, SafeKeymap::admit(&self.input, &pair)?))
        })?;
        Ok(InputWorker {
            input: self.input,
            input_authority: self.authority,
            pair,
            input_token: self.input_token,
            input_epoch: epoch,
            input_watch: watch,
            profile,
            phase: InputPhase::Fresh,
            attempted: false,
            event_base,
        })
    }
}

/// Original desktop resources carry no action text or target authority.
struct NativeChannel {
    control: Endpoint,
    pair: Pair,
    epoch: ResourceEpoch,
    observer: u32,
    profile: Option<SafeKeymap>,
    previous_input: Option<Process>,
}

struct NativeAction {
    authority: Authority,
    input_token: u32,
    input_process: Process,
    watch: ActionWatch,
}

enum ChannelPhase {
    Active(NativeAction),
    Preparing { authority: Authority, watch: ActionWatch },
    Idle,
    Uncertain,
}

/// Evidence for input quiescence only, not device disposal or an app effect.
/// The original owner may retain devices after every action has ended.
pub(super) struct InputQuiescent(());

pub(super) struct Keeper {
    channel: NativeChannel,
    phase: ChannelPhase,
}

fn key_state(endpoint: &Endpoint, device: u8) -> Result<[u8; 32]> {
    let reply = checked(checked(endpoint.connection.xinput_query_device_state(device))?.reply())?;
    let mut keys = None;
    for class in reply.classes {
        if let xinput::InputStateData::Key(value) = class.data {
            if keys.replace(value.keys).is_some() {
                return Err(unavailable());
            }
        }
    }
    keys.ok_or_else(unavailable)
}

fn input_owner(endpoint: &Endpoint, identity: InputIdentity, expected_pid: u32) -> Result<Process> {
    let connection = &endpoint.connection;
    if endpoint.server() != identity.server || identity.pid != expected_pid
        || identity.input_token <= 1 || identity.client_base == 0
        || identity.client_base == connection.setup().resource_id_base
        || identity.input_token & !connection.setup().resource_id_mask != identity.client_base
    {
        return Err(unavailable());
    }
    let process = Process::retain(expected_pid)?;
    let owner = checked(checked(connection.res_query_client_ids(&[res::ClientIdSpec {
        client: identity.input_token, mask: res::ClientIdMask::LOCAL_CLIENT_PID,
    }]))?.reply())?;
    if owner.ids.len() != 1 || owner.ids[0].spec.mask != res::ClientIdMask::LOCAL_CLIENT_PID
        || owner.ids[0].value != [expected_pid] || !process.alive()
    {
        return Err(unavailable());
    }
    checked(checked(connection.change_window_attributes(
        identity.input_token,
        &xproto::ChangeWindowAttributesAux::new().event_mask(xproto::EventMask::STRUCTURE_NOTIFY),
    ))?.check())?;
    Ok(process)
}

impl NativeChannel {
    fn clear_keys(control: &Endpoint, pair: &Pair) -> Result<()> {
        for device in [pair.keyboard, pair.slave] {
            if key_state(control, device)? != [0; 32] {
                return Err(unavailable());
            }
        }
        Ok(())
    }

    fn release_held(control: &Endpoint, pair: &Pair, profile: Option<&SafeKeymap>) -> Result<()> {
        let slave_keys = key_state(control, pair.slave)?;
        if slave_keys == [0; 32] {
            // A foreign grab must not make a verified zero-key action require
            // synthetic releases or a new grab. Both states still must be zero.
            return Self::clear_keys(control, pair);
        }
        let profile = profile.ok_or_else(unavailable)?;
        profile.check(control, pair)?;
        let connection = &control.connection;
        let sink = observer(control)?;
        checked(checked(connection.map_window(sink))?.check())?;
        let mask = [u32::from(xinput::XIEventMask::KEY_PRESS | xinput::XIEventMask::KEY_RELEASE)];
        let grab = checked(checked(connection.xinput_xi_grab_device(
            sink, x11rb::CURRENT_TIME, x11rb::NONE, u16::from(pair.keyboard),
            xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC, xinput::GrabOwner::NO_OWNER, &mask,
        ))?.reply())?;
        if grab.status != xproto::GrabStatus::SUCCESS {
            checked(checked(connection.destroy_window(sink))?.check())?;
            return Err(unavailable());
        }
        let result = (|| {
            NativeRoutingGuard::check_injection_slot(control, pair, sink)?;
            let extension = checked(connection.extension_information(xinput::X11_EXTENSION_NAME))?
                .ok_or_else(unavailable)?;
            let release = extension.first_event.checked_add(xinput::DEVICE_KEY_RELEASE_EVENT)
                .ok_or_else(unavailable)?;
            for code in 0u16..256 {
                if slave_keys[usize::from(code / 8)] & (1 << (code % 8)) != 0 {
                    if !profile.safe[usize::from(code)] {
                        return Err(unavailable());
                    }
                    checked(checked(connection.xtest_fake_input(
                        release, code as u8, x11rb::CURRENT_TIME, control.root, 0, 0, pair.slave,
                    ))?.check())?;
                }
            }
            checked(checked(connection.get_input_focus())?.reply())?;
            Self::clear_keys(control, pair)
        })();
        let ungrab = checked(connection.xinput_xi_ungrab_device(
            x11rb::CURRENT_TIME, u16::from(pair.keyboard),
        ))?.check();
        let removed = checked(connection.destroy_window(sink))?.check();
        checked(ungrab)?;
        checked(removed)?;
        result
    }
}

impl Keeper {
    pub(super) fn witness(&self) -> PairWitness { self.channel.pair.witness() }
    pub(super) fn keyboard(&self) -> u8 { self.channel.pair.keyboard }
    pub(super) fn controller_token(&self) -> u32 { self.channel.observer }

    pub(super) fn check(&mut self, deadline: Instant) -> Result<()> {
        let ChannelPhase::Active(action) = &mut self.phase else { return Err(unavailable()) };
        self.channel.control.transaction(deadline, || {
            action.authority.check(&self.channel.control)?;
            self.channel.pair.check(&self.channel.control)?;
            self.channel.profile.as_ref().ok_or_else(unavailable)?.check(&self.channel.control, &self.channel.pair)?;
            self.channel.epoch.drain(
                &self.channel.control, &self.channel.pair,
                Some((&action.authority, action.input_token, &mut action.watch)), false,
            )?;
            if !action.watch.target_good || !self.channel.epoch.pair_good
                || !self.channel.epoch.input_safe || action.watch.input_gone
            {
                return Err(unavailable());
            }
            Ok(())
        })
    }

    /// Establish fresh action subscriptions without granting focus or input.
    /// Idle carries no old window/lease authority. Resource history is NEVER reset.
    pub(super) fn prepare_action(&mut self, binding: Binding, deadline: Instant) -> Result<()> {
        if !matches!(self.phase, ChannelPhase::Idle) {
            return Err(unavailable());
        }
        self.phase = ChannelPhase::Uncertain;
        let (authority, watch) = self.channel.control.transaction(deadline, || {
            self.channel.epoch.drain(&self.channel.control, &self.channel.pair, None, false)?;
            self.channel.pair.check(&self.channel.control)?;
            self.channel.profile.as_ref().ok_or_else(unavailable)?.check(&self.channel.control, &self.channel.pair)?;
            if !self.channel.epoch.pair_good || !self.channel.epoch.input_safe
                || self.channel.previous_input.as_ref().is_some_and(Process::alive)
            {
                return Err(unavailable());
            }
            NativeChannel::clear_keys(&self.channel.control, &self.channel.pair)?;
            self.channel.pair.check_focus(&self.channel.control, x11rb::NONE)?;
            let authority = Authority::subscribe(&self.channel.control, binding, self.channel.observer)?;
            Ok((authority, ActionWatch { target_good: true, input_gone: false, controller: None }))
        })?;
        self.phase = ChannelPhase::Preparing { authority, watch };
        Ok(())
    }

    /// Caller fences the newly retained original host epoch AFTER prepare_action.
    pub(super) fn grant_action(&mut self, identity: InputIdentity, expected_pid: u32, deadline: Instant) -> Result<()> {
        let ChannelPhase::Preparing { authority, mut watch } =
            std::mem::replace(&mut self.phase, ChannelPhase::Uncertain)
        else { return Err(unavailable()) };
        let input_process = self.channel.control.transaction(deadline, || {
            authority.check(&self.channel.control)?;
            self.channel.pair.check(&self.channel.control)?;
            self.channel.profile.as_ref().ok_or_else(unavailable)?.check(&self.channel.control, &self.channel.pair)?;
            self.channel.epoch.drain(
                &self.channel.control, &self.channel.pair,
                Some((&authority, identity.input_token, &mut watch)), false,
            )?;
            if !watch.target_good || !self.channel.epoch.pair_good || !self.channel.epoch.input_safe {
                return Err(unavailable());
            }
            NativeChannel::clear_keys(&self.channel.control, &self.channel.pair)?;
            input_owner(&self.channel.control, identity, expected_pid)
        })?;
        // Own the exact new input epoch before a focus write can become uncertain.
        self.phase = ChannelPhase::Active(NativeAction { authority, watch, input_token: identity.input_token, input_process });
        let ChannelPhase::Active(action) = &self.phase else { unreachable!() };
        self.channel.control.transaction(deadline, || {
            self.channel.pair.set_focus(&self.channel.control, action.authority.binding.window)
        })
    }

    pub(super) fn cancel_prepared(&mut self, deadline: Instant) -> Result<InputQuiescent> {
        if !matches!(self.phase, ChannelPhase::Preparing { .. }) {
            return Err(unavailable());
        }
        self.phase = ChannelPhase::Uncertain;
        self.channel.control.transaction(deadline, || {
            self.channel.epoch.drain(&self.channel.control, &self.channel.pair, None, false)?;
            self.channel.pair.check(&self.channel.control)?;
            if !self.channel.epoch.pair_good {
                return Err(unavailable());
            }
            NativeChannel::clear_keys(&self.channel.control, &self.channel.pair)
        })?;
        self.phase = ChannelPhase::Idle;
        Ok(InputQuiescent(()))
    }

    /// A single release-only attempt. Failure permanently quarantines this owner;
    /// neither Drop nor a fresh action can retry uncertain requests or delete devices.
    pub(super) fn quiesce(&mut self) -> Result<InputQuiescent> {
        let phase = std::mem::replace(&mut self.phase, ChannelPhase::Uncertain);
        let mut action = match phase {
            ChannelPhase::Idle => { self.phase = ChannelPhase::Idle; return Ok(InputQuiescent(())); }
            ChannelPhase::Active(action) => action,
            ChannelPhase::Preparing { .. } | ChannelPhase::Uncertain => return Err(unavailable()),
        };
        let deadline = Instant::now() + TRANSACTION;
        let result = wait_client_gone(
            &self.channel.control, action.input_token, action.input_process.pid, deadline,
        ).and_then(|()| self.channel.control.transaction(deadline, || {
            self.channel.pair.check(&self.channel.control)?;
            self.channel.epoch.drain(
                &self.channel.control, &self.channel.pair,
                Some((&action.authority, action.input_token, &mut action.watch)), false,
            )?;
            if !self.channel.epoch.pair_good {
                return Err(unavailable());
            }
            self.channel.pair.set_focus(&self.channel.control, x11rb::NONE)?;
            NativeChannel::release_held(
                &self.channel.control, &self.channel.pair, self.channel.profile.as_ref(),
            )?;
            self.channel.pair.check(&self.channel.control)
        }));
        self.channel.previous_input = Some(action.input_process);
        if result.is_ok() {
            self.phase = ChannelPhase::Idle;
        }
        result.map(|()| InputQuiescent(()))
    }

    /// Only old disposable-server library fixtures remove their original pair.
    /// This method is absent from the actual worker and every non-test build.
    #[cfg(test)]
    fn dispose_fixture_pair(&mut self) -> bool {
        if !matches!(self.phase, ChannelPhase::Idle) {
            return false;
        }
        self.phase = ChannelPhase::Uncertain;
        self.channel.control.transaction(Instant::now() + TRANSACTION, || {
            self.channel.pair.check(&self.channel.control)?;
            let remove = xinput::HierarchyChange {
                len: 3,
                data: xinput::HierarchyChangeData::RemoveMaster(xinput::HierarchyChangeDataRemoveMaster {
                    deviceid: self.channel.pair.pointer, return_mode: xinput::ChangeMode::FLOAT,
                    return_pointer: 0, return_keyboard: 0,
                }),
            };
            checked(checked(self.channel.control.connection.xinput_xi_change_hierarchy(&[remove]))?.check())?;
            let devices = checked(checked(self.channel.control.connection.xinput_xi_query_device(0u16))?.reply())?.infos;
            if devices.iter().any(|device| self.channel.pair.devices.iter().any(|own| own.deviceid == device.deviceid)) {
                return Err(unavailable());
            }
            Ok(())
        }).is_ok()
    }

    #[cfg(test)]
    fn action_watch(&mut self) -> &mut ActionWatch {
        match &mut self.phase {
            ChannelPhase::Active(action) => &mut action.watch,
            _ => panic!("fixture requires an active action"),
        }
    }
}

impl Drop for Keeper {
    fn drop(&mut self) {
        let _ = self.quiesce();
    }
}

struct ActiveInput {
    deadline: Instant,
    next: u32,
    held: Vec<u8>,
}

impl Drop for ActiveInput {
    fn drop(&mut self) {
        self.held.zeroize();
    }
}

enum InputPhase {
    Fresh,
    Active(ActiveInput),
    Finished,
    Failed,
}

/// The worker borrows native pair witnesses, never the pair's removal authority.
/// Each private edge is backpressured by the keeper/host's current original
/// scope fence. Holding between edges is bounded by the sole action's deadline;
/// public callers still supply bounded, balanced chords, not raw hold commands.
pub(super) struct InputWorker {
    input: Endpoint,
    input_authority: Authority,
    pair: Pair,
    input_token: u32,
    input_epoch: ResourceEpoch,
    input_watch: ActionWatch,
    profile: SafeKeymap,
    phase: InputPhase,
    attempted: bool,
    event_base: u8,
}

#[derive(Debug)]
#[cfg(test)]
pub(super) struct Receipt {
    pub(super) dispatched: bool,
    pub(super) acknowledged: bool,
    pub(super) cleanup_confirmed: bool,
}

#[derive(Clone)]
#[cfg(test)]
pub(super) struct StopHandle(IoBoundary);

#[cfg(test)]
impl StopHandle {
    pub(super) fn stop(&self) {
        self.0.stop();
    }
}

/// Routing checks for one retained original private keyboard pair.
/// The caller holds this endpoint's original server transaction. Core passive
/// probes additionally require the active core grab pinned to our master;
/// XI1/XI2 probes name only that master. No event is injected by these checks.
/// Input refusal closes the original input client before keeper cleanup.
struct NativeRoutingGuard;

impl NativeRoutingGuard {
    fn check_input(
        input: &Endpoint, pair: &Pair, binding: Binding, code: u8, press: bool,
    ) -> Result<()> {
        let connection = &input.connection;
        let target = binding.window;
        let pid = binding.pid;
        checked(checked(connection.xinput_xi_set_client_pointer(x11rb::NONE, pair.pointer))?.check())?;
        let pointer = checked(checked(connection.xinput_xi_get_client_pointer(x11rb::NONE))?.reply())?;
        if !pointer.set || pointer.deviceid != pair.pointer { return Err(unavailable()) }
        let master = checked(checked(connection.grab_keyboard(
            false, target, x11rb::CURRENT_TIME,
            xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC,
        ))?.reply())?;
        if master.status != xproto::GrabStatus::SUCCESS { return Err(unavailable()) }
        // XI1 does not detach an attached slave. A checked grab/ungrab can test
        // its active-grab slot without taking input from any other device.
        Self::check_injection_slot(input, pair, target)?;
        pair.check_focus(input, target)?;
        // Both press and release can normally deliver through a pointer descendant.
        let windows = Self::windows(input, pair, target, pid)?;
        if press {
            let modifiers = Self::grab_modifiers(input, pair.keyboard)?;
            for window in windows {
                Self::passives(input, pair, window, code, modifiers)?;
            }
        }
        // On any earlier uncertainty the existing worker failure closes this
        // original client; do not retry an ungrab through a replacement client.
        checked(checked(connection.ungrab_keyboard(x11rb::CURRENT_TIME))?.check())
    }

    fn grab_modifiers(endpoint: &Endpoint, device: u8) -> Result<xproto::ModMask> {
        let connection = &endpoint.connection;
        let state = checked(checked(connection.xkb_get_state(u16::from(device)))?.reply())?;
        let controls = checked(checked(connection.xkb_get_controls(u16::from(device)))?.reply())?;
        let base = u16::from(state.base_mods);
        let latched = u16::from(state.latched_mods);
        let mods = base | latched | u16::from(state.locked_mods);
        if state.device_id != device || controls.device_id != device || u16::from(state.mods) != mods {
            return Err(unavailable());
        }
        // Xorg GetState omits the derived grab-modifier fields. Compute the
        // same mask as XkbComputeDerivedState from populated native fields;
        // do not infer a modifier state from a zero-filled reply member.
        let internal = u16::from(controls.internal_mods_mask);
        let ignored = u16::from(controls.ignore_lock_mods_mask);
        Ok(xproto::ModMask::from((mods & !internal & !ignored) | ((base | latched) & ignored)))
    }

    fn check_injection_slot(endpoint: &Endpoint, pair: &Pair, sink: u32) -> Result<()> {
        let connection = &endpoint.connection;
        let grab = checked(checked(connection.xinput_grab_device(
            sink, x11rb::CURRENT_TIME, xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC,
            false, pair.slave, &[],
        ))?.reply())?;
        if grab.status != xproto::GrabStatus::SUCCESS { return Err(unavailable()) }
        checked(checked(connection.xinput_ungrab_device(x11rb::CURRENT_TIME, pair.slave))?.check())?;
        pair.check(endpoint)
    }

    fn passives(
        endpoint: &Endpoint, pair: &Pair, window: u32, code: u8, modifiers: xproto::ModMask,
    ) -> Result<()> {
        let connection = &endpoint.connection;
        let core = modifiers;
        checked(checked(connection.grab_key(
            false, window, core, code, xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC,
        ))?.check())?;
        checked(checked(connection.ungrab_key(code, window, core))?.check())?;
        let xi2_modifiers = [u32::from(u16::from(modifiers))];
        let xi2 = checked(checked(connection.xinput_xi_passive_grab_device(
            x11rb::CURRENT_TIME, window, x11rb::NONE, u32::from(code), u16::from(pair.keyboard),
            xinput::GrabType::KEYCODE, xinput::GrabMode22::ASYNC, xproto::GrabMode::ASYNC,
            xinput::GrabOwner::NO_OWNER, &[], &xi2_modifiers,
        ))?.reply())?;
        if !xi2.modifiers.is_empty() { return Err(unavailable()) }
        checked(checked(connection.xinput_xi_passive_ungrab_device(
            window, u32::from(code), u16::from(pair.keyboard), xinput::GrabType::KEYCODE, &xi2_modifiers,
        ))?.check())?;
        // XI1 can choose an unrelated modifier keyboard whose physical modifier
        // state may change even under GrabServer. Conservatively test any modifier
        // for each actual addressable keyboard, still grabbing ONLY our master.
        let devices = checked(checked(connection.xinput_xi_query_device(0u16))?.reply())?.infos;
        for device in devices {
            if !device.classes.iter().any(|class| matches!(&class.data, xinput::DeviceClassData::Key(_))) {
                continue;
            }
            let Ok(modifier) = u8::try_from(device.deviceid) else { continue };
            checked(checked(connection.xinput_grab_device_key(
                window, xproto::ModMask::ANY, modifier, pair.keyboard, code,
                xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC, false, &[],
            ))?.check())?;
            checked(checked(connection.xinput_ungrab_device_key(
                window, xproto::ModMask::ANY, modifier, code, pair.keyboard,
            ))?.check())?;
        }
        Ok(())
    }

    fn windows(endpoint: &Endpoint, pair: &Pair, target: u32, pid: u32) -> Result<Vec<u32>> {
        const MAX_ROUTE_WINDOWS: usize = 256;
        let connection = &endpoint.connection;
        let mut windows = Vec::new();
        let mut current = target;
        loop {
            if current <= 1 || windows.len() >= MAX_ROUTE_WINDOWS || windows.contains(&current) {
                return Err(unavailable());
            }
            windows.push(current);
            if current == endpoint.root { break }
            let tree = checked(checked(connection.query_tree(current))?.reply())?;
            if tree.root != endpoint.root { return Err(unavailable()) }
            current = tree.parent;
        }
        // Xorg searches the private pointer's descendant trace only when the
        // explicit focus is on that trace. QueryPointer supplies that native path;
        // a pointer outside the focus produces no child of the target.
        current = target;
        loop {
            let pointer = checked(checked(connection.xinput_xi_query_pointer(current, pair.pointer))?.reply())?;
            if !pointer.same_screen || pointer.root != endpoint.root { return Err(unavailable()) }
            let child = pointer.child;
            if child == x11rb::NONE { break }
            if child <= 1 || windows.len() >= MAX_ROUTE_WINDOWS || windows.contains(&child) {
                return Err(unavailable());
            }
            let tree = checked(checked(connection.query_tree(child))?.reply())?;
            let owner = checked(checked(connection.res_query_client_ids(&[res::ClientIdSpec {
                client: child, mask: res::ClientIdMask::LOCAL_CLIENT_PID,
            }]))?.reply())?;
            if tree.root != endpoint.root || tree.parent != current
                || owner.ids.len() != 1 || owner.ids[0].spec.mask != res::ClientIdMask::LOCAL_CLIENT_PID
                || owner.ids[0].value != [pid]
            {
                return Err(unavailable());
            }
            windows.push(child);
            current = child;
        }
        Ok(windows)
    }
}

impl InputWorker {
    #[cfg(test)]
    pub(super) fn stop_handle(&self) -> StopHandle {
        StopHandle(self.input.boundary.clone())
    }

    pub(super) fn begin(&mut self, deadline: Instant) -> Result<()> {
        if !matches!(self.phase, InputPhase::Fresh) || self.input.boundary.stopped() || deadline <= Instant::now() {
            return Err(unavailable());
        }
        self.phase = InputPhase::Active(ActiveInput {
            deadline: deadline.min(Instant::now() + Duration::from_secs(10)),
            next: 0,
            held: Vec::new(),
        });
        Ok(())
    }

    pub(super) fn edge(&mut self, ordinal: u32, code: u8, press: bool) -> Result<()> {
        let valid = match &self.phase {
            InputPhase::Active(action) => {
                ordinal == action.next
                    && ordinal < 4096
                    && Instant::now() < action.deadline
                    && code >= self.input.connection.setup().min_keycode
                    && code <= self.input.connection.setup().max_keycode
                    && if press {
                        action.held.len() < 8 && !action.held.contains(&code)
                    } else {
                        action.held.last() == Some(&code)
                    }
            }
            _ => false,
        };
        if !valid {
            self.phase = InputPhase::Failed;
            self.input.boundary.stop();
            return Err(unavailable());
        }
        let InputPhase::Active(action) = &mut self.phase else { unreachable!() };
        let deadline = action.deadline;
        // Advance before an uncertain native write. No ordinal is retryable.
        action.next += 1;
        if press {
            action.held.push(code);
        } else {
            action.held.pop();
        }
        let result = self.input.transaction(deadline, || {
            self.input_authority.check(&self.input)?;
            self.pair.check(&self.input)?;
            self.profile.check(&self.input, &self.pair)?;
            if !self.profile.safe[usize::from(code)] {
                return Err(unavailable());
            }
            self.pair.check_focus(&self.input, self.input_authority.binding.window)?;
            self.input_epoch.drain(
                &self.input, &self.pair, Some((&self.input_authority, self.input_token, &mut self.input_watch)), false,
            )?;
            if !self.input_watch.target_good
                || self.input_watch.input_gone
                || !self.input_epoch.pair_good
                || !self.input_epoch.input_safe
            {
                return Err(unavailable());
            }
            NativeRoutingGuard::check_input(
                &self.input, &self.pair, self.input_authority.binding, code, press,
            )?;
            self.attempted = true;
            let kind =
                self.event_base + if press { xinput::DEVICE_KEY_PRESS_EVENT } else { xinput::DEVICE_KEY_RELEASE_EVENT };
            checked(
                checked(self.input.connection.xtest_fake_input(
                    kind,
                    code,
                    x11rb::CURRENT_TIME,
                    self.input.root,
                    0,
                    0,
                    self.pair.slave,
                ))?
                .check(),
            )
        });
        if result.is_err() {
            self.phase = InputPhase::Failed;
            self.input.boundary.stop();
        }
        result
    }

    pub(super) fn end(&mut self) -> Result<()> {
        if !matches!(&self.phase, InputPhase::Active(action)
            if action.held.is_empty() && Instant::now() < action.deadline)
        {
            self.phase = InputPhase::Failed;
            self.input.boundary.stop();
            return Err(unavailable());
        }
        self.phase = InputPhase::Finished;
        Ok(())
    }

    #[cfg(test)]
    fn dispatch(&mut self, chords: &[Vec<u8>], deadline: Instant) -> Receipt {
        let mut receipt = Receipt { dispatched: false, acknowledged: false, cleanup_confirmed: false };
        if self.begin(deadline).is_err() {
            return receipt;
        }
        let setup = self.input.connection.setup();
        let valid = !chords.is_empty()
            && chords.len() <= MAX_CHORDS
            && chords.iter().all(|keys| {
                !keys.is_empty()
                    && keys.len() <= 8
                    && keys.iter().enumerate().all(|(index, key)| {
                        *key >= setup.min_keycode && *key <= setup.max_keycode && !keys[..index].contains(key)
                    })
            });
        let result = (|| {
            if !valid {
                return Err(unavailable());
            }
            let mut ordinal = 0;
            for keys in chords {
                for (press, codes) in [(true, keys.clone()), (false, keys.iter().rev().copied().collect())] {
                    for code in codes {
                        self.edge(ordinal, code, press)?;
                        ordinal += 1;
                    }
                }
            }
            self.end()
        })();
        receipt.dispatched = self.attempted;
        receipt.acknowledged = result.is_ok();
        receipt
    }
}

/// Same-process composition for the older mechanism fixtures only. Actual
/// process qualification must use the separately owned keeper and worker.
#[cfg(test)]
pub(super) struct NativeKeyboard {
    keeper: Keeper,
    worker: InputWorker,
}

#[cfg(test)]
impl std::ops::Deref for NativeKeyboard {
    type Target = InputWorker;
    fn deref(&self) -> &InputWorker {
        &self.worker
    }
}

#[cfg(test)]
impl NativeKeyboard {
    pub(super) fn admit(control: Endpoint, input: Endpoint, binding: Binding, deadline: Instant) -> Result<Self> {
        let keeper = PreparedKeeper::prepare(control, binding, deadline)?;
        let input = PreparedInput::prepare(input, binding, deadline)?;
        let keeper = keeper.grant(input.identity(), std::process::id(), deadline)?;
        let worker = input.bind(keeper.witness(), deadline)?;
        Ok(Self { keeper, worker })
    }

    pub(super) fn stop_handle(&self) -> StopHandle {
        self.worker.stop_handle()
    }
    pub(super) fn dispatch(&mut self, chords: &[Vec<u8>], deadline: Instant) -> Receipt {
        self.worker.dispatch(chords, deadline)
    }
    pub(super) fn finish(mut self) -> bool {
        self.cleanup()
    }

    fn cleanup(&mut self) -> bool {
        self.worker.input.boundary.stop();
        self.keeper.quiesce().is_ok() && self.keeper.dispose_fixture_pair()
    }

    fn run(mut self, chords: &[Vec<u8>], deadline: Instant) -> Receipt {
        let mut receipt = self.dispatch(chords, deadline);
        receipt.cleanup_confirmed = self.cleanup();
        receipt
    }
}

#[cfg(test)]
impl Drop for NativeKeyboard {
    fn drop(&mut self) {
        self.cleanup();
    }
}

#[cfg(test)]
mod native {
    #[test]
    #[ignore = "owned attested disposable Xvfb only; active master and held cleanup"]
    fn active_master_grabs_refuse_input_and_never_capture_cleanup_releases() {
        for family in 0..3 {
            for late in [false, true] {
                let fixture = Fixture::new();
                let mut session = fixture.session();
                let foreign = endpoint();
                session.worker.begin(deadline()).unwrap();
                if late {
                    session.worker.edge(0, fixture.key, true).unwrap();
                    assert_eq!(fixture.events(), [(fixture.target, true)]);
                }
                foreign.transaction(deadline(), || {
                    let connection = &foreign.connection;
                    let status = if family == 0 {
                        checked(checked(connection.xinput_xi_set_client_pointer(
                            x11rb::NONE, session.pair.pointer,
                        ))?.check())?;
                        let pointer = checked(checked(connection.xinput_xi_get_client_pointer(x11rb::NONE))?.reply())?;
                        assert!(pointer.set && pointer.deviceid == session.pair.pointer);
                        checked(checked(connection.grab_keyboard(
                            false, fixture.guard, x11rb::CURRENT_TIME,
                            xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC,
                        ))?.reply())?.status
                    } else if family == 1 {
                        let extension = checked(connection.extension_information(xinput::X11_EXTENSION_NAME))?
                            .ok_or_else(unavailable)?;
                        let classes = [xinput::DEVICE_KEY_PRESS_EVENT, xinput::DEVICE_KEY_RELEASE_EVENT]
                            .map(|kind| {
                                (u32::from(session.pair.keyboard) << 8) | u32::from(extension.first_event + kind)
                            });
                        checked(checked(connection.xinput_grab_device(
                            fixture.guard, x11rb::CURRENT_TIME,
                            xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC,
                            false, session.pair.keyboard, &classes,
                        ))?.reply())?.status
                    } else {
                        let mask = [u32::from(xinput::XIEventMask::KEY_PRESS | xinput::XIEventMask::KEY_RELEASE)];
                        checked(checked(connection.xinput_xi_grab_device(
                            fixture.guard, x11rb::CURRENT_TIME, x11rb::NONE, u16::from(session.pair.keyboard),
                            xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC,
                            xinput::GrabOwner::NO_OWNER, &mask,
                        ))?.reply())?.status
                    };
                    assert_eq!(status, xproto::GrabStatus::SUCCESS);
                    session.pair.check(&foreign)?;
                    session.pair.check_focus(&foreign, fixture.target)?;
                    Ok(())
                }).unwrap();
                let edge = session.worker.edge(u32::from(late), fixture.key, !late);
                assert!(edge.is_err());
                if !late { assert!(!session.worker.attempted) }
                let cleanup = session.keeper.quiesce();
                assert_eq!(cleanup.is_err(), late);
                if late {
                    fixture.endpoint.transaction(deadline(), || {
                        assert_ne!(key_state(&fixture.endpoint, session.pair.slave)?, [0; 32]);
                        Ok(())
                    }).unwrap();
                } else {
                    fixture.assert_pair_parked(&session.pair);
                }
                foreign.transaction(deadline(), || {
                    let connection = &foreign.connection;
                    checked(checked(connection.get_input_focus())?.reply())?;
                    while let Some(event) = checked(connection.poll_for_event())? {
                        let text = format!("{event:?}");
                        assert!(!text.contains("KeyPress") && !text.contains("KeyRelease"), "{text}");
                    }
                    if family == 0 {
                        checked(checked(connection.ungrab_keyboard(x11rb::CURRENT_TIME))?.check())
                    } else if family == 1 {
                        checked(checked(connection.xinput_ungrab_device(
                            x11rb::CURRENT_TIME, session.pair.keyboard,
                        ))?.check())
                    } else {
                        checked(checked(connection.xinput_xi_ungrab_device(
                            x11rb::CURRENT_TIME, u16::from(session.pair.keyboard),
                        ))?.check())
                    }
                }).unwrap();
                drop(foreign);
                assert_eq!(fixture.keyboard_observations(), (vec![], vec![]));
                if late {
                    fixture.endpoint.transaction(deadline(), || {
                        session.pair.check(&fixture.endpoint)?;
                        let remove = xinput::HierarchyChange {
                            len: 3,
                            data: xinput::HierarchyChangeData::RemoveMaster(xinput::HierarchyChangeDataRemoveMaster {
                                deviceid: session.pair.pointer, return_mode: xinput::ChangeMode::FLOAT,
                                return_pointer: 0, return_keyboard: 0,
                            }),
                        };
                        checked(checked(fixture.endpoint.connection.xinput_xi_change_hierarchy(&[remove]))?.check())
                    }).unwrap();
                } else {
                    assert!(session.keeper.dispose_fixture_pair());
                }
                fixture.remains();
                println!(
                    "active_master family={} late={} input_refused=true cleanup_unknown={} foreign_keys=0",
                    family, late, late,
                );
            }
        }
    }

    #[test]
    #[ignore = "owned attested disposable Xvfb only; native modifier routing"]
    fn private_modifier_state_preserves_unrelated_shortcuts_and_fences_matching_chords() {
        for xi2 in [false, true] {
            for held in [false, true] {
                let fixture = Fixture::new();
                let mut session = fixture.session();
                let control = fixture.endpoint.transaction(deadline(), || {
                    let connection = &fixture.endpoint.connection;
                    let setup = connection.setup();
                    let mapping = checked(checked(connection.get_keyboard_mapping(
                        setup.min_keycode, setup.max_keycode - setup.min_keycode + 1,
                    ))?.reply())?;
                    let index = mapping.keysyms.chunks(usize::from(mapping.keysyms_per_keycode))
                        .position(|values| values.first() == Some(&0xffe3)).ok_or_else(unavailable)?;
                    let modifiers = [u32::from(u16::from(xproto::ModMask::CONTROL))];
                    if xi2 {
                        let mask = [u32::from(xinput::XIEventMask::KEY_PRESS | xinput::XIEventMask::KEY_RELEASE)];
                        let grab = checked(checked(connection.xinput_xi_passive_grab_device(
                            x11rb::CURRENT_TIME, fixture.endpoint.root, x11rb::NONE, u32::from(fixture.key),
                            u16::from(session.pair.keyboard), xinput::GrabType::KEYCODE,
                            xinput::GrabMode22::ASYNC, xproto::GrabMode::ASYNC,
                            xinput::GrabOwner::NO_OWNER, &mask, &modifiers,
                        ))?.reply())?;
                        assert!(grab.modifiers.is_empty());
                    } else {
                        checked(checked(connection.grab_key(
                            false, fixture.endpoint.root, xproto::ModMask::CONTROL, fixture.key,
                            xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC,
                        ))?.check())?;
                    }
                    Ok(setup.min_keycode + index as u8)
                }).unwrap();
                session.worker.begin(deadline()).unwrap();
                if held {
                    session.worker.edge(0, control, true).unwrap();
                    assert_eq!(fixture.events(), [(fixture.target, true)]);
                }
                let press = session.worker.edge(u32::from(held), fixture.key, true);
                if held {
                    assert!(press.is_err());
                    assert_eq!(fixture.keyboard_observations(), (vec![], vec![]));
                } else {
                    press.unwrap();
                    session.worker.edge(1, fixture.key, false).unwrap();
                    session.worker.end().unwrap();
                    assert_eq!(
                        fixture.keyboard_observations(),
                        (vec![(fixture.target, true), (fixture.target, false)], vec![]),
                    );
                }
                session.worker.input.boundary.stop();
                session.keeper.quiesce().unwrap();
                fixture.assert_pair_parked(&session.pair);
                assert_eq!(fixture.keyboard_observations(), (vec![], vec![]));
                fixture.endpoint.transaction(deadline(), || {
                    let connection = &fixture.endpoint.connection;
                    if xi2 {
                        checked(checked(connection.xinput_xi_passive_ungrab_device(
                            fixture.endpoint.root, u32::from(fixture.key), u16::from(session.pair.keyboard),
                            xinput::GrabType::KEYCODE, &[u32::from(u16::from(xproto::ModMask::CONTROL))],
                        ))?.check())
                    } else {
                        checked(checked(connection.ungrab_key(
                            fixture.key, fixture.endpoint.root, xproto::ModMask::CONTROL,
                        ))?.check())
                    }
                }).unwrap();
                assert!(session.keeper.dispose_fixture_pair());
                fixture.remains();
                println!("modifier_route xi2={xi2} held={held} matching_refused={held} foreign_keys=0");
            }
        }
    }

    fn wildcard_root_grab(endpoint: &Endpoint, device: u16, key: u8, install: bool) -> Result<()> {
        let connection = &endpoint.connection;
        let modifiers = [u32::from(xinput::ModifierMask::ANY)];
        if install {
            let mask = [u32::from(xinput::XIEventMask::KEY_PRESS | xinput::XIEventMask::KEY_RELEASE)];
            let reply = checked(checked(connection.xinput_xi_passive_grab_device(
                x11rb::CURRENT_TIME, endpoint.root, x11rb::NONE, u32::from(key), device,
                xinput::GrabType::KEYCODE, xinput::GrabMode22::ASYNC, xproto::GrabMode::ASYNC,
                xinput::GrabOwner::NO_OWNER, &mask, &modifiers,
            ))?.reply())?;
            if !reply.modifiers.is_empty() { return Err(unavailable()) }
            Ok(())
        } else {
            checked(checked(connection.xinput_xi_passive_ungrab_device(
                endpoint.root, u32::from(key), device, xinput::GrabType::KEYCODE, &modifiers,
            ))?.check())
        }
    }

    #[test]
    #[ignore = "owned attested disposable Xvfb only; XI2 wildcard/slave passive routing"]
    fn wildcard_passives_cover_future_masters_without_rejecting_an_unfocused_slave() {
        for mode in 0..6 {
            let fixture = Fixture::new();
            let wildcard = if mode % 2 == 0 {
                u16::from(xinput::Device::ALL)
            } else {
                u16::from(xinput::Device::ALL_MASTER)
            };
            if mode < 2 {
                fixture.endpoint.transaction(deadline(), || {
                    wildcard_root_grab(&fixture.endpoint, wildcard, fixture.key, true)
                }).unwrap();
            }
            let mut session = fixture.session();
            let device = match mode {
                4 => u16::from(session.pair.slave),
                5 => u16::from(session.pair.keyboard),
                _ => wildcard,
            };
            if mode >= 2 {
                fixture.endpoint.transaction(deadline(), || {
                    wildcard_root_grab(&fixture.endpoint, device, fixture.key, true)
                }).unwrap();
            }
            session.worker.begin(deadline()).unwrap();
            let press = session.worker.edge(0, fixture.key, true);
            if mode == 4 {
                press.unwrap();
                session.worker.edge(1, fixture.key, false).unwrap();
                session.worker.end().unwrap();
                assert_eq!(
                    fixture.keyboard_observations(), (vec![(fixture.target, true), (fixture.target, false)], vec![]),
                );
            } else {
                assert!(press.is_err());
                assert!(!session.worker.attempted);
                assert_eq!(fixture.keyboard_observations(), (vec![], vec![]));
            }
            let other = endpoint();
            other.transaction(deadline(), || {
                assert!(wildcard_root_grab(&other, device, fixture.key, true).is_err());
                Ok(())
            }).unwrap();
            fixture.endpoint.transaction(deadline(), || {
                wildcard_root_grab(&fixture.endpoint, device, fixture.key, false)
            }).unwrap();
            other.transaction(deadline(), || {
                wildcard_root_grab(&other, device, fixture.key, true)?;
                wildcard_root_grab(&other, device, fixture.key, false)
            }).unwrap();
            drop(other);
            assert!(session.finish());
            fixture.remains();
            println!(
                "wildcard_route mode={mode} pre_input_refused={} foreign_preserved=true residual=false", mode != 4,
            );
        }
    }

    struct ForeignChildFixture {
        child: std::process::Child,
    }

    impl Drop for ForeignChildFixture {
        fn drop(&mut self) {
            let _ = self.child.kill();
            let _ = self.child.wait();
        }
    }

    impl ForeignChildFixture {
        fn receive(&mut self) -> serde_json::Value {
            let stdout = self.child.stdout.as_mut().unwrap();
            let fd = std::os::fd::AsRawFd::as_raw_fd(stdout);
            let limit = Instant::now() + Duration::from_secs(3);
            let mut bytes = Vec::new();
            loop {
                assert!(Instant::now() < limit && bytes.len() < 1024, "foreign fixture reply deadline/size");
                let mut poll = libc::pollfd { fd, events: libc::POLLIN, revents: 0 };
                let remaining = limit.saturating_duration_since(Instant::now()).as_millis().min(1000) as i32;
                let ready = unsafe { libc::poll(&mut poll, 1, remaining.max(1)) };
                if ready == 0 || (ready < 0 && io::Error::last_os_error().kind() == ErrorKind::Interrupted) {
                    continue;
                }
                assert!(ready > 0, "foreign fixture reply missing");
                let mut byte = [0];
                std::io::Read::read_exact(stdout, &mut byte).unwrap();
                if byte[0] == b'\n' { break }
                bytes.push(byte[0]);
            }
            serde_json::from_slice(&bytes).unwrap()
        }

        fn launch(parent: u32) -> (Self, u32) {
            let script = r#"import ctypes as c,json,os,sys
assert os.environ.get('MINTCLAW_NATIVE_KEYBOARD_NATIVE')=='1'
assert os.environ['DISPLAY']==':'+os.environ['MINTCLAW_PRIVATE_X_DISPLAY_NUMBER']
x=c.CDLL('libX11.so.6')
for name,args,result in [
 ('XOpenDisplay',[c.c_char_p],c.c_void_p),
 ('XCreateSimpleWindow',
  [c.c_void_p,c.c_ulong,c.c_int,c.c_int,c.c_uint,c.c_uint,c.c_uint,c.c_ulong,c.c_ulong],c.c_ulong),
 ('XSelectInput',[c.c_void_p,c.c_ulong,c.c_long],c.c_int),
 ('XMapWindow',[c.c_void_p,c.c_ulong],c.c_int),
 ('XSync',[c.c_void_p,c.c_int],c.c_int),
 ('XPending',[c.c_void_p],c.c_int),
 ('XNextEvent',[c.c_void_p,c.c_void_p],c.c_int)]:
 f=getattr(x,name);f.argtypes=args;f.restype=result
d=x.XOpenDisplay(None);assert d
w=x.XCreateSimpleWindow(d,int(sys.argv[1]),20,20,60,30,0,0,0);assert w
x.XSelectInput(d,w,3);x.XMapWindow(d,w);x.XSync(d,0)
print(json.dumps({'window':w,'pid':os.getpid()}),flush=True)
for command in sys.stdin:
 assert command.strip()=='observe'
 x.XSync(d,0);counts={2:0,3:0};event=(c.c_long*24)()
 while x.XPending(d):
  x.XNextEvent(d,c.byref(event));kind=c.cast(c.byref(event),c.POINTER(c.c_int))[0]
  if kind in counts:counts[kind]+=1
 print(json.dumps({'press':counts[2],'release':counts[3]}),flush=True)
"#;
            let child = std::process::Command::new("/usr/bin/python3")
                .args(["-u", "-c", script, &parent.to_string()])
                .stdin(std::process::Stdio::piped())
                .stdout(std::process::Stdio::piped())
                .stderr(std::process::Stdio::inherit())
                .spawn().unwrap();
            let mut fixture = Self { child };
            let ready = fixture.receive();
            assert_eq!(ready["pid"].as_u64(), Some(u64::from(fixture.child.id())));
            let window = u32::try_from(ready["window"].as_u64().unwrap()).unwrap();
            (fixture, window)
        }

        fn observe(&mut self) -> serde_json::Value {
            std::io::Write::write_all(self.child.stdin.as_mut().unwrap(), b"observe\n").unwrap();
            self.receive()
        }
    }

    #[test]
    #[ignore = "owned attested disposable Xvfb only; separate native child application"]
    fn foreign_descendants_are_fenced_before_press_and_before_release() {
        for mode in 0..2 {
            let fixture = Fixture::new();
            fixture.endpoint.transaction(deadline(), || {
                checked(checked(fixture.endpoint.connection.configure_window(
                    fixture.target, &xproto::ConfigureWindowAux::new().stack_mode(xproto::StackMode::ABOVE),
                ))?.check())
            }).unwrap();
            let mut session = fixture.session();
            fixture.endpoint.transaction(deadline(), || {
                checked(checked(fixture.endpoint.connection.xinput_xi_warp_pointer(
                    x11rb::NONE, fixture.target, 0, 0, 0, 0, 1 << 16, 1 << 16, session.pair.pointer,
                ))?.check())
            }).unwrap();
            session.worker.begin(deadline()).unwrap();
            if mode == 1 {
                session.worker.edge(0, fixture.key, true).unwrap();
                assert_eq!(fixture.keyboard_observations(), (vec![(fixture.target, true)], vec![]));
            }
            let (mut foreign, child) = ForeignChildFixture::launch(fixture.target);
            fixture.endpoint.transaction(deadline(), || {
                let connection = &fixture.endpoint.connection;
                let owner = checked(checked(connection.res_query_client_ids(&[res::ClientIdSpec {
                    client: child, mask: res::ClientIdMask::LOCAL_CLIENT_PID,
                }]))?.reply())?;
                assert_eq!(owner.ids.len(), 1);
                assert_eq!(owner.ids[0].value, [foreign.child.id()]);
                assert_ne!(foreign.child.id(), std::process::id());
                checked(checked(connection.xinput_xi_warp_pointer(
                    x11rb::NONE, child, 0, 0, 0, 0, 1 << 16, 1 << 16, session.pair.pointer,
                ))?.check())
            }).unwrap();
            let edge = session.worker.edge(mode, fixture.key, mode == 0);
            session.worker.input.boundary.stop();
            session.keeper.quiesce().unwrap();
            fixture.assert_pair_parked(&session.pair);
            let events = foreign.observe();
            println!("foreign_descendant mode={mode} edge_ack={} events={events}", edge.is_ok());
            fixture.endpoint.transaction(deadline(), || {
                checked(checked(fixture.endpoint.connection.get_geometry(child))?.reply())?;
                Ok(())
            }).unwrap();
            drop(foreign);
            assert!(session.keeper.dispose_fixture_pair());
            fixture.remains();
            assert!(edge.is_err(), "foreign application received an admitted native edge");
            assert_eq!(events["press"].as_u64(), Some(0));
            assert_eq!(events["release"].as_u64(), Some(0));
        }
    }

    #[test]
    #[ignore = "owned attested disposable Xvfb only; paired-device native freeze"]
    fn paired_pointer_freeze_is_refused_before_input_without_replaying_after_stop() {
        for mode in 0..3 {
            let fixture = Fixture::new();
            let mut session = fixture.session();
            let pointer = fixture.endpoint.transaction(deadline(), || {
                let connection = &fixture.endpoint.connection;
                let devices = checked(checked(connection.xinput_xi_query_device(0u16))?.reply())?.infos;
                let ambient: Vec<_> = devices.iter().filter(|device| {
                    device.type_ == xinput::DeviceType::MASTER_POINTER && device.deviceid != session.pair.pointer
                }).collect();
                assert_eq!(ambient.len(), 1);
                let pointer = if mode == 0 { ambient[0].deviceid } else { session.pair.pointer };
                let paired = if mode == 1 { xproto::GrabMode::ASYNC } else { xproto::GrabMode::SYNC };
                let grab = checked(checked(connection.xinput_xi_grab_device(
                    fixture.guard, x11rb::CURRENT_TIME, x11rb::NONE, pointer,
                    xproto::GrabMode::ASYNC, paired, xinput::GrabOwner::NO_OWNER, &[],
                ))?.reply())?;
                assert_eq!(grab.status, xproto::GrabStatus::SUCCESS);
                session.pair.check(&fixture.endpoint)?;
                Ok(pointer)
            }).unwrap();
            session.worker.begin(deadline()).unwrap();
            let press = session.worker.edge(0, fixture.key, true);
            if mode == 2 {
                assert!(press.is_err());
                assert!(!session.worker.attempted);
                assert_eq!(fixture.keyboard_observations(), (vec![], vec![]));
            } else {
                press.unwrap();
                session.worker.edge(1, fixture.key, false).unwrap();
                session.worker.end().unwrap();
                assert_eq!(
                    fixture.keyboard_observations(), (vec![(fixture.target, true), (fixture.target, false)], vec![]),
                );
            }
            session.worker.input.boundary.stop();
            session.keeper.quiesce().unwrap();
            // Check retained native state before thawing the foreign grab.
            // Zero bits alone would not establish absence of queued input.
            fixture.assert_pair_parked(&session.pair);
            fixture.endpoint.transaction(deadline(), || {
                checked(checked(fixture.endpoint.connection.xinput_xi_ungrab_device(
                    x11rb::CURRENT_TIME, pointer,
                ))?.check())
            }).unwrap();
            assert_eq!(fixture.keyboard_observations(), (vec![], vec![]));
            assert!(session.keeper.dispose_fixture_pair());
            fixture.remains();
            println!("paired_freeze mode={mode} pre_input_refused={} late_key_events=0", mode == 2);
        }
    }

    #[test]
    #[ignore = "owned attested disposable Xvfb only; actual native routing paths"]
    fn native_ancestor_target_and_pointer_descendant_passives_are_fenced() {
        for location in 0..3 {
            for family in 0..4 {
                for matching in [true, false] {
                    let fixture = Fixture::new();
                    let (parent, child) = fixture.endpoint.transaction(deadline(), || {
                        let connection = &fixture.endpoint.connection;
                        let parent = checked(connection.generate_id())?;
                        let child = checked(connection.generate_id())?;
                        for (window, owner, x, y) in [
                            (parent, fixture.endpoint.root, 100, 100), (child, fixture.target, 20, 20),
                        ] {
                            checked(checked(connection.create_window(
                                x11rb::COPY_DEPTH_FROM_PARENT, window, owner, x, y, 150, 75, 0,
                                xproto::WindowClass::INPUT_OUTPUT, 0,
                                &xproto::CreateWindowAux::new().event_mask(
                                    xproto::EventMask::KEY_PRESS | xproto::EventMask::KEY_RELEASE,
                                ),
                            ))?.check())?;
                            checked(checked(connection.map_window(window))?.check())?;
                        }
                        // Establish layout BEFORE fresh target admission.
                        checked(checked(connection.reparent_window(fixture.target, parent, 0, 0))?.check())?;
                        checked(checked(connection.map_window(fixture.target))?.check())?;
                        Ok((parent, child))
                    }).unwrap();
                    let mut session = fixture.session();
                    fixture.endpoint.transaction(deadline(), || {
                        checked(checked(fixture.endpoint.connection.xinput_xi_warp_pointer(
                            x11rb::NONE, child, 0, 0, 0, 0, 1 << 16, 1 << 16, session.pair.pointer,
                        ))?.check())
                    }).unwrap();
                    session.worker.input.transaction(deadline(), || {
                        let route = NativeRoutingGuard::windows(
                            &session.worker.input, &session.pair, fixture.target, std::process::id(),
                        )?;
                        assert_eq!(route, [fixture.target, parent, fixture.endpoint.root, child]);
                        Ok(())
                    }).unwrap();
                    let window = [parent, fixture.target, child][location];
                    let modifier = fixture.endpoint.transaction(deadline(), || {
                        let devices = checked(checked(fixture.endpoint.connection
                            .xinput_xi_query_device(0u16))?.reply())?.infos;
                        let ambient: Vec<_> = devices.iter().filter(|device| {
                            device.type_ == xinput::DeviceType::MASTER_KEYBOARD
                                && device.deviceid != u16::from(session.pair.keyboard)
                        }).collect();
                        assert_eq!(ambient.len(), 1);
                        Ok(if family == 2 { u8::try_from(ambient[0].deviceid).unwrap() } else { session.pair.keyboard })
                    }).unwrap();
                    let grabbed = if matching { fixture.key } else { fixture.key.checked_add(1).unwrap() };
                    fixture.endpoint.transaction(deadline(), || {
                        window_foreign_passive(
                            &fixture.endpoint, &session.pair, window, family, modifier, grabbed, true,
                        )
                    }).unwrap();
                    session.worker.begin(deadline()).unwrap();
                    let press = session.worker.edge(0, fixture.key, true);
                    if matching {
                        assert!(press.is_err());
                        assert!(!session.worker.attempted);
                        assert_eq!(fixture.keyboard_observations(), (vec![], vec![]));
                    } else {
                        press.unwrap();
                        session.worker.edge(1, fixture.key, false).unwrap();
                        session.worker.end().unwrap();
                        assert_eq!(fixture.keyboard_observations(), (vec![(child, true), (child, false)], vec![]));
                    }
                    let other = endpoint();
                    other.transaction(deadline(), || {
                        assert!(window_foreign_passive(
                            &other, &session.pair, window, family, modifier, grabbed, true,
                        ).is_err());
                        Ok(())
                    }).unwrap();
                    fixture.endpoint.transaction(deadline(), || {
                        window_foreign_passive(
                            &fixture.endpoint, &session.pair, window, family, modifier, grabbed, false,
                        )
                    }).unwrap();
                    other.transaction(deadline(), || {
                        window_foreign_passive(&other, &session.pair, window, family, modifier, grabbed, true)?;
                        window_foreign_passive(&other, &session.pair, window, family, modifier, grabbed, false)
                    }).unwrap();
                    drop(other);
                    assert!(session.finish());
                    fixture.remains();
                    println!(
                        "native_window_path location={} family={} matching={} foreign_preserved=true residual=false",
                        location, family, matching,
                    );
                }
            }
        }
    }

    fn window_foreign_passive(
        endpoint: &Endpoint, pair: &Pair, window: u32, family: u8, modifier: u8, code: u8, install: bool,
    ) -> Result<()> {
        let connection = &endpoint.connection;
        if family == 0 {
            if install {
                checked(checked(connection.grab_key(
                    false, window, xproto::ModMask::ANY, code,
                    xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC,
                ))?.check())
            } else {
                checked(checked(connection.ungrab_key(code, window, xproto::ModMask::ANY))?.check())
            }
        } else if family == 3 {
            let modifiers = [u32::from(xinput::ModifierMask::ANY)];
            if install {
                let mask = [u32::from(xinput::XIEventMask::KEY_PRESS | xinput::XIEventMask::KEY_RELEASE)];
                let reply = checked(checked(connection.xinput_xi_passive_grab_device(
                    x11rb::CURRENT_TIME, window, x11rb::NONE, u32::from(code),
                    u16::from(pair.keyboard), xinput::GrabType::KEYCODE,
                    xinput::GrabMode22::ASYNC, xproto::GrabMode::ASYNC,
                    xinput::GrabOwner::NO_OWNER, &mask, &modifiers,
                ))?.reply())?;
                if !reply.modifiers.is_empty() { return Err(unavailable()) }
                Ok(())
            } else {
                checked(checked(connection.xinput_xi_passive_ungrab_device(
                    window, u32::from(code), u16::from(pair.keyboard), xinput::GrabType::KEYCODE, &modifiers,
                ))?.check())
            }
        } else if install {
            let extension = checked(connection.extension_information(xinput::X11_EXTENSION_NAME))?
                .ok_or_else(unavailable)?;
            let classes = [xinput::DEVICE_KEY_PRESS_EVENT, xinput::DEVICE_KEY_RELEASE_EVENT]
                .map(|kind| (u32::from(pair.keyboard) << 8) | u32::from(extension.first_event + kind));
            checked(checked(connection.xinput_grab_device_key(
                window, xproto::ModMask::ANY, modifier, pair.keyboard, code,
                xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC, false, &classes,
            ))?.check())
        } else {
            checked(checked(connection.xinput_ungrab_device_key(
                window, xproto::ModMask::ANY, modifier, code, pair.keyboard,
            ))?.check())
        }
    }

    fn root_foreign_passive(
        endpoint: &Endpoint, pair: &Pair, family: u8, modifier: u8, code: u8, install: bool,
    ) -> Result<()> {
        window_foreign_passive(endpoint, pair, endpoint.root, family, modifier, code, install)
    }
    #[test]
    #[ignore = "owned attested disposable Xvfb only; core/XI1/XI2 root passive routing"]
    fn root_passive_families_refuse_matching_and_preserve_unrelated_keys() {
        for family in 0..4 {
            for matching in [true, false] {
                let fixture = Fixture::new();
                let mut session = fixture.session();
                let modifier = fixture.endpoint.transaction(deadline(), || {
                    let devices = checked(checked(fixture.endpoint.connection
                        .xinput_xi_query_device(0u16))?.reply())?.infos;
                    let ambient: Vec<_> = devices.iter().filter(|device| {
                        device.type_ == xinput::DeviceType::MASTER_KEYBOARD
                            && device.deviceid != u16::from(session.pair.keyboard)
                    }).collect();
                    assert_eq!(ambient.len(), 1);
                    Ok(if family == 2 { u8::try_from(ambient[0].deviceid).unwrap() } else { session.pair.keyboard })
                }).unwrap();
                let grabbed = if matching { fixture.key } else { fixture.key.checked_add(1).unwrap() };
                fixture.endpoint.transaction(deadline(), || {
                    root_foreign_passive(&fixture.endpoint, &session.pair, family, modifier, grabbed, true)
                }).unwrap();
                session.worker.begin(deadline()).unwrap();
                let press = session.worker.edge(0, fixture.key, true);
                if matching {
                    assert!(press.is_err());
                    assert!(!session.worker.attempted);
                    assert_eq!(fixture.keyboard_observations(), (vec![], vec![]));
                } else {
                    press.unwrap();
                    session.worker.edge(1, fixture.key, false).unwrap();
                    session.worker.end().unwrap();
                    assert_eq!(
                        fixture.keyboard_observations(),
                        (vec![(fixture.target, true), (fixture.target, false)], vec![]),
                    );
                }
                let other = endpoint();
                other.transaction(deadline(), || {
                    assert!(root_foreign_passive(&other, &session.pair, family, modifier, grabbed, true).is_err());
                    Ok(())
                }).unwrap();
                fixture.endpoint.transaction(deadline(), || {
                    root_foreign_passive(&fixture.endpoint, &session.pair, family, modifier, grabbed, false)
                }).unwrap();
                other.transaction(deadline(), || {
                    root_foreign_passive(&other, &session.pair, family, modifier, grabbed, true)?;
                    root_foreign_passive(&other, &session.pair, family, modifier, grabbed, false)
                }).unwrap();
                // This separately owned probe can reuse the stopped input
                // client's numeric X client base. Close it before asking the
                // keeper for its conservative original-client death fence.
                drop(other);
                assert!(session.finish());
                fixture.remains();
                println!(
                    "root_passive_family family={family} matching={matching} foreign_preserved=true residual=false",
                );
            }
        }
    }

    #[test]
    #[ignore = "owned attested disposable Xvfb only; active XI1 cleanup diversion"]
    fn held_cleanup_must_not_deliver_to_a_late_active_xi1_slave_grab() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        session.worker.begin(deadline()).unwrap();
        session.worker.edge(0, fixture.key, true).unwrap();
        assert_eq!(fixture.events(), [(fixture.target, true)]);
        fixture.endpoint.transaction(deadline(), || {
            let connection = &fixture.endpoint.connection;
            checked(checked(connection.xinput_open_device(session.pair.slave))?.reply())?;
            let extension = checked(connection.extension_information(xinput::X11_EXTENSION_NAME))?
                .ok_or_else(unavailable)?;
            let classes = [xinput::DEVICE_KEY_PRESS_EVENT, xinput::DEVICE_KEY_RELEASE_EVENT]
                .map(|kind| (u32::from(session.pair.slave) << 8) | u32::from(extension.first_event + kind));
            let grab = checked(checked(connection.xinput_grab_device(
                fixture.guard, x11rb::CURRENT_TIME,
                xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC, false,
                session.pair.slave, &classes,
            ))?.reply())?;
            assert_eq!(grab.status, xproto::GrabStatus::SUCCESS);
            session.pair.check(&fixture.endpoint)?;
            session.pair.check_focus(&fixture.endpoint, fixture.target)?;
            Ok(())
        }).unwrap();
        session.worker.input.boundary.stop();
        let cleanup = session.keeper.quiesce();
        let observed = fixture.endpoint.transaction(deadline(), || {
            let connection = &fixture.endpoint.connection;
            checked(checked(connection.get_input_focus())?.reply())?;
            let mut events = Vec::new();
            while let Some(event) = checked(connection.poll_for_event())? {
                let description = format!("{event:?}");
                if description.contains("KeyPress") || description.contains("KeyRelease") {
                    events.push(description);
                }
            }
            checked(checked(connection.xinput_ungrab_device(x11rb::CURRENT_TIME, session.pair.slave))?.check())?;
            Ok(events)
        }).unwrap();
        println!("active_xi1_cleanup quiescent_receipt={} events={observed:?}", cleanup.is_ok());
        assert!(cleanup.is_err(), "foreign active grab cannot yield a quiescent receipt");
        assert!(observed.is_empty(), "held cleanup delivered a release to a foreign XI1 slave grab");
        fixture.endpoint.transaction(deadline(), || {
            session.pair.check(&fixture.endpoint)?;
            assert_ne!(key_state(&fixture.endpoint, session.pair.slave)?, [0; 32]);
            let remove = xinput::HierarchyChange {
                len: 3,
                data: xinput::HierarchyChangeData::RemoveMaster(xinput::HierarchyChangeDataRemoveMaster {
                    deviceid: session.pair.pointer, return_mode: xinput::ChangeMode::FLOAT,
                    return_pointer: 0, return_keyboard: 0,
                }),
            };
            checked(checked(fixture.endpoint.connection.xinput_xi_change_hierarchy(&[remove]))?.check())
        }).unwrap();
        fixture.remains();
    }

    #[test]
    #[ignore = "owned attested disposable Xvfb only; active XI1 slave grab"]
    fn active_xi1_injection_grab_cannot_escape_the_target() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        fixture.endpoint.transaction(deadline(), || {
            let connection = &fixture.endpoint.connection;
            checked(checked(connection.xinput_open_device(session.pair.slave))?.reply())?;
            let extension = checked(connection.extension_information(xinput::X11_EXTENSION_NAME))?
                .ok_or_else(unavailable)?;
            let classes = [xinput::DEVICE_KEY_PRESS_EVENT, xinput::DEVICE_KEY_RELEASE_EVENT]
                .map(|kind| (u32::from(session.pair.slave) << 8) | u32::from(extension.first_event + kind));
            let grab = checked(checked(connection.xinput_grab_device(
                fixture.guard, x11rb::CURRENT_TIME,
                xproto::GrabMode::ASYNC, xproto::GrabMode::ASYNC, false,
                session.pair.slave, &classes,
            ))?.reply())?;
            assert_eq!(grab.status, xproto::GrabStatus::SUCCESS);
            // XI1 active grabs do not detach this original slave. Its native
            // topology and persistent focus can still pass fresh snapshots.
            session.pair.check(&fixture.endpoint)?;
            session.pair.check_focus(&fixture.endpoint, fixture.target)?;
            Ok(())
        }).unwrap();
        session.worker.begin(deadline()).unwrap();
        let press = session.worker.edge(0, fixture.key, true);
        let release = if press.is_ok() { session.worker.edge(1, fixture.key, false) } else { Err(unavailable()) };
        let observed = fixture.endpoint.transaction(deadline(), || {
            let connection = &fixture.endpoint.connection;
            checked(checked(connection.get_input_focus())?.reply())?;
            let mut events = Vec::new();
            while let Some(event) = checked(connection.poll_for_event())? {
                let description = format!("{event:?}");
                if description.contains("KeyPress") || description.contains("KeyRelease") {
                    events.push(description);
                }
            }
            checked(checked(connection.xinput_ungrab_device(x11rb::CURRENT_TIME, session.pair.slave))?.check())?;
            Ok(events)
        }).unwrap();
        println!("active_xi1_slave press_ack={} release_ack={} events={observed:?}", press.is_ok(), release.is_ok());
        assert!(session.finish());
        fixture.remains();
        assert!(press.is_err() && observed.is_empty(), "foreign active XI1 slave grab received admitted input");
    }

    use super::*;

    pub(super) fn deadline() -> Instant {
        Instant::now() + Duration::from_secs(3)
    }

    pub(super) fn endpoint() -> Endpoint {
        assert_eq!(std::env::var("MINTCLAW_NATIVE_KEYBOARD_NATIVE").unwrap(), "1");
        let number = std::env::var("MINTCLAW_PRIVATE_X_DISPLAY_NUMBER").unwrap();
        assert!(!number.is_empty() && number.bytes().all(|byte| byte.is_ascii_digit()));
        let socket = UnixStream::connect(format!("/tmp/.X11-unix/X{number}")).unwrap();
        let metadata = UnixStream::connect(format!("/tmp/.X11-unix/X{number}")).unwrap();
        let cookie = std::env::var("MINTCLAW_PRIVATE_X_COOKIE").unwrap();
        assert_eq!(cookie.len(), 32);
        let cookie =
            (0..32).step_by(2).map(|index| u8::from_str_radix(&cookie[index..index + 2], 16).unwrap()).collect();
        let endpoint = Endpoint::from_sockets(
            socket, metadata, 0, b"MIT-MAGIC-COOKIE-1".to_vec(), cookie, deadline(),
        ).unwrap();
        let expected_pid: i32 = std::env::var("MINTCLAW_PRIVATE_XVFB_PID").unwrap().parse().unwrap();
        assert_eq!(endpoint.peer, (expected_pid, unsafe { libc::getuid() }));
        let expected = std::fs::canonicalize(std::env::var("MINTCLAW_PRIVATE_XVFB_EXECUTABLE").unwrap()).unwrap();
        assert_eq!(std::fs::read_link(format!("/proc/{expected_pid}/exe")).unwrap(), expected);
        endpoint
    }

    struct Fixture {
        endpoint: Endpoint,
        target: u32,
        guard: u32,
        token: u32,
        selection: u32,
        key: u8,
    }

    impl Fixture {
        fn new() -> Self {
            let endpoint = endpoint();
            let (target, guard, token, selection, key) = endpoint
                .transaction(deadline(), || {
                    let connection = &endpoint.connection;
                    let mut windows = Vec::new();
                    for _ in 0..2 {
                        let window = checked(connection.generate_id())?;
                        checked(
                            checked(
                                connection.create_window(
                                    x11rb::COPY_DEPTH_FROM_PARENT,
                                    window,
                                    endpoint.root,
                                    0,
                                    0,
                                    200,
                                    100,
                                    0,
                                    xproto::WindowClass::INPUT_OUTPUT,
                                    0,
                                    &xproto::CreateWindowAux::new()
                                        .event_mask(xproto::EventMask::KEY_PRESS | xproto::EventMask::KEY_RELEASE),
                                ),
                            )?
                            .check(),
                        )?;
                        checked(checked(connection.map_window(window))?.check())?;
                        windows.push(window);
                    }
                    let token = checked(connection.generate_id())?;
                    checked(
                        checked(connection.create_window(
                            0,
                            token,
                            endpoint.root,
                            0,
                            0,
                            1,
                            1,
                            0,
                            xproto::WindowClass::INPUT_ONLY,
                            0,
                            &xproto::CreateWindowAux::new(),
                        ))?
                        .check(),
                    )?;
                    let selection = checked(checked(connection.intern_atom(false, SELECTION))?.reply())?.atom;
                    checked(checked(connection.set_selection_owner(token, selection, x11rb::CURRENT_TIME))?.check())?;
                    checked(
                        checked(connection.set_input_focus(xproto::InputFocus::NONE, windows[1], x11rb::CURRENT_TIME))?
                            .check(),
                    )?;
                    let setup = connection.setup();
                    let mapping = checked(
                        checked(
                            connection
                                .get_keyboard_mapping(setup.min_keycode, setup.max_keycode - setup.min_keycode + 1),
                        )?
                        .reply(),
                    )?;
                    let index = mapping
                        .keysyms
                        .chunks(usize::from(mapping.keysyms_per_keycode))
                        .position(|symbols| symbols.first() == Some(&u32::from(b'a')))
                        .ok_or_else(unavailable)?;
                    Ok((windows[0], windows[1], token, selection, setup.min_keycode + index as u8))
                })
                .unwrap();
            Self { endpoint, target, guard, token, selection, key }
        }

        fn session(&self) -> NativeKeyboard {
            NativeKeyboard::admit(
                endpoint(),
                endpoint(),
                Binding {
                    window: self.target,
                    pid: std::process::id(),
                    lease_window: self.token,
                    lease_pid: std::process::id(),
                },
                deadline(),
            )
            .unwrap()
        }

        fn events(&self) -> Vec<(u32, bool)> {
            self.endpoint
                .transaction(deadline(), || {
                    checked(checked(self.endpoint.connection.get_input_focus())?.reply())?;
                    let mut values = Vec::new();
                    while let Some(event) = checked(self.endpoint.connection.poll_for_event())? {
                        match event {
                            Event::KeyPress(event) => values.push((event.event, true)),
                            Event::KeyRelease(event) => values.push((event.event, false)),
                            _ => (),
                        }
                    }
                    Ok(values)
                })
                .unwrap()
        }

        fn remains(&self) {
            self.endpoint
                .transaction(deadline(), || {
                    checked(checked(self.endpoint.connection.get_geometry(self.target))?.reply())?;
                    let focus = checked(checked(self.endpoint.connection.get_input_focus())?.reply())?.focus;
                    assert_eq!(focus, self.guard);
                    let devices =
                        checked(checked(self.endpoint.connection.xinput_xi_query_device(0u16))?.reply())?.infos;
                    assert!(!devices.iter().any(|device| device.name.starts_with(PREFIX)));
                    Ok(())
                })
                .unwrap();
        }

        fn observe_slave(&self, pair: &Pair) {
            self.endpoint.transaction(deadline(), || {
                let masks = [xinput::EventMask {
                    deviceid: u16::from(pair.slave),
                    mask: vec![xinput::XIEventMask::KEY_PRESS | xinput::XIEventMask::KEY_RELEASE],
                }];
                for window in [self.endpoint.root, self.guard] {
                    checked(checked(self.endpoint.connection.xinput_xi_select_events(window, &masks))?.check())?;
                }
                Ok(())
            }).unwrap();
        }

        fn keyboard_observations(&self) -> (Vec<(u32, bool)>, Vec<(u16, u32)>) {
            self.endpoint.transaction(deadline(), || {
                let connection = &self.endpoint.connection;
                checked(checked(connection.get_input_focus())?.reply())?;
                let mut core = Vec::new();
                let mut xi = Vec::new();
                while let Some(event) = checked(connection.poll_for_event())? {
                    match event {
                        Event::KeyPress(value) => core.push((value.event, true)),
                        Event::KeyRelease(value) => core.push((value.event, false)),
                        Event::XinputKeyPress(value) | Event::XinputKeyRelease(value) => {
                            xi.push((value.deviceid, value.event));
                        }
                        _ => (),
                    }
                }
                Ok((core, xi))
            }).unwrap()
        }

        fn assert_pair_parked(&self, pair: &Pair) {
            self.endpoint.transaction(deadline(), || {
                for device in [pair.keyboard, pair.slave] {
                    let focus = checked(checked(self.endpoint.connection.xinput_get_device_focus(device))?.reply())?;
                    assert_eq!(focus.focus, x11rb::NONE);
                    assert_eq!(focus.revert_to, xproto::InputFocus::NONE);
                    assert_eq!(key_state(&self.endpoint, device)?, [0; 32]);
                }
                Ok(())
            }).unwrap();
        }

        fn silent_map_restore(&self, session: &NativeKeyboard, source: &Endpoint) {
            source.transaction(deadline(), || {
                let connection = &source.connection;
                assert!(checked(checked(connection.xkb_use_extension(1, 0))?.reply())?.supported);
                let setup = connection.setup();
                let profile = session.keeper.channel.profile.as_ref().unwrap();
                for mods in [xproto::ModMask::CONTROL, 0u8.into()] {
                    let values = xkb::SetMapAux {
                        modmap: Some(vec![xkb::KeyModMap { keycode: self.key, mods }]),
                        ..Default::default()
                    };
                    checked(checked(connection.xkb_set_map(
                        u16::from(session.pair.slave), 0u16.into(), setup.min_keycode, setup.max_keycode,
                        0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
                        self.key, 1, 1, 0, 0, 0, 0u16.into(), &values,
                    ))?.check())?;
                    assert_eq!(profile.maps_match(source, &session.pair)?, u16::from(mods) == 0);
                }
                Ok(())
            }).unwrap();
        }

        /// Only this attested disposable fixture may dispose an uncertain pair.
        /// Runtime refusal/Drop cannot retry a release or claim native cleanup.
        fn dispose_uncertain(&self, session: &mut NativeKeyboard) {
            session.worker.input.boundary.stop();
            wait_client_gone(&self.endpoint, session.input_token, std::process::id(), deadline()).unwrap();
            self.endpoint.transaction(deadline(), || {
                session.pair.check(&self.endpoint)?;
                let remove = xinput::HierarchyChange {
                    len: 3,
                    data: xinput::HierarchyChangeData::RemoveMaster(xinput::HierarchyChangeDataRemoveMaster {
                        deviceid: session.pair.pointer, return_mode: xinput::ChangeMode::FLOAT,
                        return_pointer: 0, return_keyboard: 0,
                    }),
                };
                checked(checked(self.endpoint.connection.xinput_xi_change_hierarchy(&[remove]))?.check())
            }).unwrap();
            self.remains();
        }
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn injection_slave_never_delivers_to_ambient_focus_or_root() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        fixture.observe_slave(&session.pair);
        fixture.endpoint.transaction(deadline(), || {
            let focus = checked(checked(fixture.endpoint.connection
                .xinput_get_device_focus(session.pair.slave))?.reply())?;
            assert_eq!(focus.focus, x11rb::NONE);
            Ok(())
        }).unwrap();
        session.worker.begin(deadline()).unwrap();
        session.worker.edge(0, fixture.key, true).unwrap();
        session.worker.edge(1, fixture.key, false).unwrap();
        session.worker.end().unwrap();
        let (core, xi) = fixture.keyboard_observations();
        assert_eq!(core, [(fixture.target, true), (fixture.target, false)]);
        assert!(xi.is_empty(), "injection events escaped the original target: {xi:?}");
        assert!(session.finish());
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn changed_injection_focus_is_refused_before_any_key() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        fixture.observe_slave(&session.pair);
        fixture.endpoint.transaction(deadline(), || {
            checked(checked(fixture.endpoint.connection.xinput_set_device_focus(
                u32::from(xproto::InputFocus::POINTER_ROOT), x11rb::CURRENT_TIME,
                xproto::InputFocus::NONE, session.pair.slave,
            ))?.check())
        }).unwrap();
        session.worker.begin(deadline()).unwrap();
        assert!(session.worker.edge(0, fixture.key, true).is_err());
        assert!(!session.worker.attempted);
        assert_eq!(fixture.keyboard_observations(), (vec![], vec![]));
        session.keeper.quiesce().unwrap();
        fixture.assert_pair_parked(&session.pair);
        assert!(session.keeper.dispose_fixture_pair());
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn held_cleanup_parks_injection_source_before_synthetic_release() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        fixture.observe_slave(&session.pair);
        session.worker.begin(deadline()).unwrap();
        session.worker.edge(0, fixture.key, true).unwrap();
        fixture.endpoint.transaction(deadline(), || {
            checked(checked(fixture.endpoint.connection.xinput_set_device_focus(
                u32::from(xproto::InputFocus::POINTER_ROOT), x11rb::CURRENT_TIME,
                xproto::InputFocus::NONE, session.pair.slave,
            ))?.check())
        }).unwrap();
        session.worker.input.boundary.stop();
        session.keeper.quiesce().unwrap();
        fixture.assert_pair_parked(&session.pair);
        let (core, xi) = fixture.keyboard_observations();
        assert_eq!(core, [(fixture.target, true)]);
        assert!(xi.is_empty(), "cleanup released into an unrelated window: {xi:?}");
        assert!(session.keeper.dispose_fixture_pair());
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn silent_map_restore_between_keeper_and_edge_never_releases_a_held_key() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        session.worker.begin(deadline()).unwrap();
        session.worker.edge(0, fixture.key, true).unwrap();
        session.keeper.check(deadline()).unwrap();
        let future = endpoint();
        fixture.silent_map_restore(&session, &future);
        assert!(session.worker.edge(1, fixture.key, false).is_err());
        assert!(session.keeper.quiesce().is_err());
        fixture.endpoint.transaction(deadline(), || {
            assert_ne!(key_state(&fixture.endpoint, session.pair.slave)?, [0; 32]);
            let focus = checked(checked(fixture.endpoint.connection
                .xinput_get_device_focus(session.pair.keyboard))?.reply())?;
            assert_eq!(focus.focus, x11rb::NONE);
            Ok(())
        }).unwrap();
        assert_eq!(fixture.events(), [(fixture.target, true)]);
        fixture.dispose_uncertain(&mut session);
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn silent_map_restore_while_idle_cannot_join_a_fresh_action() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        session.worker.input.boundary.stop();
        session.keeper.quiesce().unwrap();
        fixture.silent_map_restore(&session, &fixture.endpoint);
        let next = Binding {
            window: fixture.guard, pid: std::process::id(), lease_window: fixture.token,
            lease_pid: std::process::id(),
        };
        assert!(session.keeper.prepare_action(next, deadline()).is_err());
        assert!(!session.keeper.channel.control.history.lock().unwrap().test_live());
        assert!(fixture.events().is_empty());
        fixture.dispose_uncertain(&mut session);
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn removing_original_context_ranges_cannot_preserve_the_history_epoch() {
        use x11rb::protocol::record::ConnectionExt as _;
        let fixture = Fixture::new();
        let mut session = fixture.session();
        let control = &session.keeper.channel.control;
        let context = control.history.lock().unwrap().test_context();
        let client = control.connection.setup().resource_id_base;
        fixture.endpoint.transaction(deadline(), || {
            checked(checked(fixture.endpoint.connection.record_unregister_clients(context, &[client]))?.check())
        }).unwrap();
        assert!(session.keeper.check(deadline()).is_err());
        assert!(session.keeper.check(deadline()).is_err());
        // No key was held; independent zero-state inspection still works.
        assert!(session.finish());
        assert!(fixture.events().is_empty());
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn disabled_recreated_context_cannot_revive_the_original_stream() {
        use x11rb::protocol::record::{self, ConnectionExt as _};
        let fixture = Fixture::new();
        let mut session = fixture.session();
        let context = session.keeper.channel.control.history.lock().unwrap().test_context();
        let control = &session.keeper.channel.control;
        control.transaction(deadline(), || {
            let connection = &control.connection;
            checked(checked(connection.record_disable_context(context))?.check())?;
            checked(checked(connection.record_free_context(context))?.check())?;
            let range = record::Range {
                core_requests: record::Range8 {
                    first: xproto::GET_INPUT_FOCUS_REQUEST, last: xproto::GET_INPUT_FOCUS_REQUEST,
                },
                ..Default::default()
            };
            checked(checked(connection.record_create_context(context, 4, &[3], &[range]))?.check())
        }).unwrap();
        assert!(session.keeper.check(deadline()).is_err());
        assert!(session.keeper.check(deadline()).is_err());
        assert!(session.finish());
        assert!(fixture.events().is_empty());
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn lost_input_metadata_stops_input_but_independent_original_release_still_works() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        session.worker.begin(deadline()).unwrap();
        session.worker.edge(0, fixture.key, true).unwrap();
        let old = session.worker.input.history.lock().unwrap().test_stop_handle();
        old.stop();
        assert!(session.worker.edge(1, fixture.key, false).is_err());
        assert_eq!(fixture.events(), [(fixture.target, true)]);
        // Its independently retained witness is continuous, so the original
        // release owner may safely quiesce without sending release to target.
        session.keeper.quiesce().unwrap();
        fixture.endpoint.transaction(deadline(), || NativeChannel::clear_keys(&fixture.endpoint, &session.pair))
            .unwrap();
        let fresh = endpoint();
        old.stop();
        fresh.transaction(deadline(), || fresh.check_history()).unwrap();
        assert!(session.keeper.dispose_fixture_pair());
        assert!(fixture.events().is_empty());
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn foreign_focus_markers_are_excluded_but_future_mutations_remain_observed() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        let future = endpoint();
        future.transaction(deadline(), || {
            for _ in 0..32 {
                checked(checked(future.connection.get_input_focus())?.reply())?;
            }
            Ok(())
        }).unwrap();
        // A foreign focus frame would be refused by the strict original-client
        // parser. Success positively verifies the server-side private filter.
        session.keeper.check(deadline()).unwrap();
        fixture.silent_map_restore(&session, &future);
        assert!(session.keeper.check(deadline()).is_err());
        assert!(session.finish());
        assert!(fixture.events().is_empty());
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn native_device_keys_reach_only_original_target_and_cleanup_preserves_app() {
        let fixture = Fixture::new();
        let session = fixture.session();
        let receipt = session.run(&[vec![fixture.key]], deadline());
        assert!(receipt.dispatched && receipt.acknowledged && receipt.cleanup_confirmed, "{receipt:?}");
        assert_eq!(fixture.events(), [(fixture.target, true), (fixture.target, false)]);
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn destruction_and_same_xid_recreation_never_retarget() {
        let fixture = Fixture::new();
        let session = fixture.session();
        fixture
            .endpoint
            .transaction(deadline(), || {
                let connection = &fixture.endpoint.connection;
                checked(checked(connection.destroy_window(fixture.target))?.check())?;
                checked(
                    checked(
                        connection.create_window(
                            x11rb::COPY_DEPTH_FROM_PARENT,
                            fixture.target,
                            fixture.endpoint.root,
                            0,
                            0,
                            200,
                            100,
                            0,
                            xproto::WindowClass::INPUT_OUTPUT,
                            0,
                            &xproto::CreateWindowAux::new()
                                .event_mask(xproto::EventMask::KEY_PRESS | xproto::EventMask::KEY_RELEASE),
                        ),
                    )?
                    .check(),
                )?;
                checked(checked(connection.map_window(fixture.target))?.check())?;
                Ok(())
            })
            .unwrap();
        let receipt = session.run(&[vec![fixture.key]], deadline());
        assert!(!receipt.dispatched && !receipt.acknowledged && receipt.cleanup_confirmed, "{receipt:?}");
        assert!(fixture.events().is_empty());
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn selection_theft_and_restoration_stay_terminal() {
        let fixture = Fixture::new();
        let session = fixture.session();
        fixture
            .endpoint
            .transaction(deadline(), || {
                for owner in [x11rb::NONE, fixture.token] {
                    checked(
                        checked(fixture.endpoint.connection.set_selection_owner(
                            owner,
                            fixture.selection,
                            x11rb::CURRENT_TIME,
                        ))?
                        .check(),
                    )?;
                }
                Ok(())
            })
            .unwrap();
        let receipt = session.run(&[vec![fixture.key]], deadline());
        assert!(!receipt.dispatched && !receipt.acknowledged && receipt.cleanup_confirmed, "{receipt:?}");
        assert!(fixture.events().is_empty());
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn cancelled_input_transport_gets_independent_cleanup() {
        let fixture = Fixture::new();
        let session = fixture.session();
        session.stop_handle().stop();
        let receipt = session.run(&[vec![fixture.key]], deadline());
        assert!(!receipt.dispatched && !receipt.acknowledged && receipt.cleanup_confirmed, "{receipt:?}");
        assert!(fixture.events().is_empty());
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn held_private_key_is_removed_by_independent_connection_after_input_loss() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        session
            .input
            .transaction(deadline(), || {
                let info =
                    checked(session.input.connection.extension_information(xinput::X11_EXTENSION_NAME))?.unwrap();
                checked(
                    checked(session.input.connection.xtest_fake_input(
                        info.first_event + xinput::DEVICE_KEY_PRESS_EVENT,
                        fixture.key,
                        0,
                        session.input.root,
                        0,
                        0,
                        session.pair.slave,
                    ))?
                    .check(),
                )
            })
            .unwrap();
        session.stop_handle().stop();
        assert!(session.cleanup());
        assert_eq!(fixture.events().first(), Some(&(fixture.target, true)));
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn dropping_admitted_session_cleans_only_its_owned_resources() {
        let fixture = Fixture::new();
        drop(fixture.session());
        assert!(fixture.events().is_empty());
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn private_repeat_change_and_restoration_do_not_resurrect_admission() {
        for slave in [false, true] {
            let fixture = Fixture::new();
            let mut session = fixture.session();
            fixture.endpoint.transaction(deadline(), || {
                let connection = &fixture.endpoint.connection;
                checked(checked(connection.xkb_use_extension(1, 0))?.reply())?;
                let device = if slave { session.pair.slave } else { session.pair.keyboard };
                checked(checked(connection.xkb_set_controls(
                    u16::from(device),
                    0u8.into(), 0u8.into(), 0u8.into(), 0u8.into(),
                    0u16.into(), 0u16.into(), 0u16.into(), 0u16.into(), 0, 0, 0u16.into(),
                    xkb::BoolCtrl::REPEAT_KEYS, xkb::BoolCtrl::REPEAT_KEYS, xkb::Control::CONTROLS_ENABLED,
                    0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
                    0u32.into(), 0u32.into(), 0u16.into(), 0u16.into(), &[0; 32],
                ))?.check())?;
                NativeRepeatGuard::configure(&fixture.endpoint, &session.pair)?;
                session.pair.check(&fixture.endpoint)
            }).unwrap();
            assert!(session.keeper.check(deadline()).is_err());
            assert!(!session.keeper.channel.epoch.input_safe);
            assert!(session.keeper.channel.epoch.pair_good);
            assert!(session.keeper.check(deadline()).is_err());
            let receipt = session.run(&[vec![fixture.key]], deadline());
            assert!(!receipt.dispatched && !receipt.acknowledged && receipt.cleanup_confirmed, "{receipt:?}");
            assert!(fixture.events().is_empty());
            fixture.remains();
        }
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn competing_admission_never_cleans_or_replaces_existing_pair() {
        let fixture = Fixture::new();
        let session = fixture.session();
        let competing = NativeKeyboard::admit(
            endpoint(),
            endpoint(),
            Binding {
                window: fixture.target,
                pid: std::process::id(),
                lease_window: fixture.token,
                lease_pid: std::process::id(),
            },
            deadline(),
        );
        assert!(competing.is_err());
        let receipt = session.run(&[vec![fixture.key]], deadline());
        assert!(receipt.acknowledged && receipt.cleanup_confirmed, "{receipt:?}");
        assert_eq!(fixture.events(), [(fixture.target, true), (fixture.target, false)]);
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn late_old_stop_cannot_cancel_a_new_native_action() {
        let fixture = Fixture::new();
        let mut first = fixture.session();
        let old = first.stop_handle();
        let receipt = first.dispatch(&[vec![fixture.key]], deadline());
        assert!(receipt.acknowledged && !receipt.cleanup_confirmed, "{receipt:?}");
        let repeat = first.dispatch(&[vec![fixture.key]], deadline());
        assert!(!repeat.acknowledged && !repeat.dispatched);
        assert!(first.finish());
        fixture.events();
        let second = fixture.session();
        old.stop();
        let receipt = second.run(&[vec![fixture.key]], deadline());
        assert!(receipt.acknowledged && receipt.cleanup_confirmed, "{receipt:?}");
        assert_eq!(fixture.events(), [(fixture.target, true), (fixture.target, false)]);
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn forged_destroy_event_cannot_attest_owned_client_cleanup() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        let pair = session.pair.clone();
        session.keeper.action_watch().controller = Some((session.input_token, false));
        fixture
            .endpoint
            .transaction(deadline(), || {
                let token = session.input_token;
                let event = xproto::DestroyNotifyEvent {
                    response_type: xproto::DESTROY_NOTIFY_EVENT,
                    sequence: 0,
                    event: token,
                    window: token,
                };
                checked(
                    checked(fixture.endpoint.connection.send_event(
                        false,
                        token,
                        xproto::EventMask::STRUCTURE_NOTIFY,
                        event,
                    ))?
                    .check(),
                )
            })
            .unwrap();
        assert!(session.keeper.check(deadline()).is_err());
        assert!(!session.keeper.channel.epoch.pair_good);
        assert!(matches!(session.keeper.action_watch().controller, Some((_, false))));
        let receipt = session.run(&[vec![fixture.key]], deadline());
        assert!(!receipt.dispatched && !receipt.acknowledged && !receipt.cleanup_confirmed, "{receipt:?}");
        assert!(fixture.events().is_empty());
        fixture
            .endpoint
            .transaction(deadline(), || {
                pair.check(&fixture.endpoint)?;
                let remove = xinput::HierarchyChange {
                    len: 3,
                    data: xinput::HierarchyChangeData::RemoveMaster(xinput::HierarchyChangeDataRemoveMaster {
                        deviceid: pair.pointer,
                        return_mode: xinput::ChangeMode::FLOAT,
                        return_pointer: 0,
                        return_keyboard: 0,
                    }),
                };
                checked(checked(fixture.endpoint.connection.xinput_xi_change_hierarchy(&[remove]))?.check())
            })
            .unwrap();
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn destroyed_observer_is_not_a_disconnected_input_client() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        let original = session.pair.clone();
        session.worker.begin(deadline()).unwrap();
        session.worker.edge(0, fixture.key, true).unwrap();
        fixture.endpoint.transaction(deadline(), || {
            checked(checked(fixture.endpoint.connection.destroy_window(session.input_token))?.check())?;
            assert_ne!(key_state(&fixture.endpoint, original.slave)?, [0; 32]);
            Ok(())
        }).unwrap();
        assert!(session.keeper.check(deadline()).is_err());
        // The original transport is deliberately still open. A genuine
        // DestroyNotify is not proof that its client or queued writer is gone.
        assert!(session.keeper.quiesce().is_err());
        fixture.endpoint.transaction(deadline(), || {
            original.check(&fixture.endpoint)?;
            assert_ne!(key_state(&fixture.endpoint, original.slave)?, [0; 32]);
            checked(checked(fixture.endpoint.connection.get_geometry(fixture.target))?.reply())?;
            Ok(())
        }).unwrap();
        assert!(session.worker.edge(1, fixture.key, false).is_err());
        session.worker.input.boundary.stop();
        wait_client_gone(&fixture.endpoint, session.input_token, std::process::id(), deadline()).unwrap();
        // Only the explicit disposable-server fixture disposes this uncertain
        // pair. Runtime Drop must neither retry it nor fabricate a clear receipt.
        fixture.endpoint.transaction(deadline(), || {
            let remove = xinput::HierarchyChange {
                len: 3,
                data: xinput::HierarchyChangeData::RemoveMaster(xinput::HierarchyChangeDataRemoveMaster {
                    deviceid: original.pointer, return_mode: xinput::ChangeMode::FLOAT,
                    return_pointer: 0, return_keyboard: 0,
                }),
            };
            checked(checked(fixture.endpoint.connection.xinput_xi_change_hierarchy(&[remove]))?.check())
        }).unwrap();
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn foreign_attachment_and_restoration_do_not_restore_the_channel_epoch() {
        let fixture = Fixture::new();
        let mut session = fixture.session();
        let original = session.pair.clone();
        fixture.endpoint.transaction(deadline(), || {
            let connection = &fixture.endpoint.connection;
            let devices = checked(checked(connection.xinput_xi_query_device(0u16))?.reply())?.infos;
            // This is the attested disposable Xvfb's synthetic keyboard, never
            // a physical device on an operator desktop. XTEST slaves cannot
            // be detached, so they cannot exercise this native ABA history.
            let candidates: Vec<_> = devices.iter().filter(|device| {
                device.type_ == xinput::DeviceType::SLAVE_KEYBOARD
                    && !device.name.ends_with(b"XTEST keyboard")
            }).collect();
            assert_eq!(candidates.len(), 1);
            let foreign = candidates[0];
            assert!(foreign.enabled && foreign.attachment > 1);
            assert_ne!(foreign.attachment, u16::from(original.keyboard));
            for master in [u16::from(original.keyboard), foreign.attachment] {
                let attach = xinput::HierarchyChange {
                    len: 2,
                    data: xinput::HierarchyChangeData::AttachSlave(xinput::HierarchyChangeDataAttachSlave {
                        deviceid: foreign.deviceid,
                        master,
                    }),
                };
                checked(checked(connection.xinput_xi_change_hierarchy(&[attach]))?.check())?;
            }
            // A fresh snapshot now passes. Only the continuously retained
            // native history can detect the intervening foreign membership.
            original.check(&fixture.endpoint)?;
            let restored = checked(checked(connection.xinput_xi_query_device(foreign.deviceid))?.reply())?.infos;
            assert_eq!(restored.len(), 1);
            assert_eq!(restored[0].name, foreign.name);
            assert_eq!(restored[0].attachment, foreign.attachment);
            assert_eq!(restored[0].enabled, foreign.enabled);
            Ok(())
        }).unwrap();
        assert!(session.keeper.check(deadline()).is_err());
        assert!(!session.keeper.channel.epoch.pair_good);
        let receipt = session.run(&[vec![fixture.key]], deadline());
        assert!(!receipt.dispatched && !receipt.acknowledged && !receipt.cleanup_confirmed, "{receipt:?}");
        assert!(fixture.events().is_empty());
        // The runtime refuses mutating cleanup once resource history is lost.
        // This fixture still owns the disposable server and its original pair.
        fixture.endpoint.transaction(deadline(), || {
            let remove = xinput::HierarchyChange {
                len: 3,
                data: xinput::HierarchyChangeData::RemoveMaster(xinput::HierarchyChangeDataRemoveMaster {
                    deviceid: original.pointer,
                    return_mode: xinput::ChangeMode::FLOAT,
                    return_pointer: 0,
                    return_keyboard: 0,
                }),
            };
            checked(checked(fixture.endpoint.connection.xinput_xi_change_hierarchy(&[remove]))?.check())
        }).unwrap();
        fixture.remains();
    }

    #[test]
    #[ignore = "explicit disposable Xvfb with attested PID and private cookie only"]
    fn same_device_ids_names_and_focus_do_not_restore_a_lost_pair_incarnation() {
        let fixture = Fixture::new();
        let session = fixture.session();
        let original = session.pair.clone();
        fixture
            .endpoint
            .transaction(deadline(), || {
                let connection = &fixture.endpoint.connection;
                let remove = xinput::HierarchyChange {
                    len: 3,
                    data: xinput::HierarchyChangeData::RemoveMaster(xinput::HierarchyChangeDataRemoveMaster {
                        deviceid: original.pointer,
                        return_mode: xinput::ChangeMode::FLOAT,
                        return_pointer: 0,
                        return_keyboard: 0,
                    }),
                };
                checked(checked(connection.xinput_xi_change_hierarchy(&[remove]))?.check())?;
                let create = xinput::HierarchyChange {
                    len: (2 + original.name.len().div_ceil(4)) as u16,
                    data: xinput::HierarchyChangeData::AddMaster(xinput::HierarchyChangeDataAddMaster {
                        send_core: true,
                        enable: true,
                        name: original.name.clone(),
                    }),
                };
                checked(checked(connection.xinput_xi_change_hierarchy(&[create]))?.check())?;
                // The selected Xvfb must actually reuse all four IDs: a different
                // ID or current-snapshot mismatch would not test the ABA fence.
                original.check(&fixture.endpoint)?;
                checked(
                    checked(connection.xinput_set_device_focus(
                        fixture.target,
                        x11rb::CURRENT_TIME,
                        xproto::InputFocus::NONE,
                        original.keyboard,
                    ))?
                    .check(),
                )?;
                Ok(())
            })
            .unwrap();
        let receipt = session.run(&[vec![fixture.key]], deadline());
        assert!(!receipt.dispatched && !receipt.acknowledged && !receipt.cleanup_confirmed, "{receipt:?}");
        assert!(fixture.events().is_empty());
        // Unknown cleanup must leave the replacement untouched. The fixture
        // that created that new pair, not the old session, removes it now.
        fixture
            .endpoint
            .transaction(deadline(), || {
                original.check(&fixture.endpoint)?;
                let remove = xinput::HierarchyChange {
                    len: 3,
                    data: xinput::HierarchyChangeData::RemoveMaster(xinput::HierarchyChangeDataRemoveMaster {
                        deviceid: original.pointer,
                        return_mode: xinput::ChangeMode::FLOAT,
                        return_pointer: 0,
                        return_keyboard: 0,
                    }),
                };
                checked(checked(fixture.endpoint.connection.xinput_xi_change_hierarchy(&[remove]))?.check())
            })
            .unwrap();
        fixture.remains();
    }
}

#[cfg(test)]
#[path = "mintclaw_native_keyboard_qualification.rs"]
mod qualification;
