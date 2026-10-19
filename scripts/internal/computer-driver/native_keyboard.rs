//! Private native keyboard engine under construction; not an admitted host.
//!
//! One action owns one MPX pair and two original transports. Only the input
//! transport issues key events. The retained control transport kills that
//! exact input client and removes only the original pair during cleanup.
//! The caller must keep its host desktop lease until cleanup is confirmed.
//! Neither protocol acknowledgement nor removal proves an application effect.

use std::io::{self, ErrorKind};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::os::unix::fs::MetadataExt;
use std::os::unix::net::UnixStream;
use std::time::{Duration, Instant};
use zeroize::Zeroize;

use x11rb::connection::{Connection, RequestConnection};
use x11rb::protocol::res::ConnectionExt as _;
use x11rb::protocol::xfixes::ConnectionExt as _;
use x11rb::protocol::xinput::ConnectionExt as _;
use x11rb::protocol::xproto::{self, ConnectionExt as _};
use x11rb::protocol::xtest::ConnectionExt as _;
use x11rb::protocol::{Event, res, xfixes, xinput};
use x11rb::rust_connection::{DefaultStream, RustConnection};

use super::mintclaw_native_x11_io::{IoBoundary, NativeStream};

type Result<T> = io::Result<T>;
const PREFIX: &[u8] = b"MintClaw private input ";
const SELECTION: &[u8] = b"_MINTCLAW_COMPUTER_INPUT_CONTROL";
const TRANSACTION: Duration = Duration::from_millis(250);
#[cfg(test)]
const MAX_CHORDS: usize = 256;

