//! Fennec's desktop window, native on GPUI.
//!
//! Terminals are herdr-app's (https://github.com/herdrdev/herdr-app,
//! Apache-2.0): `terminal.rs`, `vt.rs`, `hc.rs` and the files beside them are
//! kept as herdr-app ships them (see UPSTREAM.md). Fennec's own parts are the
//! window, the board and the shims those files call (`settings`, `mods`,
//! `keys`, `core_bin`).

// herdr-app's terminal core, unchanged. Parts serve herdr-app features
// Fennec doesn't have (mods, benchmarks), hence the dead_code allowance.
#[allow(dead_code)]
mod bench;
// The unchanged upstream terminal suite also exercises Herdr's edit-menu adapter.
#[cfg(test)]
#[path = "upstream_test_menu.rs"]
mod macos;
#[allow(dead_code)]
mod hc;
#[allow(dead_code)]
mod links;
#[allow(dead_code)]
mod process_cwd;
#[allow(dead_code)]
mod spawn;
#[allow(dead_code)]
mod sprites;
#[allow(dead_code)]
mod terminal;
#[allow(dead_code)]
mod vt;
#[allow(dead_code)]
mod win32_keys;

// What the terminal core asks of the app, answered by Fennec.
mod core_bin;
mod keys;
mod mirror;
mod mods;
mod settings;

// Fennec.
mod app;
mod app_archive;
mod app_preferences;
mod preview;
mod board;
mod control;
mod cursor;
mod dispatch_env;
mod i18n;
mod local_time;
mod project_settings;
mod shot;
mod sessions;
mod ui;

use gpui::*;

#[cfg(test)]
pub(crate) fn test_tmp() -> std::path::PathBuf {
    std::env::temp_dir()
}

fn main() {
    // Before any thread or terminal core exists: shells opened here must hand
    // `claude`, `codex`, ... to the cloud computer like any other shell.
    dispatch_env::clear();
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_env("FENNEC_DESKTOP_LOG")
                .unwrap_or_else(|_| "warn,fennec_desktop=info".into()),
        )
        .init();
    // Buttons and links keep the arrow cursor (cursor.rs).
    gpui::Application::with_platform(cursor::platform(gpui_platform::current_platform(false)))
        .with_assets(ui::Assets)
        .run(move |cx| {
            gpui_base::init(cx);
            settings::load_bundled_fonts(cx);
            keys::install_user(cx);
            let dark = matches!(
                cx.window_appearance(),
                WindowAppearance::Dark | WindowAppearance::VibrantDark
            );
            let dark = if preview::active() {
                match std::env::var("FENNEC_PREVIEW_THEME").as_deref() { Ok("dark") => true, Ok("light") => false, _ => dark }
            } else { dark };
            settings::set_dark(dark);
            let requests = mods::requests();
            // The window opens filling the screen's visible area (below the menu
            // bar, beside the Dock), like WeChat; macOS animates it in from 98%.
            let screen = cx
                .primary_display()
                .map(|display| display.visible_bounds())
                .unwrap_or_else(|| Bounds::centered(None, size(px(1280.0), px(820.0)), cx));
            let options = WindowOptions {
                window_bounds: Some(WindowBounds::Windowed(screen)),
                titlebar: Some(TitlebarOptions {
                    title: Some(if preview::active() { "Fennec design preview" } else { "Fennec" }.into()),
                    appears_transparent: true,
                    traffic_light_position: Some(point(px(16.0), px(17.0))),
                }),
                app_owns_titlebar_drag: cfg!(target_os = "macos"),
                window_decorations: Some(WindowDecorations::Client),
                app_id: Some("fennec-desktop".into()),
                window_min_size: Some(size(px(900.0), px(560.0))),
                // On macOS the window is shown by `show_after_first_frame`.
                show: !cfg!(target_os = "macos"),
                ..Default::default()
            };
            let handle = cx
                .open_window(options, |window, cx| {
                    cx.new(|cx| app::FennecWindow::new(requests, window, cx))
                })
                .expect("cannot open the Fennec window");
            #[cfg(target_os = "macos")]
            show_after_first_frame(handle.into(), cx);
            #[cfg(not(target_os = "macos"))]
            let _ = handle;
            cx.activate(true);
            // The window is the program: closing it ends it (the menu bar app starts it again).
            cx.on_window_closed(|cx, _| {
                if cx.windows().is_empty() {
                    cx.quit();
                }
            })
            .detach();
        });
}

/// Orders the window front only once its first frame is on screen, so the system
/// open animation (scale in from 98%) carries the content, as WeChat's window
/// does, instead of an empty window that fills in partway through. GPUI orders a
/// window front before it has presented anything; this opens it hidden instead.
#[cfg(target_os = "macos")]
fn show_after_first_frame(handle: AnyWindowHandle, cx: &mut App) {
    use objc2::{msg_send, runtime::AnyObject};
    use raw_window_handle::{HasWindowHandle, RawWindowHandle};
    let view = handle
        .update(cx, |_, window, _| match HasWindowHandle::window_handle(window).map(|h| h.as_raw()) {
            Ok(RawWindowHandle::AppKit(appkit)) => Some(appkit.ns_view.as_ptr() as usize),
            _ => None,
        })
        .ok()
        .flatten();
    let Some(view) = view else {
        // Not an AppKit view (never expected): show it the ordinary way.
        let _ = handle.update(cx, |_, window, _| window.activate_window());
        return;
    };
    // The view's frame callback borrows the App, so it runs from a main-thread task
    // rather than inside this (already borrowed) context. `open_window` has drawn
    // the first frame; `displayLayer:` presents it synchronously in a transaction.
    cx.foreground_executor()
        .spawn(async move {
            let view = view as *mut AnyObject;
            let none = std::ptr::null_mut::<AnyObject>();
            unsafe {
                // Size the hidden layer for the screen's scale before drawing into it.
                let _: () = msg_send![view, viewDidChangeBackingProperties];
                let layer: *mut AnyObject = msg_send![view, layer];
                let _: () = msg_send![view, displayLayer: layer];
                let window: *mut AnyObject = msg_send![view, window];
                let _: () = msg_send![window, makeKeyAndOrderFront: none];
            }
        })
        .detach();
}
