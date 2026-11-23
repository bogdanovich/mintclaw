//! Conservative pointer selection history on one private X11 server epoch.
//!
//! The owner proves a clean startup before admitting other X11 clients and
//! scans the history through its own marker under the same server grab as the
//! input. Recorded requests may fail server validation. Never use a recorded
//! removal or disconnect to erase a possibly competing registration.

use std::collections::{BTreeMap, BTreeSet};
use std::io;

use x11rb::protocol::{record, xinput, xproto, xtest};

use super::evidence::Observation;

const MAX_SELECTIONS: usize = 4096;
const CORE_EVENT_MASK: u32 = 1 << 11;
const BUTTON_EVENTS: u32 = (1 << 4) | (1 << 5);
const CORE_BUTTON_EVENTS: u32 = (1 << 2) | (1 << 3);

fn unverified() -> io::Error {
    io::Error::new(
        io::ErrorKind::InvalidData,
        "pointer recipient history unverified",
    )
}

fn word16(bytes: &[u8], at: usize) -> io::Result<u16> {
    let value = bytes.get(at..at + 2).ok_or_else(unverified)?;
    Ok(u16::from_ne_bytes(value.try_into().unwrap()))
}

fn word32(bytes: &[u8], at: usize) -> io::Result<u32> {
    let value = bytes.get(at..at + 4).ok_or_else(unverified)?;
    Ok(u32::from_ne_bytes(value.try_into().unwrap()))
}

#[derive(Clone, Copy)]
pub(super) struct Protocol {
    pub xinput_opcode: u8,
    pub xinput_first_event: u8,
    pub xtest_opcode: u8,
    pub record_opcode: u8,
}

#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
enum Device {
    Core,
    Legacy(u8),
    Xi2(u16),
}

#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
struct Registration {
    window: u32,
    client: u32,
    device: Device,
}

#[derive(Default)]
pub(super) struct PointerSelections {
    possible: BTreeSet<Registration>,
    last_xi2: BTreeMap<Registration, (u16, u32)>,
    errors: BTreeSet<(u32, u16)>,
}

fn core(opcode: u8) -> record::Range {
    record::Range {
        core_requests: record::Range8 {
            first: opcode,
            last: opcode,
        },
        ..Default::default()
    }
}

fn extension(major: u8, minor: u8) -> record::Range {
    record::Range {
        ext_requests: record::ExtRange {
            major: record::Range8 {
                first: major,
                last: major,
            },
            minor: record::Range16 {
                first: minor.into(),
                last: minor.into(),
            },
        },
        ..Default::default()
    }
}

impl Protocol {
    pub(super) fn ranges(self) -> Vec<record::Range> {
        vec![
            record::Range {
                errors: record::Range8 {
                    first: 1,
                    last: 255,
                },
                ..Default::default()
            },
            core(xproto::CREATE_WINDOW_REQUEST),
            core(xproto::CHANGE_WINDOW_ATTRIBUTES_REQUEST),
            extension(self.xinput_opcode, xinput::SELECT_EXTENSION_EVENT_REQUEST),
            extension(self.xinput_opcode, xinput::XI_SELECT_EVENTS_REQUEST),
            extension(self.xtest_opcode, xtest::GRAB_CONTROL_REQUEST),
            extension(self.record_opcode, record::REGISTER_CLIENTS_REQUEST),
            extension(self.record_opcode, record::UNREGISTER_CLIENTS_REQUEST),
        ]
    }
}

