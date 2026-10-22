//! Native-input transport boundary; private pinned-backend implementation WIP.
//!
//! A cancelled waiter is not native cleanup. This boundary stops only its own
//! retained X11 transport; the native session/supervisor must independently
//! release its owned input/devices before releasing the host's desktop lease.
//! No timer callback, reconnect, reused descriptor lookup or implicit retry.

use std::io::{self, ErrorKind, IoSlice};
use std::os::unix::io::AsRawFd;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::{Duration, Instant};

use x11rb::rust_connection::{DefaultStream, PollMode, Stream};
use x11rb::utils::RawFdContainer;

struct Inner {
    // Every stop handle retains the actual owner, not just an integer FD.
    stream: DefaultStream,
    stopped: AtomicBool,
    phase: Mutex<Option<Instant>>,
}

pub(super) struct NativeStream {
    inner: Arc<Inner>,
}

#[derive(Clone)]
pub(super) struct IoBoundary {
    inner: Arc<Inner>,
}

pub(super) struct Phase {
    boundary: IoBoundary,
    finished: bool,
}

impl NativeStream {
    /// The caller separately bounds connecting the explicit Unix socket and
    /// starts a phase before the X11 handshake. Construction probes nothing.
    pub(super) fn new(stream: DefaultStream) -> (Self, IoBoundary) {
        let inner = Arc::new(Inner {
            stream,
            stopped: AtomicBool::new(false),
            phase: Mutex::new(None),
        });
        (Self { inner: inner.clone() }, IoBoundary { inner })
    }
}

impl Drop for NativeStream {
    fn drop(&mut self) {
        // Stop handles may retain the FD for identity safety, but losing the
        // actual transport owner must still terminate its X11 connection.
        self.inner.stop();
    }
}

impl Inner {
    fn stop(&self) {
        self.stopped.store(true, Ordering::Release);
        // The Arc retains DefaultStream/its owned FD for this entire call, so
        // late cancellation cannot shut down a different, reused descriptor.
        unsafe { libc::shutdown(self.stream.as_raw_fd(), libc::SHUT_RDWR); }
    }

    fn failure(&self, kind: ErrorKind, message: &'static str) -> io::Error {
        self.stop();
        io::Error::new(kind, message)
    }

    fn phase(&self) -> io::Result<MutexGuard<'_, Option<Instant>>> {
        self.phase.lock().map_err(|_| self.failure(ErrorKind::Other, "native boundary poisoned"))
    }

    fn check(&self, requires_phase: bool) -> io::Result<Option<Instant>> {
        if self.stopped.load(Ordering::Acquire) {
            return Err(io::Error::new(ErrorKind::ConnectionAborted, "native transport stopped"));
        }
        match *self.phase()? {
            Some(deadline) if Instant::now() >= deadline =>
                Err(self.failure(ErrorKind::TimedOut, "native transport deadline")),
            None if requires_phase =>
                Err(self.failure(ErrorKind::ConnectionAborted, "native I/O outside owned phase")),
            deadline => Ok(deadline),
        }
    }

    fn record<T>(&self, result: io::Result<T>) -> io::Result<T> {
        if result.as_ref().is_err_and(|error| error.kind() != ErrorKind::WouldBlock) {
            self.stop();
        }
        result
    }
}

impl IoBoundary {
    /// Single owner, no admission queue. The native session caps this deadline
    /// against its lease/action and short-grab budgets before entering here.
    pub(super) fn begin(&self, deadline: Instant) -> io::Result<Phase> {
        let mut phase = self.inner.phase()?;
        if self.inner.stopped.load(Ordering::Acquire) {
            return Err(io::Error::new(ErrorKind::ConnectionAborted, "native transport stopped"));
        }
        if Instant::now() >= deadline {
            return Err(self.inner.failure(ErrorKind::TimedOut, "expired native phase"));
        }
        if phase.is_some() {
            return Err(io::Error::new(ErrorKind::WouldBlock, "native phase already owned"));
        }
        *phase = Some(deadline);
        Ok(Phase { boundary: self.clone(), finished: false })
    }

    /// Permanent for this native transport/session. A future session has a
    /// separately owned stream; this handle can never be rebound to it.
    pub(super) fn stop(&self) {
        self.inner.stop();
    }

    pub(super) fn stopped(&self) -> bool {
        self.inner.stopped.load(Ordering::Acquire)
    }
}

impl Phase {
    /// Call only after checked native replies and the original transaction's
    /// server fence. This is I/O completion, not an application-effect receipt.
    pub(super) fn finish(mut self) -> io::Result<()> {
        let mut phase = self.boundary.inner.phase()?;
        if self.boundary.stopped() {
            return Err(io::Error::new(ErrorKind::ConnectionAborted, "native transport stopped"));
        }
        if phase.is_none_or(|deadline| Instant::now() >= deadline) {
            return Err(self.boundary.inner.failure(ErrorKind::TimedOut, "native phase expired"));
        }
        *phase = None;
        self.finished = true;
        Ok(())
    }
}

