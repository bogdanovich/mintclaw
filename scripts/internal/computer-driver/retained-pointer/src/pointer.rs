//! Private X11 point routing inside the caller's bounded server transaction.
//!
//! The caller retains the original window, lease and device generations and
//! supplies their independently attested identities. The routing checks are
//! not an input grant or evidence of application effects.

use std::io::{self, ErrorKind};

use x11rb::connection::Connection;
use x11rb::protocol::res::ConnectionExt as _;
use x11rb::protocol::xinput::ConnectionExt as _;
use x11rb::protocol::xproto::{self, ConnectionExt as _};
use x11rb::protocol::{res, xinput};

const MAX_ROUTE_DEPTH: usize = 32;
const MAX_KEYBOARDS: usize = 32;
const XI_ANY_MODIFIER: u32 = 1 << 31;

fn refused() -> io::Error {
    io::Error::new(
        ErrorKind::PermissionDenied,
        "native pointer route unverified",
    )
}

fn checked<T, E>(result: Result<T, E>) -> io::Result<T> {
    result.map_err(|_| refused())
}

#[derive(Clone, Copy)]
pub(super) struct ObservedPoint {
    pub window: xproto::Window,
    pub owner_pid: u32,
    pub left: i16,
    pub top: i16,
    pub width: u16,
    pub height: u16,
    pub x: i16,
    pub y: i16,
}

#[derive(Clone, Copy)]
pub(super) struct Pointer {
    pub master: u16,
    pub source: u8,
}

pub(super) fn position(
    connection: &impl Connection,
    root: xproto::Window,
    original: ObservedPoint,
    pointer: Pointer,
    button: u8,
    check_recipients: impl FnOnce(&[xproto::Window]) -> io::Result<()>,
) -> io::Result<()> {
    let target = original.window;
    if target <= 1
        || target == root
        || pointer.master <= 1
        || pointer.source <= 1
        || pointer.master == u16::from(pointer.source)
        || button == 0
        || original.x < 0
        || original.y < 0
        || original.x as u16 >= original.width
        || original.y as u16 >= original.height
    {
        return Err(refused());
    }
    let geometry = checked(checked(connection.get_geometry(target))?.reply())?;
    let attributes = checked(checked(connection.get_window_attributes(target))?.reply())?;
    let origin = checked(checked(connection.translate_coordinates(target, root, 0, 0))?.reply())?;
    if geometry.root != root
        || (geometry.width, geometry.height) != (original.width, original.height)
        || attributes.class != xproto::WindowClass::INPUT_OUTPUT
        || attributes.map_state != xproto::MapState::VIEWABLE
        || !origin.same_screen
        || (origin.dst_x, origin.dst_y) != (original.left, original.top)
    {
        return Err(refused());
    }
    // Comparing the projected path before warping avoids an unguarded move to
    // a covering foreign window. The actual XI2 hit path is checked afterward.
    let projected = route(root, |window| {
        let point = checked(
            checked(connection.translate_coordinates(target, window, original.x, original.y))?
                .reply(),
        )?;
        if !point.same_screen {
            return Err(refused());
        }
        Ok(point.child)
    })?;
    let index = projected
        .iter()
        .position(|window| *window == target)
        .ok_or_else(refused)?;
    for window in &projected[index..] {
        let owner = checked(
            checked(connection.res_query_client_ids(&[res::ClientIdSpec {
                client: *window,
                mask: res::ClientIdMask::LOCAL_CLIENT_PID,
            }]))?
            .reply(),
        )?;
        if owner.ids.len() != 1
            || owner.ids[0].spec.mask != res::ClientIdMask::LOCAL_CLIENT_PID
            || owner.ids[0].value != [original.owner_pid]
        {
            return Err(refused());
        }
    }
    check_recipients(&projected)?;
    let keyboards = device_witness(connection, pointer, button)?;
    active_slots(connection, target, pointer)?;
    passives(connection, pointer, &projected, &keyboards, button)?;

    checked(
        checked(connection.xinput_xi_warp_pointer(
            x11rb::NONE,
            target,
            0,
            0,
            0,
            0,
            i32::from(original.x) << 16,
            i32::from(original.y) << 16,
            pointer.master,
        ))?
        .check(),
    )?;
    // Enter-triggered grabs can appear because of this pointer move itself.
    active_slots(connection, target, pointer)?;
    let actual = route(root, |window| {
        let point =
            checked(checked(connection.xinput_xi_query_pointer(window, pointer.master))?.reply())?;
        if !point.same_screen || point.root != root {
            return Err(refused());
        }
        Ok(point.child)
    })?;
    let point =
        checked(checked(connection.xinput_xi_query_pointer(target, pointer.master))?.reply())?;
    if actual != projected
        || !point.same_screen
        || point.root != root
        || point.win_x != (i32::from(original.x) << 16)
        || point.win_y != (i32::from(original.y) << 16)
        || point.buttons.iter().any(|state| *state != 0)
        || point.mods.effective != 0
    {
        return Err(refused());
    }
    neutral(connection, root, pointer)
}

