//! The window's copy: the `desktop.*` messages of the panel's catalog
//! (native/macos/UI/translations.json), in the language the menu bar app
//! passes as FENNEC_LANGUAGE (else the system's, else English).

use serde_json::Value;
use std::sync::LazyLock;

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
    let m = &CATALOG[format!("desktop.{key}")];
    m[*LANGUAGE]
        .as_str()
        .or_else(|| m["en-US"].as_str())
        .unwrap_or(key)
        .to_owned()
}

/// With `{name}` placeholders filled.
pub fn tf(key: &str, args: &[(&str, &str)]) -> String {
    args.iter()
        .fold(t(key), |s, (k, v)| s.replace(&format!("{{{k}}}"), v))
}
