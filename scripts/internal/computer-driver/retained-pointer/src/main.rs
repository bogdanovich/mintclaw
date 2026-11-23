use std::io::{self, BufRead, ErrorKind, IoSlice, Read, Write};
use std::os::fd::{AsFd, AsRawFd};
use std::os::unix::fs::OpenOptionsExt;
use std::os::unix::net::UnixStream;
use std::time::{Duration, Instant};
use x11rb::connection::{Connection, RequestConnection};
use x11rb::protocol::record::{self, ConnectionExt as _};
use x11rb::protocol::res::{self, ConnectionExt as _};
use x11rb::protocol::xinput::{self, ConnectionExt as _};
use x11rb::protocol::xproto::{self, ConnectionExt as _};
use x11rb::protocol::xtest::{self, ConnectionExt as _};
use x11rb::rust_connection::{DefaultStream, PollMode, RustConnection, Stream};
use x11rb::x11_utils::TryParse;
use zeroize::Zeroizing;
mod actor;
mod evidence;
mod pointer;
mod recovery;
mod selections;
mod startup;
mod x11_io;

type X11 = RustConnection<x11_io::NativeStream>;
const PROTOCOL_VERSION: u32 = 1;
const PHASE: Duration = Duration::from_millis(250);
const MAX_AUTHORITY_BYTES: u64 = 256;

fn fail() -> io::Error {
    io::Error::new(
        ErrorKind::PermissionDenied,
        "retained X11 authority unavailable",
    )
}

fn checked<T, E>(value: Result<T, E>) -> io::Result<T> {
    value.map_err(|_| fail())
}

fn value<T: std::str::FromStr>(value: Option<String>) -> io::Result<T> {
    checked(value.ok_or_else(fail)?.parse())
}

fn cookie(mut bytes: &[u8], number: &str) -> io::Result<Vec<u8>> {
    fn field<'a>(bytes: &mut &'a [u8]) -> io::Result<&'a [u8]> {
        let packet = bytes.get(..2).ok_or_else(fail)?;
        let length = usize::from(u16::from_be_bytes([packet[0], packet[1]]));
        let result = bytes.get(2..2 + length).ok_or_else(fail)?;
        *bytes = &bytes[2 + length..];
        Ok(result)
    }
    if bytes.get(..2) != Some(&[0xff, 0xff]) {
        return Err(fail());
    }
    bytes = &bytes[2..];
    if !field(&mut bytes)?.is_empty()
        || field(&mut bytes)? != number.as_bytes()
        || field(&mut bytes)? != b"MIT-MAGIC-COOKIE-1"
    {
        return Err(fail());
    }
    let result = field(&mut bytes)?;
    if !bytes.is_empty() || result.len() != 16 {
        return Err(fail());
    }
    Ok(result.to_vec())
}

fn open() -> io::Result<(X11, x11_io::IoBoundary)> {
    let display = checked(std::env::var("DISPLAY"))?;
    let number = display.strip_prefix(':').ok_or_else(fail)?;
    if number.is_empty() || !number.bytes().all(|b| b.is_ascii_digit()) {
        return Err(fail());
    }
    let approved: i32 = value(std::env::var("MINTCLAW_PRIVATE_XVFB_PID").ok())?;
    let unix = UnixStream::connect(format!("/tmp/.X11-unix/X{number}"))?;
    let mut peer: libc::ucred = unsafe { std::mem::zeroed() };
    let mut length = std::mem::size_of_val(&peer) as libc::socklen_t;
    if unsafe {
        libc::getsockopt(
            unix.as_raw_fd(),
            libc::SOL_SOCKET,
            libc::SO_PEERCRED,
            (&mut peer as *mut libc::ucred).cast(),
            &mut length,
        )
    } != 0
        || peer.pid != approved
        || peer.uid != unsafe { libc::geteuid() }
        || approved <= 1
    {
        return Err(fail());
    }
    let authority_path = checked(std::env::var("XAUTHORITY"))?;
    let authority_file = std::fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(authority_path)?;
    if !authority_file.metadata()?.is_file() {
        return Err(fail());
    }
    let mut authority = Zeroizing::new(Vec::new());
    authority_file
        .take(MAX_AUTHORITY_BYTES + 1)
        .read_to_end(&mut authority)?;
    if authority.len() as u64 > MAX_AUTHORITY_BYTES {
        return Err(fail());
    }
    let (stream, _) = DefaultStream::from_unix_stream(unix)?;
    let (stream, lifetime) = x11_io::NativeStream::new(stream);
    let phase = lifetime.begin(Instant::now() + PHASE)?;
    let conn = checked(RustConnection::connect_to_stream_with_auth_info(
        stream,
        0,
        b"MIT-MAGIC-COOKIE-1".to_vec(),
        cookie(&authority, number)?,
    ))?;
    phase.finish()?;
    Ok((conn, lifetime))
}

