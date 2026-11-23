//! Continuous request history for the private keyboard's original server.
//!
//! XKB snapshots and notifications do not prove an unchanged mapping: Xorg's
//! SetModifierMap can silently mutate and restore it. RECORD observes those
//! requests before execution. A marker from the SAME grabbed control client
//! joins that history to each input/release decision. No keyboard events or
//! text requests are selected, logged or retained. This is not a routing proof.

use std::io::{IoSlice, Read};

use x11rb::protocol::record::{self, ConnectionExt as _};
use x11rb::rust_connection::{PollMode, Stream as _};
use x11rb::x11_utils::TryParse as _;
use zeroize::Zeroizing;

use super::*;

const PAYLOAD_LIMIT: usize = 65536;
const FENCE_BYTES_LIMIT: usize = 1024 * 1024;
const FENCE_RECORDS_LIMIT: usize = 4096;
const SEQUENCE_HEADER: u8 = 4;

#[derive(Default)]
struct FenceBudget {
    bytes: usize,
    records: usize,
}

impl FenceBudget {
    fn bytes(&mut self, count: usize) -> Result<()> {
        self.bytes = self.bytes.checked_add(count).ok_or_else(invalid)?;
        if self.bytes > FENCE_BYTES_LIMIT {
            return Err(invalid());
        }
        Ok(())
    }

    fn notice(&mut self) -> Result<()> {
        self.records = self.records.checked_add(1).ok_or_else(invalid)?;
        if self.records > FENCE_RECORDS_LIMIT {
            return Err(invalid());
        }
        Ok(())
    }
}

fn invalid() -> io::Error {
    io::Error::new(ErrorKind::InvalidData, "native mutation history unavailable")
}

struct Frame {
    category: u8,
    element_header: u8,
    client_swapped: bool,
    client: u32,
    data: Zeroizing<Vec<u8>>,
}

impl std::fmt::Debug for Frame {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("native mutation metadata (payload omitted)")
    }
}

/// Bound the fixed header before either allocation or a payload read. Both
/// the encoded frame and the SDK's decoded copy are zeroed on every exit.
fn read_packet(reader: &mut impl Read, sequence: u16, budget: &mut FenceBudget) -> Result<Option<Frame>> {
    let mut header = [0u8; 32];
    reader.read_exact(&mut header)?;
    budget.bytes(32)?;
    // X11 delivers native core MappingNotify even to an otherwise exclusive
    // RECORD connection. It is a fixed-size notice, never a record/marker or
    // permission. Unknown events and forged SendEvent variants remain terminal.
    if header[0] == xproto::MAPPING_NOTIFY_EVENT {
        let (notice, remaining) = xproto::MappingNotifyEvent::try_parse(&header).map_err(|_| invalid())?;
        if !remaining.is_empty()
            || !matches!(
                notice.request,
                xproto::Mapping::MODIFIER | xproto::Mapping::KEYBOARD | xproto::Mapping::POINTER
            )
        {
            return Err(invalid());
        }
        budget.notice()?;
        return Ok(None);
    }
    if header[0] != 1 || u16::from_ne_bytes(header[2..4].try_into().unwrap()) != sequence {
        return Err(invalid());
    }
    let words = u32::from_ne_bytes(header[4..8].try_into().unwrap());
    if words > (PAYLOAD_LIMIT / 4) as u32 {
        return Err(invalid());
    }
    budget.bytes(words as usize * 4)?;
    let mut bytes = Zeroizing::new(vec![0u8; 32 + words as usize * 4]);
    bytes[..32].copy_from_slice(&header);
    reader.read_exact(&mut bytes[32..])?;
    let (reply, remaining) = record::EnableContextReply::try_parse(&bytes).map_err(|_| invalid())?;
    let frame = Frame {
        category: reply.category,
        element_header: reply.element_header,
        client_swapped: reply.client_swapped,
        client: reply.xid_base,
        data: Zeroizing::new(reply.data),
    };
    if !remaining.is_empty() {
        return Err(invalid());
    }
    Ok(Some(frame))
}

/// This role exclusively owns the fresh metadata connection after enable.
/// No managed X11 read may prefetch its stream into a separate reply cache.
struct Metadata {
    native: NativeConnection,
    sequence: u16,
}

