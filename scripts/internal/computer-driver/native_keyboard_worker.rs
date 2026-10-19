//! Private default-off process boundary. This binary is not a Cua MCP tool or
//! an admitted MintClaw runtime. Its sole caller owns the original host guard,
//! generation and desktop lease, and must fence them at the explicit permits.
//! No ambient display/auth discovery, telemetry, recording or text logging.

#[path = "mintclaw_native_keyboard.rs"]
mod mintclaw_native_keyboard;
#[path = "mintclaw_native_x11_io.rs"]
mod mintclaw_native_x11_io;

use std::io::{self, ErrorKind, Read, Write};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::os::unix::net::UnixStream;
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

use mintclaw_native_keyboard::{
    Binding, Endpoint, InputIdentity, Keeper, PairWitness, PreparedInput, PreparedKeeper,
    ServerIdentity,
};
use serde::{Deserialize, Serialize};
use zeroize::{Zeroize, Zeroizing};

type Result<T> = io::Result<T>;
type Operation = [u8; 32];
const MAX_FRAME: usize = 65536;

fn refused() -> io::Error {
    io::Error::new(
        ErrorKind::ConnectionAborted,
        "private native operation unavailable",
    )
}
fn checked<T, E>(result: std::result::Result<T, E>) -> Result<T> {
    result.map_err(|_| refused())
}

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Bootstrap {
    socket: String,
    screen: usize,
    auth: Vec<u8>,
    server: ServerIdentity,
}

impl Drop for Bootstrap {
    fn drop(&mut self) {
        self.auth.zeroize();
    }
}

impl Bootstrap {
    fn connect(&self, deadline: Instant) -> Result<Endpoint> {
        let number = self
            .socket
            .strip_prefix("/tmp/.X11-unix/X")
            .ok_or_else(refused)?;
        if number.is_empty()
            || number.len() > 5
            || !number.bytes().all(|value| value.is_ascii_digit())
            || self.auth.len() != 16
            || self.screen > 15
        {
            return Err(refused());
        }
        let socket = socket2::Socket::new(socket2::Domain::UNIX, socket2::Type::STREAM, None)?;
        let budget = deadline
            .saturating_duration_since(Instant::now())
            .min(Duration::from_millis(250));
        if budget.is_zero() {
            return Err(refused());
        }
        socket.connect_timeout(&socket2::SockAddr::unix(&self.socket)?, budget)?;
        let descriptor: OwnedFd = socket.into();
        let socket = UnixStream::from(descriptor);
        if !self.server.matches_peer(&socket) {
            return Err(refused());
        }
        let endpoint = Endpoint::from_socket(
            socket,
            self.screen,
            b"MIT-MAGIC-COOKIE-1".to_vec(),
            self.auth.clone(),
            deadline,
        )?;
        if endpoint.server() != self.server {
            return Err(refused());
        }
        Ok(endpoint)
    }
}

#[derive(Serialize, Deserialize)]
#[serde(transparent)]
struct Chords(Vec<Vec<u8>>);

impl Drop for Chords {
    fn drop(&mut self) {
        for chord in &mut self.0 {
            chord.zeroize();
        }
    }
}

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Start {
    operation: Operation,
    bootstrap: Bootstrap,
    binding: Binding,
    chords: Chords,
    budget_ms: u32,
}

#[derive(Serialize, Deserialize)]
#[serde(
    tag = "kind",
    content = "data",
    deny_unknown_fields,
    rename_all = "snake_case"
)]
enum HostMessage {
    Start(Start),
    Grant { operation: Operation },
    Ready { operation: Operation },
    Continue { operation: Operation, ordinal: u32 },
    Finish { operation: Operation },
    Stop { operation: Operation },
}

impl HostMessage {
    fn operation(&self) -> Operation {
        match self {
            Self::Start(start) => start.operation,
            Self::Grant { operation }
            | Self::Ready { operation }
            | Self::Continue { operation, .. }
            | Self::Finish { operation }
            | Self::Stop { operation } => *operation,
        }
    }
}

