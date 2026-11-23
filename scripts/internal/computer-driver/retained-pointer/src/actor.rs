use super::*;

use std::os::fd::{FromRawFd, OwnedFd};
use std::process::{Child, Command, Stdio};

pub(super) fn send<const N: usize>(wire: &mut UnixStream, words: [u32; N]) -> io::Result<()> {
    for word in words {
        wire.write_all(&word.to_ne_bytes())?;
    }
    Ok(())
}

pub(super) fn receive<const N: usize>(wire: &mut UnixStream) -> io::Result<[u32; N]> {
    let mut words = [0u32; N];
    for word in &mut words {
        let mut bytes = [0u8; 4];
        wire.read_exact(&mut bytes)?;
        *word = u32::from_ne_bytes(bytes);
    }
    Ok(words)
}

fn bound(wire: &UnixStream) -> io::Result<()> {
    wire.set_read_timeout(Some(PHASE))?;
    wire.set_write_timeout(Some(PHASE))
}

pub(super) struct Worker {
    child: Child,
    pub(super) wire: UnixStream,
    pub(super) client: u32,
}

impl Worker {
    pub(super) fn start() -> io::Result<Self> {
        let (mut wire, child_wire) = UnixStream::pair()?;
        bound(&wire)?;
        // Re-execute the sealed image of this process. Its staged pathname
        // can change while the original executable remains owned and pinned.
        let mut child = Command::new("/proc/self/exe")
            .arg("--evictable-input")
            .stdin(Stdio::from(OwnedFd::from(child_wire)))
            .stdout(Stdio::null())
            .spawn()?;
        let client = match receive::<1>(&mut wire) {
            Ok([client]) if client != 0 => client,
            _ => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(fail());
            }
        };
        Ok(Self {
            child,
            wire,
            client,
        })
    }

    pub(super) fn pid(&self) -> u32 {
        self.child.id()
    }

    pub(super) fn stop(&mut self) -> io::Result<()> {
        use std::os::unix::process::ExitStatusExt;
        let status = match self.child.try_wait()? {
            Some(status) => status,
            None => {
                self.child.kill()?;
                self.child.wait()?
            }
        };
        if status.signal() != Some(libc::SIGKILL) {
            return Err(fail());
        }
        Ok(())
    }
}

impl Drop for Worker {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

pub(super) fn run() -> io::Result<()> {
    // stdin is an inherited socketpair endpoint. No independent X11 action
    // can arrive over this process's stdin from the outer host.
    let mut wire = unsafe { UnixStream::from_raw_fd(0) };
    if wire.peer_addr().is_err() {
        return Err(fail());
    }
    let (conn, lifetime) = open()?;
    send(&mut wire, [conn.setup().resource_id_base])?;
    let [
        epoch0,
        epoch1,
        window,
        owner_pid,
        left,
        top,
        width,
        height,
        x,
        y,
        master,
        source,
        lease,
        lease_pid,
        cut_mode,
    ] = receive::<15>(&mut wire)?;
    let cut_mode = checked(u8::try_from(cut_mode))?;
    if !accepted_cut_mode(cut_mode) {
        return Err(fail());
    }
    bound(&wire)?;
    let generation = [epoch0, epoch1];
    if generation == [0, 0] {
        return Err(fail());
    }
    let original = pointer::ObservedPoint {
        window,
        owner_pid,
        left: left as i16,
        top: top as i16,
        width: u16::try_from(width).map_err(|_| fail())?,
        height: u16::try_from(height).map_err(|_| fail())?,
        x: x as i16,
        y: y as i16,
    };
    let private = pointer::Pointer {
        master: u16::try_from(master).map_err(|_| fail())?,
        source: u8::try_from(source).map_err(|_| fail())?,
    };
    let root = conn.setup().roots[0].root;
    let native = lifetime.begin(Instant::now() + PHASE)?;
    checked(checked(conn.xinput_xi_query_version(2, 0))?.reply())?;
    checked(checked(conn.xtest_get_version(2, 2))?.reply())?;
    let xi = checked(conn.extension_information(xinput::X11_EXTENSION_NAME))?.ok_or_else(fail)?;
    let xtest = checked(conn.extension_information(xtest::X11_EXTENSION_NAME))?.ok_or_else(fail)?;
    let protocol = selections::Protocol {
        xinput_opcode: xi.major_opcode,
        xinput_first_event: xi.first_event,
        xtest_opcode: xtest.major_opcode,
        record_opcode: 0,
    };
    checked(checked(conn.grab_server())?.check())?;
    send(&mut wire, [epoch0, epoch1, fence(&conn)?])?;
    let selection =
        checked(checked(conn.intern_atom(true, b"_MINTCLAW_COMPUTER_INPUT_CONTROL"))?.reply())?
            .atom;
    if selection == 0
        || checked(checked(conn.get_selection_owner(selection))?.reply())?.owner != lease
        || xres_pid(&conn, lease)? != lease_pid
        || xres_pid(&conn, original.window)? != original.owner_pid
    {
        return Err(fail());
    }
    pointer::position(&conn, root, original, private, 5, |path| {
        send(
            &mut wire,
            [
                epoch0,
                epoch1,
                u32::try_from(path.len()).map_err(|_| fail())?,
            ],
        )?;
        for window in path {
            send(&mut wire, [*window])?;
        }
        if receive::<2>(&mut wire)? != generation {
            return Err(fail());
        }
        Ok(())
    })?;
    // The marker is the last request before input. Arm the exact next sequence
    // on the still-live keeper before any potentially visible write.
    let sequence = fence(&conn)?.checked_add(1).ok_or_else(fail)?;
    let press = button_request(protocol, root, private, xinput::DEVICE_BUTTON_PRESS_EVENT)?;
    send(&mut wire, [epoch0, epoch1, sequence])?;
    if receive::<3>(&mut wire)? != [epoch0, epoch1, sequence] {
        return Err(fail());
    }
    if emit(&conn, press)? != sequence {
        return Err(fail());
    }
    let state = checked(checked(conn.xinput_xi_query_pointer(root, private.master))?.reply())?;
    if state.buttons.first() != Some(&(1 << 5)) {
        return Err(fail());
    }
    if cut_mode == 2 {
        // Crash after the native round trip but before acknowledging it over IPC.
        unsafe {
            libc::raise(libc::SIGKILL);
        }
        return Err(fail());
    }
    send(&mut wire, generation)?;
    // The fault injector terminates this process without a native ungrab or
    // release. Its only live X11 connection is closed by the kernel.
    let _ = receive::<1>(&mut wire);
    drop(native);
    Err(fail())
}
