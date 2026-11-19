//! Fennec's key bindings. Terminal keys are herdr-app's macOS defaults
//! (defaults/keys.macos.json); the terminal core sends what it doesn't
//! handle itself up to the window as `ShellAction`s.

use gpui::{App, KeyBinding};
use gpui_shell::action::ShellAction;

#[cfg(target_os = "macos")]
const TERMINAL: &[(&str, &str)] = &[
    ("cmd-f", "terminal.search"),
    ("cmd-a", "terminal.select_all"),
    ("cmd-c", "terminal.copy"),
    ("cmd-v", "terminal.paste"),
    ("ctrl-v", "terminal.paste_image"),
];
#[cfg(not(target_os = "macos"))]
const TERMINAL: &[(&str, &str)] = &[
    ("ctrl-shift-f", "terminal.search"),
    ("ctrl-shift-a", "terminal.select_all"),
    ("ctrl-shift-c", "terminal.copy"),
    ("ctrl-shift-v", "terminal.paste"),
    ("ctrl-v", "terminal.paste_image"),
];

pub fn install(cx: &mut App) {
    cx.bind_keys(
        TERMINAL.iter().map(|(keys, action)| {
            KeyBinding::new(keys, ShellAction::new(*action), Some("Terminal"))
        }),
    );
}
