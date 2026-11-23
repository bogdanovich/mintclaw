//! Qualification child only. NOT an admitted production/private RPC worker.
//! Its parent must own every application/lease witness on the attested Xvfb.

use super::*;
use std::io::{Read, Write};
use std::os::unix::fs::OpenOptionsExt;

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Job {
    window: u32,
    pid: u32,
    lease_window: u32,
    lease_pid: u32,
    chords: Vec<Vec<u8>>,
}

fn wait_for_parent(expected: u8) {
    let mut fd = libc::pollfd { fd: 0, events: libc::POLLIN, revents: 0 };
    assert_eq!(unsafe { libc::poll(&mut fd, 1, 5000) }, 1);
    let mut release = [0u8; 1];
    std::io::stdin().read_exact(&mut release).unwrap();
    assert_eq!(release, [expected]);
}

#[test]
#[ignore = "explicit disposable GTK parent and attested private Xvfb only"]
fn owned_gtk_parent_keyboard_child() {
    let path = std::path::PathBuf::from(std::env::var("MINTCLAW_PRIVATE_NATIVE_JOB").unwrap());
    assert!(path.is_absolute());
    let file =
        std::fs::OpenOptions::new().read(true).custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC).open(path).unwrap();
    let metadata = file.metadata().unwrap();
    assert!(metadata.is_file());
    assert_eq!(metadata.uid(), unsafe { libc::getuid() });
    assert_eq!(metadata.mode() & 0o777, 0o600);
    assert_eq!(metadata.nlink(), 1);
    let mut bytes = Vec::new();
    file.take(16385).read_to_end(&mut bytes).unwrap();
    assert!(bytes.len() <= 16384);
    let job: Job = serde_json::from_slice(&bytes).unwrap();
    let parent = unsafe { libc::getppid() } as u32;
    assert_eq!(job.pid, parent);
    assert_eq!(job.lease_pid, parent);
    let mut session = NativeKeyboard::admit(
        native::endpoint(),
        native::endpoint(),
        Binding { window: job.window, pid: parent, lease_window: job.lease_window, lease_pid: parent },
        native::deadline(),
    )
    .unwrap();
    println!("MINTCLAW_NATIVE_KEYBOARD_ADMITTED={}", session.pair.keyboard);
    std::io::stdout().flush().unwrap();
    wait_for_parent(1);
    let receipt = session.dispatch(&job.chords, native::deadline());
    assert!(receipt.dispatched && receipt.acknowledged && !receipt.cleanup_confirmed, "{receipt:?}");
    println!("MINTCLAW_NATIVE_KEYBOARD_DISPATCH_ACKNOWLEDGED");
    std::io::stdout().flush().unwrap();
    // Qualification parent independently pumps/reads its owned GTK state, then
    // releases the still-live pair. No sleep or server ACK stands for effects.
    wait_for_parent(2);
    assert!(session.finish());
    println!("MINTCLAW_NATIVE_KEYBOARD_QUALIFICATION=completed_cleanup_confirmed");
}