struct Wire<'a>(&'a X11);

impl Read for Wire<'_> {
    fn read(&mut self, buffer: &mut [u8]) -> io::Result<usize> {
        loop {
            let mut fds = Vec::new();
            let got = self.0.stream().read(buffer, &mut fds);
            if !fds.is_empty() {
                return Err(fail());
            }
            match got {
                Err(e) if e.kind() == ErrorKind::WouldBlock => {
                    self.0.stream().poll(PollMode::Readable)?
                }
                result => return result,
            }
        }
    }
}

fn packet(wire: &mut Wire<'_>, sequence: u16) -> io::Result<record::EnableContextReply> {
    let mut header = [0u8; 32];
    wire.read_exact(&mut header)?;
    if header[0] != 1 || word16(&header, 2) != sequence {
        return Err(fail());
    }
    let words = word32(&header, 4) as usize;
    if words > 16384 {
        return Err(fail());
    }
    let mut packet = vec![0; 32 + words * 4];
    packet[..32].copy_from_slice(&header);
    wire.read_exact(&mut packet[32..])?;
    let (reply, rest) = checked(record::EnableContextReply::try_parse(&packet))?;
    if !rest.is_empty() || reply.client_swapped || reply.element_header != 4 {
        return Err(fail());
    }
    Ok(reply)
}

fn word16(bytes: &[u8], at: usize) -> u16 {
    u16::from_ne_bytes(bytes[at..at + 2].try_into().unwrap())
}
fn word32(bytes: &[u8], at: usize) -> u32 {
    u32::from_ne_bytes(bytes[at..at + 4].try_into().unwrap())
}

fn xres_pid(connection: &X11, owner: u32) -> io::Result<u32> {
    let reply = checked(
        checked(connection.res_query_client_ids(&[res::ClientIdSpec {
            client: owner,
            mask: res::ClientIdMask::LOCAL_CLIENT_PID,
        }]))?
        .reply(),
    )?;
    if reply.ids.len() != 1 || reply.ids[0].value.len() != 1 {
        return Err(fail());
    }
    Ok(reply.ids[0].value[0])
}

fn clean_boot(conn: &X11, data: &X11, actor_base: u32) -> io::Result<()> {
    let reply = checked(checked(conn.res_query_clients())?.reply())?;
    let seen = reply
        .clients
        .iter()
        .filter(|c| c.resource_base != 0)
        .map(|c| c.resource_base)
        .collect::<std::collections::BTreeSet<_>>();
    let expected = [
        conn.setup().resource_id_base,
        data.setup().resource_id_base,
        actor_base,
    ]
    .into_iter()
    .collect();
    if seen != expected {
        return Err(io::Error::other("other X11 clients at recorder bootstrap"));
    }
    Ok(())
}

fn require_worker_retired(conn: &X11, worker_client: u32) -> io::Result<()> {
    let clients = checked(checked(conn.res_query_clients())?.reply())?;
    if clients
        .clients
        .iter()
        .any(|client| client.resource_base == worker_client)
    {
        return Err(io::Error::other(
            "terminated worker still has an X11 client",
        ));
    }
    Ok(())
}

fn range(first: u8, last: u8) -> record::Range {
    record::Range {
        core_requests: record::Range8 { first, last },
        ..Default::default()
    }
}

fn ext_range(major: u8, first: u8, last: u8) -> record::Range {
    record::Range {
        ext_requests: record::ExtRange {
            major: record::Range8 {
                first: major,
                last: major,
            },
            minor: record::Range16 {
                first: first.into(),
                last: last.into(),
            },
        },
        ..Default::default()
    }
}