#[derive(Serialize, Deserialize)]
#[serde(
    tag = "kind",
    content = "data",
    deny_unknown_fields,
    rename_all = "snake_case"
)]
enum HostReply {
    Prepared {
        operation: Operation,
    },
    Admitted {
        operation: Operation,
        keyboard: u8,
    },
    Edge {
        operation: Operation,
        ordinal: u32,
        held: u8,
    },
    Dispatched {
        operation: Operation,
    },
    Complete {
        operation: Operation,
        acknowledged: bool,
        canceled: bool,
        cancellation_delivered: bool,
        cleanup_confirmed: bool,
    },
    Rejected,
}

#[derive(Serialize, Deserialize)]
#[serde(
    tag = "kind",
    content = "data",
    deny_unknown_fields,
    rename_all = "snake_case"
)]
enum WorkerMessage {
    Init {
        operation: Operation,
        bootstrap: Bootstrap,
        binding: Binding,
        budget_ms: u32,
    },
    Bind {
        operation: Operation,
        witness: PairWitness,
    },
    Begin {
        operation: Operation,
    },
    Edge {
        operation: Operation,
        ordinal: u32,
        code: u8,
        press: bool,
    },
    End {
        operation: Operation,
    },
}

#[derive(Serialize, Deserialize)]
#[serde(
    tag = "kind",
    content = "data",
    deny_unknown_fields,
    rename_all = "snake_case"
)]
enum WorkerReply {
    Prepared {
        operation: Operation,
        identity: InputIdentity,
    },
    Bound {
        operation: Operation,
    },
    Begun {
        operation: Operation,
    },
    Edge {
        operation: Operation,
        ordinal: u32,
    },
    Ended {
        operation: Operation,
    },
}

struct Wire(UnixStream);

impl Wire {
    fn inherited() -> Result<(Self, u32)> {
        // Only a kernel-owned inherited Unix stream from our real parent. No
        // terminal, path, TCP client or model-selected shell transport.
        let socket = unsafe { UnixStream::from_raw_fd(0) };
        let mut address: libc::sockaddr_un = unsafe { std::mem::zeroed() };
        let mut size = std::mem::size_of_val(&address) as libc::socklen_t;
        let status = unsafe {
            libc::getpeername(
                socket.as_raw_fd(),
                (&mut address as *mut libc::sockaddr_un).cast(),
                &mut size,
            )
        };
        let mut peer: libc::ucred = unsafe { std::mem::zeroed() };
        let mut length = std::mem::size_of_val(&peer) as libc::socklen_t;
        let credentials = unsafe {
            libc::getsockopt(
                socket.as_raw_fd(),
                libc::SOL_SOCKET,
                libc::SO_PEERCRED,
                (&mut peer as *mut libc::ucred).cast(),
                &mut length,
            )
        };
        let mut kind = 0i32;
        let mut kind_length = std::mem::size_of_val(&kind) as libc::socklen_t;
        let typed = unsafe {
            libc::getsockopt(
                socket.as_raw_fd(),
                libc::SOL_SOCKET,
                libc::SO_TYPE,
                (&mut kind as *mut i32).cast(),
                &mut kind_length,
            )
        };
        if status != 0
            || address.sun_family != libc::AF_UNIX as libc::sa_family_t
            || credentials != 0
            || typed != 0
            || kind_length as usize != std::mem::size_of_val(&kind)
            || kind != libc::SOCK_STREAM
            || length as usize != std::mem::size_of_val(&peer)
            || peer.pid != unsafe { libc::getppid() }
            || peer.uid != unsafe { libc::getuid() }
            || peer.pid <= 0
        {
            return Err(refused());
        }
        Ok((Self(socket), peer.pid as u32))
    }

    fn transfer(&mut self, mut data: &mut [u8], writing: bool, deadline: Instant) -> Result<()> {
        while !data.is_empty() {
            let remaining = deadline
                .checked_duration_since(Instant::now())
                .ok_or_else(refused)?;
            if remaining.is_zero() {
                return Err(refused());
            }
            let result = if writing {
                self.0.set_write_timeout(Some(remaining))?;
                self.0.write(data)
            } else {
                self.0.set_read_timeout(Some(remaining))?;
                self.0.read(data)
            };
            let count = match result {
                Err(error) if error.kind() == ErrorKind::Interrupted => continue,
                result => result?,
            };
            if count == 0 || Instant::now() >= deadline {
                return Err(refused());
            }
            data = &mut data[count..];
        }
        Ok(())
    }