pub(super) fn neutral(
    connection: &impl Connection,
    root: xproto::Window,
    pointer: Pointer,
) -> io::Result<()> {
    let master =
        checked(checked(connection.xinput_xi_query_pointer(root, pointer.master))?.reply())?;
    let source = checked(checked(connection.xinput_query_device_state(pointer.source))?.reply())?;
    let mut states = source.classes.iter().filter_map(|class| {
        if let xinput::InputStateData::Button(state) = &class.data {
            Some(state)
        } else {
            None
        }
    });
    let buttons = states.next().ok_or_else(refused)?;
    if !master.same_screen
        || master.root != root
        || master.buttons.iter().any(|value| *value != 0)
        || states.next().is_some()
        || buttons.buttons != [0; 32]
    {
        return Err(refused());
    }
    Ok(())
}

fn device_witness(
    connection: &impl Connection,
    pointer: Pointer,
    button: u8,
) -> io::Result<Vec<u8>> {
    let devices = checked(checked(connection.xinput_xi_query_device(0u16))?.reply())?.infos;
    let mut masters = devices
        .iter()
        .filter(|device| device.deviceid == pointer.master);
    let master = masters.next().ok_or_else(refused)?;
    let mut attached = devices.iter().filter(|device| {
        device.type_ == xinput::DeviceType::SLAVE_POINTER && device.attachment == pointer.master
    });
    let source = attached.next().ok_or_else(refused)?;
    if !master.enabled
        || master.type_ != xinput::DeviceType::MASTER_POINTER
        || masters.next().is_some()
        || source.deviceid != u16::from(pointer.source)
        || !source.enabled
        || attached.next().is_some()
    {
        return Err(refused());
    }
    for device in [
        u8::try_from(pointer.master).map_err(|_| refused())?,
        pointer.source,
    ] {
        let mapping =
            checked(checked(connection.xinput_get_device_button_mapping(device))?.reply())?;
        if mapping.map.get(usize::from(button) - 1) != Some(&button) {
            return Err(refused());
        }
    }
    let mut keyboards = Vec::new();
    for device in &devices {
        if device
            .classes
            .iter()
            .any(|class| matches!(&class.data, xinput::DeviceClassData::Key(_)))
        {
            if keyboards.len() == MAX_KEYBOARDS {
                return Err(refused());
            }
            keyboards.push(u8::try_from(device.deviceid).map_err(|_| refused())?);
        }
    }
    if keyboards.is_empty() {
        return Err(refused());
    }
    Ok(keyboards)
}

fn route(
    root: xproto::Window,
    mut child: impl FnMut(xproto::Window) -> io::Result<xproto::Window>,
) -> io::Result<Vec<xproto::Window>> {
    let mut path = Vec::new();
    let mut current = root;
    while path.len() < MAX_ROUTE_DEPTH && current > 1 && !path.contains(&current) {
        path.push(current);
        current = child(current)?;
        if current == x11rb::NONE {
            return Ok(path);
        }
    }
    Err(refused())
}

