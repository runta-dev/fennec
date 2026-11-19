//! Project settings (docs/desktop-app.md): General, Knowledge, Queue, Agents.

use crate::app::{FennecWindow, Route, primary_button, secondary_button};
use crate::board::{self, Concept, Project, QueueProject, Tag, Worker};
use crate::i18n::{t, tf};
use crate::ui::{self, c};
use gpui::prelude::FluentBuilder as _;
use gpui::*;
use gpui_base::input::{Input, InputEvent, InputState};
use gpui_base::{h_flex, v_flex};
use serde_json::json;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Tab {
    General,
    Knowledge,
    Queue,
    Agents,
}

#[derive(Clone, Copy, PartialEq, Eq, Default)]
pub enum KnowledgeFilter {
    #[default]
    All,
    Review,
    Draft,
    Deprecated,
}

#[derive(Default)]
pub struct State {
    project: String,
    name: Option<Entity<InputState>>,
    key: Option<Entity<InputState>>,
    description: Option<Entity<InputState>>,
    repository: Option<Entity<InputState>>,
    new_tag: Option<Entity<InputState>>,
    tags: Vec<Tag>,
    knowledge: Vec<Concept>,
    filter: KnowledgeFilter,
    queue: Option<QueueProject>,
}

const DEFAULT_TAGS: &[(&str, &str)] = &[
    ("bug", "Bug"),
    ("feature", "Feature"),
    ("discussion", "Discussion"),
];

