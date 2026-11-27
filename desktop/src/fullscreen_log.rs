//! Full-screen titlebar diagnostics (FEN-87).
//!
//! A user reported that in native full screen the titlebar (traffic lights)
//! never drops down when the pointer reaches the top of the screen. It could
//! not be reproduced on a built-in notched display, so the window logs its
//! full-screen state for the next report: the screen it is on, the app's
//! windows in front-to-back order (the content `GPUIWindow`, AppKit's
//! `NSToolbarFullScreenWindow` holding the traffic lights, and the
//! `_NSFullScreenMouseDetectionWindow` that grows when the reveal fires),
//! whether the pointer is at the top edge, and the menu bar's visibility.
//!
//! Lines go to the daily log `~/.config/fennec/logs/fennec-<date>.log`,
//! prefixed `fennec-desktop FEN-87 fullscreen`, so Settings › Export logs
//! carries them. A line is
//! written only when the state changes: entering and leaving full screen, and
//! each reveal or hide while in it. Outside full screen it only reads the
//! window's style mask.

use gpui::{App, WindowHandle};
use objc2::runtime::AnyObject;
use objc2::{Encode, Encoding, msg_send};
use std::ffi::CStr;
use std::fmt::Write;
use std::time::Duration;

const POLL: Duration = Duration::from_millis(250);
const FULL_SCREEN_MASK: usize = 1 << 14; // NSWindowStyleMaskFullScreen

#[repr(C)]
#[derive(Clone, Copy)]
struct Point {
    x: f64,
    y: f64,
}

#[repr(C)]
#[derive(Clone, Copy)]
struct Size {
    width: f64,
    height: f64,
}

#[repr(C)]
#[derive(Clone, Copy)]
struct Rect {
    origin: Point,
    size: Size,
}

#[repr(C)]
#[derive(Clone, Copy)]
struct EdgeInsets {
    top: f64,
    left: f64,
    bottom: f64,
    right: f64,
}

unsafe impl Encode for Point {
    const ENCODING: Encoding = Encoding::Struct("CGPoint", &[f64::ENCODING, f64::ENCODING]);
}
unsafe impl Encode for Size {
    const ENCODING: Encoding = Encoding::Struct("CGSize", &[f64::ENCODING, f64::ENCODING]);
}
unsafe impl Encode for Rect {
    const ENCODING: Encoding = Encoding::Struct("CGRect", &[Point::ENCODING, Size::ENCODING]);
}
unsafe impl Encode for EdgeInsets {
    const ENCODING: Encoding = Encoding::Struct(
        "NSEdgeInsets",
        &[f64::ENCODING, f64::ENCODING, f64::ENCODING, f64::ENCODING],
    );
}

/// Appends a line to today's `~/.config/fennec/logs/fennec-<date>.log`, the
/// daemon's daily log (internal/applog), which Export logs collects. NSLog
/// didn't reliably reach the unified log from the desktop process.
fn log(line: &str) {
    use std::io::Write as _;
    tracing::info!(target: "fennec_desktop::fullscreen", "FEN-87 fullscreen {line}");
    let Some(home) = std::env::var_os("HOME") else { return };
    let now = chrono::Local::now();
    let dir = std::path::Path::new(&home).join(".config/fennec/logs");
    let path = dir.join(format!("fennec-{}.log", now.format("%Y-%m-%d")));
    let _ = std::fs::create_dir_all(&dir);
    let file = std::fs::OpenOptions::new().create(true).append(true).open(path);
    if let Ok(mut file) = file {
        // One write per line, so lines don't interleave with the daemon's.
        let text = format!("{} fennec-desktop FEN-87 fullscreen {line}\n", now.format("%Y/%m/%d %H:%M:%S"));
        let _ = file.write_all(text.as_bytes());
    }
}

fn rect(r: Rect) -> String {
    format!("({:.0},{:.0} {:.0}x{:.0})", r.origin.x, r.origin.y, r.size.width, r.size.height)
}

unsafe fn class_name(object: *mut AnyObject) -> String {
    unsafe { (*object).class().name().to_string_lossy().into_owned() }
}

unsafe fn ns_string(object: *mut AnyObject) -> String {
    if object.is_null() {
        return String::new();
    }
    let utf8: *const std::ffi::c_char = unsafe { msg_send![object, UTF8String] };
    if utf8.is_null() {
        return String::new();
    }
    unsafe { CStr::from_ptr(utf8) }.to_string_lossy().into_owned()
}