fn unavailable() -> io::Error {
    io::Error::new(ErrorKind::ConnectionAborted, "native keyboard authority unavailable")
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
pub(super) struct Endpoint {
    connection: RustConnection<NativeStream>,
    boundary: IoBoundary,
    root: u32,
    peer: (i32, u32),
}

impl Endpoint {
    pub(super) fn from_socket(
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

struct Process(OwnedFd);

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
        let process = Self(unsafe { OwnedFd::from_raw_fd(fd as i32) });
        let metadata = std::fs::metadata(format!("/proc/{pid}"))?;
        if metadata.uid() != unsafe { libc::getuid() } || !process.alive() {
            return Err(unavailable());
        }
        Ok(process)
    }

    fn alive(&self) -> bool {
        let mut fd = libc::pollfd { fd: self.0.as_raw_fd(), events: libc::POLLIN, revents: 0 };
        unsafe { libc::poll(&mut fd, 1, 0) == 0 }
    }
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
    fn create(endpoint: &Endpoint) -> Result<Self> {
        let connection = &endpoint.connection;
        checked(checked(connection.xinput_xi_query_version(2, 0))?.reply())?;
        checked(checked(connection.xtest_get_version(2, 2))?.reply())?;
        let before = checked(checked(connection.xinput_xi_query_device(0u16))?.reply())?.infos;
        // Refuse orphaned resources. A name is only a busy fence, NEVER
        // authority to delete a device from an old/unknown invocation.
        if before.iter().any(|device| device.name.starts_with(PREFIX)) {
            return Err(io::Error::new(ErrorKind::WouldBlock, "native input cleanup pending"));
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

struct Watch {
    target_good: bool,
    pair_good: bool,
    input_gone: bool,
}

impl Watch {
    fn drain(
        &mut self,
        endpoint: &Endpoint,
        authority: &Authority,
        pair: &Pair,
        input_token: u32,
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
            // A client-forged DestroyNotify cannot attest that the original
            // input client is gone (and thereby skip the KillClient fence).
            // response_type() deliberately masks this protocol flag in x11rb.
            if event.raw_response_type() & 0x80 != 0 {
                self.target_good = false;
                self.pair_good = false;
                continue;
            }
            match event {
                Event::DestroyNotify(event) => {
                    if [authority.binding.window, authority.binding.lease_window].contains(&event.window) {
                        self.target_good = false;
                    }
                    if event.window == input_token {
                        self.input_gone = true;
                    }
                }
                Event::UnmapNotify(event) if event.window == authority.binding.window => self.target_good = false,
                Event::XfixesSelectionNotify(event) if event.selection == authority.selection => {
                    self.target_good = false
                }
                Event::XinputHierarchy(event) => {
                    for info in
                        event.infos.iter().filter(|info| pair.devices.iter().any(|own| own.deviceid == info.deviceid))
                    {
                        let allowed = xinput::HierarchyMask::MASTER_ADDED
                            | xinput::HierarchyMask::SLAVE_ADDED
                            | xinput::HierarchyMask::DEVICE_ENABLED
                            | xinput::HierarchyMask::SLAVE_ATTACHED;
                        if u32::from(info.flags) != 0
                            && (!allow_creation || u32::from(info.flags) & !u32::from(allowed) != 0)
                        {
                            self.pair_good = false;
                        }
                    }
                }
                _ => (),
            }
        }
        Err(unavailable())
    }
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

impl Endpoint {
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
        let authority = at(
            "native control preparation",
            control.transaction(deadline, || {
                let observer = observer(&control)?;
                Authority::subscribe(&control, binding, observer)
            }),
        )?;
        Ok(Self { control, authority })
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
            control: self.control,
            authority: self.authority,
            pair,
            input_token: identity.input_token,
            _input_process: input_process,
            watch: Watch { target_good: true, pair_good: true, input_gone: false },
            cleanup_result: None,
        };
        keeper.control.transaction(deadline, || {
            keeper.authority.check(&keeper.control)?;
            keeper.pair.check(&keeper.control)?;
            keeper.watch.drain(&keeper.control, &keeper.authority, &keeper.pair, keeper.input_token, true)?;
            if !keeper.watch.target_good || !keeper.watch.pair_good {
                return Err(unavailable());
            }
            checked(
                checked(keeper.control.connection.xinput_set_device_focus(
                    keeper.authority.binding.window,
                    x11rb::CURRENT_TIME,
                    xproto::InputFocus::NONE,
                    keeper.pair.keyboard,
                ))?
                .check(),
            )
        })?;
        Ok(keeper)
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
        let mut watch = Watch { target_good: true, pair_good: true, input_gone: false };
        let event_base = self.input.transaction(deadline, || {
            self.authority.check(&self.input)?;
            pair.check(&self.input)?;
            watch.drain(&self.input, &self.authority, &pair, self.input_token, true)?;
            if !watch.target_good || !watch.pair_good {
                return Err(unavailable());
            }
            let extension = checked(self.input.connection.extension_information(xinput::X11_EXTENSION_NAME))?
                .ok_or_else(unavailable)?;
            extension.first_event.checked_add(xinput::DEVICE_KEY_RELEASE_EVENT).ok_or_else(unavailable)?;
            Ok(extension.first_event)
        })?;
        Ok(InputWorker {
            input: self.input,
            input_authority: self.authority,
            pair,
            input_token: self.input_token,
            input_watch: watch,
            phase: InputPhase::Fresh,
            attempted: false,
            event_base,
        })
    }
}

/// This owner must remain outside the killable input process and keep the host
/// desktop lease alive until cleanup is independently confirmed. It never types.
pub(super) struct Keeper {
    control: Endpoint,
    authority: Authority,
    pair: Pair,
    input_token: u32,
    _input_process: Process,
    watch: Watch,
    cleanup_result: Option<bool>,
}

impl Keeper {
    pub(super) fn witness(&self) -> PairWitness {
        self.pair.witness()
    }
    pub(super) fn keyboard(&self) -> u8 {
        self.pair.keyboard
    }

    pub(super) fn check(&mut self, deadline: Instant) -> Result<()> {
        self.control.transaction(deadline, || {
            self.authority.check(&self.control)?;
            self.pair.check(&self.control)?;
            self.watch.drain(&self.control, &self.authority, &self.pair, self.input_token, false)?;
            if !self.watch.target_good || !self.watch.pair_good || self.watch.input_gone {
                return Err(unavailable());
            }
            Ok(())
        })
    }

    pub(super) fn cleanup(&mut self) -> bool {
        if let Some(result) = self.cleanup_result {
            return result;
        }
        self.cleanup_result = Some(false);
        let deadline = Instant::now() + TRANSACTION;
        let result = self
            .control
            .transaction(deadline, || {
                self.pair.check(&self.control)?;
                self.watch.drain(&self.control, &self.authority, &self.pair, self.input_token, false)?;
                if !self.watch.pair_good {
                    return Err(unavailable());
                }
                if !self.watch.input_gone {
                    let killed = checked(self.control.connection.kill_client(self.input_token))?.check();
                    checked(checked(self.control.connection.get_input_focus())?.reply())?;
                    self.watch.drain(&self.control, &self.authority, &self.pair, self.input_token, false)?;
                    if !self.watch.input_gone {
                        checked(killed)?;
                        return Err(unavailable());
                    }
                }
                checked(
                    checked(self.control.connection.xinput_set_device_focus(
                        x11rb::NONE,
                        x11rb::CURRENT_TIME,
                        xproto::InputFocus::NONE,
                        self.pair.keyboard,
                    ))?
                    .check(),
                )?;
                let remove = xinput::HierarchyChange {
                    len: 3,
                    data: xinput::HierarchyChangeData::RemoveMaster(xinput::HierarchyChangeDataRemoveMaster {
                        deviceid: self.pair.pointer,
                        return_mode: xinput::ChangeMode::FLOAT,
                        return_pointer: 0,
                        return_keyboard: 0,
                    }),
                };
                checked(checked(self.control.connection.xinput_xi_change_hierarchy(&[remove]))?.check())?;
                let devices = checked(checked(self.control.connection.xinput_xi_query_device(0u16))?.reply())?.infos;
                if devices.iter().any(|device| {
                    self.pair.devices.iter().any(|own| own.deviceid == device.deviceid)
                        || device.name.starts_with(&self.pair.name)
                }) {
                    return Err(unavailable());
                }
                Ok(())
            })
            .is_ok();
        self.cleanup_result = Some(result);
        result
    }
}

impl Drop for Keeper {
    fn drop(&mut self) {
        self.cleanup();
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
    input_watch: Watch,
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
            let focus = checked(checked(self.input.connection.xinput_get_device_focus(self.pair.keyboard))?.reply())?;
            self.input_watch.drain(&self.input, &self.input_authority, &self.pair, self.input_token, false)?;
            if !self.input_watch.target_good
                || !self.input_watch.pair_good
                || focus.focus != self.input_authority.binding.window
                || focus.revert_to != xproto::InputFocus::NONE
            {
                return Err(unavailable());
            }
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
        self.keeper.cleanup()
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
    use super::*;

    pub(super) fn deadline() -> Instant {
        Instant::now() + Duration::from_secs(3)
    }

    pub(super) fn endpoint() -> Endpoint {
        assert_eq!(std::env::var("MINTCLAW_NATIVE_KEYBOARD_NATIVE").unwrap(), "1");
        let number = std::env::var("MINTCLAW_PRIVATE_X_DISPLAY_NUMBER").unwrap();
        assert!(!number.is_empty() && number.bytes().all(|byte| byte.is_ascii_digit()));
        let socket = UnixStream::connect(format!("/tmp/.X11-unix/X{number}")).unwrap();
        let cookie = std::env::var("MINTCLAW_PRIVATE_X_COOKIE").unwrap();
        assert_eq!(cookie.len(), 32);
        let cookie =
            (0..32).step_by(2).map(|index| u8::from_str_radix(&cookie[index..index + 2], 16).unwrap()).collect();
        let endpoint = Endpoint::from_socket(socket, 0, b"MIT-MAGIC-COOKIE-1".to_vec(), cookie, deadline()).unwrap();
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
        let session = fixture.session();
        let pair = session.pair.clone();
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