    fn send<T: Serialize>(&mut self, packet: &T, deadline: Instant) -> Result<()> {
        let deadline = deadline.min(Instant::now() + Duration::from_millis(250));
        let mut bytes = Zeroizing::new(checked(serde_json::to_vec(packet))?);
        if bytes.is_empty() || bytes.len() > MAX_FRAME {
            return Err(refused());
        }
        self.transfer(&mut (bytes.len() as u32).to_be_bytes(), true, deadline)?;
        self.transfer(&mut bytes, true, deadline)
    }

    fn receive<T: serde::de::DeserializeOwned>(&mut self, deadline: Instant) -> Result<T> {
        let mut header = [0u8; 4];
        self.transfer(&mut header, false, deadline)?;
        let length = u32::from_be_bytes(header) as usize;
        if length == 0 || length > MAX_FRAME {
            return Err(refused());
        }
        let mut bytes = Zeroizing::new(vec![0u8; length]);
        self.transfer(&mut bytes, false, deadline)?;
        checked(serde_json::from_slice(&bytes))
    }
}

struct OwnedChild {
    process: Child,
    pidfd: OwnedFd,
}

impl OwnedChild {
    fn spawn() -> Result<(Self, Wire)> {
        let (parent, child) = UnixStream::pair()?;
        let child: OwnedFd = child.into();
        // /proc/self/exe refers to the inherited executable until exec. A
        // replaced pathname cannot substitute another binary for this child.
        let mut process = Command::new("/proc/self/exe")
            .arg("--input")
            .env_clear()
            .env("CUA_TELEMETRY", "0")
            .env("DO_NOT_TRACK", "1")
            .stdin(Stdio::from(child))
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()?;
        let descriptor = unsafe { libc::syscall(libc::SYS_pidfd_open, process.id(), 0) };
        if descriptor < 0 {
            // The child has not been reaped, so its PID cannot be reused. No
            // pair/focus/input has been granted at this preparation stage.
            let _ = process.kill();
            let until = Instant::now() + Duration::from_millis(100);
            while Instant::now() < until {
                if process.try_wait().ok().flatten().is_some() {
                    break;
                }
                unsafe {
                    libc::poll(std::ptr::null_mut(), 0, 5);
                }
            }
            return Err(refused());
        }
        Ok((
            Self {
                process,
                pidfd: unsafe { OwnedFd::from_raw_fd(descriptor as i32) },
            },
            Wire(parent),
        ))
    }

    fn stop(&mut self) -> bool {
        unsafe {
            libc::syscall(
                libc::SYS_pidfd_send_signal,
                self.pidfd.as_raw_fd(),
                libc::SIGKILL,
                std::ptr::null::<libc::siginfo_t>(),
                0,
            ) == 0
        }
    }
}

impl Drop for OwnedChild {
    fn drop(&mut self) {
        self.stop();
        // Never turn process-exit waiting into an unbounded native cleanup.
        let until = Instant::now() + Duration::from_millis(100);
        while Instant::now() < until {
            if self.process.try_wait().ok().flatten().is_some() {
                return;
            }
            let mut fd = libc::pollfd {
                fd: self.pidfd.as_raw_fd(),
                events: libc::POLLIN,
                revents: 0,
            };
            unsafe {
                libc::poll(&mut fd, 1, 5);
            }
        }
    }
}

fn timeout(seconds: u64, overall: Instant) -> Instant {
    overall.min(Instant::now() + Duration::from_secs(seconds))
}

