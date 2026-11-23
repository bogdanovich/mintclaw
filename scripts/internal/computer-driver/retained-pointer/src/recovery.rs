//! Correlate an authorized native button edge with a single wire delivery.
//!
//! The caller holds the original server transaction through verification and
//! must separately retain the lease, window, device and client generations.
//! This proof says nothing about the resulting application effect.

use std::borrow::Cow;
use std::io;

use x11rb::protocol::{xinput, xtest};

use super::evidence::{ButtonReceipt, Evidence, Observation};
use super::selections::{PointerSelections, Protocol};

fn unverified() -> io::Error {
    io::Error::new(
        io::ErrorKind::PermissionDenied,
        "original native pointer edge unverified",
    )
}

pub(super) struct Edge {
    client: u32,
    sequence: u32,
    request: Cow<'static, [u8]>,
    delivery: ButtonReceipt,
    observed: bool,
}

impl Edge {
    pub(super) fn new(
        protocol: Protocol,
        client: u32,
        sequence: u32,
        request: xtest::FakeInputRequest,
        delivery: ButtonReceipt,
    ) -> io::Result<Self> {
        let kind = request
            .type_
            .checked_sub(protocol.xinput_first_event)
            .ok_or_else(unverified)?;
        let expected = match kind {
            xinput::DEVICE_BUTTON_PRESS_EVENT => xinput::BUTTON_PRESS_EVENT,
            xinput::DEVICE_BUTTON_RELEASE_EVENT => xinput::BUTTON_RELEASE_EVENT,
            _ => return Err(unverified()),
        };
        if client == 0
            || sequence == 0
            || delivery.recipient == 0
            || delivery.window <= 1
            || delivery.window == delivery.root
            || delivery.root != request.root
            || delivery.kind != expected
            || delivery.detail != u32::from(request.detail)
            || request.detail == 0
            || request.deviceid <= 1
            || request.time != x11rb::CURRENT_TIME
            || (request.root_x, request.root_y) != (0, 0)
        {
            return Err(unverified());
        }
        let ([request], _) = request.serialize(protocol.xtest_opcode);
        Ok(Self {
            client,
            sequence,
            request,
            delivery,
            observed: false,
        })
    }

    pub(super) fn observe(
        &mut self,
        selections: &PointerSelections,
        record: &Observation<'_>,
    ) -> io::Result<()> {
        match *record {
            Observation::Request {
                client,
                sequence,
                bytes,
                after_button,
            } if bytes.starts_with(&self.request[..2]) => {
                if after_button
                    || self.observed
                    || client != self.client
                    || sequence != self.sequence
                    || bytes != self.request.as_ref()
                {
                    return Err(unverified());
                }
                self.observed = true;
            }
            Observation::Request { client, bytes, .. } if self.observed => {
                if !selections.preserves_button_route(client, bytes)? {
                    return Err(unverified());
                }
            }
            Observation::Error { client, sequence }
                if client == self.client && sequence == self.sequence as u16 =>
            {
                return Err(unverified());
            }
            _ => {}
        }
        Ok(())
    }

    pub(super) fn verify(self, evidence: &Evidence) -> io::Result<()> {
        if !self.observed || evidence.buttons != [self.delivery] {
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
        xinput_first_event: 66,
        xtest_opcode: 132,
        record_opcode: 133,
    };
    const ACTOR: u32 = 0x100000;
    const SEQUENCE: u32 = 65537;
    const RECEIPT: ButtonReceipt = ButtonReceipt {
        recipient: 0x200000,
        window: 0x200001,
        root: 31,
        kind: xinput::BUTTON_PRESS_EVENT,
        detail: 5,
    };
    const REQUEST: xtest::FakeInputRequest = xtest::FakeInputRequest {
        type_: PROTOCOL.xinput_first_event + xinput::DEVICE_BUTTON_PRESS_EVENT,
        detail: 5,
        time: x11rb::CURRENT_TIME,
        root: RECEIPT.root,
        root_x: 0,
        root_y: 0,
        deviceid: 12,
    };

    fn edge() -> Edge {
        Edge::new(PROTOCOL, ACTOR, SEQUENCE, REQUEST, RECEIPT).unwrap()
    }

    fn recorded(bytes: &[u8]) -> Observation<'_> {
        Observation::Request {
            client: ACTOR,
            sequence: SEQUENCE,
            bytes,
            after_button: false,
        }
    }

    #[test]
    fn binds_the_native_sequence_and_checks_the_actual_recipient() {
        let ([bytes], _) = REQUEST.serialize(PROTOCOL.xtest_opcode);
        let ledger = PointerSelections::default();
        let mut known = edge();
        known.observe(&ledger, &recorded(&bytes)).unwrap();
        let evidence = Evidence {
            buttons: vec![RECEIPT],
            ..Default::default()
        };
        assert!(known.verify(&evidence).is_ok());
        assert!(edge().verify(&evidence).is_err());

        for receipt in [
            ButtonReceipt {
                recipient: 0x300000,
                ..RECEIPT
            },
            ButtonReceipt {
                window: 0x200002,
                ..RECEIPT
            },
        ] {
            let mut known = edge();
            known.observe(&ledger, &recorded(&bytes)).unwrap();
            let evidence = Evidence {
                buttons: vec![receipt],
                ..Default::default()
            };
            assert!(known.verify(&evidence).is_err());
        }
    }

    #[test]
    fn refuses_late_duplicate_altered_and_failed_requests() {
        let ([bytes], _) = REQUEST.serialize(PROTOCOL.xtest_opcode);
        let ledger = PointerSelections::default();
        let mut known = edge();
        for record in [
            Observation::Request {
                client: ACTOR,
                sequence: SEQUENCE,
                bytes: &bytes,
                after_button: true,
            },
            Observation::Request {
                client: ACTOR,
                sequence: SEQUENCE + 1,
                bytes: &bytes,
                after_button: false,
            },
            Observation::Error {
                client: ACTOR,
                sequence: SEQUENCE as u16,
            },
        ] {
            assert!(known.observe(&ledger, &record).is_err());
        }
        let mut altered = bytes.to_vec();
        *altered.last_mut().unwrap() += 1;
        assert!(known.observe(&ledger, &recorded(&altered)).is_err());
        known.observe(&ledger, &recorded(&bytes)).unwrap();
        assert!(known.observe(&ledger, &recorded(&bytes)).is_err());
    }
}
