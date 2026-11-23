//! Bounded RECORD metadata for the retained private pointer.
//!
//! The surrounding owner starts recording before dispatch and holds the original
//! server transaction from the fence through any release. A fence reports wire
//! facts. The caller still verifies the immutable lease, target and device epoch.

use std::io::{self, Read};

use x11rb::protocol::{record, xproto};
use x11rb::x11_utils::TryParse;
use zeroize::Zeroizing;

const MAX_FENCE_BYTES: usize = 1024 * 1024;
const MAX_REPLY_BYTES: usize = 64 * 1024;
const MAX_RECORDS: usize = 4096;
const MAX_BUTTON_RECEIPTS: usize = 8;
const CLIENT_SEQUENCE: u8 = 4;
const FROM_SERVER: u8 = 0;
const FROM_CLIENT: u8 = 1;
const GENERIC_EVENT: u8 = 35;

#[derive(Clone, Copy)]
pub(super) struct Fence {
    pub reply_sequence: u16,
    pub control_client: u32,
    pub marker_sequence: u32,
    pub pointer_device: u16,
    pub xinput_opcode: u8,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(super) struct ButtonReceipt {
    pub recipient: u32,
    pub window: u32,
    pub root: u32,
    pub kind: u16,
    pub detail: u32,
}

#[derive(Default)]
pub(super) struct Evidence {
    pub buttons: Vec<ButtonReceipt>,
    pub requests_after_first_button: usize,
}

#[derive(Default)]
struct Budget {
    bytes: usize,
    records: usize,
}

impl Budget {
    fn record(&mut self) -> io::Result<()> {
        self.records += 1;
        if self.records > MAX_RECORDS {
            return Err(invalid("record entry budget exhausted"));
        }
        Ok(())
    }
}

fn invalid(reason: &'static str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, reason)
}

fn native16(bytes: &[u8], offset: usize) -> u16 {
    u16::from_ne_bytes(bytes[offset..offset + 2].try_into().unwrap())
}

fn native32(bytes: &[u8], offset: usize) -> u32 {
    u32::from_ne_bytes(bytes[offset..offset + 4].try_into().unwrap())
}

struct RecordedFrame {
    xid_base: u32,
    category: u8,
    data: Zeroizing<Vec<u8>>,
}

fn frame(
    wire: &mut impl Read,
    sequence: u16,
    budget: &mut Budget,
) -> io::Result<Option<RecordedFrame>> {
    let mut header = [0; 32];
    wire.read_exact(&mut header)?;
    budget.bytes += header.len();
    if budget.bytes > MAX_FENCE_BYTES {
        return Err(invalid("record fence byte budget exhausted"));
    }
    // The metadata socket itself may receive a core MappingNotify. It never
    // selects the private device as its client pointer. The enclosing owner
    // records mapping mutation requests and validates the private device.
    if header[0] == xproto::MAPPING_NOTIFY_EVENT {
        let (notice, trailing) = xproto::MappingNotifyEvent::try_parse(&header)
            .map_err(|_| invalid("invalid native mapping notice"))?;
        if !trailing.is_empty()
            || !matches!(
                notice.request,
                xproto::Mapping::MODIFIER | xproto::Mapping::KEYBOARD | xproto::Mapping::POINTER
            )
        {
            return Err(invalid("invalid native mapping notice"));
        }
        budget.record()?;
        return Ok(None);
    }
    if header[0] != 1 || native16(&header, 2) != sequence {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!(
                "unexpected record packet type {} sequence {}",
                header[0],
                native16(&header, 2)
            ),
        ));
    }
    let words = native32(&header, 4) as usize;
    if words > MAX_REPLY_BYTES / 4 {
        return Err(invalid("record reply exceeds byte limit"));
    }
    let length = header.len() + words * 4;
    budget.bytes += words * 4;
    if budget.bytes > MAX_FENCE_BYTES {
        return Err(invalid("record fence byte budget exhausted"));
    }
    let mut packet = Zeroizing::new(vec![0; length]);
    packet[..header.len()].copy_from_slice(&header);
    wire.read_exact(&mut packet[header.len()..])?;
    let (reply, trailing) = record::EnableContextReply::try_parse(&packet)
        .map_err(|_| invalid("invalid record reply"))?;
    let data = Zeroizing::new(reply.data);
    if reply.client_swapped || reply.element_header != CLIENT_SEQUENCE || !trailing.is_empty() {
        return Err(invalid("record reply framing changed"));
    }
    Ok(Some(RecordedFrame {
        xid_base: reply.xid_base,
        category: reply.category,
        data,
    }))
}