fn active_slots(
    connection: &impl Connection,
    target: xproto::Window,
    pointer: Pointer,
) -> io::Result<()> {
    let grab = checked(
        checked(connection.xinput_xi_grab_device(
            target,
            x11rb::CURRENT_TIME,
            x11rb::NONE,
            pointer.master,
            xproto::GrabMode::ASYNC,
            xproto::GrabMode::ASYNC,
            xinput::GrabOwner::NO_OWNER,
            &[],
        ))?
        .reply(),
    )?;
    if grab.status != xproto::GrabStatus::SUCCESS {
        return Err(refused());
    }
    checked(
        checked(connection.xinput_xi_ungrab_device(x11rb::CURRENT_TIME, pointer.master))?.check(),
    )?;
    let grab = checked(
        checked(connection.xinput_grab_device(
            target,
            x11rb::CURRENT_TIME,
            xproto::GrabMode::ASYNC,
            xproto::GrabMode::ASYNC,
            false,
            pointer.source,
            &[],
        ))?
        .reply(),
    )?;
    if grab.status != xproto::GrabStatus::SUCCESS {
        return Err(refused());
    }
    checked(checked(connection.xinput_ungrab_device(x11rb::CURRENT_TIME, pointer.source))?.check())
}

fn passives(
    connection: &impl Connection,
    pointer: Pointer,
    windows: &[xproto::Window],
    keyboards: &[u8],
    button: u8,
) -> io::Result<()> {
    checked(
        checked(connection.xinput_xi_set_client_pointer(x11rb::NONE, pointer.master))?.check(),
    )?;
    let client = checked(checked(connection.xinput_xi_get_client_pointer(x11rb::NONE))?.reply())?;
    if !client.set || client.deviceid != pointer.master {
        return Err(refused());
    }
    for &window in windows {
        checked(
            checked(connection.grab_button(
                false,
                window,
                xproto::EventMask::NO_EVENT,
                xproto::GrabMode::ASYNC,
                xproto::GrabMode::ASYNC,
                x11rb::NONE,
                x11rb::NONE,
                xproto::ButtonIndex::from(button),
                xproto::ModMask::ANY,
            ))?
            .check(),
        )?;
        checked(
            checked(connection.ungrab_button(
                xproto::ButtonIndex::from(button),
                window,
                xproto::ModMask::ANY,
            ))?
            .check(),
        )?;
        for &keyboard in keyboards {
            checked(
                checked(connection.xinput_grab_device_button(
                    window,
                    pointer.source,
                    keyboard,
                    xproto::ModMask::ANY,
                    xproto::GrabMode::ASYNC,
                    xproto::GrabMode::ASYNC,
                    button,
                    false,
                    &[],
                ))?
                .check(),
            )?;
            checked(
                checked(connection.xinput_ungrab_device_button(
                    window,
                    xproto::ModMask::ANY,
                    keyboard,
                    button,
                    pointer.source,
                ))?
                .check(),
            )?;
        }
        for device in [pointer.master, u16::from(pointer.source)] {
            let mods = [XI_ANY_MODIFIER];
            let grab = checked(
                checked(connection.xinput_xi_passive_grab_device(
                    x11rb::CURRENT_TIME,
                    window,
                    x11rb::NONE,
                    u32::from(button),
                    device,
                    xinput::GrabType::BUTTON,
                    xinput::GrabMode22::ASYNC,
                    xproto::GrabMode::ASYNC,
                    xinput::GrabOwner::NO_OWNER,
                    &[],
                    &mods,
                ))?
                .reply(),
            )?;
            if !grab.modifiers.is_empty() {
                return Err(refused());
            }
            checked(
                checked(connection.xinput_xi_passive_ungrab_device(
                    window,
                    u32::from(button),
                    device,
                    xinput::GrabType::BUTTON,
                    &mods,
                ))?
                .check(),
            )?;
        }
    }
    Ok(())
}