fn guarded_scope(
    conn: &X11,
    original: u32,
    app_pid: u32,
    lease: u32,
    lease_pid: u32,
    private: pointer::Pointer,
) -> io::Result<()> {
    let root = conn.setup().roots[0].root;
    let control =
        checked(checked(conn.intern_atom(true, b"_MINTCLAW_COMPUTER_INPUT_CONTROL"))?.reply())?
            .atom;
    if control == 0
        || checked(checked(conn.get_selection_owner(control))?.reply())?.owner != lease
        || xres_pid(conn, original)? != app_pid
        || xres_pid(conn, lease)? != lease_pid
    {
        return Err(fail());
    }
    let view = checked(checked(conn.get_window_attributes(original))?.reply())?;
    if view.map_state != xproto::MapState::VIEWABLE {
        return Err(fail());
    }
    checked(checked(conn.xinput_xi_query_version(2, 0))?.reply())?;
    let resources = checked(checked(conn.xinput_xi_query_device(0u16))?.reply())?.infos;
    let mut sole = resources.iter().filter(|device| {
        device.type_ == xinput::DeviceType::SLAVE_POINTER && device.attachment == private.master
    });
    let source = sole.next().ok_or_else(fail)?;
    if source.deviceid != u16::from(private.source) || !source.enabled || sole.next().is_some() {
        return Err(fail());
    }
    if checked(checked(conn.xinput_get_device_button_mapping(private.source))?.reply())?
        .map
        .get(4)
        != Some(&5)
    {
        return Err(fail());
    }
    let mut current = root;
    let mut belongs = false;
    for _ in 0..32 {
        if current == original {
            belongs = true;
        }
        if belongs && xres_pid(conn, current)? != app_pid {
            return Err(fail());
        }
        let location =
            checked(checked(conn.xinput_xi_query_pointer(current, private.master))?.reply())?;
        if !location.same_screen
            || location.root != root
            || location.buttons.first() != Some(&(1 << 5))
        {
            return Err(fail());
        }
        if location.child == 0 && belongs {
            return Ok(());
        }
        if location.child == 0 || location.child == current {
            return Err(fail());
        }
        current = location.child;
    }
    Err(fail())
}

fn fence(conn: &X11) -> io::Result<u32> {
    let marker = checked(conn.get_input_focus())?;
    let sequence = marker.sequence_number() as u32;
    checked(marker.reply())?;
    Ok(sequence)
}

fn refuse_hierarchy(
    protocol: selections::Protocol,
    event: &evidence::Observation<'_>,
) -> io::Result<()> {
    if let evidence::Observation::Request { bytes, .. } = event {
        if bytes.first() == Some(&protocol.xinput_opcode)
            && bytes.get(1) == Some(&xinput::XI_CHANGE_HIERARCHY_REQUEST)
        {
            return Err(io::Error::other("owned input device hierarchy changed"));
        }
    }
    Ok(())
}

fn observe_registration(
    ledger: &mut selections::PointerSelections,
    protocol: selections::Protocol,
    context: u32,
    event: evidence::Observation<'_>,
) -> io::Result<()> {
    if let evidence::Observation::Request { bytes, .. } = event {
        let o = (bytes[0], bytes[1]);
        let relevant = matches!(
            o.0,
            xproto::CREATE_WINDOW_REQUEST | xproto::CHANGE_WINDOW_ATTRIBUTES_REQUEST
        ) || o.0 == protocol.record_opcode
            && matches!(
                o.1,
                record::REGISTER_CLIENTS_REQUEST | record::UNREGISTER_CLIENTS_REQUEST
            )
            || o.0 == protocol.xinput_opcode
                && matches!(
                    o.1,
                    xinput::XI_SELECT_EVENTS_REQUEST | xinput::SELECT_EXTENSION_EVENT_REQUEST
                )
            || o == (protocol.xtest_opcode, xtest::GRAB_CONTROL_REQUEST);
        if !relevant {
            return Ok(());
        }
    }
    ledger.observe(protocol, context, event)
}

