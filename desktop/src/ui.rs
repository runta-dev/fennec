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
    ("icons/trash.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5"/></svg>"##),
    ("icons/sliders.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 5h6M12 5h1M3 11h1M7 11h6"/><circle cx="10.5" cy="5" r="1.5"/><circle cx="5.5" cy="11" r="1.5"/></svg>"##),
    ("icons/bot.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="5" width="10" height="8" rx="2"/><path d="M8 2.5V5M6 9h.01M10 9h.01"/></svg>"##),
    ("icons/layers.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="m8 2.5 5.5 3L8 8.5l-5.5-3Z"/><path d="m2.5 8.5 5.5 3 5.5-3M2.5 11l5.5 3 5.5-3"/></svg>"##),
    ("icons/book.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3.5h4a1.5 1.5 0 0 1 1.5 1.5v8A1.5 1.5 0 0 0 7 11.5H3Zm10 0H9.5A1.5 1.5 0 0 0 8 5v8a1.5 1.5 0 0 1 1.5-1.5H13Z"/></svg>"##),
    ("icons/back.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M10 3.5 5.5 8l4.5 4.5"/></svg>"##),
    ("icons/code.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="m5.5 4.5-3 3.5 3 3.5M10.5 4.5l3 3.5-3 3.5"/></svg>"##),
    ("icons/quote.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 4v8M6 5h7M6 8h7M6 11h5"/></svg>"##),
    ("icons/olist.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6.5 4H14M6.5 8H14M6.5 12H14M2.5 3h1v3M2.3 9.2c.4-.6 1.7-.5 1.7.3 0 .8-1.7 1.4-1.7 2.5H4"/></svg>"##),
    ("icons/list.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6 4h8M6 8h8M6 12h8"/><circle cx="2.8" cy="4" r=".6"/><circle cx="2.8" cy="8" r=".6"/><circle cx="2.8" cy="12" r=".6"/></svg>"##),
    ("icons/link.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M7 9a2.5 2.5 0 0 0 3.5 0l2-2A2.5 2.5 0 0 0 9 3.5l-.7.7M9 7a2.5 2.5 0 0 0-3.5 0l-2 2A2.5 2.5 0 0 0 7 12.5l.7-.7"/></svg>"##),
    ("icons/image.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="10" rx="2"/><circle cx="6" cy="6.5" r="1.2"/><path d="m3 12 3.5-3.5L9 11l2-2 3 3"/></svg>"##),
    ("icons/sort.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M5 3v10M2.5 10.5 5 13l2.5-2.5M11 13V3M8.5 5.5 11 3l2.5 2.5"/></svg>"##),
    ("icons/term.svg", include_bytes!("../assets/icons/terminal.svg")),
    ("fennec.svg", include_bytes!("../assets/fennec.svg")),
    ("agents/claudecode.png", include_bytes!("../assets/agents/claudecode.png")),
    ("agents/codex.png", include_bytes!("../assets/agents/codex.png")),
    ("agents/cursor.png", include_bytes!("../assets/agents/cursor.png")),
    ("agents/kimi.png", include_bytes!("../assets/agents/kimi.png")),
    ("agents/kimi-dark.png", include_bytes!("../assets/agents/kimi-dark.png")),
    ("agents/opencode.png", include_bytes!("../assets/agents/opencode.png")),
    ("agents/pi.png", include_bytes!("../assets/agents/pi.png")),
    ("agents/xai.png", include_bytes!("../assets/agents/xai.png")),
    ("icons/search.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="m11 11 3 3"/><circle cx="7" cy="7" r="4.5"/></svg>"##),
    ("icons/sidebar.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="10" rx="2"/><path d="M6 3v10"/></svg>"##),
    ("icons/plus.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10M3 8h10"/></svg>"##),
    ("icons/gear.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><g transform="scale(.6667)" stroke-width="2.25"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></g></svg>"##),
    ("icons/chat.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 4.5A1.5 1.5 0 0 1 4.5 3h7A1.5 1.5 0 0 1 13 4.5v5a1.5 1.5 0 0 1-1.5 1.5H7l-3 2.2V11h0a1 1 0 0 1-1-1Z"/></svg>"##),
    ("icons/check.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="m3.5 8.5 3 3 6-7"/></svg>"##),
    ("icons/close.svg", br##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="#000" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="m4 4 8 8M12 4l-8 8"/></svg>"##),
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
        None => return icon("term", size, c("f_muted")).into_any_element(),
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
        .font_weight(FontWeight::MEDIUM)
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
        .h(px(19.0))
        .rounded(px(5.0))
        .flex()
        .items_center()
        .text_size(px(11.0))
        .font_weight(FontWeight::MEDIUM)
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
        .size(px(4.0))
        .rounded_full()
        .bg(color)
        .border_1()
        .border_color(c("f_bg"))
}

pub fn project_icon(project: &crate::board::Project, size: f32) -> AnyElement {
    use base64::Engine;
    if let Some(encoded) = project.icon.strip_prefix("data:image/png;base64,") {
        if let Ok(bytes) = base64::engine::general_purpose::STANDARD.decode(encoded) {
            return div()
                .size(px(size))
                .flex_none()
                .rounded(px(if size > 24.0 { 8.0 } else { 5.0 }))
                .overflow_hidden()
                .child(
                    img(std::sync::Arc::new(Image::from_bytes(
                        ImageFormat::Png,
                        bytes,
                    )))
                    .size(px(size)),
                )
                .into_any_element();
        }
    }
    project_mark(&project.name, &project.color, size).into_any_element()
}

/// Reference .side-row.sel: an outside hairline and a subtle lower shadow.
/// Shadows do not take layout space, so selection cannot move row contents.
pub fn selection_shadow() -> Vec<BoxShadow> {
    vec![
        BoxShadow {
            color: hsla(0.0, 0.0, 0.0, 0.07),
            offset: point(px(0.0), px(1.0)),
            blur_radius: px(2.0),
            spread_radius: px(0.0),
            inset: false,
        },
        BoxShadow {
            color: c("f_line"),
            offset: point(px(0.0), px(0.0)),
            blur_radius: px(0.0),
            spread_radius: px(1.0),
            inset: false,
        },
    ]
}
pub fn selection_ring() -> Vec<BoxShadow> {
    vec![BoxShadow {
        color: c("f_line"),
        offset: point(px(0.0), px(0.0)),
        blur_radius: px(0.0),
        spread_radius: px(1.0),
        inset: false,
    }]
}

/// The menu bar's .ios-app-close/#settings metrics, reused for chrome actions.
pub fn icon_button(id: &'static str, name: &str) -> Stateful<Div> {
    div()
        .id(id)
        .size(px(28.0))
        .flex_none()
        .rounded(px(6.0))
        .flex()
        .items_center()
        .justify_center()
        .text_color(c("f_muted"))
        .hover(|s| s.bg(c("f_line")).text_color(c("f_text")))
        .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
        .child(
            svg()
                .path(SharedString::from(format!("icons/{name}.svg")))
                .text_color(c("f_muted"))
                .size(px(17.0)),
        )
}

pub fn drag_titlebar(event: &MouseDownEvent, window: &mut Window, _: &mut App) {
    if event.click_count == 2 {
        window.zoom_window();
    } else {
        window.start_window_move();
    }
}
