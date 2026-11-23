use std::io::{self, Read};

use x11rb::protocol::xinput::{self, ConnectionExt as _};

use crate::{X11, checked, fail, pointer};

#[derive(PartialEq, Eq)]
struct Device {
    id: u16,
    kind: u16,
    attachment: u16,
    name: Vec<u8>,
    enabled: bool,
}

pub(super) struct OwnedPointer {
    pub(super) pointer: pointer::Pointer,
    pub(super) name: String,
    devices: Vec<Device>,
}

fn devices(conn: &X11) -> io::Result<Vec<Device>> {
    let rows = checked(checked(conn.xinput_xi_query_device(0u16))?.reply())?.infos;
    if rows.len() > 128 {
        return Err(fail());
    }
    Ok(rows
        .into_iter()
        .map(|row| Device {
            id: row.deviceid,
            kind: row.type_.into(),
            attachment: row.attachment,
            name: row.name,
            enabled: row.enabled,
        })
        .collect())
}

impl OwnedPointer {
    // The caller already holds the bounded server grab, runs RECORD, and has
    // established that only its original three clients are connected.
    pub(super) fn create(conn: &X11) -> io::Result<(Self, u32)> {
        checked(checked(conn.xinput_xi_query_version(2, 0))?.reply())?;
        let before = devices(conn)?;
        let mut random = [0u8; 16];
        std::fs::File::open("/dev/urandom")?.read_exact(&mut random)?;
        let nonce: String = random.iter().map(|byte| format!("{byte:02x}")).collect();
        let name = format!("MintClaw owned scroll {nonce}");
        let change = xinput::HierarchyChange {
            len: checked(u16::try_from(2 + name.len().div_ceil(4)))?,
            data: xinput::HierarchyChangeData::AddMaster(xinput::HierarchyChangeDataAddMaster {
                send_core: false,
                enable: true,
                name: name.as_bytes().to_vec(),
            }),
        };
        let changes = [change];
        let creation = checked(conn.xinput_xi_change_hierarchy(&changes))?;
        let sequence = checked(u32::try_from(creation.sequence_number()))?;
        checked(creation.check())?;
        let after = devices(conn)?;
        if before.iter().any(|device| !after.contains(device)) || after.len() != before.len() + 4 {
            return Err(fail());
        }
        let mut born: Vec<_> = after
            .into_iter()
            .filter(|row| !before.iter().any(|earlier| earlier.id == row.id))
            .collect();
        born.sort_by_key(|device| device.id);
        let lookup = |suffix: &str, kind: xinput::DeviceType| {
            let full_name = format!("{name} {suffix}");
            born.iter()
                .find(|row| row.name == full_name.as_bytes() && row.kind == u16::from(kind))
                .ok_or_else(fail)
        };
        let master = lookup("pointer", xinput::DeviceType::MASTER_POINTER)?;
        let keyboard = lookup("keyboard", xinput::DeviceType::MASTER_KEYBOARD)?;
        let source = lookup("XTEST pointer", xinput::DeviceType::SLAVE_POINTER)?;
        let source_keyboard = lookup("XTEST keyboard", xinput::DeviceType::SLAVE_KEYBOARD)?;
        if born.len() != 4
            || born.iter().any(|row| !row.enabled)
            || master.attachment != keyboard.id
            || keyboard.attachment != master.id
            || source.attachment != master.id
            || source_keyboard.attachment != keyboard.id
        {
            return Err(fail());
        }
        let pointer = pointer::Pointer {
            master: master.id,
            source: checked(u8::try_from(source.id))?,
        };
        Ok((
            Self {
                pointer,
                name,
                devices: born,
            },
            sequence,
        ))
    }

    pub(super) fn check(&self, conn: &X11) -> io::Result<()> {
        let current = devices(conn)?;
        if self.devices.iter().any(|device| !current.contains(device)) {
            return Err(fail());
        }
        Ok(())
    }
}
