//! Settings › Channels (FEN-228, design FEN-217 hd.html `?s=set`): the places
//! whose messages become the project's requests, with the tags each one
//! gives them and a switch, then the kinds still to connect. There is no
//! triage agent and no rate limit: each message is a new request.
//!
//! Connecting is real: the form takes the user's own bot / site keys, the
//! daemon pipes them over SSH to `fennec-terminal channel setup` on the cloud
//! computer (`internal/channel/setup.go`), which checks them with Discord or
//! Cloudflare and keeps them in its 0600 config. Only the non-secret row
//! (kind, source, tags, on) is saved to the project. A channel appears under
//! Connected only after that check passed.

use crate::app::{FennecWindow, primary_button, secondary_button};
use crate::board::{Channel, Project, Tag};
use crate::i18n::{t, tf};
use crate::ui::{self, c};
use gpui::prelude::FluentBuilder as _;
use gpui::*;
use gpui_base::input::{Input, InputState};
use gpui_base::{h_flex, v_flex};
use serde_json::{Value, json};

/// Every kind, in the order the page lists them.
const KINDS: [&str; 4] = ["discord", "widget", "slack", "x"];

/// Kinds the intake service has an adapter for (FEN-224, FEN-226). Slack
/// and X are listed as coming later.
const READY: [&str; 2] = ["discord", "widget"];

/// The form's fields per kind: key, and whether it is a secret.
fn fields(kind: &str) -> &'static [(&'static str, bool)] {
    match kind {
        "discord" => &[
            ("application_id", false),
            ("public_key", false),
            ("bot_token", true),
            ("channels", false),
            ("guilds", false),
        ],
        _ => &[("origins", false), ("site_key", false), ("secret", true)],
    }
}

/// Where to get the keys, opened from the form.
fn portal(kind: &str) -> &'static str {
    match kind {
        "discord" => "https://discord.com/developers/applications",
        _ => "https://dash.cloudflare.com/?to=/:account/turnstile",
    }
}

/// The open connect / edit form.
pub(crate) struct ChannelForm {
    /// The channel's id, shared by the project row and the cloud config.
    id: String,
    kind: &'static str,
    /// Not in the project yet: saving adds it.
    new: bool,
    inputs: Vec<(&'static str, Entity<InputState>)>,
    busy: bool,
    error: Option<String>,
    /// After saving: what to do next (Discord's endpoint URL, the widget's
    /// embed tag, or that the intake isn't running yet).
    note: Option<(String, Option<String>)>,
}

impl ChannelForm {
    pub(crate) fn inputs(&self) -> impl Iterator<Item = &Entity<InputState>> {
        self.inputs.iter().map(|(_, i)| i)
    }
}

/// The logo tile's glyph and color for a kind.
fn logo(kind: &str) -> (&'static str, Hsla) {
    match kind {
        "discord" => ("discord", rgb(0x5865f2).into()),
        "widget" => ("globe", c("orange")),
        // Slack's mark keeps its four colors, on white like its app icon.
        "slack" => ("", rgb(0xffffff).into()),
        _ => ("x-logo", rgb(0x000000).into()),
    }
}

/// The tags a new channel of `kind` gives its requests: outside people's
/// messages are Feedback plus the channel; Slack is the team's own, so only
/// Slack (FEN-217 round 5).
fn default_tags(kind: &str) -> Vec<Tag> {
    let own = Tag {
        id: kind.to_owned(),
        label: match kind {
            "discord" => "Discord",
            "widget" => "Widget",
            "slack" => "Slack",
            _ => "X",
        }
        .to_owned(),
    };
    if kind == "slack" {
        vec![own]
    } else {
        vec![
            Tag {
                id: "feedback".into(),
                label: "Feedback".into(),
            },
            own,
        ]
    }
}

/// A fresh channel id. The cloud config is shared by every project, so the
/// id carries a random part, not just the kind.
fn new_id(kind: &str, channels: &[Channel]) -> String {
    let mut seed = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0);
    loop {
        seed = seed.wrapping_mul(6364136223846793005).wrapping_add(1442695040888963407);
        let id = format!("{kind}-{:04x}", (seed >> 48) as u16);
        if !channels.iter().any(|ch| ch.id == id) {
            return id;
        }
    }
}