fn worker(mut wire: Wire, overall: Instant) -> Result<()> {
    let WorkerMessage::Init {
        operation,
        bootstrap,
        binding,
        budget_ms,
    } = wire.receive(timeout(3, overall))?
    else {
        return Err(refused());
    };
    if budget_ms == 0 || budget_ms > 10000 {
        return Err(refused());
    }
    let input = PreparedInput::prepare(
        bootstrap.connect(timeout(3, overall))?,
        binding,
        timeout(3, overall),
    )?;
    wire.send(
        &WorkerReply::Prepared {
            operation,
            identity: input.identity(),
        },
        timeout(3, overall),
    )?;
    let WorkerMessage::Bind {
        operation: key,
        witness,
    } = wire.receive(timeout(5, overall))?
    else {
        return Err(refused());
    };
    if key != operation {
        return Err(refused());
    }
    let mut input = input.bind(witness, timeout(3, overall))?;
    wire.send(&WorkerReply::Bound { operation }, timeout(3, overall))?;
    let WorkerMessage::Begin { operation: key } = wire.receive(timeout(5, overall))? else {
        return Err(refused());
    };
    if key != operation {
        return Err(refused());
    }
    let deadline = overall.min(Instant::now() + Duration::from_millis(u64::from(budget_ms)));
    input.begin(deadline)?;
    wire.send(&WorkerReply::Begun { operation }, deadline)?;
    loop {
        match wire.receive(deadline)? {
            WorkerMessage::Edge {
                operation: key,
                ordinal,
                code,
                press,
            } if key == operation => {
                input.edge(ordinal, code, press)?;
                wire.send(&WorkerReply::Edge { operation, ordinal }, deadline)?;
            }
            WorkerMessage::End { operation: key } if key == operation => {
                input.end()?;
                return wire.send(&WorkerReply::Ended { operation }, deadline);
            }
            _ => return Err(refused()),
        }
    }
}

enum Permit {
    Grant,
    Ready,
    Edge(u32),
    Finish,
}

/// Wait on both retained streams. A blocked child cannot conceal a scoped
/// emergency stop until the action deadline. Frame IO also has a short cap;
/// no timer/callback can later target a new process or descriptor.
fn wait_worker(
    host: &mut Wire,
    worker: &mut Wire,
    operation: Operation,
    deadline: Instant,
) -> Result<Option<WorkerReply>> {
    loop {
        let remaining = deadline
            .checked_duration_since(Instant::now())
            .ok_or_else(refused)?;
        if remaining.is_zero() {
            return Err(refused());
        }
        let mut fds = [
            libc::pollfd {
                fd: host.0.as_raw_fd(),
                events: libc::POLLIN,
                revents: 0,
            },
            libc::pollfd {
                fd: worker.0.as_raw_fd(),
                events: libc::POLLIN,
                revents: 0,
            },
        ];
        let milliseconds = remaining.min(Duration::from_millis(20)).as_millis().max(1) as i32;
        let ready = unsafe { libc::poll(fds.as_mut_ptr(), 2, milliseconds) };
        if ready < 0 {
            if io::Error::last_os_error().kind() == ErrorKind::Interrupted {
                continue;
            }
            return Err(refused());
        }
        if fds[0].revents != 0 {
            let packet: HostMessage =
                host.receive(deadline.min(Instant::now() + Duration::from_millis(250)))?;
            if matches!(packet, HostMessage::Stop { operation: key } if key == operation) {
                return Ok(None);
            }
            host.send(&HostReply::Rejected, deadline)?;
        }
        if fds[1].revents != 0 {
            return worker
                .receive(deadline.min(Instant::now() + Duration::from_millis(250)))
                .map(Some);
        }
    }
}

fn permit(
    host: &mut Wire,
    operation: Operation,
    expected: Permit,
    deadline: Instant,
) -> Result<bool> {
    for _ in 0..256 {
        let packet: HostMessage = host.receive(deadline)?;
        // An old/foreign cancellation or ordinal never stops another action.
        if packet.operation() == operation {
            match (&packet, &expected) {
                (HostMessage::Stop { .. }, _) => return Ok(false),
                (HostMessage::Grant { .. }, Permit::Grant)
                | (HostMessage::Ready { .. }, Permit::Ready)
                | (HostMessage::Finish { .. }, Permit::Finish) => return Ok(true),
                (HostMessage::Continue { ordinal, .. }, Permit::Edge(index))
                    if ordinal == index =>
                {
                    return Ok(true);
                }
                _ => (),
            }
        }
        host.send(&HostReply::Rejected, deadline)?;
    }
    Err(refused())
}