impl Drop for Phase {
    fn drop(&mut self) {
        if !self.finished {
            self.boundary.stop();
        }
    }
}

impl Stream for NativeStream {
    fn poll(&self, mode: PollMode) -> io::Result<()> {
        let events = match mode {
            PollMode::Readable => libc::POLLIN,
            PollMode::Writable => libc::POLLOUT,
            PollMode::ReadAndWritable => libc::POLLIN | libc::POLLOUT,
        };
        loop {
            let deadline = self.inner.check(true)?.expect("checked active native phase");
            let remaining = deadline.saturating_duration_since(Instant::now());
            let milliseconds = remaining.min(Duration::from_millis(20)).as_millis().max(1) as i32;
            let mut fd = libc::pollfd { fd: self.inner.stream.as_raw_fd(), events, revents: 0 };
            // The retained stream owns this FD, including across stop/drop of
            // another handle. poll never waits past the finite phase budget.
            let ready = unsafe { libc::poll(&mut fd, 1, milliseconds) };
            if ready < 0 {
                if io::Error::last_os_error().kind() == ErrorKind::Interrupted {
                    continue;
                }
                return Err(self.inner.failure(ErrorKind::ConnectionAborted, "native poll failed"));
            }
            self.inner.check(true)?;
            if fd.revents & (libc::POLLERR | libc::POLLHUP | libc::POLLNVAL) != 0 {
                return Err(self.inner.failure(ErrorKind::ConnectionAborted, "native peer lost"));
            }
            if ready > 0 {
                return Ok(());
            }
        }
    }

    fn read(&self, data: &mut [u8], descriptors: &mut Vec<RawFdContainer>) -> io::Result<usize> {
        // A nonblocking idle event drain is allowed; idle code cannot issue or
        // wait for requests. Session guards latch destruction/loss separately.
        self.inner.check(false)?;
        let result = self.inner.record(self.inner.stream.read(data, descriptors));
        if matches!(result, Ok(0)) && !data.is_empty() {
            self.inner.stop();
        }
        self.inner.check(false)?;
        result
    }

    fn write(&self, data: &[u8], descriptors: &mut Vec<RawFdContainer>) -> io::Result<usize> {
        self.inner.check(true)?;
        let result = self.inner.record(self.inner.stream.write(data, descriptors));
        self.inner.check(true)?;
        result
    }

