//! Native keymap with per-user overrides in ~/.config/fennec/desktop-keys.json.
//! The file uses Herdr's global/terminal map shape; unsupported actions are
//! ignored rather than intercepting keys for features Fennec does not provide.
use gpui::{App, KeyBinding};
use gpui_shell::action::ShellAction;
use std::collections::BTreeMap;
use std::sync::OnceLock;
static ACTIVE: OnceLock<BTreeMap<String, String>> = OnceLock::new();
pub fn shortcut(action: &str) -> String {
    ACTIVE
        .get()
        .into_iter()
        .flat_map(|keys| keys.iter())
        .filter(|(_, v)| v.as_str() == action)
        .map(|(k, _)| {
            k.replace("cmd-", "⌘")
                .replace("ctrl-", "⌃")
                .replace("alt-", "⌥")
                .replace("shift-", "⇧")
                .replace("tab", "⇥")
                .to_uppercase()
        })
        .collect::<Vec<_>>()
        .join(" / ")
}

fn supported(action: &str) -> bool {
    action.starts_with("send:")
        || action.starts_with("tab.select:")
        || action.starts_with("space.select:")
        || matches!(
            action,
            "app.settings"
                | "tab.new"
                | "tab.close"
                | "tab.next"
                | "tab.prev"
                | "space.next"
                | "space.prev"
                | "agent.next"
                | "agent.prev"
                | "layout.toggle:sidebar"
                | "mod.open:picker"
                | "mod.open:tui-prefix"
                | "mod.open:tui-close-others"
                | "app.zoom_in"
                | "app.zoom_out"
                | "app.zoom_reset"
                | "terminal.copy"
                | "terminal.paste"
                | "terminal.paste_image"
                | "terminal.search"
                | "terminal.select_all"
        )
}
pub fn install(cx: &mut App) {
    let mut global = BTreeMap::<String, String>::from_iter(
        [
            ("cmd-,", "app.settings"),
            ("cmd-t", "tab.new"),
            ("cmd-w", "tab.close"),
            ("ctrl-tab", "tab.next"),
            ("ctrl-shift-tab", "tab.prev"),
            ("cmd-shift-]", "tab.next"),
            ("cmd-shift-[", "tab.prev"),
            ("cmd-p", "mod.open:picker"),
        ]
        .map(|(k, v)| (k.into(), v.into())),
    );
    let mut terminal = BTreeMap::<String, String>::from_iter(
        [
            ("cmd-c", "terminal.copy"),
            ("cmd-v", "terminal.paste"),
            ("ctrl-v", "terminal.paste_image"),
            ("cmd-f", "terminal.search"),
            ("cmd-a", "terminal.select_all"),
        ]
        .map(|(k, v)| (k.into(), v.into())),
    );
    if let Some(home) = std::env::var_os("HOME") {
        let path = std::path::PathBuf::from(home).join(".config/fennec/desktop-keys.json");
        if let Ok(bytes) = std::fs::read(path) {
            if let Ok(config) = serde_json::from_slice::<serde_json::Value>(&bytes) {
                for (section, target) in [("global", &mut global), ("terminal", &mut terminal)] {
                    if let Some(map) = config[section].as_object() {
                        for (key, value) in map {
                            if let Some(action) = value.as_str().filter(|a| supported(a)) {
                                target.insert(key.clone(), action.to_owned());
                            }
                        }
                    }
                }
            }
        }
    }
    let _ = ACTIVE.set(global.clone());
    cx.bind_keys(global.into_iter().map(|(key, action)| {
        KeyBinding::new(&key, ShellAction::new(action), Some("FennecWindow"))
    }));
    cx.bind_keys(
        terminal
            .into_iter()
            .map(|(key, action)| KeyBinding::new(&key, ShellAction::new(action), Some("Terminal"))),
    );
}
