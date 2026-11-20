//! Preferences owned by the native desktop, independent of the menu bar.
use crate::app::{FennecWindow, Route};
use crate::i18n::t;
use crate::ui::{self, c};
use gpui::prelude::FluentBuilder as _;
use gpui::*;
use gpui_base::{h_flex, v_flex};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

#[derive(Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct Preferences {
    pub notification_sounds: bool,
    pub revision: u64,
    pub theme: String,
    pub language: String,
    pub font_size: u16,
    pub padding_x: u16,
    pub padding_y: u16,
}
impl Default for Preferences {
    fn default() -> Self {
        Self {
            notification_sounds: true,
            revision: 0,
            theme: "system".into(),
            language: "system".into(),
            font_size: 14,
            padding_x: 14,
            padding_y: 12,
        }
    }
}
#[derive(Default)]
pub struct State {
    pub value: Preferences,
    pub previous: Option<Route>,
    pub saving: bool,
}
impl State {
    pub fn load() -> Self {
        let value = std::env::var_os("HOME")
            .and_then(|home| {
                std::fs::read(
                    std::path::PathBuf::from(home).join(".config/fennec/desktop-settings.json"),
                )
                .ok()
            })
            .and_then(|bytes| serde_json::from_slice::<Preferences>(&bytes).ok())
            .filter(|p| {
                ["system", "light", "dark"].contains(&p.theme.as_str())
                    && ["system", "en-US", "zh-CN"].contains(&p.language.as_str())
                    && (8..=32).contains(&p.font_size)
                    && p.padding_x <= 32
                    && p.padding_y <= 32
            })
            .unwrap_or_default();
        Self {
            value,
            ..Default::default()
        }
    }
}
impl FennecWindow {
    pub(crate) fn apply_desktop_preferences(
        &mut self,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let p = &self.app_preferences.value;
        let mut settings = (*crate::settings::get()).clone();
        settings.dark = match p.theme.as_str() {
            "dark" => true,
            "light" => false,
            _ => matches!(
                window.appearance(),
                WindowAppearance::Dark | WindowAppearance::VibrantDark
            ),
        };
        settings.font_size = f32::from(p.font_size);
        settings.terminal_padding = [f32::from(p.padding_x), f32::from(p.padding_y)];
        crate::settings::set(settings);
        crate::i18n::set_language(&p.language);
        self.search
            .update(cx, |s, cx| s.set_placeholder(t("search"), window, cx));
        self.composer
            .update(cx, |s, cx| s.set_placeholder(t("new_request"), window, cx));
        self.reply
            .update(cx, |s, cx| s.set_placeholder(t("reply"), window, cx));
        for term in self.terms.values() {
            term.read(cx).refresh_theme();
        }
        cx.notify();
    }
    fn change_desktop_preferences(
        &mut self,
        mut change: Value,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if self.app_preferences.saving || crate::preview::active() {
            return;
        }
        change["expected_revision"] = json!(self.app_preferences.value.revision);
        self.app_preferences.saving = true;
        cx.notify();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let result = cx
                .background_spawn(async move {
                    crate::control::call("desktop.settings.set", change)
                        .or_else(|_| crate::control::call("desktop.settings.get", json!({})))
                        .and_then(|v| Ok(serde_json::from_value::<Preferences>(v)?))
                })
                .await;
            let _ = this.update_in(cx, |this, window, cx| {
                this.app_preferences.saving = false;
                match result {
                    Ok(value) => {
                        this.app_preferences.value = value;
                        this.apply_desktop_preferences(window, cx);
                    }
                    Err(_) => this.say(t("try_again"), cx),
                };
                cx.notify();
            });
        }));
    }
    pub(crate) fn desktop_preferences_view(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let p = &self.app_preferences.value;
        let busy = self.app_preferences.saving;
        let choice = |id: &'static str, label: String, on: bool, change: Value| {
            div()
                .id(id)
                .h(px(28.0))
                .px(px(10.0))
                .rounded(px(6.0))
                .flex()
                .items_center()
                .text_size(px(12.5))
                .text_color(if on { c("f_text") } else { c("f_muted") })
                .when(on, |d| d.bg(c("f_card")).shadow(ui::selection_ring()))
                .opacity(if busy { 0.55 } else { 1.0 })
                .on_click(cx.listener(move |this, _, window, cx| {
                    this.change_desktop_preferences(change.clone(), window, cx)
                }))
                .child(label)
        };
        let row = |label: String, control: AnyElement| {
            h_flex()
                .min_h(px(48.0))
                .gap(px(14.0))
                .border_b_1()
                .border_color(c("f_line"))
                .child(div().flex_1().text_size(px(13.0)).child(label))
                .child(control)
        };
        let stepper = |key: &'static str, value: u16, min: u16, max: u16| {
            h_flex()
                .h(px(28.0))
                .rounded(px(7.0))
                .border_1()
                .border_color(c("f_field"))
                .overflow_hidden()
                .child(
                    div()
                        .id(SharedString::from(format!("{key}-less")))
                        .w(px(28.0))
                        .h_full()
                        .flex()
                        .items_center()
                        .justify_center()
                        .opacity(if value > min && !busy { 1.0 } else { 0.35 })
                        .on_click(cx.listener(move |this, _, window, cx| {
                            if value > min {
                                this.change_desktop_preferences(json!({(key):value-1}), window, cx);
                            }
                        }))
                        .child("−"),
                )
                .child(
                    div()
                        .w(px(36.0))
                        .flex()
                        .justify_center()
                        .text_size(px(13.0))
                        .child(value.to_string()),
                )
                .child(
                    div()
                        .id(SharedString::from(format!("{key}-more")))
                        .w(px(28.0))
                        .h_full()
                        .flex()
                        .items_center()
                        .justify_center()
                        .opacity(if value < max && !busy { 1.0 } else { 0.35 })
                        .on_click(cx.listener(move |this, _, window, cx| {
                            if value < max {
                                this.change_desktop_preferences(json!({(key):value+1}), window, cx);
                            }
                        }))
                        .child("+"),
                )
        };
        let card = || {
            v_flex()
                .px(px(14.0))
                .py(px(4.0))
                .bg(c("f_card"))
                .rounded(px(11.0))
                .border_1()
                .border_color(c("f_line"))
        };
        v_flex()
            .flex_1()
            .min_w_0()
            .h_full()
            .bg(c("f_expanded"))
            .child(
                h_flex()
                    .h(px(58.0))
                    .flex_none()
                    .px(px(18.0))
                    .gap(px(11.0))
                    .border_b_1()
                    .border_color(c("f_line"))
                    .on_mouse_down(MouseButton::Left, ui::drag_titlebar)
                    .when(!self.sidebar, |d| {
                        d.pl(px(crate::app::SIDEBAR_TOGGLE_LEFT))
                            .child(crate::app::toggle_room())
                    })
                    .child(
                        ui::icon_button("settings-home", "back").on_click(cx.listener(
                            |this, _, window, cx| {
                                if let Some(route) = this.app_preferences.previous.clone() {
                                    this.go(route, window, cx);
                                } else {
                                    this.route = None;
                                    cx.notify();
                                }
                            },
                        )),
                    )
                    .child(
                        div()
                            .text_size(px(13.5))
                            .font_weight(FontWeight::MEDIUM)
                            .child(t("settings")),
                    ),
            )
            .child(
                div()
                    .id("desktop-settings-scroll")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .child(
                        v_flex()
                            .max_w(px(760.0))
                            .px(px(28.0))
                            .py(px(20.0))
                            .gap(px(14.0))
                            .child(
                                div()
                                    .text_size(px(17.0))
                                    .font_weight(FontWeight::MEDIUM)
                                    .child(t("general")),
                            )
                            .child(
                                card()
                                    .child(row(
                                        t("notification_sounds"),
                                        h_flex()
                                            .gap(px(2.0))
                                            .child(choice(
                                                "sounds-on",
                                                t("on"),
                                                p.notification_sounds,
                                                json!({"notification_sounds":true}),
                                            ))
                                            .child(choice(
                                                "sounds-off",
                                                t("off"),
                                                !p.notification_sounds,
                                                json!({"notification_sounds":false}),
                                            ))
                                            .into_any_element(),
                                    ))
                                    .child(row(
                                        t("app_appearance"),
                                        h_flex()
                                            .p(px(2.0))
                                            .gap(px(2.0))
                                            .rounded(px(8.0))
                                            .bg(c("f_line"))
                                            .child(choice(
                                                "theme-system",
                                                t("app_system"),
                                                p.theme == "system",
                                                json!({"theme":"system"}),
                                            ))
                                            .child(choice(
                                                "theme-light",
                                                t("app_light"),
                                                p.theme == "light",
                                                json!({"theme":"light"}),
                                            ))
                                            .child(choice(
                                                "theme-dark",
                                                t("app_dark"),
                                                p.theme == "dark",
                                                json!({"theme":"dark"}),
                                            ))
                                            .into_any_element(),
                                    ))
                                    .child(row(
                                        t("app_language"),
                                        h_flex()
                                            .gap(px(2.0))
                                            .child(choice(
                                                "language-system",
                                                t("app_system"),
                                                p.language == "system",
                                                json!({"language":"system"}),
                                            ))
                                            .child(choice(
                                                "language-en",
                                                "English".into(),
                                                p.language == "en-US",
                                                json!({"language":"en-US"}),
                                            ))
                                            .child(choice(
                                                "language-zh",
                                                t("app_chinese"),
                                                p.language == "zh-CN",
                                                json!({"language":"zh-CN"}),
                                            ))
                                            .into_any_element(),
                                    ))
                                    .child(row(
                                        t("app_font_size"),
                                        stepper("font_size", p.font_size, 8, 32).into_any_element(),
                                    )),
                            )
                            .child(
                                div()
                                    .mt(px(8.0))
                                    .text_size(px(13.0))
                                    .font_weight(FontWeight::MEDIUM)
                                    .child(t("app_padding")),
                            )
                            .child(
                                card()
                                    .child(row(
                                        t("app_horizontal"),
                                        stepper("padding_x", p.padding_x, 0, 32).into_any_element(),
                                    ))
                                    .child(row(
                                        t("app_vertical"),
                                        stepper("padding_y", p.padding_y, 0, 32).into_any_element(),
                                    )),
                            )
                            .child(
                                div()
                                    .mt(px(8.0))
                                    .text_size(px(13.0))
                                    .font_weight(FontWeight::MEDIUM)
                                    .child(t("app_shortcuts")),
                            )
                            .child(
                                card().children(
                                    [
                                        ("tab.new", "new_tab"),
                                        ("tab.close", "close_tab"),
                                        ("tab.prev", "app_prev_tab"),
                                        ("tab.next", "app_next_tab"),
                                        ("mod.open:picker", "search"),
                                        ("app.settings", "settings"),
                                    ]
                                    .into_iter()
                                    .map(|(action, label)| {
                                        row(
                                            t(label),
                                            div()
                                                .text_size(px(12.0))
                                                .text_color(c("f_muted"))
                                                .child(crate::keys::shortcut(action))
                                                .into_any_element(),
                                        )
                                    }),
                                ),
                            ),
                    ),
            )
    }
}