/// A new channel row of `kind` and the project tags it needs, added to
/// `tags` where missing.
pub(crate) fn connect(kind: &str, id: String, channels: &[Channel], tags: &[Tag]) -> (Channel, Vec<Tag>) {
    let _ = channels;
    let mut tags = tags.to_vec();
    let mut ids = Vec::new();
    for tag in default_tags(kind) {
        if let Some(have) = tags
            .iter()
            .find(|t| t.id == tag.id || t.label.eq_ignore_ascii_case(&tag.label))
        {
            ids.push(have.id.clone());
        } else {
            ids.push(tag.id.clone());
            tags.push(tag);
        }
    }
    let channel = Channel {
        id,
        kind: kind.to_owned(),
        source: String::new(),
        tags: ids,
        on: true,
    };
    (channel, tags)
}

/// Splits a list typed into one field: commas, spaces or new lines.
fn list(value: &str) -> Vec<String> {
    value
        .split(|c: char| c == ',' || c == '，' || c.is_whitespace())
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(str::to_owned)
        .collect()
}

/// The `channels.setup` request for a form's values.
fn setup_request(project: &str, id: &str, kind: &str, values: &[(&str, String)]) -> Value {
    let get = |k: &str| values.iter().find(|(key, _)| *key == k).map(|(_, v)| v.trim().to_owned()).unwrap_or_default();
    let mut req = json!({"project": project, "id": id, "kind": kind});
    if kind == "discord" {
        req["discord"] = json!({
            "application_id": get("application_id"),
            "public_key": get("public_key"),
            "bot_token": get("bot_token"),
            "channels": list(&get("channels")),
            "guilds": list(&get("guilds")),
        });
    } else {
        req["widget"] = json!({
            "origins": list(&get("origins")),
            "site_key": get("site_key"),
            "secret": get("secret"),
        });
    }
    req
}

/// The user-facing words for a failed check.
fn setup_error(result: &Value) -> String {
    let code = result["code"].as_str().unwrap_or("");
    let detail = result["detail"].as_str().unwrap_or("");
    let key = format!("channel_error_{code}");
    let text = t(&key);
    if text == key {
        t("channel_error_cloud")
    } else {
        tf(&key, &[("detail", detail)])
    }
}

impl FennecWindow {
    fn save_channels(
        &mut self,
        channels: Vec<Channel>,
        tags: Option<Vec<Tag>>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let mut change = json!({ "channels": channels });
        if let Some(tags) = tags {
            // General's tag list saves from this state, so keep it current.
            self.settings.tags = tags.clone();
            change["tags"] = json!(tags);
        }
        // Show the change now; the saved project replaces it.
        let id = self.settings.project.clone();
        if let Some(p) = self.projects.iter_mut().find(|p| p.id == id) {
            p.channels = channels;
        }
        self.patch_project(change, window, cx);
        cx.notify();
    }