fn active(
    host: &mut Wire,
    worker: &mut Wire,
    keeper: &mut Keeper,
    start: &Start,
    overall: Instant,
) -> Result<bool> {
    let operation = start.operation;
    worker.send(
        &WorkerMessage::Bind {
            operation,
            witness: keeper.witness(),
        },
        timeout(3, overall),
    )?;
    let Some(reply) = wait_worker(host, worker, operation, timeout(3, overall))? else {
        return Ok(false);
    };
    if !matches!(reply, WorkerReply::Bound { operation: key } if key == operation) {
        return Err(refused());
    }
    host.send(
        &HostReply::Admitted {
            operation,
            keyboard: keeper.keyboard(),
        },
        timeout(3, overall),
    )?;
    if !permit(host, operation, Permit::Ready, timeout(5, overall))? {
        return Ok(false);
    }
    let deadline = overall.min(Instant::now() + Duration::from_millis(u64::from(start.budget_ms)));
    worker.send(&WorkerMessage::Begin { operation }, deadline)?;
    let Some(reply) = wait_worker(host, worker, operation, deadline)? else {
        return Ok(false);
    };
    if !matches!(reply, WorkerReply::Begun { operation: key } if key == operation) {
        return Err(refused());
    }
    let mut ordinal = 0;
    let mut held = 0u8;
    for chord in &start.chords.0 {
        for (press, codes) in [
            (true, chord.clone()),
            (false, chord.iter().rev().copied().collect()),
        ] {
            let codes = Zeroizing::new(codes);
            for code in codes.iter() {
                host.send(
                    &HostReply::Edge {
                        operation,
                        ordinal,
                        held,
                    },
                    deadline,
                )?;
                if !permit(host, operation, Permit::Edge(ordinal), deadline)? {
                    return Ok(false);
                }
                keeper.check(deadline)?;
                worker.send(
                    &WorkerMessage::Edge {
                        operation,
                        ordinal,
                        code: *code,
                        press,
                    },
                    deadline,
                )?;
                let Some(reply) = wait_worker(host, worker, operation, deadline)? else {
                    return Ok(false);
                };
                if !matches!(reply, WorkerReply::Edge { operation: key, ordinal: index }
                    if key == operation && index == ordinal)
                {
                    return Err(refused());
                }
                held = if press { held + 1 } else { held - 1 };
                ordinal += 1;
            }
        }
    }
    worker.send(&WorkerMessage::End { operation }, deadline)?;
    let Some(reply) = wait_worker(host, worker, operation, deadline)? else {
        return Ok(false);
    };
    if !matches!(reply, WorkerReply::Ended { operation: key } if key == operation) {
        return Err(refused());
    }
    host.send(&HostReply::Dispatched { operation }, timeout(3, overall))?;
    permit(host, operation, Permit::Finish, timeout(5, overall))
}

fn supervise(mut host: Wire, parent: u32, overall: Instant) -> Result<()> {
    let HostMessage::Start(start) = host.receive(timeout(3, overall))? else {
        return Err(refused());
    };
    if start.binding.lease_pid != parent
        || start.operation == [0; 32]
        || start.budget_ms == 0
        || start.budget_ms > 10000
        || start.chords.0.is_empty()
        || start.chords.0.len() > 256
        || start.chords.0.iter().any(|chord| {
            chord.is_empty()
                || chord.len() > 8
                || chord
                    .iter()
                    .enumerate()
                    .any(|(index, code)| *code < 8 || chord[..index].contains(code))
        })
    {
        return Err(refused());
    }
    let control = start.bootstrap.connect(timeout(3, overall))?;
    let prepared = PreparedKeeper::prepare(control, start.binding, timeout(3, overall))?;
    let (mut child, mut wire) = OwnedChild::spawn()?;
    // Protected bootstrap is sent once over the inherited private socket, not
    // argv/env, model messages, logs or an on-disk task manifest.
    wire.send(
        &WorkerMessage::Init {
            operation: start.operation,
            bootstrap: Bootstrap {
                socket: start.bootstrap.socket.clone(),
                screen: start.bootstrap.screen,
                auth: start.bootstrap.auth.clone(),
                server: start.bootstrap.server,
            },
            binding: start.binding,
            budget_ms: start.budget_ms,
        },
        timeout(3, overall),
    )?;
    let Some(reply) = wait_worker(&mut host, &mut wire, start.operation, timeout(3, overall))?
    else {
        child.stop();
        return host.send(
            &HostReply::Complete {
                operation: start.operation,
                acknowledged: false,
                canceled: true,
                cancellation_delivered: false,
                cleanup_confirmed: false,
            },
            timeout(3, overall),
        );
    };
    let WorkerReply::Prepared {
        operation,
        identity,
    } = reply
    else {
        return Err(refused());
    };
    if operation != start.operation {
        return Err(refused());
    }
    host.send(&HostReply::Prepared { operation }, timeout(3, overall))?;
    // No pair or native focus exists before this original-host epoch fence.
    if !permit(&mut host, operation, Permit::Grant, timeout(5, overall))? {
        child.stop();
        return host.send(
            &HostReply::Complete {
                operation,
                acknowledged: false,
                canceled: true,
                cancellation_delivered: false,
                cleanup_confirmed: false,
            },
            timeout(3, overall),
        );
    }
    let mut keeper = prepared.grant(identity, child.process.id(), timeout(3, overall))?;
    let outcome = active(&mut host, &mut wire, &mut keeper, &start, overall);
    let canceled = matches!(outcome, Ok(false));
    let delivered = child.stop();
    let cleanup_confirmed = keeper.cleanup();
    host.send(
        &HostReply::Complete {
            operation,
            acknowledged: matches!(outcome, Ok(true)),
            canceled,
            cancellation_delivered: canceled && delivered,
            cleanup_confirmed,
        },
        timeout(3, overall),
    )
}