/// Trusted Xorg RECORD supplies a 32-byte GE delivery prefix. Only a private
/// button receipt is retained. Other event data stays inside the frame and
/// never becomes result data, a logged payload or release authority.
#[cfg(test)]
pub(super) fn read_fence(wire: &mut impl Read, fence: Fence) -> io::Result<Evidence> {
    read_fence_observing(wire, fence, |_| Ok(()))
}

pub(super) enum Observation<'a> {
    Request {
        client: u32,
        sequence: u32,
        bytes: &'a [u8],
        after_button: bool,
    },
    Error {
        client: u32,
        sequence: u16,
    },
}

pub(super) fn read_fence_observing(
    wire: &mut impl Read,
    fence: Fence,
    mut observe: impl FnMut(Observation<'_>) -> io::Result<()>,
) -> io::Result<Evidence> {
    let mut budget = Budget::default();
    let mut evidence = Evidence::default();
    loop {
        let Some(reply) = frame(wire, fence.reply_sequence, &mut budget)? else {
            continue;
        };
        if reply.xid_base == 0 || reply.data.is_empty() {
            return Err(invalid("record fence lacks a client or payload"));
        }
        match reply.category {
            FROM_SERVER => {
                let events = reply.data.chunks_exact(32);
                if !events.remainder().is_empty() {
                    return Err(invalid("record event prefix is truncated"));
                }
                for event in events {
                    budget.record()?;
                    if event[0] == 0 {
                        observe(Observation::Error {
                            client: reply.xid_base,
                            sequence: native16(event, 2),
                        })?;
                        continue;
                    }
                    if event[0] != GENERIC_EVENT
                        || event[1] != fence.xinput_opcode
                        || !matches!(native16(event, 8), 4 | 5)
                        || native16(event, 10) != fence.pointer_device
                    {
                        continue;
                    }
                    if evidence.buttons.len() == MAX_BUTTON_RECEIPTS {
                        return Err(invalid("too many private button receipts"));
                    }
                    evidence.buttons.push(ButtonReceipt {
                        recipient: reply.xid_base,
                        window: native32(event, 24),
                        root: native32(event, 20),
                        kind: native16(event, 8),
                        detail: native32(event, 16),
                    });
                }
            }
            FROM_CLIENT => {
                let mut pending = &reply.data[..];
                while !pending.is_empty() {
                    budget.record()?;
                    let prefix = pending
                        .get(..8)
                        .ok_or_else(|| invalid("truncated record request header"))?;
                    let words = usize::from(native16(prefix, 6));
                    let length = 4 + words * 4;
                    if words == 0 || length > pending.len() {
                        return Err(invalid("invalid record request length"));
                    }
                    let payload = &pending[4..length];
                    pending = &pending[length..];
                    let marker = reply.xid_base == fence.control_client
                        && prefix[4] == xproto::GET_INPUT_FOCUS_REQUEST
                        && prefix[5] == 0
                        && words == 1;
                    if marker {
                        let sequence = native32(prefix, 0);
                        if sequence == fence.marker_sequence && pending.is_empty() {
                            return Ok(evidence);
                        }
                        if sequence >= fence.marker_sequence {
                            return Err(invalid("record fence marker ordering changed"));
                        }
                    } else {
                        observe(Observation::Request {
                            client: reply.xid_base,
                            sequence: native32(prefix, 0),
                            bytes: payload,
                            after_button: !evidence.buttons.is_empty(),
                        })?;
                        if !evidence.buttons.is_empty() {
                            evidence.requests_after_first_button += 1;
                        }
                    }
                }
            }
            _ => return Err(invalid("record context terminated or restarted")),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const FENCE: Fence = Fence {
        reply_sequence: 4,
        control_client: 0x100000,
        marker_sequence: 10,
        pointer_device: 11,
        xinput_opcode: 131,
    };

    fn reply(category: u8, client: u32, data: &[u8]) -> Vec<u8> {
        assert_eq!(data.len() % 4, 0);
        let mut packet = vec![0u8; 32];
        packet[0] = 1;
        packet[1] = category;
        packet[2..4].copy_from_slice(&FENCE.reply_sequence.to_ne_bytes());
        packet[4..8].copy_from_slice(&((data.len() / 4) as u32).to_ne_bytes());
        packet[8] = CLIENT_SEQUENCE;
        packet[12..16].copy_from_slice(&client.to_ne_bytes());
        packet.extend_from_slice(data);
        packet
    }

    fn request(sequence: u32, opcode: u8) -> [u8; 8] {
        let mut request = [0u8; 8];
        request[..4].copy_from_slice(&sequence.to_ne_bytes());
        request[4] = opcode;
        request[6..8].copy_from_slice(&1u16.to_ne_bytes());
        request
    }

    fn marker(sequence: u32) -> Vec<u8> {
        reply(
            FROM_CLIENT,
            FENCE.control_client,
            &request(sequence, xproto::GET_INPUT_FOCUS_REQUEST),
        )
    }

    fn button() -> [u8; 32] {
        let mut event = [0u8; 32];
        event[0] = GENERIC_EVENT;
        event[1] = FENCE.xinput_opcode;
        event[8..10].copy_from_slice(&4u16.to_ne_bytes());
        event[10..12].copy_from_slice(&FENCE.pointer_device.to_ne_bytes());
        event[16..20].copy_from_slice(&5u32.to_ne_bytes());
        event[20..24].copy_from_slice(&31u32.to_ne_bytes());
        event[24..28].copy_from_slice(&0x200001u32.to_ne_bytes());
        event
    }

    #[test]
    fn keeps_two_receivers_of_the_same_window_distinct() {
        let wire = [
            reply(FROM_SERVER, 0x200000, &button()),
            reply(FROM_SERVER, 0x300000, &button()),
            marker(9),
            marker(10),
        ]
        .concat();
        let output = read_fence(&mut &wire[..], FENCE).unwrap();
        assert_eq!(output.buttons.len(), 2);
        assert_eq!(output.buttons[0].window, output.buttons[1].window);
        assert_ne!(output.buttons[0].recipient, output.buttons[1].recipient);
        assert_eq!(output.requests_after_first_button, 0);
    }

    #[test]
    fn distinguishes_request_before_and_after_the_observed_press() {
        let external = reply(FROM_CLIENT, 0x300000, &request(2, 26));
        let wire = [
            external.clone(),
            reply(FROM_SERVER, 0x200000, &button()),
            external,
            marker(10),
        ]
        .concat();
        let output = read_fence(&mut &wire[..], FENCE).unwrap();
        assert_eq!(output.buttons.len(), 1);
        assert_eq!(output.requests_after_first_button, 1);
    }

    #[test]
    fn rejects_incomplete_context_and_foreign_or_future_markers() {
        for wire in [
            reply(FROM_SERVER, 0x200000, &button()),
            [reply(FROM_SERVER, 0x200000, &button()), reply(5, 0, &[])].concat(),
            reply(
                FROM_CLIENT,
                0x400000,
                &request(10, xproto::GET_INPUT_FOCUS_REQUEST),
            ),
            marker(11),
            reply(FROM_SERVER, 0x200000, &[0; 28]),
        ] {
            assert!(read_fence(&mut &wire[..], FENCE).is_err());
        }
    }

    #[test]
    fn known_core_mapping_notice_does_not_replace_the_record_marker() {
        let mut notice = vec![0; 32];
        notice[0] = xproto::MAPPING_NOTIFY_EVENT;
        notice[4] = u8::from(xproto::Mapping::KEYBOARD);
        let wire = [
            reply(FROM_SERVER, 0x200000, &button()),
            notice.clone(),
            marker(10),
        ]
        .concat();
        assert_eq!(read_fence(&mut &wire[..], FENCE).unwrap().buttons.len(), 1);
        assert!(read_fence(&mut &notice[..], FENCE).is_err());
        notice[0] |= 0x80;
        let wire = [notice, marker(10)].concat();
        assert!(read_fence(&mut &wire[..], FENCE).is_err());
    }

    #[test]
    fn rejects_unbounded_fences() {
        let event = reply(FROM_SERVER, 0x200000, &button());
        let wire = [event.repeat(MAX_BUTTON_RECEIPTS + 1), marker(10)].concat();
        assert!(read_fence(&mut &wire[..], FENCE).is_err());
        let event = reply(FROM_SERVER, 0x200000, &[0; MAX_REPLY_BYTES]);
        let wire = [event.repeat(17), marker(10)].concat();
        assert!(read_fence(&mut &wire[..], FENCE).is_err());
    }
}