fn read_to(
    wire: &mut Wire<'_>,
    sequence: u16,
    conn: &X11,
    master: u16,
    xi: u8,
    check: impl FnMut(evidence::Observation<'_>) -> io::Result<()>,
) -> io::Result<evidence::Evidence> {
    evidence::read_fence_observing(
        wire,
        evidence::Fence {
            reply_sequence: sequence,
            control_client: conn.setup().resource_id_base,
            marker_sequence: fence(conn)?,
            pointer_device: master,
            xinput_opcode: xi,
        },
        check,
    )
}
fn expected_receipt(root: u32, window: u32, client: u32, kind: u16) -> evidence::ButtonReceipt {
    evidence::ButtonReceipt {
        recipient: client,
        window,
        root,
        kind,
        detail: 5,
    }
}

fn button_request(
    protocol: selections::Protocol,
    root: u32,
    private: pointer::Pointer,
    kind: u8,
) -> io::Result<xtest::FakeInputRequest> {
    Ok(xtest::FakeInputRequest {
        type_: protocol
            .xinput_first_event
            .checked_add(kind)
            .ok_or_else(fail)?,
        detail: 5,
        time: x11rb::CURRENT_TIME,
        root,
        root_x: 0,
        root_y: 0,
        deviceid: private.source,
    })
}

fn emit(conn: &X11, request: xtest::FakeInputRequest) -> io::Result<u32> {
    let edge = checked(conn.xtest_fake_input(
        request.type_,
        request.detail,
        request.time,
        request.root,
        request.root_x,
        request.root_y,
        request.deviceid,
    ))?;
    u32::try_from(edge.sequence_number()).map_err(|_| fail())
}

#[derive(Default)]
struct Probe {
    id: u64,
    attempted: bool,
    verified: bool,
    recovered: bool,
}

fn report(probe: &Probe) {
    println!(
        "{{\"request_seq\":{},\"attempted\":{},\"dispatch_verified\":{},\"recovery_verified\":{}}}",
        probe.id, probe.attempted, probe.verified, probe.recovered
    );
}

fn next_line(input: &mut impl BufRead) -> io::Result<Option<String>> {
    let mut line = String::new();
    let count = input.take(513).read_line(&mut line)?;
    if count == 0 {
        return Ok(None);
    }
    if count > 512 || !line.ends_with('\n') {
        return Err(fail());
    }
    line.pop();
    Ok(Some(line))
}

fn accepted_cut_mode(mode: u8) -> bool {
    mode == 0 || (cfg!(feature = "fixture-fault-injection") && mode == 2)
}

struct NativeAction {
    id: u64,
    original: pointer::ObservedPoint,
    lease: u32,
    lease_pid: u32,
    cut_mode: u8,
}

impl NativeAction {
    fn parse(line: &str) -> io::Result<Self> {
        let mut args = line.split_whitespace().map(str::to_owned);
        let id = value(args.next())?;
        let original = pointer::ObservedPoint {
            window: value(args.next())?,
            owner_pid: value(args.next())?,
            left: value(args.next())?,
            top: value(args.next())?,
            width: value(args.next())?,
            height: value(args.next())?,
            x: value(args.next())?,
            y: value(args.next())?,
        };
        let lease = value(args.next())?;
        let lease_pid = value(args.next())?;
        let generation: u64 = value(args.next())?;
        let cut_mode = value(args.next())?;
        if args.next().is_some() || lease <= 1 || generation == 0 || !accepted_cut_mode(cut_mode) {
            return Err(fail());
        }
        Ok(Self {
            id,
            original,
            lease,
            lease_pid,
            cut_mode,
        })
    }
}

struct Keeper<'a> {
    connection: &'a X11,
    transaction: &'a x11_io::IoBoundary,
    recording: &'a x11_io::IoBoundary,
    wire: Wire<'a>,
    stream_sequence: u16,
    context: u32,
    protocol: selections::Protocol,
    owned: startup::OwnedPointer,
    recipients: selections::PointerSelections,
}

