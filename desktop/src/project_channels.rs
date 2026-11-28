//! Settings › Channels (FEN-228, design FEN-217 hd.html `?s=set`): the places
//! whose messages become the project's requests, with the tags each one
//! gives them and a switch, then the kinds still to connect. There is no
//! triage agent and no rate limit: each message is a new request.

use crate::app::{FennecWindow, secondary_button};
use crate::board::{Channel, Project, Tag};
use crate::i18n::{t, tf};
use crate::ui::{self, c};
use gpui::prelude::FluentBuilder as _;
use gpui::*;
use gpui_base::{h_flex, v_flex};
use serde_json::json;

/// Every kind, in the order the page lists them.
const KINDS: [&str; 4] = ["discord", "widget", "slack", "x"];

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

/// A new channel of `kind` and the project tags it needs, added to `tags`
/// where missing; the first free id is `kind`, then `kind-2`, …
pub(crate) fn connect(kind: &str, channels: &[Channel], tags: &[Tag]) -> (Channel, Vec<Tag>) {
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
    let id = (1..)
        .map(|n| if n == 1 { kind.to_owned() } else { format!("{kind}-{n}") })
        .find(|id| !channels.iter().any(|ch| &ch.id == id))
        .unwrap_or_default();
    let channel = Channel {
        id,
        kind: kind.to_owned(),
        source: String::new(),
        tags: ids,
        // Off until the channel's credentials are set up and it has a
        // source: a row alone takes nothing in.
        on: false,
    };
    (channel, tags)
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

    fn connect_channel(&mut self, p: &Project, kind: &str, window: &mut Window, cx: &mut Context<Self>) {
        let (channel, tags) = connect(kind, &p.channels, &p.tags);
        let mut channels = p.channels.clone();
        channels.push(channel);
        let tags = (tags != p.tags).then_some(tags);
        self.settings.adding_channel = false;
        self.save_channels(channels, tags, window, cx);
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

        let connected = p.channels.iter().enumerate().map(|(i, ch)| {
            let label = |id: &String| {
                p.tags
                    .iter()
                    .find(|t| &t.id == id)
                    .map_or_else(|| id.clone(), |t| t.label.clone())
            };
            let (detail, waiting) = if ch.source.is_empty() {
                (t("channel_not_set_up"), true)
            } else {
                (ch.source.clone(), false)
            };
            let on = ch.on;
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
                // Without credentials there is nothing to switch on.
                .when(waiting, |d| d.opacity(0.4))
                .when(!waiting, |d| d.on_click(cx.listener(move |this, _, window, cx| {
                    let Some(p) = this.projects.iter().find(|p| p.id == pid) else { return };
                    let mut channels = p.channels.clone();
                    if let Some(ch) = channels.get_mut(i) {
                        ch.on = !ch.on;
                    }
                    this.save_channels(channels, None, window, cx);
                })));
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
                .on_click(cx.listener(move |this, _, window, cx| {
                    let Some(p) = this.projects.iter().find(|p| p.id == pid) else { return };
                    let mut channels = p.channels.clone();
                    if i < channels.len() {
                        channels.remove(i);
                    }
                    this.save_channels(channels, None, window, cx);
                }))
                .child(ui::icon("trash", 14.0, c("f_muted")));
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
                .child(remove)
                .child(switch)
        });

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
            .children(KINDS.map(|kind| {
                let pid = p.id.clone();
                secondary_button(SharedString::from(format!("channel-add-{kind}")), t(&format!("channel_{kind}")), false)
                    .on_click(cx.listener(move |this, _, window, cx| {
                        let Some(p) = this.projects.iter().find(|p| p.id == pid).cloned() else { return };
                        this.connect_channel(&p, kind, window, cx);
                    }))
            }));

        let available: Vec<&str> = KINDS
            .into_iter()
            .filter(|kind| !p.channels.iter().any(|ch| ch.kind == *kind))
            .collect();
        let connectable = available.iter().map(|&kind| {
            let pid = p.id.clone();
            row(SharedString::from(format!("channel-available-{kind}")))
                .child(tile(kind))
                .child(text(t(&format!("channel_{kind}")), t(&format!("channel_{kind}_hint")), false))
                .child(
                    secondary_button(SharedString::from(format!("channel-connect-{kind}")), t("channel_connect"), false)
                        .on_click(cx.listener(move |this, _, window, cx| {
                            let Some(p) = this.projects.iter().find(|p| p.id == pid).cloned() else { return };
                            this.connect_channel(&p, kind, window, cx);
                        })),
                )
        });

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
                    .when(p.channels.is_empty(), |d| {
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
    use super::connect;
    use crate::board::{Channel, Tag};

    fn tag(id: &str, label: &str) -> Tag {
        Tag { id: id.into(), label: label.into() }
    }

    #[test]
    fn discord_gets_feedback_and_discord() {
        let (ch, tags) = connect("discord", &[], &[tag("bug", "Bug")]);
        assert_eq!(ch.id, "discord");
        assert_eq!(ch.tags, ["feedback", "discord"]);
        assert!(!ch.on);
        assert_eq!(tags.len(), 3);
    }

    #[test]
    fn slack_only_slack_and_reuses_existing_tags() {
        let existing = [tag("bug", "Bug"), tag("team-slack", "Slack")];
        let (ch, tags) = connect("slack", &[], &existing);
        assert_eq!(ch.tags, ["team-slack"]);
        assert_eq!(tags, existing);
    }

    #[test]
    fn second_channel_of_a_kind_gets_a_new_id() {
        let first = Channel { id: "discord".into(), kind: "discord".into(), ..Default::default() };
        let (ch, _) = connect("discord", &[first], &[]);
        assert_eq!(ch.id, "discord-2");
    }
}
