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
                | "request.new"
                | "history.back"
                | "history.forward"
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
pub const DEFAULTS: &str = r#"{
    "global": {
        "cmd-,": "app.settings", "cmd-n": "request.new", "cmd-t": "tab.new", "cmd-w": "tab.close",
        "ctrl-tab": "tab.next", "ctrl-shift-tab": "tab.prev",
        "cmd-shift-]": "tab.next", "cmd-shift-[": "tab.prev", "cmd-p": "mod.open:picker",
        "cmd-[": "history.back", "cmd-]": "history.forward"
    },
    "terminal": {
        "cmd-c": "terminal.copy", "cmd-v": "terminal.paste",
        "ctrl-v": "terminal.paste_image", "cmd-f": "terminal.search", "cmd-a": "terminal.select_all"
    }
}"#;

#[derive(serde::Deserialize)]
pub struct Bindings {
    global: BTreeMap<String, String>,
    terminal: BTreeMap<String, String>,
}

pub fn parse(source: &str) -> serde_json::Result<Bindings> {
    serde_json::from_str(source)
}

pub fn install_user(cx: &mut App) {
    let Bindings { mut global, mut terminal } = parse(DEFAULTS).expect("shipped keys parse");
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
    install(Bindings { global, terminal }, cx);
}

// Keep the upstream terminal tests on the same defaults and binding path as
// production, without reading the user's overrides in those tests.
pub fn install(bindings: Bindings, cx: &mut App) {
    let Bindings { global, terminal } = bindings;
    let _ = ACTIVE.set(global.clone());
    // Copy what is selected in thread and knowledge text; terminals and inputs bind their own.
    cx.bind_keys([KeyBinding::new(
        if cfg!(target_os = "macos") { "cmd-c" } else { "ctrl-c" },
        gpui_base::input::Copy,
        Some("FennecWindow"),
    )]);
    cx.bind_keys(global.into_iter().map(|(key, action)| {
        KeyBinding::new(&key, ShellAction::new(action), Some("FennecWindow"))
    }));
    cx.bind_keys(
        terminal
            .into_iter()
            .map(|(key, action)| KeyBinding::new(&key, ShellAction::new(action), Some("Terminal"))),
    );
}