impl Keeper<'_> {
    /// Consume one bounded batch on the original RECORD stream. This
    /// checkpoint only projects routing metadata into the existing table.
    fn idle_checkpoint(&mut self) -> io::Result<()> {
        let deadline = Instant::now() + PHASE;
        let tx = self.transaction.begin(deadline)?;
        let rx = self.recording.begin(deadline)?;
        let evidence = read_to(
            &mut self.wire,
            self.stream_sequence,
            self.connection,
            self.owned.pointer.master,
            self.protocol.xinput_opcode,
            |event| {
                refuse_hierarchy(self.protocol, &event)?;
                observe_registration(&mut self.recipients, self.protocol, self.context, event)
            },
        )?;
        if !evidence.buttons.is_empty() {
            return Err(io::Error::other(
                "private pointer received input while idle",
            ));
        }
        rx.finish()?;
        tx.finish()
    }

    /// Preserve kernel backpressure without sending periodic traffic on a
    /// quiet display. A pending command goes through the normal action fence.
    fn await_command(&mut self, input: &io::BufReader<std::fs::File>) -> io::Result<()> {
        while input.buffer().is_empty() {
            let mut sockets = [
                libc::pollfd {
                    fd: input.get_ref().as_raw_fd(),
                    events: libc::POLLIN,
                    revents: 0,
                },
                libc::pollfd {
                    fd: self.recording.poll_fd(),
                    events: libc::POLLIN,
                    revents: 0,
                },
            ];
            let ready =
                unsafe { libc::poll(sockets.as_mut_ptr(), sockets.len() as libc::nfds_t, -1) };
            if ready < 0 {
                let err = io::Error::last_os_error();
                if err.kind() == ErrorKind::Interrupted {
                    continue;
                }
                return Err(err);
            }
            if sockets[0].revents & (libc::POLLIN | libc::POLLHUP) != 0 {
                return Ok(());
            }
            if sockets[0].revents != 0 {
                return Err(fail());
            }
            if sockets[1].revents & (libc::POLLERR | libc::POLLHUP | libc::POLLNVAL) != 0 {
                return Err(io::Error::other("original idle recorder disconnected"));
            }
            if sockets[1].revents & libc::POLLIN != 0 {
                self.idle_checkpoint()?;
            }
        }
        Ok(())
    }

    fn bind(&mut self, actor: &actor::Worker) -> io::Result<()> {
        let conn = self.connection;
        let deadline = Instant::now() + PHASE;
        let phase = self.transaction.begin(deadline)?;
        let recording = self.recording.begin(deadline)?;
        checked(checked(conn.grab_server())?.check())?;
        if xres_pid(conn, actor.client)? != actor.pid() {
            return Err(fail());
        }
        let evidence = read_to(
            &mut self.wire,
            self.stream_sequence,
            conn,
            self.owned.pointer.master,
            self.protocol.xinput_opcode,
            |event| {
                refuse_hierarchy(self.protocol, &event)?;
                observe_registration(&mut self.recipients, self.protocol, self.context, event)
            },
        )?;
        if !evidence.buttons.is_empty() {
            return Err(fail());
        }
        self.owned.check(conn)?;
        pointer::neutral(conn, conn.setup().roots[0].root, self.owned.pointer)?;
        checked(checked(conn.ungrab_server())?.check())?;
        recording.finish()?;
        phase.finish()
    }

    fn act(
        &mut self,
        mut actor: actor::Worker,
        action: NativeAction,
        input: &mut impl BufRead,
        probe: &mut Probe,
    ) -> io::Result<()> {
        let NativeAction {
            id,
            original,
            lease,
            lease_pid,
            cut_mode,
        } = action;
        let epoch = [id as u32, (id >> 32) as u32];
        let private = self.owned.pointer;
        let conn = self.connection;
        let protocol = self.protocol;
        let context = self.context;
        let actor_base = actor.client;
        let root = conn.setup().roots[0].root;
        let deadline = Instant::now() + PHASE;
        let observing = self.recording.begin(deadline)?;
        actor::send(
            &mut actor.wire,
            [
                epoch[0],
                epoch[1],
                original.window,
                original.owner_pid,
                original.left as u32,
                original.top as u32,
                u32::from(original.width),
                u32::from(original.height),
                original.x as u32,
                original.y as u32,
                u32::from(private.master),
                u32::from(private.source),
                lease,
                lease_pid,
                u32::from(cut_mode),
            ],
        )?;
        let [epoch0, epoch1, marker] = actor::receive::<3>(&mut actor.wire)?;
        if [epoch0, epoch1] != epoch || marker == 0 {
            return Err(fail());
        }
        let before = evidence::read_fence_observing(
            &mut self.wire,
            evidence::Fence {
                reply_sequence: self.stream_sequence,
                control_client: actor_base,
                marker_sequence: marker,
                pointer_device: private.master,
                xinput_opcode: protocol.xinput_opcode,
            },
            |event| {
                refuse_hierarchy(protocol, &event)?;
                observe_registration(&mut self.recipients, protocol, context, event)
            },
        )?;
        if !before.buttons.is_empty() {
            return Err(io::Error::other("private pointer already acted"));
        }
        observing.finish()?;
        let [epoch0, epoch1, depth] = actor::receive::<3>(&mut actor.wire)?;
        if [epoch0, epoch1] != epoch || !(2..=32).contains(&depth) {
            return Err(fail());
        }
        let mut path = Vec::new();
        for _ in 0..depth {
            let [window] = actor::receive::<1>(&mut actor.wire)?;
            path.push(window);
        }
        let original_id = original.window & !conn.setup().resource_id_mask;
        self.recipients.require(
            &path,
            original.window,
            original_id,
            private.master,
            private.source,
        )?;
        actor::send(&mut actor.wire, epoch)?;
        let [epoch0, epoch1, press_sequence] = actor::receive::<3>(&mut actor.wire)?;
        if [epoch0, epoch1] != epoch || press_sequence == 0 {
            return Err(fail());
        }
        let request = button_request(protocol, root, private, xinput::DEVICE_BUTTON_PRESS_EVENT)?;
        let mut press = recovery::Edge::new(
            protocol,
            actor_base,
            press_sequence,
            request,
            expected_receipt(
                root,
                original.window,
                original_id,
                xinput::BUTTON_PRESS_EVENT,
            ),
        )?;
        probe.attempted = true;
        let acknowledged = actor::send(&mut actor.wire, [epoch[0], epoch[1], press_sequence])
            .and_then(|()| actor::receive::<2>(&mut actor.wire))
            .is_ok_and(|response| response == epoch);
        actor.stop()?;
        if !acknowledged || cut_mode == 2 {
            println!("{{\"input_cut\":true,\"worker_acknowledged\":{acknowledged}}}");
            io::stdout().flush()?;
            if next_line(input)?.as_deref() != Some("recover") {
                return Err(fail());
            }
        }

        let deadline = Instant::now() + PHASE;
        let recovery = self.transaction.begin(deadline)?;
        let observing = self.recording.begin(deadline)?;
        checked(checked(conn.grab_server())?.check())?;
        require_worker_retired(conn, actor_base)?;
        let after_loss = read_to(
            &mut self.wire,
            self.stream_sequence,
            conn,
            private.master,
            protocol.xinput_opcode,
            |event| {
                press.observe(&self.recipients, &event).map_err(|error| {
                    if let evidence::Observation::Request {
                        bytes,
                        after_button: true,
                        ..
                    } = &event
                    {
                        eprintln!(
                            "held route denied request major={} minor={}",
                            bytes[0], bytes[1]
                        );
                    }
                    error
                })?;
                refuse_hierarchy(protocol, &event)?;
                observe_registration(&mut self.recipients, protocol, context, event)
            },
        )?;
        self.owned.check(conn)?;
        press.verify(&after_loss)?;
        guarded_scope(
            conn,
            original.window,
            original.owner_pid,
            lease,
            lease_pid,
            private,
        )?;

        let release = button_request(protocol, root, private, xinput::DEVICE_BUTTON_RELEASE_EVENT)?;
        let sequence = emit(conn, release)?;
        let mut release = recovery::Edge::new(
            protocol,
            conn.setup().resource_id_base,
            sequence,
            release,
            expected_receipt(
                root,
                original.window,
                original_id,
                xinput::BUTTON_RELEASE_EVENT,
            ),
        )?;
        let released = read_to(
            &mut self.wire,
            self.stream_sequence,
            conn,
            private.master,
            protocol.xinput_opcode,
            |event| release.observe(&self.recipients, &event),
        )?;
        release.verify(&released)?;
        pointer::neutral(conn, root, private)?;
        checked(checked(conn.ungrab_server())?.check())?;
        recovery.finish()?;
        observing.finish()?;
        probe.recovered = true;
        probe.verified = cut_mode == 0 && acknowledged;
        Ok(())
    }
}