/// The state that matters for the reveal; a line is logged when it changes.
unsafe fn snapshot(window: *mut AnyObject) -> (bool, String) {
    unsafe {
        let mask: usize = msg_send![window, styleMask];
        let full_screen = mask & FULL_SCREEN_MASK != 0;
        let mut s = String::new();
        let frame: Rect = msg_send![window, frame];
        let transparent: bool = msg_send![window, titlebarAppearsTransparent];
        let _ = write!(s, "full_screen={full_screen} window={} transparent_titlebar={transparent}", rect(frame));

        let screen: *mut AnyObject = msg_send![window, screen];
        if !screen.is_null() {
            let screen_frame: Rect = msg_send![screen, frame];
            let visible: Rect = msg_send![screen, visibleFrame];
            let insets: EdgeInsets = msg_send![screen, safeAreaInsets];
            let scale: f64 = msg_send![screen, backingScaleFactor];
            let name: *mut AnyObject = msg_send![screen, localizedName];
            let _ = write!(
                s,
                " screen=\"{}\" {} visible={} safe_top={:.0} scale={scale}",
                ns_string(name),
                rect(screen_frame),
                rect(visible),
                insets.top
            );
            let mouse: Point = msg_send![objc2::class!(NSEvent), mouseLocation];
            let top = screen_frame.origin.y + screen_frame.size.height;
            let on_screen = mouse.x >= screen_frame.origin.x
                && mouse.x < screen_frame.origin.x + screen_frame.size.width;
            let _ = write!(s, " pointer_at_top={}", on_screen && mouse.y >= top - 3.0);
        }
        let screens: *mut AnyObject = msg_send![objc2::class!(NSScreen), screens];
        let screen_count: usize = msg_send![screens, count];
        let app: *mut AnyObject = msg_send![objc2::class!(NSApplication), sharedApplication];
        let options: usize = msg_send![app, presentationOptions];
        let menu_bar: bool = msg_send![objc2::class!(NSMenu), menuBarVisible];
        let active: bool = msg_send![app, isActive];
        let _ = write!(
            s,
            " screens={screen_count} presentation=0x{options:x} menu_bar_visible={menu_bar} active={active}"
        );

        // The app's windows on the active space, front to back.
        let numbers: *mut AnyObject =
            msg_send![objc2::class!(NSWindow), windowNumbersWithOptions: 0usize];
        let count: usize = if numbers.is_null() { 0 } else { msg_send![numbers, count] };
        let _ = write!(s, " order=[");
        for i in 0..count {
            let number: *mut AnyObject = msg_send![numbers, objectAtIndex: i];
            let number: isize = msg_send![number, integerValue];
            let w: *mut AnyObject = msg_send![app, windowWithWindowNumber: number];
            if i > 0 {
                s.push_str(", ");
            }
            if w.is_null() {
                let _ = write!(s, "#{number}");
                continue;
            }
            let frame: Rect = msg_send![w, frame];
            let level: isize = msg_send![w, level];
            let alpha: f64 = msg_send![w, alphaValue];
            let visible: bool = msg_send![w, isVisible];
            let _ = write!(
                s,
                "{} #{number} {} level={level} alpha={alpha:.2} visible={visible}",
                class_name(w),
                rect(frame)
            );
        }
        s.push(']');

        // Where the close button lives and whether it is drawn.
        let close: *mut AnyObject = msg_send![window, standardWindowButton: 0usize];
        if !close.is_null() {
            let hidden: bool = msg_send![close, isHidden];
            let host: *mut AnyObject = msg_send![close, window];
            let frame: Rect = msg_send![close, frame];
            let host = if host.is_null() { "none".to_string() } else { class_name(host) };
            let _ = write!(s, " close_button={{window={host} frame={} hidden={hidden}}}", rect(frame));
        }
        (full_screen, s)
    }
}

/// Watches the main window and logs full-screen state changes.
pub fn watch<V: 'static>(handle: WindowHandle<V>, window: usize, cx: &mut App) {
    cx.spawn(async move |cx| {
        let window = window as *mut AnyObject;
        let mut was_full_screen = false;
        let mut last = String::new();
        loop {
            cx.background_executor().timer(POLL).await;
            // Stop once the window is gone (the app quits with it).
            if handle.update(cx, |_, _, _| ()).is_err() {
                return;
            }
            let mask: usize = unsafe { msg_send![window, styleMask] };
            let full_screen = mask & FULL_SCREEN_MASK != 0;
            if !full_screen && !was_full_screen {
                continue;
            }
            let (full_screen, state) = unsafe { snapshot(window) };
            if state != last {
                let event = match (was_full_screen, full_screen) {
                    (false, true) => "entered",
                    (true, false) => "left",
                    _ => "changed",
                };
                log(&format!("{event}: {state}"));
                last = state;
            }
            was_full_screen = full_screen;
        }
    })
    .detach();
}