impl FennecWindow {
    /// Fills the forms from the project and loads its knowledge and queue.
    pub(crate) fn open_settings(
        &mut self,
        id: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(p) = self.projects.iter().find(|p| p.id == id).cloned() else {
            return;
        };
        if self.settings.project != id {
            let input = |value: &str, window: &mut Window, cx: &mut Context<Self>| {
                let value = value.to_owned();
                cx.new(|cx| {
                    let mut s = InputState::new(window, cx);
                    s.set_value(value, window, cx);
                    s
                })
            };
            let new_tag = cx.new(|cx| InputState::new(window, cx).placeholder(t("add_tag")));
            self._subs.push(cx.subscribe_in(
                &new_tag,
                window,
                |this, input, ev: &InputEvent, window, cx| {
                    if let InputEvent::PressEnter { .. } = ev {
                        let label = input.read(cx).value().trim().to_owned();
                        if !label.is_empty() {
                            let id = label
                                .to_lowercase()
                                .chars()
                                .map(|c| if c.is_alphanumeric() { c } else { '-' })
                                .collect::<String>();
                            if !this.settings.tags.iter().any(|t| t.id == id) {
                                this.settings.tags.push(Tag { id, label });
                            }
                            input.update(cx, |s, cx| s.set_value("", window, cx));
                            cx.notify();
                        }
                    }
                },
            ));
            self.settings = State {
                project: id.clone(),
                name: Some(input(&p.name, window, cx)),
                key: Some(input(&p.key, window, cx)),
                description: Some(input(&p.description, window, cx)),
                repository: Some(input(&p.repository, window, cx)),
                new_tag: Some(new_tag),
                tags: p.tags.clone(),
                ..Default::default()
            };
        }
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let (knowledge, queue) = cx
                .background_spawn(async move { (board::knowledge(&id), board::queue()) })
                .await;
            let _ = this.update(cx, |this, cx| {
                if let Ok(k) = knowledge {
                    this.settings.knowledge = k;
                }
                if let Ok(q) = queue {
                    this.settings.queue =
                        q.into_iter().find(|q| q.project == this.settings.project);
                }
                cx.notify();
            });
        }));
    }

    fn patch_project(
        &mut self,
        change: serde_json::Value,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let id = self.settings.project.clone();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let changed = cx
                .background_spawn(async move { board::update_project(&id, change) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| match changed {
                Ok(p) => {
                    if let Some(old) = this.projects.iter_mut().find(|o| o.id == p.id) {
                        let (open, needs_you) = (old.open, old.needs_you);
                        *old = Project {
                            open,
                            needs_you,
                            ..p
                        };
                    }
                    this.refresh_now(window, cx);
                }
                Err(_) => this.say(t("try_again"), cx),
            });
        }));
    }

    fn save_general(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let s = &self.settings;
        let value = |i: &Option<Entity<InputState>>| {
            i.as_ref()
                .map(|i| i.read(cx).value().trim().to_owned())
                .unwrap_or_default()
        };
        let change = json!({
            "name": value(&s.name),
            "key": value(&s.key).to_uppercase(),
            "description": value(&s.description),
            "repository": value(&s.repository),
            "tags": s.tags,
        });
        self.patch_project(change, window, cx);
    }

    fn review(
        &mut self,
        concept: String,
        action: &'static str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let (id, person) = (self.settings.project.clone(), self.me().to_owned());
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let done = cx
                .background_spawn(async move { board::review(&id, &concept, action, &person) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| match done {
                Ok(_) => {
                    let id = this.settings.project.clone();
                    this.open_settings(id, window, cx);
                }
                Err(_) => this.say(t("try_again"), cx),
            });
        }));
    }

    pub(crate) fn project_settings(
        &mut self,
        p: &Project,
        tab: Tab,
        _window: &mut Window,
        cx: &mut Context<Self>,
    ) -> impl IntoElement {
        let tabs = [
            (Tab::General, t("general")),
            (Tab::Knowledge, t("knowledge")),
            (Tab::Queue, t("queue")),
            (Tab::Agents, t("agents")),
        ]
        .map(|(which, label)| {
            let id = p.id.clone();
            div()
                .id(SharedString::from(format!("tab-{which:?}")))
                .h(px(28.0))
                .px(px(12.0))
                .rounded(px(7.0))
                .flex()
                .items_center()
                .text_size(px(12.5))
                .font_weight(FontWeight::MEDIUM)
                .text_color(if which == tab {
                    c("f_text")
                } else {
                    c("f_muted")
                })
                .when(which == tab, |d| {
                    d.bg(c("f_card")).border_1().border_color(c("f_line"))
                })
                .when(which != tab, |d| d.hover(|s| s.bg(c("f_line"))))
                .on_click(cx.listener(move |this, _, window, cx| {
                    this.go(Route::Settings(id.clone(), which), window, cx)
                }))
                .child(label)
        });
        let body = match tab {
            Tab::General => self.general(p, cx).into_any_element(),
            Tab::Knowledge => self.knowledge(p, cx).into_any_element(),
            Tab::Queue => self.queue_tab(p, cx).into_any_element(),
            Tab::Agents => self.agents(p, cx).into_any_element(),
        };
        let back = p.id.clone();
        v_flex()
            .flex_1()
            .min_w_0()
            .h_full()
            .bg(c("f_expanded"))
            .child(
                h_flex()
                    .id("settings-head")
                    .h(px(58.0))
                    .flex_none()
                    .px(px(18.0))
                    .gap(px(11.0))
                    .border_b_1()
                    .border_color(c("f_line"))
                    .on_mouse_down(MouseButton::Left, |_, window, _| window.start_window_move())
                    .child(
                        div()
                            .id("settings-back")
                            .size(px(28.0))
                            .rounded(px(6.0))
                            .flex()
                            .items_center()
                            .justify_center()
                            .hover(|s| s.bg(c("f_line")))
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.go(Route::Project(back.clone()), window, cx)
                            }))
                            .child(ui::project_mark(&p.name, &p.color, 22.0)),
                    )
                    .child(
                        div()
                            .text_size(px(13.5))
                            .font_weight(FontWeight::SEMIBOLD)
                            .child(t("project_settings")),
                    )
                    .child(div().flex_1())
                    .child(
                        h_flex()
                            .p(px(2.0))
                            .gap(px(2.0))
                            .rounded(px(9.0))
                            .bg(c("f_line"))
                            .children(tabs),
                    ),
            )
            .child(
                div()
                    .id("settings-body")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .child(
                        v_flex()
                            .w_full()
                            .max_w(px(640.0))
                            .mx_auto()
                            .p(px(24.0))
                            .gap(px(18.0))
                            .child(body),
                    ),
            )
    }

    fn me(&self) -> &str {
        &self.me
    }

    fn general(&self, p: &Project, cx: &mut Context<Self>) -> impl IntoElement {
        let s = &self.settings;
        let field = |label: String, hint: Option<String>, input: &Option<Entity<InputState>>| {
            v_flex()
                .gap(px(5.0))
                .child(
                    div()
                        .text_size(px(12.0))
                        .font_weight(FontWeight::MEDIUM)
                        .text_color(c("f_text"))
                        .child(label),
                )
                .when_some(input.as_ref(), |d, i| {
                    d.child(
                        div()
                            .h(px(32.0))
                            .px(px(9.0))
                            .rounded(px(8.0))
                            .border_1()
                            .border_color(c("f_field"))
                            .bg(c("f_card"))
                            .flex()
                            .items_center()
                            .text_size(px(13.0))
                            .child(Input::new(i)),
                    )
                })
                .when_some(hint, |d, h| {
                    d.child(div().text_size(px(11.5)).text_color(c("f_muted")).child(h))
                })
        };
        let swatches = board::COLORS.iter().map(|color| {
            let on = p.color.eq_ignore_ascii_case(color);
            let value = color.to_string();
            div()
                .id(SharedString::from(format!("color-{color}")))
                .size(px(22.0))
                .rounded_full()
                .bg(crate::settings::parse_hex(color).unwrap_or(c("orange")))
                .border_2()
                .border_color(if on { c("f_text") } else { transparent_black() })
                .on_click(cx.listener(move |this, _, window, cx| {
                    this.patch_project(json!({"color": value}), window, cx)
                }))
        });
        let tags = s.tags.iter().map(|tag| {
            let id = tag.id.clone();
            h_flex()
                .gap(px(4.0))
                .child(ui::tag_chip(&tag.id, &tag.label))
                .when(s.tags.len() > 1, |d| {
                    d.child(
                        div()
                            .id(SharedString::from(format!("rm-tag-{}", tag.id)))
                            .rounded(px(4.0))
                            .hover(|s| s.bg(c("f_line")))
                            .on_click(cx.listener(move |this, _, _, cx| {
                                this.settings.tags.retain(|t| t.id != id);
                                cx.notify();
                            }))
                            .child(ui::icon("close", 12.0, c("f_muted"))),
                    )
                })
        });
        let key = s
            .key
            .as_ref()
            .map(|k| k.read(cx).value().trim().to_uppercase())
            .filter(|k| !k.is_empty())
            .unwrap_or_else(|| p.key.clone());
        v_flex()
            .gap(px(18.0))
            .child(
                v_flex()
                    .gap(px(8.0))
                    .child(
                        div()
                            .text_size(px(12.0))
                            .font_weight(FontWeight::MEDIUM)
                            .child(t("icon")),
                    )
                    .child(
                        h_flex()
                            .gap(px(8.0))
                            .child(ui::project_mark(&p.name, &p.color, 30.0))
                            .children(swatches),
                    ),
            )
            .child(field(t("name"), None, &s.name))
            .child(field(
                t("key"),
                Some(tf("key_hint", &[("key", &key)])),
                &s.key,
            ))
            .child(field(t("description"), None, &s.description))
            .child(field(t("repository"), None, &s.repository))
            .child(
                v_flex()
                    .gap(px(8.0))
                    .child(
                        div()
                            .text_size(px(12.0))
                            .font_weight(FontWeight::MEDIUM)
                            .child(t("tags")),
                    )
                    .child(h_flex().flex_wrap().gap(px(8.0)).children(tags))
                    .when_some(s.new_tag.as_ref(), |d, i| {
                        d.child(
                            div()
                                .h(px(30.0))
                                .w(px(240.0))
                                .px(px(9.0))
                                .rounded(px(8.0))
                                .border_1()
                                .border_color(c("f_field"))
                                .bg(c("f_card"))
                                .flex()
                                .items_center()
                                .text_size(px(12.5))
                                .child(Input::new(i)),
                        )
                    })
                    .child(
                        h_flex()
                            .gap(px(10.0))
                            .child(
                                div()
                                    .text_size(px(11.5))
                                    .text_color(c("f_muted"))
                                    .child(t("tags_hint")),
                            )
                            .child(
                                div()
                                    .id("reset-tags")
                                    .text_size(px(11.5))
                                    .text_color(c("accent_text"))
                                    .on_click(cx.listener(|this, _, _, cx| {
                                        this.settings.tags = DEFAULT_TAGS
                                            .iter()
                                            .map(|(id, label)| Tag {
                                                id: (*id).into(),
                                                label: (*label).into(),
                                            })
                                            .collect();
                                        cx.notify();
                                    }))
                                    .child(t("reset_tags")),
                            ),
                    ),
            )
            .child(
                h_flex()
                    .justify_end()
                    .child(primary_button("save-general", t("save_changes")).on_click(
                        cx.listener(|this, _, window, cx| this.save_general(window, cx)),
                    )),
            )
    }

    fn knowledge(&self, p: &Project, cx: &mut Context<Self>) -> impl IntoElement {
        let s = &self.settings;
        let show = |k: &Concept| match s.filter {
            KnowledgeFilter::All => true,
            KnowledgeFilter::Review => k.verified.is_empty() && k.status != "deprecated",
            KnowledgeFilter::Draft => k.status == "draft",
            KnowledgeFilter::Deprecated => k.status == "deprecated",
        };
        let filters = [
            (KnowledgeFilter::All, t("all")),
            (KnowledgeFilter::Review, t("needs_review")),
            (KnowledgeFilter::Draft, t("k_draft")),
            (KnowledgeFilter::Deprecated, t("k_deprecated")),
        ]
        .map(|(f, label)| {
            let on = s.filter == f;
            div()
                .id(SharedString::from(format!("kf-{label}")))
                .h(px(24.0))
                .px(px(10.0))
                .rounded(px(12.0))
                .flex()
                .items_center()
                .text_size(px(12.0))
                .font_weight(FontWeight::MEDIUM)
                .text_color(if on { c("f_text") } else { c("f_muted") })
                .when(on, |d| d.bg(c("f_line")))
                .on_click(cx.listener(move |this, _, _, cx| {
                    this.settings.filter = f;
                    cx.notify();
                }))
                .child(label)
        });
        let mut sections: Vec<(String, Vec<&Concept>)> = Vec::new();
        for k in s.knowledge.iter().filter(|k| show(k)) {
            let folder = k.id.split('/').next().unwrap_or_default().to_owned();
            match sections.iter_mut().find(|(f, _)| *f == folder) {
                Some((_, list)) => list.push(k),
                None => sections.push((folder, vec![k])),
            }
        }
        let section_name = |folder: &str| match folder {
            "architecture" | "runbooks" | "decisions" | "glossary" => {
                t(&format!("section_{folder}"))
            }
            other => {
                let words = other.replace(['-', '_'], " ");
                let mut chars = words.chars();
                chars
                    .next()
                    .map(|f| f.to_uppercase().collect::<String>() + chars.as_str())
                    .unwrap_or_default()
            }
        };
        let concept = |k: &Concept| {
            let (status, color) = match k.status.as_str() {
                "deprecated" => (t("k_deprecated"), c("f_muted")),
                "draft" => (t("k_draft"), c("accent_text")),
                _ => (t("k_current"), c("ok")),
            };
            let reviewed: Vec<String> = k
                .verified
                .iter()
                .map(|a| a.by.trim_start_matches("human:").to_owned())
                .collect();
            let (id_review, id_dep) = (k.id.clone(), k.id.clone());
            let deprecated = k.status == "deprecated";
            v_flex()
                .p(px(13.0))
                .gap(px(5.0))
                .rounded(px(10.0))
                .bg(c("f_card"))
                .border_1()
                .border_color(c("f_line"))
                .child(
                    h_flex()
                        .gap(px(8.0))
                        .child(
                            div()
                                .flex_1()
                                .text_size(px(13.5))
                                .font_weight(FontWeight::SEMIBOLD)
                                .child(k.title.clone()),
                        )
                        .child(
                            div()
                                .text_size(px(11.0))
                                .font_weight(FontWeight::SEMIBOLD)
                                .text_color(color)
                                .child(status),
                        ),
                )
                .when(!k.description.is_empty(), |d| {
                    d.child(
                        div()
                            .text_size(px(12.5))
                            .text_color(c("f_muted"))
                            .child(k.description.clone()),
                    )
                })
                .child(
                    h_flex()
                        .gap(px(12.0))
                        .text_size(px(11.5))
                        .text_color(c("f_muted"))
                        .when(!k.generated_by.is_empty(), |d| {
                            d.child(format!(
                                "{} {}",
                                t("written_by"),
                                crate::sessions::agent_name(&k.generated_by)
                            ))
                        })
                        .child(format!(
                            "{} {}",
                            t("reviewed_by"),
                            if reviewed.is_empty() {
                                t("not_yet")
                            } else {
                                reviewed.join(", ")
                            }
                        ))
                        .when(!k.tags.is_empty(), |d| d.child(k.tags.join(" · "))),
                )
                .child(
                    h_flex()
                        .mt(px(4.0))
                        .gap(px(8.0))
                        .when(k.verified.is_empty() && !deprecated, |d| {
                            d.child(
                                secondary_button("k-review", t("mark_reviewed"), true).on_click(
                                    cx.listener(move |this, _, window, cx| {
                                        this.review(id_review.clone(), "review", window, cx)
                                    }),
                                ),
                            )
                        })
                        .child(
                            secondary_button(
                                "k-deprecate",
                                if deprecated {
                                    t("restore")
                                } else {
                                    t("deprecate")
                                },
                                false,
                            )
                            .on_click(cx.listener(
                                move |this, _, window, cx| {
                                    this.review(
                                        id_dep.clone(),
                                        if deprecated { "restore" } else { "deprecate" },
                                        window,
                                        cx,
                                    )
                                },
                            )),
                        ),
                )
        };
        v_flex()
            .gap(px(14.0))
            .child(
                div()
                    .text_size(px(12.5))
                    .text_color(c("f_muted"))
                    .child(tf("knowledge_hint", &[("name", &p.name)])),
            )
            .child(h_flex().gap(px(4.0)).children(filters))
            .when(sections.is_empty(), |d| {
                d.child(
                    div()
                        .py(px(30.0))
                        .text_size(px(12.5))
                        .text_color(c("f_muted"))
                        .child(if s.knowledge.is_empty() {
                            t("knowledge_empty")
                        } else {
                            t("nothing_here")
                        }),
                )
            })
            .children(sections.iter().map(|(folder, list)| {
                v_flex()
                    .gap(px(8.0))
                    .child(
                        div()
                            .text_size(px(11.5))
                            .font_weight(FontWeight::SEMIBOLD)
                            .text_color(c("f_muted"))
                            .child(section_name(folder)),
                    )
                    .children(list.iter().map(|k| concept(k)))
            }))
    }

    fn queue_tab(&self, p: &Project, cx: &mut Context<Self>) -> impl IntoElement {
        let limit = p.queue.max(1);
        let working: Vec<board::Request> = self
            .requests
            .get(&p.id)
            .map(|l| {
                l.iter()
                    .filter(|r| r.status == "working")
                    .cloned()
                    .collect()
            })
            .unwrap_or_default();
        let waiting: Vec<board::Request> = self
            .settings
            .queue
            .as_ref()
            .map(|q| q.queued.clone())
            .unwrap_or_default();
        let step = |id: &'static str, label: &'static str, to: usize, enabled: bool| {
            div()
                .id(id)
                .size(px(28.0))
                .rounded(px(7.0))
                .border_1()
                .border_color(c("f_field"))
                .bg(c("f_card"))
                .flex()
                .items_center()
                .justify_center()
                .text_size(px(14.0))
                .text_color(if enabled {
                    c("f_text")
                } else {
                    c("f_muted").opacity(0.5)
                })
                .when(enabled, |d| {
                    d.on_click(cx.listener(move |this, _, window, cx| {
                        this.patch_project(json!({"queue": to}), window, cx)
                    }))
                })
                .child(label)
        };
        let row = |r: &board::Request, waiting: bool| {
            let (pid, n) = (p.id.clone(), r.number);
            h_flex()
                .id(SharedString::from(format!("q-{}", r.number)))
                .h(px(36.0))
                .px(px(12.0))
                .gap(px(10.0))
                .rounded(px(8.0))
                .bg(c("f_card"))
                .border_1()
                .border_color(c("f_line"))
                .hover(|s| s.border_color(c("f_field")))
                .on_click(cx.listener(move |this, _, window, cx| {
                    this.go(Route::Project(pid.clone()), window, cx);
                    this.open_request(n, window, cx);
                }))
                .child(
                    div()
                        .text_size(px(12.0))
                        .text_color(c("f_muted"))
                        .child(format!("{}-{}", p.key, r.number)),
                )
                .child(
                    div()
                        .flex_1()
                        .min_w_0()
                        .truncate()
                        .text_size(px(13.0))
                        .child(r.title.clone()),
                )
                .when(!r.agent.is_empty(), |d| {
                    d.child(ui::agent_icon(&r.agent, 15.0))
                })
                .child(ui::status_pill(if waiting { "queued" } else { "working" }))
        };
        v_flex()
            .gap(px(18.0))
            .child(
                h_flex()
                    .gap(px(12.0))
                    .child(
                        v_flex()
                            .flex_1()
                            .gap(px(3.0))
                            .child(
                                div()
                                    .text_size(px(13.0))
                                    .font_weight(FontWeight::MEDIUM)
                                    .child(t("at_once")),
                            )
                            .child(
                                div()
                                    .text_size(px(11.5))
                                    .text_color(c("f_muted"))
                                    .child(t("at_once_hint")),
                            ),
                    )
                    .child(step("q-less", "−", limit.saturating_sub(1), limit > 1))
                    .child(
                        div()
                            .w(px(24.0))
                            .flex()
                            .justify_center()
                            .text_size(px(14.0))
                            .font_weight(FontWeight::SEMIBOLD)
                            .child(limit.to_string()),
                    )
                    .child(step("q-more", "+", limit + 1, limit < 10)),
            )
            .child(
                v_flex()
                    .gap(px(8.0))
                    .child(
                        div()
                            .text_size(px(12.0))
                            .font_weight(FontWeight::MEDIUM)
                            .child(format!(
                                "{} · {}",
                                tf(
                                    "in_progress",
                                    &[
                                        ("n", &working.len().to_string()),
                                        ("limit", &limit.to_string())
                                    ]
                                ),
                                tf("waiting", &[("n", &waiting.len().to_string())])
                            )),
                    )
                    .when(working.is_empty() && waiting.is_empty(), |d| {
                        d.child(
                            div()
                                .py(px(20.0))
                                .text_size(px(12.5))
                                .text_color(c("f_muted"))
                                .child(t("queue_empty")),
                        )
                    })
                    .children(working.iter().map(|r| row(r, false)))
                    .children(waiting.iter().map(|r| row(r, true))),
            )
    }

    fn agents(&self, p: &Project, cx: &mut Context<Self>) -> impl IntoElement {
        let workers = p.workers.clone();
        let enabled = workers.iter().filter(|w| w.on).count();
        let rows = workers.iter().enumerate().map(|(i, w)| {
            let mut next = workers.clone();
            next[i].on = !w.on;
            let locked = w.on && enabled <= 1;
            h_flex()
                .h(px(44.0))
                .px(px(12.0))
                .gap(px(10.0))
                .rounded(px(9.0))
                .bg(c("f_card"))
                .border_1()
                .border_color(c("f_line"))
                .child(ui::agent_icon(&w.agent, 18.0))
                .child(
                    div()
                        .flex_1()
                        .text_size(px(13.0))
                        .font_weight(FontWeight::MEDIUM)
                        .child(crate::sessions::agent_name(&w.agent).to_owned()),
                )
                .child(div().text_size(px(12.0)).text_color(c("f_muted")).child(
                    if w.model.is_empty() {
                        t("default_model")
                    } else {
                        w.model.clone()
                    },
                ))
                .child(
                    switch(SharedString::from(format!("w-{i}")), w.on, !locked).when(
                        !locked,
                        |d| {
                            d.on_click(cx.listener(move |this, _, window, cx| {
                                this.patch_project(json!({"workers": next}), window, cx)
                            }))
                        },
                    ),
                )
        });
        let add = board::AGENTS
            .iter()
            .filter(|a| !workers.iter().any(|w| &w.agent == *a))
            .map(|agent| {
                let mut next = workers.clone();
                next.push(Worker {
                    agent: agent.to_string(),
                    model: String::new(),
                    on: true,
                });
                h_flex()
                    .id(SharedString::from(format!("add-{agent}")))
                    .h(px(28.0))
                    .px(px(10.0))
                    .gap(px(6.0))
                    .rounded(px(7.0))
                    .border_1()
                    .border_color(c("f_field"))
                    .bg(c("f_card"))
                    .text_size(px(12.0))
                    .hover(|s| s.bg(c("f_expanded")))
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.patch_project(json!({"workers": next}), window, cx)
                    }))
                    .child(ui::icon("plus", 12.0, c("f_muted")))
                    .child(ui::agent_icon(agent, 14.0))
                    .child(crate::sessions::agent_name(agent).to_owned())
            });
        v_flex()
            .gap(px(10.0))
            .child(
                div()
                    .text_size(px(12.5))
                    .text_color(c("f_muted"))
                    .child(t("agents_hint")),
            )
            .children(rows)
            .child(h_flex().flex_wrap().gap(px(8.0)).mt(px(6.0)).children(add))
    }
}

/// The panel's 32×19 switch.
fn switch(id: SharedString, on: bool, enabled: bool) -> Stateful<Div> {
    div()
        .id(id)
        .w(px(32.0))
        .h(px(19.0))
        .flex_none()
        .rounded(px(10.0))
        .bg(if on { c("ok") } else { c("f_field") })
        .opacity(if enabled { 1.0 } else { 0.55 })
        .child(
            div()
                .absolute()
                .top(px(2.0))
                .left(px(if on { 15.0 } else { 2.0 }))
                .size(px(15.0))
                .rounded_full()
                .bg(white())
                .shadow_sm(),
        )
        .relative()
}