fn run(probe: &mut Probe) -> io::Result<()> {
    let (conn, bound) = open()?;
    let (data, reader_bound) = open()?;
    let actor = actor::Worker::start()?;
    let actor_base = actor.client;
    let phase = bound.begin(Instant::now() + PHASE)?;
    checked(checked(conn.record_query_version(1, 13))?.reply())?;
    checked(checked(conn.res_query_version(1, 2))?.reply())?;
    clean_boot(&conn, &data, actor_base)?;
    if xres_pid(&conn, actor_base)? != actor.pid() {
        return Err(fail());
    }
    let extension = |name| checked(conn.extension_information(name))?.ok_or_else(fail);
    let record_opcode = extension(record::X11_EXTENSION_NAME)?.major_opcode;
    let xi = extension(xinput::X11_EXTENSION_NAME)?;
    let protocol = selections::Protocol {
        xinput_opcode: xi.major_opcode,
        xinput_first_event: xi.first_event,
        xtest_opcode: extension(xtest::X11_EXTENSION_NAME)?.major_opcode,
        record_opcode,
    };
    let context = checked(conn.generate_id())?;
    let mut ranges = protocol.ranges();
    ranges.push(record::Range {
        delivered_events: record::Range8 {
            first: 35,
            last: 35,
        },
        ..Default::default()
    });
    ranges.extend([
        range(4, 12),
        range(26, 35),
        range(41, 42),
        range(113, 113),
        range(116, 116),
        ext_range(
            protocol.xtest_opcode,
            xtest::FAKE_INPUT_REQUEST,
            xtest::FAKE_INPUT_REQUEST,
        ),
    ]);
    for opcode in [
        xinput::CLOSE_DEVICE_REQUEST,
        xinput::SET_DEVICE_MODE_REQUEST,
        xinput::SELECT_EXTENSION_EVENT_REQUEST,
        xinput::CHANGE_DEVICE_DONT_PROPAGATE_LIST_REQUEST,
        xinput::CHANGE_KEYBOARD_DEVICE_REQUEST,
        xinput::CHANGE_POINTER_DEVICE_REQUEST,
        xinput::GRAB_DEVICE_REQUEST,
        xinput::UNGRAB_DEVICE_REQUEST,
        xinput::GRAB_DEVICE_KEY_REQUEST,
        xinput::UNGRAB_DEVICE_KEY_REQUEST,
        xinput::GRAB_DEVICE_BUTTON_REQUEST,
        xinput::UNGRAB_DEVICE_BUTTON_REQUEST,
        xinput::ALLOW_DEVICE_EVENTS_REQUEST,
        xinput::SET_DEVICE_FOCUS_REQUEST,
        xinput::CHANGE_FEEDBACK_CONTROL_REQUEST,
        xinput::SET_DEVICE_BUTTON_MAPPING_REQUEST,
        xinput::SEND_EXTENSION_EVENT_REQUEST,
        xinput::SET_DEVICE_VALUATORS_REQUEST,
        xinput::CHANGE_DEVICE_CONTROL_REQUEST,
        xinput::CHANGE_DEVICE_PROPERTY_REQUEST,
        xinput::DELETE_DEVICE_PROPERTY_REQUEST,
        xinput::XI_WARP_POINTER_REQUEST,
        xinput::XI_CHANGE_HIERARCHY_REQUEST,
        xinput::XI_SET_CLIENT_POINTER_REQUEST,
        xinput::XI_SELECT_EVENTS_REQUEST,
        xinput::XI_SET_FOCUS_REQUEST,
        xinput::XI_GRAB_DEVICE_REQUEST,
        xinput::XI_UNGRAB_DEVICE_REQUEST,
        xinput::XI_ALLOW_EVENTS_REQUEST,
        xinput::XI_PASSIVE_GRAB_DEVICE_REQUEST,
        xinput::XI_PASSIVE_UNGRAB_DEVICE_REQUEST,
        xinput::XI_CHANGE_PROPERTY_REQUEST,
        xinput::XI_DELETE_PROPERTY_REQUEST,
        xinput::XI_BARRIER_RELEASE_POINTER_REQUEST,
    ] {
        ranges.push(ext_range(protocol.xinput_opcode, opcode, opcode));
    }
    // Every trusted per-action worker gets a new X11 connection. RECORD sees
    // all markers, but acceptance always binds the proved client and sequence.
    ranges.push(range(
        xproto::GET_INPUT_FOCUS_REQUEST,
        xproto::GET_INPUT_FOCUS_REQUEST,
    ));
    checked(checked(conn.record_create_context(context, 4, &[3], &ranges))?.check())?;
    phase.finish()?;

    let phase = reader_bound.begin(Instant::now() + PHASE)?;
    let (bytes, fds) = record::EnableContextRequest { context }.serialize(record_opcode);
    let enable = checked(data.send_request_without_reply(&[IoSlice::new(&bytes[0])], fds))?;
    let stream_sequence = u16::try_from(enable.sequence_number()).map_err(|_| fail())?;
    drop(enable);
    checked(data.flush())?;
    let mut wire = Wire(&data);
    if packet(&mut wire, stream_sequence)?.category != 4 {
        return Err(fail());
    }
    phase.finish()?;
    let phase = bound.begin(Instant::now() + PHASE)?;
    clean_boot(&conn, &data, actor_base)?;
    if xres_pid(&conn, actor_base)? != actor.pid() {
        return Err(fail());
    }
    let recording = reader_bound.begin(Instant::now() + PHASE)?;
    checked(checked(conn.grab_server())?.check())?;
    clean_boot(&conn, &data, actor_base)?;
    let (owned, birth) = startup::OwnedPointer::create(&conn)?;
    let mut birth_seen = false;
    let evidence = read_to(
        &mut wire,
        stream_sequence,
        &conn,
        owned.pointer.master,
        xi.major_opcode,
        |event| {
            if let evidence::Observation::Request {
                client,
                sequence,
                bytes,
                ..
            } = event
            {
                if bytes.first() == Some(&protocol.xinput_opcode)
                    && bytes.get(1) == Some(&xinput::XI_CHANGE_HIERARCHY_REQUEST)
                {
                    if birth_seen || client != conn.setup().resource_id_base || sequence != birth {
                        return Err(fail());
                    }
                    birth_seen = true;
                }
            }
            Ok(())
        },
    )?;
    if !birth_seen || !evidence.buttons.is_empty() {
        return Err(fail());
    }
    owned.check(&conn)?;
    pointer::neutral(&conn, conn.setup().roots[0].root, owned.pointer)?;
    checked(checked(conn.ungrab_server())?.check())?;
    recording.finish()?;
    phase.finish()?;
    let pointer::Pointer { master, source } = owned.pointer;
    println!(
        "{{\"ready\":true,\"protocol_version\":{PROTOCOL_VERSION},\"context\":{context},\"master\":{master},\"source\":{source},\"name\":\"{}\",\"keeper_pid\":{},\"worker_pid\":{}}}",
        owned.name,
        std::process::id(),
        actor.pid()
    );
    io::stdout().flush()?;
    let mut keeper = Keeper {
        connection: &conn,
        transaction: &bound,
        recording: &reader_bound,
        wire,
        stream_sequence,
        context,
        protocol,
        owned,
        recipients: selections::PointerSelections::default(),
    };
    // Retain the initial authenticated child for the first attempt. Additional
    // owners are neither inherited from the desktop nor recycled after input.
    let mut initial = Some(actor);
    let stdin = io::stdin().as_fd().try_clone_to_owned()?;
    let mut input = io::BufReader::new(std::fs::File::from(stdin));
    let mut previous = 0u64;
    loop {
        *probe = Probe::default();
        let idle_failure = keeper.await_command(&input).err();
        if idle_failure.is_some() {
            // The action loop only enters idle with a neutral pointer. Do
            // not create a new worker or reconnect a lost record epoch.
            keeper.transaction.stop();
            keeper.recording.stop();
            drop(initial.take());
        }
        let Some(line) = next_line(&mut input)? else {
            return Ok(());
        };
        if line == "finish" {
            if let Some(error) = idle_failure {
                return Err(error);
            }
            let phase = keeper.transaction.begin(Instant::now() + PHASE)?;
            checked(checked(conn.record_disable_context(context))?.check())?;
            checked(checked(conn.record_free_context(context))?.check())?;
            phase.finish()?;
            println!("{{\"finished\":true}}");
            return Ok(());
        }
        let action = NativeAction::parse(&line)?;
        if action.id <= previous || action.id > 32 {
            return Err(io::Error::other("invalid native action sequence"));
        }
        previous = action.id;
        probe.id = action.id;
        if let Some(error) = idle_failure {
            return Err(error);
        }
        let actor = match initial.take() {
            Some(actor) => actor,
            None => actor::Worker::start()?,
        };
        // A post-disconnect action may create a client using the prior base.
        // Consume history up through the keeper's own fence before that client
        // is allowed to send any request. Retain the recipient table.
        keeper.bind(&actor)?;
        keeper.act(actor, action, &mut input, probe)?;
        report(probe);
        if !probe.verified {
            return Ok(());
        }
    }
}

