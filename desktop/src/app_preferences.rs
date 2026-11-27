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
    pub sidebar_width: u16,
    /// The queue's request list; 0 follows the window (a quarter of it, so list:thread is 1:3).
    pub queue_width: u16,
}
impl Default for Preferences {
    fn default() -> Self {
        Self {
            notification_sounds: true,
            revision: 0,
            theme: "dark".into(),
            language: "system".into(),
            font_size: 14,
            padding_x: 0,
            padding_y: 0,
            sidebar_width: 252,
            queue_width: 0,
        }
    }
}
#[derive(Default)]
pub struct State {
    pub value: Preferences,
    pub previous: Option<Route>,
    pub saving: bool,
    /// Settings → Export logs: busy while the daemon builds the archive.
    pub exporting_logs: bool,
    /// What the export row says after the save panel closes.
    pub log_export_status: Option<String>,
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
                    && (180..=480).contains(&p.sidebar_width)
                    && (p.queue_width == 0 || (240..=720).contains(&p.queue_width))
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
        // FEN-187: give AppKit the chosen theme too. Otherwise a dark Fennec
        // theme on a light Mac keeps a light NSWindow, whose inactive traffic
        // lights are drawn dark (black dots on the dark title bar) instead of gray.
        cx.set_window_appearance(match p.theme.as_str() {
            "dark" => Some(WindowAppearance::Dark),
            "light" => Some(WindowAppearance::Light),
            _ => None,
        });
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
        let old_language = crate::i18n::language();
        crate::i18n::set_language(&p.language);
        if old_language != crate::i18n::language() {
            self.settings = Default::default();
        }
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
                    Ok(mut value) => {
                        if this.column_resize_busy() {
                            value.queue_width = this.app_preferences.value.queue_width;
                        }
                        this.app_preferences.value = value;
                        this.apply_desktop_preferences(window, cx);
                    }
                    Err(_) => this.say(t("try_again"), cx),
                };
                cx.notify();
            });
        }));
    }
    /// The daemon zips the last seven days of logs (THI-145); the user picks
    /// where the archive goes, and Finder shows it.
    fn export_logs(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.app_preferences.exporting_logs || crate::preview::active() {
            return;
        }
        self.app_preferences.exporting_logs = true;
        self.app_preferences.log_export_status = Some(t("export_logs_collecting"));
        cx.notify();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let exported = cx
                .background_spawn(async move { crate::control::call("logs.export", json!({})) })
                .await
                .ok()
                .and_then(|v| Some((v["path"].as_str()?.to_owned(), v["name"].as_str()?.to_owned())));
            let finish = |this: &mut FennecWindow, status: Option<String>, cx: &mut Context<FennecWindow>| {
                this.app_preferences.exporting_logs = false;
                this.app_preferences.log_export_status = status;
                cx.notify();
            };
            let Some((source, name)) = exported else {
                let _ = this.update(cx, |this, cx| finish(this, Some(t("export_logs_retry")), cx));
                return;
            };
            let downloads = std::env::var_os("HOME")
                .map(|home| std::path::PathBuf::from(home).join("Downloads"))
                .unwrap_or_default();
            let Ok(chosen) = this.update(cx, |_, cx| cx.prompt_for_new_path(&downloads, Some(&name))) else {
                return;
            };
            let Ok(Ok(Some(destination))) = chosen.await else {
                let _ = std::fs::remove_file(&source);
                let _ = this.update(cx, |this, cx| finish(this, None, cx));
                return;
            };
            let target = destination.clone();
            let moved = cx
                .background_spawn(async move {
                    let _ = std::fs::remove_file(&target);
                    std::fs::rename(&source, &target).or_else(|_| {
                        std::fs::copy(&source, &target)?;
                        std::fs::remove_file(&source)
                    })
                })
                .await;
            let _ = this.update(cx, |this, cx| match moved {
                Ok(()) => {
                    cx.reveal_path(&destination);
                    finish(this, Some(t("export_logs_saved")), cx);
                }
                Err(error) => {
                    tracing::warn!("log export: {error}");
                    finish(this, Some(t("export_logs_retry")), cx);
                }
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
        // Dividers go between rows only, so a card never ends in a stray line.
        let card = |rows: Vec<Div>| {
            v_flex()
                .px(px(14.0))
                .py(px(4.0))
                .bg(c("f_card"))
                .rounded(px(11.0))
                .border_1()
                .border_color(c("f_line"))
                .children(rows.into_iter().enumerate().map(|(i, r)| {
                    r.when(i > 0, |r| r.border_t_1().border_color(c("f_line")))
                }))
        };
        v_flex()
            .flex_1()
            .min_w_0()
            .h_full()
            .bg(c("background"))
            .child(
                h_flex()
                    .h(px(58.0))
                    .flex_none()
                    .px(px(18.0))
                    .gap(px(11.0))
                    .border_b_1()
                    .border_color(c("f_line"))
                    .on_mouse_down(MouseButton::Left, ui::drag_titlebar)
                    .pl(px(18.0))
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
                                card(vec![
                                    row(
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
                                    ),
                                    row(
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
                                    ),
                                    row(
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
                                    ),
                                    row(
                                        t("app_font_size"),
                                        stepper("font_size", p.font_size, 8, 32).into_any_element(),
                                    ),
                                ]),
                            )
                            .child(
                                div()
                                    .mt(px(8.0))
                                    .text_size(px(13.0))
                                    .font_weight(FontWeight::MEDIUM)
                                    .child(t("app_padding")),
                            )
                            .child(
                                card(vec![
                                    row(
                                        t("app_horizontal"),
                                        stepper("padding_x", p.padding_x, 0, 32).into_any_element(),
                                    ),
                                    row(
                                        t("app_vertical"),
                                        stepper("padding_y", p.padding_y, 0, 32).into_any_element(),
                                    ),
                                ]),
                            )
                            .child(
                                div()
                                    .mt(px(8.0))
                                    .text_size(px(13.0))
                                    .font_weight(FontWeight::MEDIUM)
                                    .child(t("app_shortcuts")),
                            )
                            .child(
                                card(
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
                                    })
                                    .collect(),
                                ),
                            )
                            .child(
                                div()
                                    .mt(px(8.0))
                                    .text_size(px(13.0))
                                    .font_weight(FontWeight::MEDIUM)
                                    .child(t("archive_section")),
                            )
                            .child(card(vec![
                                archive_row(
                                    "archive-export-row",
                                    t("archive_export"),
                                    t("archive_export_description"),
                                    crate::app_archive::Mode::Export,
                                    cx,
                                ),
                                archive_row(
                                    "archive-import-row",
                                    t("archive_import"),
                                    t("archive_import_description"),
                                    crate::app_archive::Mode::Import,
                                    cx,
                                ),
                            ]))
                            .child(
                                div()
                                    .mt(px(8.0))
                                    .text_size(px(13.0))
                                    .font_weight(FontWeight::MEDIUM)
                                    .child(t("app_troubleshooting")),
                            )
                            .child(card(vec![row(
                                t("export_logs_label"),
                                h_flex()
                                    .gap(px(10.0))
                                    .when_some(
                                        self.app_preferences.log_export_status.clone(),
                                        |d, status| {
                                            d.child(
                                                div()
                                                    .text_size(px(12.0))
                                                    .text_color(c("f_muted"))
                                                    .child(status),
                                            )
                                        },
                                    )
                                    .child(
                                        div()
                                            .id("export-logs")
                                            .h(px(28.0))
                                            .px(px(12.0))
                                            .rounded(px(7.0))
                                            .border_1()
                                            .border_color(c("f_field"))
                                            .flex()
                                            .items_center()
                                            .text_size(px(12.5))
                                            .opacity(if self.app_preferences.exporting_logs { 0.55 } else { 1.0 })
                                            .on_click(cx.listener(|this, _, window, cx| {
                                                this.export_logs(window, cx)
                                            }))
                                            .child(t("export_logs")),
                                    )
                                    .into_any_element(),
                            )])),
                    ),
            )
    }
}

/// A Settings › Cloud computer row that opens the export or import dialog.
fn archive_row(
    id: &'static str,
    label: String,
    description: String,
    mode: crate::app_archive::Mode,
    cx: &mut Context<FennecWindow>,
) -> Div {
    h_flex().child(
        h_flex()
            .id(id)
            .flex_1()
            .min_h(px(52.0))
            .py(px(8.0))
            .gap(px(14.0))
            .mx(px(-8.0))
            .px(px(8.0))
            .rounded(px(7.0))
            .cursor_pointer()
            .hover(|s| s.bg(c("f_expanded")))
            .on_click(cx.listener(move |this, _, window, cx| this.open_archive(mode, window, cx)))
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .gap(px(2.0))
                    .child(div().text_size(px(13.0)).child(label))
                    .child(div().text_size(px(11.5)).text_color(c("f_muted")).child(description)),
            )
            .child(ui::icon("forward", 13.0, c("f_muted"))),
    )
}