    /// Opens the form for a new channel of `kind`, or for channel `id`.
    fn open_channel_form(&mut self, p: &Project, kind: &'static str, id: Option<String>, window: &mut Window, cx: &mut Context<Self>) {
        let new = id.is_none();
        let id = id.unwrap_or_else(|| new_id(kind, &p.channels));
        let inputs = fields(kind)
            .iter()
            .map(|(key, secret)| {
                let input = cx.new(|cx| {
                    InputState::new(window, cx)
                        .placeholder(t(&format!("channel_field_{key}_hint")))
                        .masked(*secret)
                });
                (*key, input)
            })
            .collect();
        self.settings.adding_channel = false;
        self.settings.channel_form = Some(ChannelForm { id: id.clone(), kind, new, inputs, busy: !new, error: None, note: None });
        cx.notify();
        if new || crate::preview::active() {
            if let Some(f) = self.settings.channel_form.as_mut() {
                f.busy = false;
            }
            return;
        }
        // Fill in what's saved; secrets stay empty ("saved, leave empty").
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let fetch_id = id.clone();
            let info = cx
                .background_spawn(async move { crate::control::call("channels.info", json!({"id": fetch_id})) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| {
                let Some(form) = this.settings.channel_form.as_mut().filter(|f| f.id == id) else { return };
                form.busy = false;
                let Ok(info) = info else {
                    form.error = Some(t("channel_error_cloud"));
                    cx.notify();
                    return;
                };
                let section = &info[if form.kind == "discord" { "discord" } else { "widget" }];
                for (key, input) in &form.inputs {
                    let value = match &section[*key] {
                        Value::String(s) => s.clone(),
                        Value::Array(a) => a.iter().filter_map(|v| v.as_str()).collect::<Vec<_>>().join(", "),
                        _ => String::new(),
                    };
                    let secret = fields(form.kind).iter().any(|(k, s)| k == key && *s);
                    input.update(cx, |s, cx| {
                        if secret && info["has_secret"].as_bool() == Some(true) {
                            s.set_placeholder(t("channel_secret_saved"), window, cx);
                        }
                        s.set_value(value, window, cx);
                    });
                }
                cx.notify();
            });
        }));
    }

    /// Checks and saves the form on the cloud computer; on success the
    /// project gets (or updates) the channel's row.
    fn submit_channel_form(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let project = self.settings.project.clone();
        let Some(form) = self.settings.channel_form.as_mut() else { return };
        if form.busy {
            return;
        }
        let values: Vec<(&str, String)> = form
            .inputs
            .iter()
            .map(|(k, i)| (*k, i.read(cx).value().to_string()))
            .collect();
        let req = setup_request(&project, &form.id, form.kind, &values);
        let (id, kind, new) = (form.id.clone(), form.kind, form.new);
        form.busy = true;
        form.error = None;
        cx.notify();
        if crate::preview::active() {
            return;
        }
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let result = cx
                .background_spawn(async move { crate::control::call("channels.setup", req) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| {
                let Some(form) = this.settings.channel_form.as_mut().filter(|f| f.id == id) else { return };
                form.busy = false;
                let result = match result {
                    Ok(r) => r,
                    Err(_) => {
                        form.error = Some(t("channel_error_cloud"));
                        cx.notify();
                        return;
                    }
                };
                if result["ok"].as_bool() != Some(true) {
                    form.error = Some(setup_error(&result));
                    cx.notify();
                    return;
                }
                form.new = false;
                let source = result["source"].as_str().unwrap_or_default().to_owned();
                form.note = Some(match (result["interactions_url"].as_str(), result["snippet"].as_str()) {
                    (Some(url), _) if !url.is_empty() => (t("channel_saved_discord_url"), Some(url.to_owned())),
                    (_, Some(tag)) if !tag.is_empty() => (t("channel_saved_widget_snippet"), Some(tag.to_owned())),
                    _ => (t("channel_saved_not_live"), None),
                });
                let Some(p) = this.projects.iter().find(|p| p.id == project).cloned() else { return };
                let mut channels = p.channels.clone();
                let mut tags = None;
                if let Some(ch) = channels.iter_mut().find(|ch| ch.id == id) {
                    ch.source = source;
                } else if new {
                    let (mut ch, all) = connect(kind, id.clone(), &p.channels, &p.tags);
                    ch.source = source;
                    channels.push(ch);
                    tags = (all != p.tags).then_some(all);
                }
                this.save_channels(channels, tags, window, cx);
            });
        }));
    }

    /// Removes a channel from the project and its secrets from the cloud.
    fn remove_channel(&mut self, pid: &str, i: usize, window: &mut Window, cx: &mut Context<Self>) {
        let Some(p) = self.projects.iter().find(|p| p.id == pid) else { return };
        let mut channels = p.channels.clone();
        if i >= channels.len() {
            return;
        }
        let gone = channels.remove(i);
        if self.settings.channel_form.as_ref().is_some_and(|f| f.id == gone.id) {
            self.settings.channel_form = None;
        }
        self.save_channels(channels, None, window, cx);
        if crate::preview::active() {
            return;
        }
        self._tasks.push(cx.background_spawn(async move {
            if let Err(err) = crate::control::call("channels.remove", json!({"id": gone.id})) {
                tracing::warn!("removing channel {} from the cloud config: {err:#}", gone.id);
            }
        }));
    }

    fn channel_form_view(&self, form: &ChannelForm, cx: &mut Context<Self>) -> impl IntoElement {
        let kind = form.kind;
        let field = |key: &str, input: &Entity<InputState>| {
            v_flex()
                .gap(px(5.0))
                .child(div().text_size(px(12.0)).font_weight(FontWeight::MEDIUM).child(t(&format!("channel_field_{key}"))))
                .child(
                    div()
                        .h(px(30.0))
                        .px(px(9.0))
                        .rounded(px(7.0))
                        .border_1()
                        .border_color(c("f_field"))
                        .bg(c("background"))
                        .flex()
                        .items_center()
                        .text_size(px(12.5))
                        .child(Input::new(input)),
                )
        };
        let url = portal(kind);
        v_flex()
            .px(px(14.0))
            .py(px(14.0))
            .gap(px(12.0))
            .border_b_1()
            .border_color(c("f_line"))
            .bg(c("f_expanded"))
            .child(
                h_flex()
                    .gap(px(8.0))
                    .child(div().flex_1().text_size(px(13.0)).font_weight(FontWeight::MEDIUM).child(tf(
                        if form.new { "channel_connect_title" } else { "channel_edit_title" },
                        &[("name", &t(&format!("channel_{kind}")))],
                    )))
                    .child(
                        div()
                            .id("channel-form-close")
                            .size(px(24.0))
                            .rounded(px(6.0))
                            .flex()
                            .items_center()
                            .justify_center()
                            .hover(|s| s.bg(c("f_line")))
                            .on_click(cx.listener(|this, _, _, cx| {
                                this.settings.channel_form = None;
                                cx.notify();
                            }))
                            .child(ui::icon("close", 13.0, c("f_muted"))),
                    ),
            )
            .child(
                div()
                    .text_size(px(12.0))
                    .text_color(c("f_muted"))
                    .child(t(&format!("channel_{kind}_steps"))),
            )
            .child(
                div()
                    .id("channel-portal")
                    .text_size(px(12.0))
                    .text_color(c("accent_text"))
                    .cursor_pointer()
                    .on_click(move |_, _, cx| cx.open_url(url))
                    .child(t(&format!("channel_{kind}_portal"))),
            )
            .children(form.inputs.iter().map(|(k, i)| field(k, i)))
            .when_some(form.error.clone(), |d, e| {
                d.child(div().text_size(px(12.0)).text_color(c("bug")).child(e))
            })
            .when_some(form.note.clone(), |d, (text, value)| {
                d.child(
                    v_flex()
                        .gap(px(6.0))
                        .child(h_flex().gap(px(6.0)).child(ui::icon("check", 13.0, c("ok"))).child(div().text_size(px(12.0)).child(text)))
                        .when_some(value, |d, value| {
                            let copy = value.clone();
                            d.child(
                                h_flex()
                                    .gap(px(8.0))
                                    .child(
                                        div()
                                            .flex_1()
                                            .min_w_0()
                                            .px(px(8.0))
                                            .py(px(6.0))
                                            .rounded(px(6.0))
                                            .bg(c("background"))
                                            .text_size(px(11.5))
                                            .font_family("Menlo")
                                            .child(value),
                                    )
                                    .child(secondary_button("channel-copy", t("copy"), false).on_click(move |_, _, cx| {
                                        cx.write_to_clipboard(ClipboardItem::new_string(copy.clone()))
                                    })),
                            )
                        }),
                )
            })
            .child(
                h_flex()
                    .gap(px(8.0))
                    .child(
                        primary_button("channel-save", if form.busy { t("channel_checking") } else { t("channel_check_save") })
                            .when(form.busy, |d| d.opacity(0.6))
                            .on_click(cx.listener(|this, _, window, cx| this.submit_channel_form(window, cx))),
                    ),
            )
    }

    pub(crate) fn channels_tab(&self, p: &Project, cx: &mut Context<Self>) -> impl IntoElement {
        let section = |label: String| {
            div()
                .mt(px(6.0))
                .text_size(px(12.0))
                .font_weight(FontWeight::MEDIUM)
                .text_color(c("f_muted"))
                .child(label)
        };
        let card = || {
            v_flex()
                .bg(c("f_card"))
                .border_1()
                .border_color(c("f_line"))
                .rounded(px(11.0))
                .overflow_hidden()
        };
        let tile = |kind: &str| {
            let (glyph, color) = logo(kind);
            div()
                .size(px(30.0))
                .flex_none()
                .rounded(px(8.0))
                .bg(color)
                .when(kind == "x", |d| d.border_1().border_color(c("f_field")))
                .flex()
                .items_center()
                .justify_center()
                .when(kind == "slack", |d| d.child(img("brands/slack.svg").size(px(17.0)).flex_none()))
                .when(kind != "slack", |d| {
                    d.child(ui::icon(glyph, if kind == "x" { 13.0 } else { 17.0 }, rgb(0xffffff).into()))
                })
        };
        let text = |name: String, detail: String, muted: bool| {
            v_flex()
                .flex_1()
                .min_w_0()
                .gap(px(2.0))
                .child(div().text_size(px(13.0)).font_weight(FontWeight::MEDIUM).child(name))
                .child(
                    div()
                        .truncate()
                        .text_size(px(11.5))
                        .text_color(c("f_muted"))
                        .when(muted, |d| d.italic())
                        .child(detail),
                )
        };
        let row = |id: SharedString| {
            h_flex()
                .id(id)
                .group("channel-row")
                .min_h(px(54.0))
                .px(px(14.0))
                .py(px(10.0))
                .gap(px(12.0))
                .border_b_1()
                .border_color(c("f_line"))
        };
        let form = self.settings.channel_form.as_ref();
        let editing = |id: &str| form.is_some_and(|f| f.id == id);
        // Built first: it needs `cx` mutably. Goes under its channel's row,
        // or under the list for a new channel.
        let form_el = std::cell::RefCell::new(form.map(|f| self.channel_form_view(f, cx).into_any_element()));

        let connected = p.channels.iter().enumerate().map(|(i, ch)| {
            let label = |id: &String| {
                p.tags
                    .iter()
                    .find(|t| &t.id == id)
                    .map_or_else(|| id.clone(), |t| t.label.clone())
            };
            let ready = READY.contains(&ch.kind.as_str());
            let (detail, waiting) = if ch.source.is_empty() {
                (t(if ready { "channel_not_set_up" } else { "channel_soon" }), true)
            } else {
                (ch.source.clone(), false)
            };
            let on = ch.on && !waiting;
            let pid = p.id.clone();
            let switch = div()
                .id(SharedString::from(format!("channel-on-{}", ch.id)))
                .w(px(32.0))
                .h(px(19.0))
                .flex_none()
                .rounded_full()
                .p(px(2.0))
                .bg(if on { c("ok") } else { c("f_field") })
                .flex()
                .when(on, |d| d.justify_end())
                .child(div().size(px(15.0)).rounded_full().bg(rgb(0xffffff)))
                // Without verified credentials there is nothing to switch on.
                .when(waiting, |d| d.opacity(0.4))
                .when(!waiting, |d| {
                    d.on_click(cx.listener(move |this, _, window, cx| {
                        let Some(p) = this.projects.iter().find(|p| p.id == pid) else { return };
                        let mut channels = p.channels.clone();
                        if let Some(ch) = channels.get_mut(i) {
                            ch.on = !ch.on;
                        }
                        this.save_channels(channels, None, window, cx);
                    }))
                });
            let pid = p.id.clone();
            let remove = div()
                .id(SharedString::from(format!("channel-remove-{}", ch.id)))
                .size(px(24.0))
                .flex_none()
                .rounded(px(6.0))
                .flex()
                .items_center()
                .justify_center()
                .opacity(0.0)
                .group_hover("channel-row", |s| s.opacity(1.0))
                .hover(|s| s.bg(c("f_line")))
                .on_click(cx.listener(move |this, _, window, cx| this.remove_channel(&pid, i, window, cx)))
                .child(ui::icon("trash", 14.0, c("f_muted")));
            let kind: &'static str = READY.iter().copied().find(|k| *k == ch.kind).unwrap_or("widget");
            let (pid, cid) = (p.id.clone(), ch.id.clone());
            let edit = secondary_button(
                SharedString::from(format!("channel-edit-{}", ch.id)),
                t(if waiting { "channel_set_up" } else { "channel_edit" }),
                false,
            )
            .when(!waiting, |d| d.opacity(0.0).group_hover("channel-row", |s| s.opacity(1.0)))
            .on_click(cx.listener(move |this, _, window, cx| {
                let Some(p) = this.projects.iter().find(|p| p.id == pid).cloned() else { return };
                this.open_channel_form(&p, kind, Some(cid.clone()), window, cx);
            }));
            let editing_this = editing(&ch.id);
            v_flex()
                .child(
                    row(SharedString::from(format!("channel-{}", ch.id)))
                        .when(!on, |d| d.opacity(0.65))
                        .child(tile(&ch.kind))
                        .child(text(t(&format!("channel_{}", ch.kind)), detail, waiting))
                        .child(
                            h_flex()
                                .flex_none()
                                .gap(px(5.0))
                                .when(!ch.tags.is_empty(), |d| {
                                    d.child(ui::icon("arrow-right", 12.0, c("f_muted")))
                                })
                                .children(ch.tags.iter().map(|id| ui::tag_chip(id, &label(id)))),
                        )
                        .when(ready && !editing_this, |d| d.child(edit))
                        .child(remove)
                        .child(switch),
                )
                .when(editing_this, |d| d.children(form_el.borrow_mut().take()))
        }).collect::<Vec<_>>();

        let adding = self.settings.adding_channel;
        let add = h_flex()
            .id("channel-add")
            .h(px(42.0))
            .px(px(14.0))
            .gap(px(9.0))
            .text_size(px(12.5))
            .text_color(c("f_muted"))
            .hover(|s| s.bg(c("f_line").opacity(0.5)))
            .on_click(cx.listener(|this, _, _, cx| {
                this.settings.adding_channel = !this.settings.adding_channel;
                cx.notify();
            }))
            .child(ui::icon("plus", 14.0, c("f_muted")))
            .child(t("channel_add"));
        let picker = h_flex()
            .px(px(14.0))
            .pb(px(12.0))
            .gap(px(8.0))
            .flex_wrap()
            .children(READY.map(|kind| {
                let pid = p.id.clone();
                secondary_button(SharedString::from(format!("channel-add-{kind}")), t(&format!("channel_{kind}")), false)
                    .on_click(cx.listener(move |this, _, window, cx| {
                        let Some(p) = this.projects.iter().find(|p| p.id == pid).cloned() else { return };
                        this.open_channel_form(&p, kind, None, window, cx);
                    }))
            }));
        // A new channel's form, shown until its first save succeeds.
        let new_form = form.filter(|f| f.new && !p.channels.iter().any(|ch| ch.id == f.id));

        let available: Vec<&'static str> = KINDS
            .into_iter()
            .filter(|kind| !p.channels.iter().any(|ch| ch.kind == *kind))
            .filter(|kind| new_form.is_none_or(|f| f.kind != *kind))
            .collect();
        let connectable = available.iter().map(|&kind| {
            let pid = p.id.clone();
            let ready = READY.contains(&kind);
            row(SharedString::from(format!("channel-available-{kind}")))
                .child(tile(kind))
                .child(text(t(&format!("channel_{kind}")), t(&format!("channel_{kind}_hint")), false))
                .when(ready, |d| {
                    d.child(
                        secondary_button(SharedString::from(format!("channel-connect-{kind}")), t("channel_connect"), false)
                            .on_click(cx.listener(move |this, _, window, cx| {
                                let Some(p) = this.projects.iter().find(|p| p.id == pid).cloned() else { return };
                                this.open_channel_form(&p, kind, None, window, cx);
                            })),
                    )
                })
                .when(!ready, |d| {
                    d.child(div().text_size(px(11.5)).text_color(c("f_muted")).child(t("channel_soon_short")))
                })
        }).collect::<Vec<_>>();

        v_flex()
            .gap(px(10.0))
            .child(
                div()
                    .mt(px(-6.0))
                    .mb(px(6.0))
                    .text_size(px(12.5))
                    .text_color(c("f_muted"))
                    .child(tf("channels_lead", &[("project", &p.display_name())])),
            )
            .child(section(t("channels_connected")))
            .child(
                card()
                    .when(p.channels.is_empty() && new_form.is_none(), |d| {
                        d.child(
                            div()
                                .px(px(14.0))
                                .py(px(16.0))
                                .border_b_1()
                                .border_color(c("f_line"))
                                .text_size(px(12.5))
                                .text_color(c("f_muted"))
                                .child(t("channels_none")),
                        )
                    })
                    .children(connected)
                    .when(new_form.is_some(), |d| d.children(form_el.borrow_mut().take()))
                    .child(add)
                    .when(adding, |d| d.child(picker)),
            )
            .when(!available.is_empty(), |d| {
                d.child(div().h(px(8.0)))
                    .child(section(t("channels_available")))
                    .child(card().children(connectable))
            })
    }
}