impl PointerSelections {
    pub(super) fn observe(
        &mut self,
        protocol: Protocol,
        context: u32,
        observation: Observation<'_>,
    ) -> io::Result<()> {
        let (client, sequence, request) = match observation {
            Observation::Request {
                client,
                sequence,
                bytes,
                ..
            } => (client, sequence, bytes),
            Observation::Error { client, sequence } => {
                self.errors.insert((client, sequence));
                if self.errors.len() > MAX_SELECTIONS {
                    return Err(unverified());
                }
                return Ok(());
            }
        };
        let header = request.get(..4).ok_or_else(unverified)?;
        if client == 0 || usize::from(word16(header, 2)?) * 4 != request.len() {
            return Err(unverified());
        }
        let (major, minor) = (header[0], header[1]);
        match major {
            xproto::CREATE_WINDOW_REQUEST => {
                let window = word32(request, 4)?;
                self.last_xi2.retain(|entry, _| entry.window != window);
                self.core_mask(client, request, 28)
            }
            xproto::CHANGE_WINDOW_ATTRIBUTES_REQUEST => self.core_mask(client, request, 8),
            op if op == protocol.xinput_opcode => match minor {
                xinput::XI_SELECT_EVENTS_REQUEST => self.xi2(client, sequence as u16, request),
                xinput::SELECT_EXTENSION_EVENT_REQUEST => self.legacy(protocol, client, request),
                _ => Err(unverified()),
            },
            // An impervious input client can bypass GrabServer. Bootstrap must
            // exclude pre-existing clients. A later such request poisons the epoch.
            op if op == protocol.xtest_opcode && minor == xtest::GRAB_CONTROL_REQUEST => {
                Err(unverified())
            }
            op if op == protocol.record_opcode
                && matches!(
                    minor,
                    record::REGISTER_CLIENTS_REQUEST | record::UNREGISTER_CLIENTS_REQUEST
                ) =>
            {
                if word32(request, 4)? == context {
                    Err(unverified())
                } else {
                    Ok(())
                }
            }
            _ => Err(unverified()),
        }
    }

    pub(super) fn require(
        &self,
        path: &[u32],
        original: u32,
        owner_client: u32,
        master: u16,
        source: u8,
    ) -> io::Result<()> {
        let target = path
            .iter()
            .position(|window| *window == original)
            .ok_or_else(unverified)?;
        // Ordinary Xorg device delivery stops at the first window where
        // XI2 delivers. An unconfirmed selection cannot prove that stopping
        // point. Keep all competing interests, including requests that may
        // have failed, until there is a fresh trusted display epoch.
        let confirmed = |device| {
            self.last_xi2
                .iter()
                .filter_map(|(entry, (sequence, mask))| {
                    let index = path.iter().position(|window| *window == entry.window)?;
                    let matches = matches!(entry.device, Device::Xi2(id) if id == 0
                    || id == device || id == 1 && device == master);
                    if index >= target
                        && entry.client == owner_client
                        && matches
                        && *mask == BUTTON_EVENTS
                        && !self.errors.contains(&(owner_client, *sequence))
                    {
                        Some(index)
                    } else {
                        None
                    }
                })
                .max()
        };
        let master_stop = confirmed(master).ok_or_else(unverified)?;
        let source_stop = confirmed(u16::from(source));
        for interest in &self.possible {
            let Some(index) = path.iter().position(|window| *window == interest.window) else {
                continue;
            };
            if interest.client == owner_client && index >= target {
                continue;
            }
            let reaches = match interest.device {
                Device::Core | Device::Xi2(1) => index >= master_stop,
                Device::Legacy(id) if u16::from(id) == master => index >= master_stop,
                Device::Xi2(id) if id == master => index >= master_stop,
                Device::Xi2(0) => {
                    index >= master_stop || !source_stop.is_some_and(|stop| index < stop)
                }
                Device::Legacy(id) if id == source => !source_stop.is_some_and(|stop| index < stop),
                Device::Xi2(id) if id == u16::from(source) => {
                    !source_stop.is_some_and(|stop| index < stop)
                }
                _ => false,
            };
            if reaches {
                return Err(unverified());
            }
        }
        Ok(())
    }

    fn register(&mut self, window: u32, client: u32, device: Device) -> io::Result<()> {
        if window <= 1 {
            return Err(unverified());
        }
        self.possible.insert(Registration {
            window,
            client,
            device,
        });
        if self.possible.len() > MAX_SELECTIONS {
            return Err(unverified());
        }
        Ok(())
    }