fn main() -> std::process::ExitCode {
    // Error reports are stable status only; never render a decoded packet or
    // panic payload containing protected auth/text/physical key sequences.
    std::panic::set_hook(Box::new(|_| {}));
    let result = (|| {
        let (wire, parent) = Wire::inherited()?;
        let overall = Instant::now() + Duration::from_secs(30);
        match std::env::args().skip(1).collect::<Vec<_>>().as_slice() {
            [mode] if mode == "--keeper" => supervise(wire, parent, overall),
            [mode] if mode == "--input" => worker(wire, overall),
            _ => Err(refused()),
        }
    })();
    if result.is_ok() {
        std::process::ExitCode::SUCCESS
    } else {
        std::process::ExitCode::FAILURE
    }
}

#[cfg(test)]
mod wire_tests {
    use super::*;

    fn deadline() -> Instant {
        Instant::now() + Duration::from_secs(2)
    }

    fn pair() -> (Wire, Wire) {
        let (left, right) = UnixStream::pair().unwrap();
        (Wire(left), Wire(right))
    }

    fn raw_frame(wire: &mut Wire, bytes: &[u8]) {
        wire.0
            .write_all(&(bytes.len() as u32).to_be_bytes())
            .unwrap();
        wire.0.write_all(bytes).unwrap();
    }

    #[test]
    fn fragmented_frame_round_trip() {
        let (mut writer, mut reader) = pair();
        let operation = [37; 32];
        let bytes = serde_json::to_vec(&HostMessage::Continue {
            operation,
            ordinal: 91,
        })
        .unwrap();
        let sender = std::thread::spawn(move || {
            for byte in (bytes.len() as u32)
                .to_be_bytes()
                .iter()
                .chain(bytes.iter())
            {
                writer.0.write_all(&[*byte]).unwrap();
            }
        });
        assert!(matches!(reader.receive(deadline()).unwrap(),
            HostMessage::Continue { operation: key, ordinal: 91 } if key == operation));
        sender.join().unwrap();
    }

    #[test]
    fn empty_and_oversized_lengths_are_refused_before_payload() {
        for length in [0, MAX_FRAME as u32 + 1, u32::MAX] {
            let (mut writer, mut reader) = pair();
            writer.0.write_all(&length.to_be_bytes()).unwrap();
            assert!(reader.receive::<HostMessage>(deadline()).is_err());
        }
    }

    #[test]
    fn malformed_trailing_and_invalid_utf8_payloads_are_refused() {
        for bytes in [
            b"null".as_slice(),
            b"{",
            b"{}{}",
            b"\xff",
            br#"{"kind":"unknown"}"#,
        ] {
            let (mut writer, mut reader) = pair();
            raw_frame(&mut writer, bytes);
            assert!(reader.receive::<HostMessage>(deadline()).is_err());
        }
    }

    #[test]
    fn unknown_envelope_and_phase_fields_are_refused() {
        let packet = serde_json::to_value(HostMessage::Continue {
            operation: [37; 32],
            ordinal: 1,
        })
        .unwrap();
        for nested in [false, true] {
            let mut packet = packet.clone();
            let object = if nested {
                packet["data"].as_object_mut()
            } else {
                packet.as_object_mut()
            }
            .unwrap();
            object.insert("unexpected".into(), serde_json::Value::Bool(true));
            let (mut writer, mut reader) = pair();
            raw_frame(&mut writer, &serde_json::to_vec(&packet).unwrap());
            assert!(reader.receive::<HostMessage>(deadline()).is_err());
        }
    }