fn main() {
    match std::env::args().nth(1).as_deref() {
        Some("--evictable-input") => {
            if let Err(error) = actor::run() {
                eprintln!("worker: {error}");
            }
            std::process::exit(2);
        }
        Some("--owned-device-bootstrap") => {}
        _ => std::process::exit(2),
    }
    let mut probe = Probe::default();
    if let Err(error) = run(&mut probe) {
        report(&probe);
        eprintln!("{error}");
        std::process::exit(if probe.attempted { 3 } else { 2 });
    }
}

#[cfg(test)]
mod input_lines {
    use super::*;

    #[test]
    fn fault_modes_require_the_fixture_build() {
        assert!(accepted_cut_mode(0));
        assert!(!accepted_cut_mode(1));
        assert_eq!(
            accepted_cut_mode(2),
            cfg!(feature = "fixture-fault-injection")
        );
        assert!(!accepted_cut_mode(3));
    }

    #[test]
    fn rejects_oversized_or_truncated_lines() {
        assert_eq!(
            next_line(&mut io::Cursor::new(b"finish\n")).unwrap(),
            Some("finish".into())
        );
        assert_eq!(next_line(&mut io::Cursor::new(b"")).unwrap(), None);
        assert!(next_line(&mut io::Cursor::new(b"finish")).is_err());
        let oversized = format!("{}\n", "x".repeat(512));
        assert!(next_line(&mut io::Cursor::new(oversized)).is_err());
    }
}