impl Read for Metadata {
    fn read(&mut self, bytes: &mut [u8]) -> Result<usize> {
        loop {
            let mut descriptors = Vec::new();
            let result = self.native.connection.stream().read(bytes, &mut descriptors);
            if !descriptors.is_empty() { return Err(invalid()) }
            match result {
                Err(error) if error.kind() == ErrorKind::WouldBlock => {
                    self.native.connection.stream().poll(PollMode::Readable)?;
                }
                result => return result,
            }
        }
    }
}

#[derive(Clone, Copy)]
struct Classes {
    xkb: u8,
    xi: u8,
    record: u8,
    context: u32,
    client: u32,
}

fn core_range(opcode: u8) -> record::Range {
    record::Range {
        core_requests: record::Range8 { first: opcode, last: opcode },
        ..Default::default()
    }
}

fn extension_range(major: u8, first: u16, last: u16) -> record::Range {
    record::Range {
        ext_requests: record::ExtRange {
            major: record::Range8 { first: major, last: major },
            minor: record::Range16 { first, last },
        },
        ..Default::default()
    }
}

impl Classes {
    fn ranges(&self) -> Vec<record::Range> {
        vec![
            core_range(xproto::CHANGE_KEYBOARD_MAPPING_REQUEST),
            core_range(xproto::SET_MODIFIER_MAPPING_REQUEST),
            extension_range(self.xkb, xkb::SET_MAP_REQUEST.into(), xkb::SET_MAP_REQUEST.into()),
            extension_range(self.xkb, xkb::SET_COMPAT_MAP_REQUEST.into(), xkb::SET_COMPAT_MAP_REQUEST.into()),
            extension_range(self.xkb, xkb::GET_KBD_BY_NAME_REQUEST.into(), xkb::GET_KBD_BY_NAME_REQUEST.into()),
            extension_range(self.xi, xinput::CHANGE_DEVICE_KEY_MAPPING_REQUEST.into(),
                            xinput::CHANGE_DEVICE_KEY_MAPPING_REQUEST.into()),
            extension_range(self.xi, xinput::SET_DEVICE_MODIFIER_MAPPING_REQUEST.into(),
                            xinput::SET_DEVICE_MODIFIER_MAPPING_REQUEST.into()),
            // A live context can have its ranges removed without EndOfData.
            // Capture that request before it removes its own observation hook.
            extension_range(self.record, record::REGISTER_CLIENTS_REQUEST.into(),
                            record::UNREGISTER_CLIENTS_REQUEST.into()),
        ]
    }

    /// Parse only selected protocol classes. Client sequence prefixes use the
    /// recording client's order; original request fields use their own order.
    /// BigRequests and malformed/unknown records are refused before input.
    fn scan(&self, frame: &Frame, marker: u32, records: &mut usize) -> Result<bool> {
        if frame.category != 1 || frame.element_header != SEQUENCE_HEADER || frame.client == 0 {
            return Err(invalid());
        }
        let mut offset = 0usize;
        let mut matched = false;
        while offset < frame.data.len() {
            *records = records.checked_add(1).ok_or_else(invalid)?;
            if *records > FENCE_RECORDS_LIMIT { return Err(invalid()) }
            let prefix = frame.data.get(offset..offset + 8).ok_or_else(invalid)?;
            let sequence = u32::from_ne_bytes(prefix[..4].try_into().unwrap());
            let major = prefix[4];
            let minor = prefix[5];
            let mut words = u16::from_ne_bytes(prefix[6..8].try_into().unwrap());
            if frame.client_swapped { words = words.swap_bytes() }
            if words == 0 { return Err(invalid()) }
            let end = offset.checked_add(4 + usize::from(words) * 4).ok_or_else(invalid)?;
            let request = frame.data.get(offset + 4..end).ok_or_else(invalid)?;
            let is_marker = major == xproto::GET_INPUT_FOCUS_REQUEST && minor == 0 && words == 1;
            if is_marker {
                // Marker capture is configured only for this original client.
                // A foreign client with the same sequence is never authority.
                if frame.client != self.client { return Err(invalid()) }
                if sequence == marker {
                    if matched { return Err(invalid()) }
                    matched = true;
                }
            } else if major == self.record
                && matches!(minor, record::REGISTER_CLIENTS_REQUEST | record::UNREGISTER_CLIENTS_REQUEST)
            {
                let mut context = u32::from_ne_bytes(request.get(4..8).ok_or_else(invalid)?.try_into().unwrap());
                if frame.client_swapped { context = context.swap_bytes() }
                if context == self.context { return Err(invalid()) }
            } else if major == self.xkb && minor == xkb::GET_KBD_BY_NAME_REQUEST {
                // A successful full load always sends NewKeyboardNotify for
                // the actual device group. Original-device ResourceEpoch
                // subscriptions, not a server-wide mutation guess, fence it.
                // Silent mutator classes remain terminal in this stream.
                let _ = request.get(10).ok_or_else(invalid)?;
            } else {
                // All other selected requests can mutate maps/actions/compat.
                // Conservatively terminal even if the server later rejects it.
                return Err(invalid());
            }
            offset = end;
        }
        Ok(matched)
    }
}