    #[test]
    fn truncated_header_and_body_are_terminal() {
        for bytes in [vec![0, 0], vec![0, 0, 0, 8, b'{']] {
            let (mut writer, mut reader) = pair();
            writer.0.write_all(&bytes).unwrap();
            drop(writer);
            assert!(reader.receive::<HostMessage>(deadline()).is_err());
        }
    }

    #[test]
    fn partial_frame_cannot_extend_absolute_read_deadline() {
        let (mut writer, mut reader) = pair();
        writer.0.write_all(&8u32.to_be_bytes()).unwrap();
        writer.0.write_all(b"{").unwrap();
        let start = Instant::now();
        assert!(
            reader
                .receive::<HostMessage>(start + Duration::from_millis(30))
                .is_err()
        );
        assert!(start.elapsed() < Duration::from_millis(500));
    }

    #[test]
    fn blocked_write_cannot_extend_absolute_send_deadline() {
        let (mut writer, _reader) = pair();
        let size = 1024i32;
        assert_eq!(
            unsafe {
                libc::setsockopt(
                    writer.0.as_raw_fd(),
                    libc::SOL_SOCKET,
                    libc::SO_SNDBUF,
                    (&size as *const i32).cast(),
                    std::mem::size_of_val(&size) as libc::socklen_t,
                )
            },
            0
        );
        let start = Instant::now();
        assert!(
            writer
                .send(
                    &"x".repeat(MAX_FRAME - 16),
                    start + Duration::from_millis(30)
                )
                .is_err()
        );
        assert!(start.elapsed() < Duration::from_millis(500));
    }

    #[test]
    fn scoped_stop_interrupts_wait_without_a_worker_reply() {
        let (mut host, mut caller) = pair();
        let (mut worker, _blocked) = pair();
        let operation = [37; 32];
        caller
            .send(&HostMessage::Stop { operation }, deadline())
            .unwrap();
        assert!(
            wait_worker(&mut host, &mut worker, operation, deadline())
                .unwrap()
                .is_none()
        );
    }

    #[test]
    fn foreign_stop_cannot_cancel_the_current_worker() {
        let (mut host, mut caller) = pair();
        let (mut worker, mut child) = pair();
        let operation = [37; 32];
        caller
            .send(
                &HostMessage::Stop {
                    operation: [38; 32],
                },
                deadline(),
            )
            .unwrap();
        child
            .send(&WorkerReply::Begun { operation }, deadline())
            .unwrap();
        assert!(
            matches!(wait_worker(&mut host, &mut worker, operation, deadline()).unwrap(),
            Some(WorkerReply::Begun { operation: key }) if key == operation)
        );
        assert!(matches!(
            caller.receive::<HostReply>(deadline()).unwrap(),
            HostReply::Rejected
        ));
    }

    #[test]
    fn permit_requires_current_operation_phase_and_ordinal() {
        let (mut host, mut caller) = pair();
        let operation = [37; 32];
        for packet in [
            HostMessage::Stop {
                operation: [38; 32],
            },
            HostMessage::Grant { operation },
            HostMessage::Continue {
                operation,
                ordinal: 4,
            },
            HostMessage::Continue {
                operation,
                ordinal: 5,
            },
        ] {
            caller.send(&packet, deadline()).unwrap();
        }
        assert!(permit(&mut host, operation, Permit::Edge(5), deadline()).unwrap());
        for _ in 0..3 {
            assert!(matches!(
                caller.receive::<HostReply>(deadline()).unwrap(),
                HostReply::Rejected
            ));
        }
    }

    #[test]
    fn already_expired_io_and_overlarge_outbound_frame_are_refused() {
        let (mut writer, mut reader) = pair();
        assert!(writer.send(&HostReply::Rejected, Instant::now()).is_err());
        assert!(reader.receive::<HostMessage>(Instant::now()).is_err());
        assert!(writer.send(&"x".repeat(MAX_FRAME), deadline()).is_err());
        reader.0.set_nonblocking(true).unwrap();
        let mut byte = [0];
        assert_eq!(
            reader.0.read(&mut byte).unwrap_err().kind(),
            ErrorKind::WouldBlock
        );
    }
}