    /// A core notification subscription replaces the previous mask. It
    /// preserves button routing only when neither mask can select buttons.
    pub(super) fn preserves_button_route(&self, client: u32, request: &[u8]) -> io::Result<bool> {
        if client != 0
            && request.get(..2) == Some(&[xproto::GET_INPUT_FOCUS_REQUEST, 0])
            && request.len() == 4
            && word16(request, 2)? == 1
        {
            return Ok(true);
        }
        let notifications = u32::from(xproto::EventMask::STRUCTURE_NOTIFY)
            | u32::from(xproto::EventMask::PROPERTY_CHANGE);
        if request.len() != 16
            || request[0] != xproto::CHANGE_WINDOW_ATTRIBUTES_REQUEST
            || word16(request, 2)? != 4
            || word32(request, 8)? != u32::from(xproto::CW::EVENT_MASK)
        {
            return Ok(false);
        }
        let window = word32(request, 4)?;
        Ok(client != 0
            && window > 1
            && word32(request, 12)? & !notifications == 0
            && !self.possible.contains(&Registration {
                window,
                client,
                device: Device::Core,
            }))
    }

    fn core_mask(&mut self, client: u32, request: &[u8], mask_at: usize) -> io::Result<()> {
        let mask = word32(request, mask_at)?;
        let values_at = mask_at + 4;
        if request.len() != values_at + mask.count_ones() as usize * 4 {
            return Err(unverified());
        }
        if mask & CORE_EVENT_MASK != 0 {
            let index = (mask & (CORE_EVENT_MASK - 1)).count_ones() as usize;
            if word32(request, values_at + index * 4)? & CORE_BUTTON_EVENTS != 0 {
                self.register(word32(request, 4)?, client, Device::Core)?;
            }
        }
        Ok(())
    }

    fn legacy(&mut self, protocol: Protocol, client: u32, request: &[u8]) -> io::Result<()> {
        let window = word32(request, 4)?;
        let count = usize::from(word16(request, 8)?);
        if request.len() != 12 + count * 4 {
            return Err(unverified());
        }
        let first = protocol
            .xinput_first_event
            .checked_add(xinput::DEVICE_BUTTON_PRESS_EVENT)
            .ok_or_else(unverified)?;
        let last = protocol
            .xinput_first_event
            .checked_add(xinput::DEVICE_BUTTON_RELEASE_EVENT)
            .ok_or_else(unverified)?;
        for index in 0..count {
            let event_class = word32(request, 12 + index * 4)?;
            if (first..=last).contains(&(event_class as u8)) {
                let device = u8::try_from(event_class >> 8).map_err(|_| unverified())?;
                self.register(window, client, Device::Legacy(device))?;
            }
        }
        Ok(())
    }