#[cfg(test)]
mod channel_tests {
    use super::{connect, list, new_id, setup_request};
    use crate::board::{Channel, Tag};

    fn tag(id: &str, label: &str) -> Tag {
        Tag { id: id.into(), label: label.into() }
    }

    #[test]
    fn discord_gets_feedback_and_discord() {
        let (ch, tags) = connect("discord", "discord-1a2b".into(), &[], &[tag("bug", "Bug")]);
        assert_eq!(ch.tags, ["feedback", "discord"]);
        assert_eq!(tags.len(), 3);
    }

    #[test]
    fn slack_only_slack_and_reuses_existing_tags() {
        let existing = [tag("bug", "Bug"), tag("team-slack", "Slack")];
        let (ch, tags) = connect("slack", "slack-1".into(), &[], &existing);
        assert_eq!(ch.tags, ["team-slack"]);
        assert_eq!(tags, existing);
    }

    #[test]
    fn ids_are_unique_and_valid() {
        let first = Channel { id: new_id("discord", &[]), kind: "discord".into(), ..Default::default() };
        let second = new_id("discord", std::slice::from_ref(&first));
        assert_ne!(first.id, second);
        assert!(second.starts_with("discord-") && second.len() == 12);
    }

    #[test]
    fn form_values_become_a_setup_request() {
        assert_eq!(list("123, 456\n789，10"), ["123", "456", "789", "10"]);
        let req = setup_request("p", "discord-1", "discord", &[("bot_token", " t ".into()), ("channels", "1 2".into())]);
        assert_eq!(req["discord"]["bot_token"], "t");
        assert_eq!(req["discord"]["channels"], serde_json::json!(["1", "2"]));
    }
}