pub(super) struct MutationHistory {
    data: Metadata,
    classes: Classes,
    live: bool,
}

impl MutationHistory {
    #[cfg(test)]
    pub(super) fn test_context(&self) -> u32 { self.classes.context }

    #[cfg(test)]
    pub(super) fn test_stop_handle(&self) -> IoBoundary { self.data.native.boundary.clone() }

    #[cfg(test)]
    pub(super) fn test_live(&self) -> bool { self.live }

    pub(super) fn new(main: &NativeConnection, data: NativeConnection, deadline: Instant) -> Result<Self> {
        let (classes, record_opcode) = main.transaction(deadline, || {
            let version = checked(checked(main.connection.record_query_version(1, 13))?.reply())?;
            if (version.major_version, version.minor_version) < (1, 13) { return Err(unavailable()) }
            let opcode = |name: &'static str| -> Result<u8> {
                Ok(checked(main.connection.extension_information(name))?.ok_or_else(unavailable)?.major_opcode)
            };
            let classes = Classes {
                xkb: opcode(xkb::X11_EXTENSION_NAME)?, xi: opcode(xinput::X11_EXTENSION_NAME)?,
                record: opcode(record::X11_EXTENSION_NAME)?, context: checked(main.connection.generate_id())?,
                client: main.connection.setup().resource_id_base,
            };
            let mut ranges = classes.ranges();
            // AllClients expands to existing clients plus FutureClients. The
            // second registration replaces ONLY main's ranges, retaining its
            // mutation classes and adding the private completion marker.
            checked(checked(main.connection.record_create_context(
                classes.context, SEQUENCE_HEADER, &[3], &ranges,
            ))?.check())?;
            ranges.push(core_range(xproto::GET_INPUT_FOCUS_REQUEST));
            checked(checked(main.connection.record_register_clients(
                classes.context, SEQUENCE_HEADER, &[classes.client], &ranges,
            ))?.check())?;
            Ok((classes, classes.record))
        })?;
        let phase = data.boundary.begin(deadline.min(Instant::now() + TRANSACTION))?;
        let (bytes, descriptors) = record::EnableContextRequest { context: classes.context }.serialize(record_opcode);
        let cookie = checked(data.connection.send_request_without_reply(&[IoSlice::new(&bytes[0])], descriptors))?;
        let sequence = u16::try_from(cookie.sequence_number()).map_err(|_| invalid())?;
        drop(cookie);
        checked(data.connection.flush())?;
        let mut history = Self { data: Metadata { native: data, sequence }, classes, live: true };
        let mut budget = FenceBudget::default();
        let frame = loop {
            if let Some(frame) = read_packet(&mut history.data, sequence, &mut budget)? {
                break frame;
            }
        };
        if frame.category != 4 || !frame.data.is_empty() { return Err(invalid()) }
        phase.finish()?;
        // Join the original stream before exposing this endpoint. No pair or
        // target exists yet, so construction never resets a retained epoch.
        main.transaction(deadline, || history.fence(main))?;
        Ok(history)
    }

    pub(super) fn fence(&mut self, main: &NativeConnection) -> Result<()> {
        if !self.live { return Err(unavailable()) }
        let result = self.fence_live(main);
        if result.is_err() {
            self.live = false;
            self.data.native.boundary.stop();
        }
        result
    }

    fn fence_live(&mut self, main: &NativeConnection) -> Result<()> {
        // This exact budget belongs to the enclosing original server grab.
        let phase = self.data.native.boundary.begin(main.boundary.deadline()?)?;
        let cookie = checked(main.connection.get_input_focus())?;
        let marker = u32::try_from(cookie.sequence_number()).map_err(|_| invalid())?;
        checked(cookie.reply())?;
        let mut budget = FenceBudget::default();
        loop {
            main.boundary.deadline()?;
            let sequence = self.data.sequence;
            let Some(frame) = read_packet(&mut self.data, sequence, &mut budget)? else { continue };
            // EndOfData, a restarted stream, loss or malformed framing cannot
            // restore continuity. Never reconnect, recreate or replay here.
            if self.classes.scan(&frame, marker, &mut budget.records)? {
                phase.finish()?;
                return Ok(());
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn native_mapping_notices_are_non_authorizing_but_forged_or_unknown_events_fail() {
        for request in 0..3 {
            let mut notice = [0u8; 32];
            notice[0] = xproto::MAPPING_NOTIFY_EVENT;
            notice[2..4].copy_from_slice(&99u16.to_ne_bytes());
            notice[4] = request;
            let mut input = io::Cursor::new(notice);
            let mut budget = FenceBudget::default();
            assert!(read_packet(&mut input, 7, &mut budget).unwrap().is_none());
            assert_eq!(input.position(), 32);
            assert_eq!(budget.records, 1);
            assert_eq!(budget.bytes, 32);
        }
        for (response, request) in
            [(xproto::MAPPING_NOTIFY_EVENT | 0x80, 0), (xproto::MAPPING_NOTIFY_EVENT, 3), (xproto::KEY_PRESS_EVENT, 0)]
        {
            let mut notice = [0u8; 32];
            notice[0] = response;
            notice[4] = request;
            assert!(read_packet(&mut io::Cursor::new(notice), 7, &mut FenceBudget::default()).is_err());
        }
    }

    #[test]
    fn mapping_notice_floods_share_the_original_fence_record_and_byte_limits() {
        let mut notice = [0u8; 32];
        notice[0] = xproto::MAPPING_NOTIFY_EVENT;
        let mut input = io::Cursor::new(notice.repeat(FENCE_RECORDS_LIMIT + 1));
        let mut budget = FenceBudget::default();
        for _ in 0..FENCE_RECORDS_LIMIT {
            assert!(read_packet(&mut input, 7, &mut budget).unwrap().is_none());
        }
        assert!(read_packet(&mut input, 7, &mut budget).is_err());
        assert_eq!(input.position(), ((FENCE_RECORDS_LIMIT + 1) * 32) as u64);
        let mut budget = FenceBudget { bytes: FENCE_BYTES_LIMIT - 16, records: 0 };
        assert!(read_packet(&mut io::Cursor::new(notice), 7, &mut budget).is_err());
    }

    fn classes() -> Classes {
        Classes { xkb: 135, xi: 131, record: 146, context: 0x200001, client: 0x200000 }
    }

    fn frame(client: u32, sequence: u32, request: &[u8], swapped: bool) -> Frame {
        let mut data = sequence.to_ne_bytes().to_vec();
        data.extend_from_slice(request);
        Frame { category: 1, element_header: SEQUENCE_HEADER, client_swapped: swapped,
                client, data: Zeroizing::new(data) }
    }

    fn request(major: u8, minor: u8, length: usize, swapped: bool) -> Vec<u8> {
        let mut bytes = vec![0u8; length];
        bytes[0] = major;
        bytes[1] = minor;
        let words = (length / 4) as u16;
        bytes[2..4].copy_from_slice(&if swapped { words.swap_bytes() } else { words }.to_ne_bytes());
        bytes
    }

    #[test]
    fn caps_payload_and_original_reply_sequence_before_body_read() {
        for (sequence, words) in [(7u16, u32::MAX), (8u16, 1u32)] {
            let mut header = [0u8; 32];
            header[0] = 1;
            header[2..4].copy_from_slice(&sequence.to_ne_bytes());
            header[4..8].copy_from_slice(&words.to_ne_bytes());
            let mut reader = io::Cursor::new(header);
            assert_eq!(
                read_packet(&mut reader, 7, &mut FenceBudget::default()).unwrap_err().kind(),
                ErrorKind::InvalidData,
            );
            assert_eq!(reader.position(), 32);
        }
    }

    #[test]
    fn truncated_and_fragmented_frames_preserve_epoch_and_omit_payload() {
        let reply = record::EnableContextReply {
            category: 1, sequence: 7, element_header: SEQUENCE_HEADER, client_swapped: false,
            xid_base: 0x200000, server_time: 0, rec_sequence_num: 17, data: vec![1, 2, 3, 4],
        };
        let bytes = reply.serialize();
        for length in [5, 32, bytes.len() - 1] {
            assert_eq!(
                read_packet(&mut io::Cursor::new(&bytes[..length]), 7, &mut FenceBudget::default()).unwrap_err().kind(),
                ErrorKind::UnexpectedEof,
            );
        }
        struct Fragmented(io::Cursor<Vec<u8>>);
        impl Read for Fragmented {
            fn read(&mut self, bytes: &mut [u8]) -> Result<usize> {
                let length = bytes.len().min(1);
                self.0.read(&mut bytes[..length])
            }
        }
        let parsed = read_packet(
            &mut Fragmented(io::Cursor::new(bytes)), 7, &mut FenceBudget::default(),
        ).unwrap().unwrap();
        assert_eq!(parsed.client, reply.xid_base);
        assert_eq!(parsed.data.as_slice(), reply.data.as_slice());
        assert!(!format!("{parsed:?}").contains("1, 2, 3, 4"));
    }

    #[test]
    fn marker_requires_original_client_and_exact_sequence() {
        let classes = classes();
        let request = request(xproto::GET_INPUT_FOCUS_REQUEST, 0, 4, false);
        assert!(!classes.scan(&frame(classes.client, 16, &request, false), 17, &mut 0).unwrap());
        assert!(classes.scan(&frame(classes.client, 17, &request, false), 17, &mut 0).unwrap());
        assert!(classes.scan(&frame(0x400000, 17, &request, false), 17, &mut 0).is_err());
    }

    #[test]
    fn silent_mapping_mutators_are_terminal_and_notifying_loads_are_framed() {
        let classes = classes();
        for swapped in [false, true] {
            for (major, minor) in [
                (xproto::CHANGE_KEYBOARD_MAPPING_REQUEST, 1), (xproto::SET_MODIFIER_MAPPING_REQUEST, 1),
                (classes.xkb, xkb::SET_MAP_REQUEST), (classes.xkb, xkb::SET_COMPAT_MAP_REQUEST),
                (classes.xi, xinput::CHANGE_DEVICE_KEY_MAPPING_REQUEST),
                (classes.xi, xinput::SET_DEVICE_MODIFIER_MAPPING_REQUEST),
            ] {
                assert!(classes.scan(&frame(0x400000, 1, &request(major, minor, 8, swapped), swapped),
                                     17, &mut 0).is_err());
            }
            let mut load = request(classes.xkb, xkb::GET_KBD_BY_NAME_REQUEST, 12, swapped);
            assert!(!classes.scan(&frame(0x400000, 1, &load, swapped), 17, &mut 0).unwrap());
            load[10] = 1;
            assert!(!classes.scan(&frame(0x400000, 1, &load, swapped), 17, &mut 0).unwrap());
        }
    }

    #[test]
    fn original_context_range_changes_cannot_remove_their_own_witness() {
        let classes = classes();
        for swapped in [false, true] {
            for minor in [record::REGISTER_CLIENTS_REQUEST, record::UNREGISTER_CLIENTS_REQUEST] {
                let mut bytes = request(classes.record, minor, 8, swapped);
                let context = if swapped { classes.context.swap_bytes() } else { classes.context };
                bytes[4..8].copy_from_slice(&context.to_ne_bytes());
                assert!(classes.scan(&frame(0x400000, 1, &bytes, swapped), 17, &mut 0).is_err());
                bytes[4..8].fill(0);
                assert!(!classes.scan(&frame(0x400000, 1, &bytes, swapped), 17, &mut 0).unwrap());
            }
        }
    }

    #[test]
    fn ended_restarted_malformed_big_and_flooded_streams_cannot_authorize_input() {
        let classes = classes();
        let marker = request(xproto::GET_INPUT_FOCUS_REQUEST, 0, 4, false);
        for category in [0, 2, 3, 4, 5] {
            let mut value = frame(classes.client, 17, &marker, false);
            value.category = category;
            assert!(classes.scan(&value, 17, &mut 0).is_err());
        }
        let mut big = marker.clone();
        big[2..4].fill(0);
        assert!(classes.scan(&frame(classes.client, 17, &big, false), 17, &mut 0).is_err());
        assert!(classes.scan(&frame(classes.client, 17, &marker[..3], false), 17, &mut 0).is_err());
        let mut exhausted = FENCE_RECORDS_LIMIT;
        assert!(classes.scan(&frame(classes.client, 17, &marker, false), 17, &mut exhausted).is_err());
    }
}
