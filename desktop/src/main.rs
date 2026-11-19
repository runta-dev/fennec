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
mod mirror;
mod keys;
mod mods;
mod settings;

// Fennec.
mod app;
mod board;
mod control;
mod i18n;
mod project_settings;
mod sessions;
mod ui;

use gpui::*;

#[cfg(test)]
pub(crate) fn test_tmp() -> std::path::PathBuf {
    std::env::temp_dir()
}

fn main() {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::try_from_env("FENNEC_DESKTOP_LOG").unwrap_or_else(|_| "warn,fennec_desktop=info".into()))
        .init();
    let core = hc::Core::connect();
    gpui_platform::application().with_assets(ui::Assets).run(move |cx| {
        gpui_base::init(cx);
        settings::load_bundled_fonts(cx);
        keys::install(cx);
        let dark = matches!(cx.window_appearance(), WindowAppearance::Dark | WindowAppearance::VibrantDark);
        settings::set_dark(dark);
        let requests = mods::requests();
        let options = WindowOptions {
            window_bounds: Some(WindowBounds::centered(size(px(1280.0), px(820.0)), cx)),
            titlebar: Some(TitlebarOptions {
                title: Some("Fennec".into()),
                appears_transparent: true,
                traffic_light_position: Some(point(px(16.0), px(17.0))),
            }),
            app_owns_titlebar_drag: cfg!(target_os = "macos"),
            window_decorations: Some(WindowDecorations::Client),
            app_id: Some("fennec-desktop".into()),
            window_min_size: Some(size(px(900.0), px(560.0))),
            ..Default::default()
        };
        cx.open_window(options, |window, cx| cx.new(|cx| app::FennecWindow::new(core, requests, window, cx)))
            .expect("cannot open the Fennec window");
        cx.activate(true);
    });
}