    fn xi2(&mut self, client: u32, sequence: u16, request: &[u8]) -> io::Result<()> {
        let window = word32(request, 4)?;
        let count = word16(request, 8)?;
        let mut masks = request.get(12..).ok_or_else(unverified)?;
        for _ in 0..count {
            let device = word16(masks, 0)?;
            let bytes = 4 * usize::from(word16(masks, 2)?);
            let mask = masks.get(4..4 + bytes).ok_or_else(unverified)?;
            let button_mask = if mask.is_empty() {
                0
            } else {
                word32(mask, 0)? & BUTTON_EVENTS
            };
            let entry = Registration {
                window,
                client,
                device: Device::Xi2(device),
            };
            self.last_xi2.insert(entry, (sequence, button_mask));
            if self.last_xi2.len() > MAX_SELECTIONS {
                return Err(unverified());
            }
            if button_mask != 0 {
                self.register(window, client, Device::Xi2(device))?;
            }
            masks = &masks[4 + bytes..];
        }
        if !masks.is_empty() {
            return Err(unverified());
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const PROTOCOL: Protocol = Protocol {
        xinput_opcode: 131,
        xinput_first_event: 80,
        xtest_opcode: 132,
        record_opcode: 133,
    };
    const ROOT: u32 = 5;
    const TARGET: u32 = 0x200001;
    const GUARD: u32 = 0x300001;
    const APP: u32 = 0x200000;
    const FOREIGN: u32 = 0x300000;

    #[test]
    fn observing_focus_preserves_route_only_for_the_read_only_request() {
        let history = PointerSelections::default();
        let [low, high] = 1u16.to_ne_bytes();
        let query = [xproto::GET_INPUT_FOCUS_REQUEST, 0, low, high];
        assert!(history.preserves_button_route(FOREIGN, &query).unwrap());
        assert!(!history.preserves_button_route(0, &query).unwrap());
        let request = [xproto::SET_INPUT_FOCUS_REQUEST, 0, low, high];
        assert!(!history.preserves_button_route(FOREIGN, &request).unwrap());
        assert!(
            !history
                .preserves_button_route(FOREIGN, &query[..3])
                .unwrap()
        );
    }

    const CONTEXT: u32 = 0x100001;
    const MASTER: u16 = 8;
    const SLAVE: u8 = 10;

    fn xi_select(window: u32, device: u16, event_mask: u32) -> Vec<u8> {
        let mut request = vec![0; 20];
        request[0] = PROTOCOL.xinput_opcode;
        request[1] = xinput::XI_SELECT_EVENTS_REQUEST;
        request[2..4].copy_from_slice(&5u16.to_ne_bytes());
        request[4..8].copy_from_slice(&window.to_ne_bytes());
        request[8..10].copy_from_slice(&1u16.to_ne_bytes());
        request[12..14].copy_from_slice(&device.to_ne_bytes());
        request[14..16].copy_from_slice(&1u16.to_ne_bytes());
        request[16..20].copy_from_slice(&event_mask.to_ne_bytes());
        request
    }

    fn observe(
        history: &mut PointerSelections,
        client: u32,
        sequence: u32,
        bytes: &[u8],
    ) -> io::Result<()> {
        history.observe(
            PROTOCOL,
            CONTEXT,
            Observation::Request {
                client,
                sequence,
                bytes,
                after_button: false,
            },
        )
    }

    fn core_subscribe(window: u32, event_mask: u32) -> [u8; 16] {
        let mut request = [0; 16];
        request[0] = xproto::CHANGE_WINDOW_ATTRIBUTES_REQUEST;
        request[2..4].copy_from_slice(&4u16.to_ne_bytes());
        request[4..8].copy_from_slice(&window.to_ne_bytes());
        request[8..12].copy_from_slice(&u32::from(xproto::CW::EVENT_MASK).to_ne_bytes());
        request[12..16].copy_from_slice(&event_mask.to_ne_bytes());
        request
    }

    #[test]
    fn notifications_cannot_discard_a_potential_button_subscription() {
        let mut history = PointerSelections::default();
        let mask = u32::from(xproto::EventMask::STRUCTURE_NOTIFY)
            | u32::from(xproto::EventMask::PROPERTY_CHANGE);
        let notification = core_subscribe(TARGET, mask);
        let button = core_subscribe(TARGET, u32::from(xproto::EventMask::BUTTON_RELEASE));
        assert!(
            history
                .preserves_button_route(FOREIGN, &notification)
                .unwrap()
        );
        assert!(!history.preserves_button_route(FOREIGN, &button).unwrap());
        observe(&mut history, FOREIGN, 10, &button).unwrap();
        assert!(
            !history
                .preserves_button_route(FOREIGN, &notification)
                .unwrap()
        );
        assert!(history.preserves_button_route(APP, &notification).unwrap());
        assert!(
            history
                .preserves_button_route(FOREIGN, &core_subscribe(GUARD, mask))
                .unwrap()
        );
        let mut suppression = notification;
        suppression[8..12].copy_from_slice(&u32::from(xproto::CW::DONT_PROPAGATE).to_ne_bytes());
        assert!(
            !history
                .preserves_button_route(FOREIGN, &suppression)
                .unwrap()
        );
    }

    #[test]
    fn needs_an_original_selection_and_refuses_competing_wildcards() {
        let mut history = PointerSelections::default();
        let check =
            |value: &PointerSelections| value.require(&[ROOT, TARGET], TARGET, APP, MASTER, SLAVE);
        assert!(check(&history).is_err());
        observe(&mut history, APP, 10, &xi_select(TARGET, 1, BUTTON_EVENTS)).unwrap();
        observe(&mut history, FOREIGN, 11, &xi_select(GUARD, 0, 1 << 4)).unwrap();
        assert!(check(&history).is_ok());
        observe(&mut history, FOREIGN, 12, &xi_select(TARGET, 99, 1 << 4)).unwrap();
        assert!(check(&history).is_ok());
        observe(&mut history, FOREIGN, 13, &xi_select(TARGET, 0, 1 << 5)).unwrap();
        assert!(check(&history).is_err());
    }

    #[test]
    fn unconfirmed_unsubscribe_cannot_erase_a_competing_recipient() {
        let mut history = PointerSelections::default();
        for (client, seq, mask) in [
            (APP, 10, BUTTON_EVENTS),
            (FOREIGN, 11, 1 << 4),
            (FOREIGN, 12, 0),
        ] {
            observe(&mut history, client, seq, &xi_select(TARGET, MASTER, mask)).unwrap();
        }
        assert!(
            history
                .require(&[ROOT, TARGET], TARGET, APP, MASTER, SLAVE)
                .is_err()
        );
    }

    #[test]
    fn ancestor_can_only_be_excluded_by_an_effective_deeper_selection() {
        let mut history = PointerSelections::default();
        let check =
            |value: &PointerSelections| value.require(&[ROOT, TARGET], TARGET, APP, MASTER, SLAVE);
        observe(&mut history, FOREIGN, 9, &xi_select(ROOT, 1, BUTTON_EVENTS)).unwrap();
        observe(&mut history, APP, 10, &xi_select(TARGET, 1, BUTTON_EVENTS)).unwrap();
        assert!(check(&history).is_ok());
        history
            .observe(
                PROTOCOL,
                CONTEXT,
                Observation::Error {
                    client: APP,
                    sequence: 10,
                },
            )
            .unwrap();
        assert!(check(&history).is_err());
        observe(&mut history, APP, 11, &xi_select(TARGET, 1, BUTTON_EVENTS)).unwrap();
        assert!(check(&history).is_ok());
        observe(&mut history, APP, 12, &xi_select(TARGET, 1, 0)).unwrap();
        assert!(check(&history).is_err());
    }

    #[test]
    fn reused_window_cannot_inherit_an_earlier_reply_proof() {
        let mut history = PointerSelections::default();
        observe(&mut history, APP, 10, &xi_select(TARGET, 1, BUTTON_EVENTS)).unwrap();
        assert!(
            history
                .require(&[ROOT, TARGET], TARGET, APP, MASTER, SLAVE)
                .is_ok()
        );
        let mut create = [0u8; 32];
        create[0] = xproto::CREATE_WINDOW_REQUEST;
        create[2..4].copy_from_slice(&8u16.to_ne_bytes());
        create[4..8].copy_from_slice(&TARGET.to_ne_bytes());
        observe(&mut history, APP, 11, &create).unwrap();
        assert!(
            history
                .require(&[ROOT, TARGET], TARGET, APP, MASTER, SLAVE)
                .is_err()
        );
    }

    #[test]
    fn malformed_masks_and_impervious_clients_poison_the_fence() {
        let mut history = PointerSelections::default();
        let mut request = xi_select(TARGET, MASTER, 1 << 4);
        request[14..16].copy_from_slice(&2u16.to_ne_bytes());
        assert!(observe(&mut history, APP, 10, &request).is_err());
        let mut impervious = [
            PROTOCOL.xtest_opcode,
            xtest::GRAB_CONTROL_REQUEST,
            0,
            0,
            1,
            0,
            0,
            0,
        ];
        impervious[2..4].copy_from_slice(&2u16.to_ne_bytes());
        assert!(observe(&mut history, FOREIGN, 11, &impervious).is_err());
    }
}