    fn write_vectored(&self, data: &[IoSlice<'_>], descriptors: &mut Vec<RawFdContainer>) -> io::Result<usize> {
        self.inner.check(true)?;
        let result = self.inner.record(self.inner.stream.write_vectored(data, descriptors));
        self.inner.check(true)?;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::os::unix::net::UnixStream;
    use std::sync::Barrier;
    use std::thread;

    fn pair() -> (NativeStream, IoBoundary, UnixStream) {
        let (local, peer) = UnixStream::pair().unwrap();
        let (stream, _) = DefaultStream::from_unix_stream(local).unwrap();
        let (stream, boundary) = NativeStream::new(stream);
        (stream, boundary, peer)
    }

    #[test]
    fn socket_poll_is_bounded_and_timeout_is_terminal() {
        let (stream, boundary, _peer) = pair();
        let phase = boundary.begin(Instant::now() + Duration::from_millis(25)).unwrap();
        let start = Instant::now();
        assert_eq!(stream.poll(PollMode::Readable).unwrap_err().kind(), ErrorKind::TimedOut);
        assert!(start.elapsed() < Duration::from_secs(1));
        assert!(boundary.stopped());
        assert!(phase.finish().is_err());
        assert!(boundary.begin(Instant::now() + Duration::from_secs(1)).is_err());
    }

    #[test]
    fn cancel_wakes_owned_poll_without_waiting_for_the_long_deadline() {
        let (stream, boundary, _peer) = pair();
        let _phase = boundary.begin(Instant::now() + Duration::from_secs(30)).unwrap();
        let ready = Arc::new(Barrier::new(2));
        let child_ready = ready.clone();
        let child = thread::spawn(move || { child_ready.wait(); stream.poll(PollMode::Readable) });
        ready.wait();
        let start = Instant::now();
        boundary.stop();
        assert_eq!(child.join().unwrap().unwrap_err().kind(), ErrorKind::ConnectionAborted);
        assert!(start.elapsed() < Duration::from_secs(1));
    }

    #[test]
    fn unfinished_scope_stops_permanently_and_never_rearms() {
        let (_stream, boundary, _peer) = pair();
        let phase = boundary.begin(Instant::now() + Duration::from_secs(1)).unwrap();
        drop(phase);
        assert!(boundary.stopped());
        assert!(boundary.begin(Instant::now() + Duration::from_secs(1)).is_err());
    }

    #[test]
    fn successful_scope_can_end_before_the_next_one_without_a_late_timer() {
        let (stream, boundary, mut peer) = pair();
        let first = boundary.begin(Instant::now() + Duration::from_secs(1)).unwrap();
        assert_eq!(stream.write(b"a", &mut Vec::new()).unwrap(), 1);
        let mut byte = [0];
        peer.read_exact(&mut byte).unwrap();
        assert_eq!(byte, [b'a']);
        first.finish().unwrap();
        let second = boundary.begin(Instant::now() + Duration::from_secs(1)).unwrap();
        peer.write_all(b"b").unwrap();
        stream.poll(PollMode::Readable).unwrap();
        assert_eq!(stream.read(&mut byte, &mut Vec::new()).unwrap(), 1);
        assert_eq!(byte, [b'b']);
        second.finish().unwrap();
        assert!(!boundary.stopped());
    }

    #[test]
    fn no_unowned_request_or_second_owner_is_admitted() {
        let (stream, boundary, _peer) = pair();
        assert_eq!(stream.write(b"x", &mut Vec::new()).unwrap_err().kind(), ErrorKind::ConnectionAborted);
        assert!(boundary.stopped());
        let (_stream, other, _peer) = pair();
        let phase = other.begin(Instant::now() + Duration::from_secs(1)).unwrap();
        assert_eq!(other.begin(Instant::now() + Duration::from_secs(1)).err().unwrap().kind(), ErrorKind::WouldBlock);
        phase.finish().unwrap();
    }

    #[test]
    fn late_stop_retains_old_descriptor_and_cannot_touch_a_new_connection() {
        let (old, old_boundary, _old_peer) = pair();
        let original_fd = old.inner.stream.as_raw_fd();
        drop(old);
        assert!(old_boundary.stopped());
        let (new, new_boundary, mut peer) = pair();
        assert_ne!(new.inner.stream.as_raw_fd(), original_fd);
        let phase = new_boundary.begin(Instant::now() + Duration::from_secs(1)).unwrap();
        old_boundary.stop();
        peer.write_all(b"n").unwrap();
        new.poll(PollMode::Readable).unwrap();
        let mut byte = [0];
        assert_eq!(new.read(&mut byte, &mut Vec::new()).unwrap(), 1);
        assert_eq!(byte, [b'n']);
        phase.finish().unwrap();
    }

    #[test]
    fn peer_loss_is_terminal_and_late_data_cannot_be_accepted_after_stop() {
        let (stream, boundary, peer) = pair();
        let phase = boundary.begin(Instant::now() + Duration::from_secs(1)).unwrap();
        drop(peer);
        assert_eq!(stream.poll(PollMode::Readable).unwrap_err().kind(), ErrorKind::ConnectionAborted);
        assert!(phase.finish().is_err());
        let mut byte = [0];
        assert_eq!(stream.read(&mut byte, &mut Vec::new()).unwrap_err().kind(), ErrorKind::ConnectionAborted);
        assert!(boundary.stopped());
        let (stream, boundary, mut peer) = pair();
        let phase = boundary.begin(Instant::now() + Duration::from_secs(1)).unwrap();
        peer.write_all(b"late").unwrap();
        boundary.stop();
        assert_eq!(stream.read(&mut byte, &mut Vec::new()).unwrap_err().kind(), ErrorKind::ConnectionAborted);
        assert!(phase.finish().is_err());
    }

    #[test]
    fn idle_event_drain_is_nonblocking_without_minting_a_request_budget() {
        let (stream, boundary, mut peer) = pair();
        let mut byte = [0];
        assert_eq!(stream.read(&mut byte, &mut Vec::new()).unwrap_err().kind(), ErrorKind::WouldBlock);
        assert!(!boundary.stopped());
        peer.write_all(b"e").unwrap();
        let start = Instant::now();
        loop {
            match stream.read(&mut byte, &mut Vec::new()) {
                Ok(1) => break,
                Err(error) if error.kind() == ErrorKind::WouldBlock && start.elapsed() < Duration::from_secs(1) =>
                    thread::yield_now(),
                other => panic!("idle event read failed: {other:?}"),
            }
        }
        assert_eq!(byte, [b'e']);
        let phase = boundary.begin(Instant::now() + Duration::from_secs(1)).unwrap();
        phase.finish().unwrap();
    }

    #[test]
    fn expired_scope_cannot_be_finished_and_rearmed_without_any_socket_poll() {
        let (_stream, boundary, _peer) = pair();
        let phase = boundary.begin(Instant::now() + Duration::from_millis(5)).unwrap();
        thread::sleep(Duration::from_millis(10));
        assert_eq!(phase.finish().unwrap_err().kind(), ErrorKind::TimedOut);
        assert!(boundary.stopped());
        assert!(boundary.begin(Instant::now() + Duration::from_secs(1)).is_err());
    }
}
