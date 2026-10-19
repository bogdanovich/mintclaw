//! Private, default-off native prerequisite; not an authority or model tool.
//!
//! Address one bounded chord to the supplied X11 window without propagation,
//! focus changes, AT-SPI lookup, clipboard access, keymap edits or fallback.
//! The caller must retain its original native-window/process guard and input
//! lease, bind this exact server/connection, and impose transport deadlines.
//! A numeric XID alone is NOT an incarnation or authorization proof.
//!
//! Stop is cooperative between requests, not preemption of a blocked request.
//! Receipts describe server acknowledgements, NEVER application effects or
//! independently attested held-state cleanup. Unknown outcomes must not replay.

use x11rb::connection::Connection;
use x11rb::protocol::xproto::*;
use x11rb::rust_connection::RustConnection;

const MAX_KEYS: usize = 8;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Outcome {
    Refused,
    Completed,
    Stopped,
    Unknown,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Receipt {
    pub outcome: Outcome,
    pub press_attempts: usize,
    pub press_acknowledgements: usize,
    pub release_attempts: usize,
    pub release_acknowledgements: usize,
}

impl Receipt {
    fn refused() -> Self {
        Self {
            outcome: Outcome::Refused,
            press_attempts: 0,
            press_acknowledgements: 0,
            release_attempts: 0,
            release_acknowledgements: 0,
        }
    }
}

/// Native dispatch only. `should_stop` must be nonblocking and remains latched
/// by this operation once true. It may combine a cancellation token, deadline
/// and the host's already-retained authority; it must not reacquire authority.
/// Every attempted press receives a release attempt, even when its response was
/// lost. Failed releases do not prevent remaining cleanup attempts. There is no
/// retry or replacement connection. No key can remain intentionally held past
/// this call. A panic/transport loss still requires the host's independent stop
/// and cleanup protocol; a dropped waiter is not cleanup confirmation.
pub fn dispatch(
    connection: &RustConnection,
    root: Window,
    window: Window,
    keys: &[u8],
    mut should_stop: impl FnMut() -> bool,
) -> Receipt {
    let setup = connection.setup();
    if window <= 1 || setup.roots.iter().any(|screen| screen.root == window)
        || !setup.roots.iter().any(|screen| screen.root == root)
    {
        return Receipt::refused();
    }
    if should_stop() {
        return Receipt { outcome: Outcome::Stopped, ..Receipt::refused() };
    }
    // Read the existing map; never borrow/remap a keycode. Synthetic key
    // presses do not change server modifier state, so each event explicitly
    // carries only this chord's modifiers (never the user's held modifiers).
    let mapping = match connection.get_modifier_mapping() {
        Ok(cookie) => match cookie.reply() {
            Ok(mapping) => mapping,
            Err(_) => return Receipt::refused(),
        },
        Err(_) => return Receipt::refused(),
    };
    let mut modifiers = [0u16; 256];
    let per_modifier = mapping.keycodes.len() / 8;
    if per_modifier > 0 {
        for (index, codes) in mapping.keycodes.chunks(per_modifier).enumerate() {
            for code in codes.iter().filter(|code| **code != 0) {
                modifiers[usize::from(*code)] |= 1 << index;
            }
        }
    }
    run(keys, setup.min_keycode, setup.max_keycode, &modifiers, should_stop, |key, press, state| {
        let event = KeyPressEvent {
            response_type: if press { KEY_PRESS_EVENT } else { KEY_RELEASE_EVENT },
            detail: key,
            sequence: 0,
            time: x11rb::CURRENT_TIME,
            root,
            event: window,
            child: x11rb::NONE,
            root_x: 0,
            root_y: 0,
            event_x: 0,
            event_y: 0,
            state: KeyButMask::from(state),
            same_screen: true,
        };
        // false is essential: a child that did not select key events must not
        // route the event to its parent or another window. Never InputFocus.
        connection.send_event(false, window,
            if press { EventMask::KEY_PRESS } else { EventMask::KEY_RELEASE }, event)
            .map_err(|_| ())?.check().map_err(|_| ())
    })
}

fn run(
    keys: &[u8],
    minimum: u8,
    maximum: u8,
    modifiers: &[u16; 256],
    mut should_stop: impl FnMut() -> bool,
    mut edge: impl FnMut(u8, bool, u16) -> Result<(), ()>,
) -> Receipt {
    let mut receipt = Receipt::refused();
    if keys.is_empty() || keys.len() > MAX_KEYS || minimum > maximum
        || keys.iter().enumerate().any(|(index, key)|
            *key < minimum || *key > maximum || keys[..index].contains(key))
    {
        return receipt;
    }
    if should_stop() {
        receipt.outcome = Outcome::Stopped;
        return receipt;
    }
    receipt.outcome = Outcome::Completed;
    for key in keys {
        if should_stop() {
            receipt.outcome = Outcome::Stopped;
            break;
        }
        // Own the cleanup obligation BEFORE issuing an uncertain press.
        receipt.press_attempts += 1;
        let state = keys[..receipt.press_attempts - 1].iter()
            .fold(0, |mask, held| mask | modifiers[usize::from(*held)]);
        if edge(*key, true, state).is_err() {
            receipt.outcome = Outcome::Unknown;
            break;
        }
        receipt.press_acknowledgements += 1;
    }
    if receipt.outcome == Outcome::Completed && should_stop() {
        receipt.outcome = Outcome::Stopped;
    }
    for (index, key) in keys[..receipt.press_attempts].iter().enumerate().rev() {
        receipt.release_attempts += 1;
        let state = keys[..=index].iter()
            .fold(0, |mask, held| mask | modifiers[usize::from(*held)]);
        if edge(*key, false, state).is_ok() {
            receipt.release_acknowledgements += 1;
        } else {
            receipt.outcome = Outcome::Unknown;
        }
    }
    receipt
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::{Cell, RefCell};

    #[test]
    fn valid_chord_releases_in_reverse_order() {
        let edges = RefCell::new(Vec::new());
        let receipt = run(&[37, 38], 8, 255, &[0; 256], || false, |key, press, _| {
            edges.borrow_mut().push((key, press)); Ok(())
        });
        assert_eq!(*edges.borrow(), [(37, true), (38, true), (38, false), (37, false)]);
        assert_eq!(receipt, Receipt { outcome: Outcome::Completed, press_attempts: 2,
            press_acknowledgements: 2, release_attempts: 2, release_acknowledgements: 2 });
    }

    #[test]
    fn invalid_or_pre_stopped_batches_deliver_nothing() {
        for keys in [&[][..], &[7], &[38, 38], &[8, 9, 10, 11, 12, 13, 14, 15, 16]] {
            assert_eq!(run(keys, 8, 255, &[0; 256], || panic!("invalid request"), |_, _, _| panic!("no input")),
                Receipt::refused());
        }
        for (minimum, maximum, keys) in [(8, 254, &[255][..]), (10, 9, &[10][..])] {
            assert_eq!(run(keys, minimum, maximum, &[0; 256],
                || panic!("invalid range"), |_, _, _| panic!("no input")), Receipt::refused());
        }
        assert_eq!(run(&[38], 8, 255, &[0; 256], || true, |_, _, _| panic!("already stopped")).outcome,
            Outcome::Stopped);
    }

    #[test]
    fn stop_at_each_boundary_never_dispatches_later_keys() {
        for stop_after in 0..=3 {
            let presses = Cell::new(0);
            let edges = RefCell::new(Vec::new());
            let receipt = run(&[37, 38, 39], 8, 255, &[0; 256], || presses.get() >= stop_after, |key, press, _| {
                edges.borrow_mut().push((key, press));
                if press { presses.set(presses.get() + 1); }
                Ok(())
            });
            assert_eq!(receipt.outcome, Outcome::Stopped);
            assert_eq!(receipt.press_attempts, stop_after);
            assert_eq!(receipt.release_acknowledgements, stop_after);
            assert_eq!(edges.borrow().len(), stop_after * 2);
        }
    }

    #[test]
    fn failed_press_stays_unknown_even_after_acknowledged_releases() {
        for failed_key in [37, 38, 39] {
            let edges = RefCell::new(Vec::new());
            let receipt = run(&[37, 38, 39], 8, 255, &[0; 256], || false, |key, press, _| {
                edges.borrow_mut().push((key, press));
                if press && key == failed_key { Err(()) } else { Ok(()) }
            });
            assert_eq!(receipt.outcome, Outcome::Unknown);
            assert_eq!(receipt.press_attempts, receipt.press_acknowledgements + 1);
            assert_eq!(receipt.press_attempts, receipt.release_acknowledgements);
            assert!(edges.borrow().contains(&(failed_key, false)));
        }
    }

    #[test]
    fn every_release_is_attempted_after_failures() {
        for failure_mask in 1..8 {
            let receipt = run(&[37, 38, 39], 8, 255, &[0; 256], || false, |key, press, _| {
                if !press && failure_mask & (1 << (key - 37)) != 0 { Err(()) } else { Ok(()) }
            });
            assert_eq!(receipt.outcome, Outcome::Unknown);
            assert_eq!(receipt.release_attempts, 3);
            assert_eq!(receipt.release_acknowledgements, 3 - (failure_mask as u32).count_ones() as usize);
        }
    }
    #[test]
    fn chord_events_carry_only_owned_modifiers_on_both_edges() {
        let mut modifiers = [0; 256];
        modifiers[37] = 4;
        modifiers[50] = 1;
        let states = RefCell::new(Vec::new());
        let receipt = run(&[37, 50, 38], 8, 255, &modifiers, || false, |_, _, state| {
            states.borrow_mut().push(state); Ok(())
        });
        assert_eq!(receipt.outcome, Outcome::Completed);
        assert_eq!(*states.borrow(), [0, 4, 5, 5, 5, 4]);
    }
}

#[cfg(test)]
mod native {
    use super::*;
    use std::cell::RefCell;
    use x11rb::protocol::Event;

    struct Fixture {
        app: RustConnection,
        worker: RustConnection,
        root: Window,
        target: Window,
        guard: Window,
    }

    impl Fixture {
        fn open() -> Self {
            assert_eq!(std::env::var("MINTCLAW_WINDOW_KEYS_NATIVE").as_deref(), Ok("1"),
                "native tests require explicit disposable-window opt-in");
            let (app, screen) = RustConnection::connect(None).unwrap();
            let root = app.setup().roots[screen].root;
            let create = || {
                let window = app.generate_id().unwrap();
                // Identical geometry, no names/PID labels or AT-SPI tree.
                // Unmapped fixtures never steal focus or receive physical input.
                app.create_window(x11rb::COPY_DEPTH_FROM_PARENT, window, root,
                    0, 0, 10, 10, 0, WindowClass::INPUT_OUTPUT, 0,
                    &CreateWindowAux::new().event_mask(EventMask::KEY_PRESS | EventMask::KEY_RELEASE))
                    .unwrap().check().unwrap();
                window
            };
            let target = create();
            let guard = create();
            let (worker, _) = RustConnection::connect(None).unwrap();
            Self { app, worker, root, target, guard }
        }

        fn events(&self) -> Vec<(Window, u8, bool)> {
            self.app.get_input_focus().unwrap().reply().unwrap();
            let mut events = Vec::new();
            while let Some(event) = self.app.poll_for_event().unwrap() {
                match event {
                    Event::KeyPress(event) => events.push((event.event, event.detail, true)),
                    Event::KeyRelease(event) => events.push((event.event, event.detail, false)),
                    _ => {}
                }
            }
            events
        }

        fn focus(&self) -> Window {
            self.app.get_input_focus().unwrap().reply().unwrap().focus
        }
    }

    #[test]
    #[ignore = "explicit Linux native fixture only"]
    fn addressed_chord_preserves_guard_and_focus() {
        let fixture = Fixture::open();
        let focus = fixture.focus();
        let first = fixture.worker.setup().min_keycode;
        let receipt = dispatch(&fixture.worker, fixture.root, fixture.target,
            &[first, first + 1], || false);
        assert_eq!(receipt.outcome, Outcome::Completed);
        assert_eq!(fixture.events(), [(fixture.target, first, true),
            (fixture.target, first + 1, true), (fixture.target, first + 1, false),
            (fixture.target, first, false)]);
        assert_eq!(fixture.focus(), focus);
        fixture.app.get_geometry(fixture.guard).unwrap().reply().unwrap();
        fixture.app.get_geometry(fixture.target).unwrap().reply().unwrap();
    }

    #[test]
    #[ignore = "explicit Linux native fixture only"]
    fn stop_after_independently_observed_press_delivers_only_its_release() {
        let fixture = Fixture::open();
        let focus = fixture.focus();
        let first = fixture.worker.setup().min_keycode;
        let observed = RefCell::new(Vec::new());
        let receipt = dispatch(&fixture.worker, fixture.root, fixture.target,
            &[first, first + 1, first + 2], || {
                observed.borrow_mut().extend(fixture.events());
                !observed.borrow().is_empty()
            });
        observed.borrow_mut().extend(fixture.events());
        assert_eq!(receipt, Receipt { outcome: Outcome::Stopped, press_attempts: 1,
            press_acknowledgements: 1, release_attempts: 1, release_acknowledgements: 1 });
        assert_eq!(*observed.borrow(), [(fixture.target, first, true), (fixture.target, first, false)]);
        assert_eq!(fixture.focus(), focus);
    }

    #[test]
    #[ignore = "explicit Linux native fixture only"]
    fn unselected_child_does_not_propagate_to_parent() {
        let fixture = Fixture::open();
        let child = fixture.app.generate_id().unwrap();
        fixture.app.create_window(x11rb::COPY_DEPTH_FROM_PARENT, child, fixture.target,
            0, 0, 5, 5, 0, WindowClass::INPUT_OUTPUT, 0, &CreateWindowAux::new())
            .unwrap().check().unwrap();
        let receipt = dispatch(&fixture.worker, fixture.root, child,
            &[fixture.worker.setup().min_keycode], || false);
        assert_eq!(receipt.outcome, Outcome::Completed);
        assert_eq!(receipt.release_acknowledgements, 1);
        assert!(fixture.events().is_empty(), "server acknowledgement is not an application effect");
    }

    #[test]
    #[ignore = "explicit Linux native fixture only"]
    fn destroyed_target_is_unknown_not_redirected_or_retried() {
        let fixture = Fixture::open();
        fixture.app.destroy_window(fixture.target).unwrap().check().unwrap();
        let receipt = dispatch(&fixture.worker, fixture.root, fixture.target,
            &[fixture.worker.setup().min_keycode], || false);
        assert_eq!(receipt.outcome, Outcome::Unknown);
        assert_eq!(receipt.press_attempts, 1);
        assert_eq!(receipt.press_acknowledgements, 0);
        assert_eq!(receipt.release_attempts, 1);
        assert_eq!(receipt.release_acknowledgements, 0);
        assert!(fixture.events().is_empty());
        fixture.app.get_geometry(fixture.guard).unwrap().reply().unwrap();
    }
}
