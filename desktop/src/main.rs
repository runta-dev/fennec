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
            let options = WindowOptions {
                window_bounds: Some(WindowBounds::centered(size(px(1280.0), px(820.0)), cx)),
                titlebar: Some(TitlebarOptions {
                    title: Some(if preview::active() { "Fennec design preview" } else { "Fennec" }.into()),
                    appears_transparent: true,
                    traffic_light_position: Some(point(px(16.0), px(17.0))),
                }),
                app_owns_titlebar_drag: cfg!(target_os = "macos"),
                window_decorations: Some(WindowDecorations::Client),
                app_id: Some("fennec-desktop".into()),
                window_min_size: Some(size(px(900.0), px(560.0))),
                ..Default::default()
            };
            cx.open_window(options, |window, cx| {
                cx.new(|cx| app::FennecWindow::new(requests, window, cx))
            })
            .expect("cannot open the Fennec window");
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
