//! The window's copy: the `desktop.*` messages of the panel's catalog
//! (native/macos/UI/translations.json), in the language the menu bar app
//! passes as FENNEC_LANGUAGE (else the system's, else English).

use serde_json::Value;
use std::sync::LazyLock;
use std::sync::atomic::{AtomicU8, Ordering};
static OVERRIDE: AtomicU8 = AtomicU8::new(0);
pub fn set_language(language: &str) {
    OVERRIDE.store(
        if language == "system" {
            0
        } else if language == "zh-CN" {
            2
        } else {
            1
        },
        Ordering::Relaxed,
    );
}
pub fn language() -> &'static str {
    match OVERRIDE.load(Ordering::Relaxed) {
        1 => "en-US",
        2 => "zh-CN",
        _ => *LANGUAGE,
    }
}

static CATALOG: LazyLock<Value> = LazyLock::new(|| {
    serde_json::from_str::<Value>(include_str!("../../native/macos/UI/translations.json"))
        .map(|v| v["messages"].clone())
        .unwrap_or_default()
});

static LANGUAGE: LazyLock<&'static str> = LazyLock::new(|| {
    let wanted = std::env::var("FENNEC_LANGUAGE")
        .ok()
        .or_else(|| std::env::var("LANG").ok())
        .unwrap_or_default()
        .to_lowercase();
    if wanted.starts_with("zh") {
        "zh-CN"
    } else {
        "en-US"
    }
});

/// `desktop.<key>` in the current language (English when missing).
pub fn t(key: &str) -> String {
	text_in(key, language())
}

pub fn text_in(key: &str, language: &str) -> String {
    let m = &CATALOG[format!("desktop.{key}")];
    m[language]
        .as_str()
        .or_else(|| m["en-US"].as_str())
        .unwrap_or(key)
        .to_owned()
}

/// Default copy shipped before a rewording, still stored on existing boards
/// (FEN-207 renamed "queue" to "project" in the UI). Translated retired copy
/// lives in the catalog as `desktop.<key>_retired` (FEN-231 dropped "delete
/// it if you don't need it": the last project can't be deleted).
const RETIRED_DEFAULTS: &[(&str, &str)] = &[(
    "starter_queue_description",
    "Send what you need here. Agents take it in turn and reply to you. Delete this queue if you don't need it.",
)];

/// Existing starter queues may hold any shipped translation. Localize their
/// defaults at display time without rewriting shared data or custom copy.
pub fn default_copy(key: &str, stored: &str, language: &str) -> String {
    let row = &CATALOG[format!("desktop.{key}")];
    let holds = |row: &serde_json::Value| row.as_object().is_some_and(|row| row.values().any(|v| v.as_str() == Some(stored)));
    let retired = RETIRED_DEFAULTS.iter().any(|(k, v)| *k == key && *v == stored)
        || holds(&CATALOG[format!("desktop.{key}_retired")]);
    if retired || holds(row) {
        text_in(key, language)
    } else {
        stored.to_owned()
    }
}

/// With `{name}` placeholders filled.
pub fn tf(key: &str, args: &[(&str, &str)]) -> String {
    args.iter()
        .fold(t(key), |s, (k, v)| s.replace(&format!("{{{k}}}"), v))
}
