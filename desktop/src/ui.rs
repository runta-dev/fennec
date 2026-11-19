//! Fennec's look in GPUI: the panel's tokens (native/macos/UI/panel.css),
//! embedded assets and the few shared pieces the window draws with.

use gpui::*;
use std::borrow::Cow;

/// A theme color (settings.rs): Fennec's `f_*` tokens, Herdr's chrome, the accents.
pub fn c(name: &str) -> Hsla {
    crate::settings::color(name)
}

pub struct Assets;

const FILES: &[(&str, &[u8])] = &[
    ("fennec.svg", include_bytes!("../assets/fennec.svg")),
    ("agents/claudecode.png", include_bytes!("../assets/agents/claudecode.png")),
    ("agents/codex.png", include_bytes!("../assets/agents/codex.png")),
    ("agents/cursor.png", include_bytes!("../assets/agents/cursor.png")),
    ("agents/kimi.png", include_bytes!("../assets/agents/kimi.png")),
    ("agents/kimi-dark.png", include_bytes!("../assets/agents/kimi-dark.png")),
    ("agents/opencode.png", include_bytes!("../assets/agents/opencode.png")),
    ("agents/pi.png", include_bytes!("../assets/agents/pi.png")),
    ("agents/xai.png", include_bytes!("../assets/agents/xai.png")),
    ("icons/search.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round"><circle cx="7" cy="7" r="4.5"/><path d="m11 11 3 3"/></svg>"##),
    ("icons/sidebar.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linejoin="round"><rect x="2" y="3" width="12" height="10" rx="2"/><path d="M6 3v10"/></svg>"##),
    ("icons/plus.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round"><path d="M8 3v10M3 8h10"/></svg>"##),
    ("icons/gear.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round"><circle cx="8" cy="8" r="2.2"/><path d="M8 1.8v1.6M8 12.6v1.6M1.8 8h1.6M12.6 8h1.6M3.6 3.6l1.1 1.1M11.3 11.3l1.1 1.1M3.6 12.4l1.1-1.1M11.3 4.7l1.1-1.1"/></svg>"##),
    ("icons/chat.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linejoin="round"><path d="M3 4.5A1.5 1.5 0 0 1 4.5 3h7A1.5 1.5 0 0 1 13 4.5v5a1.5 1.5 0 0 1-1.5 1.5H7l-3 2.2V11a1 1 0 0 1-1-1Z"/></svg>"##),
    ("icons/check.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="m3.5 8.5 3 3 6-7"/></svg>"##),
    ("icons/close.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round"><path d="m4.5 4.5 7 7M11.5 4.5l-7 7"/></svg>"##),
    ("icons/chevron.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="m4.5 6.5 3.5 3.5 3.5-3.5"/></svg>"##),
];

impl AssetSource for Assets {
    fn load(&self, path: &str) -> anyhow::Result<Option<Cow<'static, [u8]>>> {
        Ok(FILES
            .iter()
            .find(|(p, _)| *p == path)
            .map(|(_, b)| Cow::Borrowed(*b)))
    }
    fn list(&self, path: &str) -> anyhow::Result<Vec<SharedString>> {
        Ok(FILES
            .iter()
            .filter(|(p, _)| p.starts_with(path))
            .map(|(p, _)| SharedString::from(*p))
            .collect())
    }
}

/// A 1.5 px line icon, tinted.
pub fn icon(name: &str, size: f32, color: Hsla) -> Svg {
    svg()
        .path(SharedString::from(format!("icons/{name}.svg")))
        .size(px(size))
        .flex_none()
        .text_color(color)
}

/// The agent's icon (the iPhone app's), with Kimi's dark variant on dark.
pub fn agent_icon(agent: &str, size: f32) -> AnyElement {
    match crate::sessions::agent_icon(agent) {
        Some(name) => {
            let name = if name == "kimi" && crate::settings::get().dark {
                "kimi-dark"
            } else {
                name
            };
            img(SharedString::from(format!("agents/{name}.png")))
                .size(px(size))
                .flex_none()
                .into_any_element()
        }
        None => div()
            .size(px(size))
            .flex_none()
            .rounded(px(4.0))
            .bg(c("f_line"))
            .into_any_element(),
    }
}

/// A project's mark: its color at 18% with its initial, as on the panel.
pub fn project_mark(name: &str, color: &str, size: f32) -> Div {
    let tint = crate::settings::parse_hex(color).unwrap_or(c("orange"));
    let initial: String = name
        .chars()
        .next()
        .map(|ch| ch.to_uppercase().collect())
        .unwrap_or_default();
    div()
        .size(px(size))
        .flex_none()
        .rounded(px(if size > 24.0 { 8.0 } else { 5.0 }))
        .bg(tint.opacity(0.18))
        .text_color(tint)
        .text_size(px(size * 0.6))
        .font_weight(FontWeight::SEMIBOLD)
        .flex()
        .items_center()
        .justify_center()
        .child(initial)
}

/// Request status pill: Needs you (tint), Working, Queued (outline), Resolved.
pub fn status_pill(status: &str) -> Div {
    let (label, fg, bg, border) = match status {
        "needs_you" | "you" => (
            crate::i18n::t("needs_you"),
            c("accent_text"),
            c("tint"),
            c("orange").opacity(0.35),
        ),
        "working" => (
            crate::i18n::t("working"),
            c("ok"),
            c("ok").opacity(0.13),
            gpui::transparent_black(),
        ),
        "resolved" => (
            crate::i18n::t("resolved"),
            c("f_muted"),
            c("f_line"),
            gpui::transparent_black(),
        ),
        _ => (
            crate::i18n::t("queued"),
            c("f_muted"),
            gpui::transparent_black(),
            c("f_field"),
        ),
    };
    div()
        .flex_none()
        .px(px(7.0))
        .py(px(1.0))
        .rounded(px(9.0))
        .text_size(px(10.5))
        .font_weight(FontWeight::SEMIBOLD)
        .text_color(fg)
        .bg(bg)
        .border_1()
        .border_color(border)
        .child(label)
}

/// A tag chip (Bug, Feature, Discussion, or the project's own).
pub fn tag_chip(id: &str, label: &str) -> Div {
    let color = match id {
        "bug" | "feature" | "discussion" => c(id),
        _ => c("f_muted"),
    };
    div()
        .flex_none()
        .h(px(19.0))
        .px(px(7.0))
        .rounded(px(5.0))
        .text_size(px(11.0))
        .font_weight(FontWeight::MEDIUM)
        .text_color(color)
        .bg(color.opacity(0.12))
        .flex()
        .items_center()
        .child(label.to_owned())
}

/// The 7-on-30 status dot from Fennec for iPhone, here 4 px on a 16 px icon.
pub fn status_dot(state: crate::sessions::State) -> Div {
    let color = match state {
        crate::sessions::State::NeedsYou => c("orange"),
        crate::sessions::State::Working => c("ok"),
        crate::sessions::State::Idle => c("f_muted"),
    };
    div()
        .size(px(5.0))
        .rounded_full()
        .bg(color)
        .border_1()
        .border_color(c("f_bg"))
}
