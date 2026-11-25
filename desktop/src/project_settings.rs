//! Project settings (docs/desktop-app.md): General, Knowledge, Queue, Agents.

use crate::app::{FennecWindow, Route, secondary_button};
use crate::board::{self, Concept, Project, QueueProject, Tag, Worker};
use crate::i18n::{t, tf};
use crate::ui::{self, c};
use gpui::prelude::FluentBuilder as _;
use gpui::*;
use gpui_base::input::{Input, InputEditorStyle, InputEvent, InputState};
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
    concept: String,
    add_worker_open: bool,
    model_menu: Option<usize>,
    model_input: Option<Entity<InputState>>,
    queue: Option<QueueProject>,
}

const DEFAULT_TAGS: &[(&str, &str)] = &[
    ("bug", "Bug"),
    ("feature", "Feature"),
    ("discussion", "Discussion"),
];

impl State {
    pub(crate) fn sync_input_styles(&self, style: &InputEditorStyle, cx: &mut App) {
        for input in [
            &self.name,
            &self.key,
            &self.description,
            &self.repository,
            &self.new_tag,
            &self.model_input,
        ]
        .into_iter()
        .flatten()
        {
            input.update(cx, |s, _| s.set_editor_style(style.clone()));
        }
    }
}

impl FennecWindow {
    pub(crate) fn focus_project_name(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if let Some(input) = &self.settings.name {
            input.read(cx).focus_handle(cx).focus(window, cx);
            input.update(cx, |input, cx| input.select_all(window, cx));
        }
    }

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
                            this.save_general(window, cx);
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
            for input in [
                &self.settings.name,
                &self.settings.key,
                &self.settings.description,
                &self.settings.repository,
            ]
            .into_iter()
            .flatten()
            {
                self._subs.push(cx.subscribe_in(
                    input,
                    window,
                    |this, _, event: &InputEvent, window, cx| {
                        if matches!(event, InputEvent::Blur) {
                            this.save_general(window, cx);
                        }
                    },
                ));
            }
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
        if crate::preview::active() {
            return;
        }
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

    pub(crate) fn project_settings(
        &mut self,
        p: &Project,
        tab: Tab,
        _window: &mut Window,
        cx: &mut Context<Self>,
    ) -> impl IntoElement {
        let tabs = [
            (Tab::General, "general", "sliders"),
            (Tab::Knowledge, "knowledge", "book"),
            (Tab::Queue, "queue", "layers"),
            (Tab::Agents, "agents", "bot"),
        ]
        .map(|(which, label, icon)| {
            let id = p.id.clone();
            h_flex()
                .id(SharedString::from(format!("tab-{which:?}")))
                .h(px(30.0))
                .w_full()
                .px(px(8.0))
                .gap(px(9.0))
                .mb(px(1.0))
                .rounded(px(7.0))
                .text_size(px(13.0))
                .font_weight(FontWeight::MEDIUM)
                .text_color(c("f_text"))
                .when(which == tab, |d| {
                    d.bg(c("f_card")).shadow(ui::selection_shadow())
                })
                .when(which != tab, |d| {
                    d.hover(|s| s.bg(c("f_line").opacity(0.5)))
                })
                .on_click(cx.listener(move |this, _, window, cx| {
                    this.go(Route::Settings(id.clone(), which), window, cx)
                }))
                .child(ui::icon(icon, 15.0, c("f_muted")))
                .child(t(label))
        });
        let body = match tab {
            Tab::General => self.general(p, cx).into_any_element(),
            Tab::Knowledge => self.knowledge(p, cx).into_any_element(),
            Tab::Queue => self.queue_tab(p, cx).into_any_element(),
            Tab::Agents => self.agents(p, cx).into_any_element(),
        };
        let heading = match tab {
            Tab::General => "general",
            Tab::Knowledge => "knowledge",
            Tab::Queue => "queue",
            Tab::Agents => "agents",
        };
        let back = p.id.clone();
        v_flex()
            .flex_1()
            .min_w_0()
            .h_full()
            .bg(c("background"))
            .child(
                h_flex()
                    .id("settings-head")
                    .h(px(58.0))
                    .flex_none()
                    .px(px(18.0))
                    .gap(px(11.0))
                    .border_b_1()
                    .border_color(c("f_line"))
                    .on_mouse_down(MouseButton::Left, ui::drag_titlebar)
                    .pl(self.title_inset(18.0, 11.0))
                    .child(
                        div()
                            .id("settings-back")
                            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                            .size(px(28.0))
                            .rounded(px(6.0))
                            .flex()
                            .items_center()
                            .justify_center()
                            .hover(|s| s.bg(c("f_line")))
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.go(Route::Project(back.clone()), window, cx)
                            }))
                            .child(ui::icon("back", 17.0, c("f_muted"))),
                    )
                    .child(ui::project_icon(p, 30.0))
                    .child(
                        v_flex()
                            .gap(px(2.0))
                            .child(
                                div()
                                    .text_size(px(13.5))
                                    .font_weight(FontWeight::MEDIUM)
                                    .child(p.name.clone()),
                            )
                            .child(
                                div()
                                    .text_size(px(11.5))
                                    .text_color(c("f_muted"))
                                    .child(t("project_settings")),
                            ),
                    ),
            )
            .child(
                h_flex()
                    .flex_1()
                    .min_h_0()
                    .items_stretch()
                    .child(
                        v_flex()
                            .w(px(188.0))
                            .flex_none()
                            .px(px(8.0))
                            .py(px(12.0))
                            .border_r_1()
                            .border_color(c("f_line"))
                            .children(tabs),
                    )
                    .child(
                        div()
                            .id("settings-body")
                            .flex_1()
                            .min_w_0()
                            .min_h_0()
                            .overflow_y_scroll()
                            .child(
                                v_flex()
                                    .w_full()
                                    .when(tab != Tab::Knowledge, |d| d.max_w(px(760.0)))
                                    .px(px(28.0))
                                    .pt(px(20.0))
                                    .pb(px(40.0))
                                    .child(
                                        div()
                                            .mb(px(14.0))
                                            .text_size(px(17.0))
                                            .font_weight(FontWeight::MEDIUM)
                                            .child(t(heading)),
                                    )
                                    .child(body),
                            ),
                    ),
            )
    }

    fn upload_project_icon(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if crate::preview::active() {
            return;
        }
        let project = self.settings.project.clone();
        let selected = cx.prompt_for_paths(PathPromptOptions {
            files: true,
            directories: false,
            multiple: false,
            prompt: Some(t("upload_image").into()),
        });
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let Ok(Ok(Some(paths))) = selected.await else {
                return;
            };
            let Some(path) = paths.first() else {
                return;
            };
            let path = path.to_string_lossy().into_owned();
            let target = project.clone();
            let result = cx
                .background_spawn(async move {
                    let value = crate::control::call("board.icon", json!({"path":path}))?;
                    board::update_project(&target, json!({"icon":value["icon"]}))
                })
                .await;
            let _ = this.update(cx, |this, cx| match result {
                Ok(project) => {
                    if let Some(old) = this.projects.iter_mut().find(|p| p.id == project.id) {
                        let (open, needs_you) = (old.open, old.needs_you);
                        *old = Project {
                            open,
                            needs_you,
                            ..project
                        };
                    }
                    cx.notify();
                }
                Err(error) => {
                    tracing::warn!("project icon update failed: {error:#}");
                    if this.project_id().as_deref() == Some(project.as_str()) {
                        this.say(t("try_again"), cx);
                    }
                }
            });
        }));
    }

    fn general(&self, p: &Project, cx: &mut Context<Self>) -> impl IntoElement {
        let s = &self.settings;
        let field = |label: String, hint: Option<String>, input: &Option<Entity<InputState>>| {
            let compact = hint.is_some();
            h_flex()
                .min_h(px(46.0))
                .py(px(8.0))
                .gap(px(14.0))
                .border_t_1()
                .border_color(c("f_line"))
                .child(
                    div()
                        .w(px(150.0))
                        .flex_none()
                        .text_size(px(13.0))
                        .child(label),
                )
                .when_some(input.as_ref(), |d, i| {
                    d.child(
                        div()
                            .h(px(30.0))
                            .when(compact, |d| d.w(px(110.0)).flex_none())
                            .when(!compact, |d| d.flex_1())
                            .min_w_0()
                            .px(px(9.0))
                            .rounded(px(7.0))
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
                    d.child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .truncate()
                            .text_size(px(12.0))
                            .text_color(c("f_muted"))
                            .child(h),
                    )
                })
        };
        let swatches = board::COLORS.iter().map(|color| {
            let on = p.color.eq_ignore_ascii_case(color);
            let value = color.to_string();
            let tint = crate::settings::parse_hex(color).unwrap_or(c("orange"));
            div()
                .id(SharedString::from(format!("color-{color}")))
                .size(px(18.0))
                .rounded_full()
                .bg(tint)
                .when(on, |d| {
                    d.shadow(vec![BoxShadow {
                        color: tint,
                        offset: point(px(0.0), px(0.0)),
                        blur_radius: px(0.0),
                        spread_radius: px(3.0),
                        inset: false,
                    }])
                    .border_2()
                    .border_color(c("f_card"))
                })
                .on_click(cx.listener(move |this, _, window, cx| {
                    this.patch_project(json!({"color":value,"icon":""}), window, cx)
                }))
        });
        let tags = s.tags.iter().map(|tag| {
            let id = tag.id.clone();
            let color = ui::tag_color(&tag.id);
            h_flex()
                .h(px(24.0))
                .pl(px(9.0))
                .pr(px(4.0))
                .gap(px(3.0))
                .rounded(px(5.0))
                .bg(color.opacity(0.12))
                .text_color(color)
                .text_size(px(12.0))
                .child(tag.label.clone())
                .when(s.tags.len() > 1, |d| {
                    d.child(
                        div()
                            .id(SharedString::from(format!("rm-tag-{}", tag.id)))
                            .size(px(16.0))
                            .rounded(px(4.0))
                            .flex()
                            .items_center()
                            .justify_center()
                            .hover(|s| s.bg(c("f_line")))
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.settings.tags.retain(|t| t.id != id);
                                this.save_general(window, cx);
                            }))
                            .child(ui::icon("close", 10.0, color.opacity(0.6))),
                    )
                })
        });
        let key = s
            .key
            .as_ref()
            .map(|k| k.read(cx).value().trim().to_uppercase())
            .filter(|k| !k.is_empty())
            .unwrap_or_else(|| p.key.clone());
        let card = || {
            v_flex()
                .bg(c("f_card"))
                .border_1()
                .border_color(c("f_line"))
                .rounded(px(11.0))
                .px(px(14.0))
                .py(px(4.0))
        };
        v_flex()
            .child(
                card()
                    .child(
                        h_flex()
                            .min_h(px(56.0))
                            .py(px(8.0))
                            .gap(px(14.0))
                            .child(
                                div()
                                    .w(px(150.0))
                                    .flex_none()
                                    .text_size(px(13.0))
                                    .child(t("icon")),
                            )
                            .child(ui::project_icon(p, 40.0))
                            .child(h_flex().gap(px(6.0)).children(swatches))
                            .child(div().flex_1())
                            .child(
                                secondary_button("upload-icon", t("upload_image"), false).on_click(
                                    cx.listener(|this, _, window, cx| {
                                        this.upload_project_icon(window, cx)
                                    }),
                                ),
                            ),
                    )
                    .child(field(t("name"), None, &s.name))
                    .child(field(
                        t("key"),
                        Some(tf("key_hint", &[("key", &key)])),
                        &s.key,
                    ))
                    .child(field(t("description"), None, &s.description))
                    .child(field(t("repository"), None, &s.repository)),
            )
            .child(
                div()
                    .mt(px(22.0))
                    .mb(px(4.0))
                    .text_size(px(13.0))
                    .font_weight(FontWeight::MEDIUM)
                    .child(t("tags")),
            )
            .child(
                div()
                    .mb(px(10.0))
                    .text_size(px(12.0))
                    .text_color(c("f_muted"))
                    .child(t("tags_hint")),
            )
            .child(
                card()
                    .child(
                        h_flex()
                            .flex_wrap()
                            .py(px(12.0))
                            .gap(px(7.0))
                            .children(tags)
                            .when_some(s.new_tag.as_ref(), |d, i| {
                                d.child(
                                    div()
                                        .h(px(26.0))
                                        .flex_1()
                                        .min_w(px(150.0))
                                        .px(px(9.0))
                                        .rounded(px(7.0))
                                        .border_1()
                                        .border_color(c("f_field"))
                                        .flex()
                                        .items_center()
                                        .text_size(px(13.0))
                                        .child(Input::new(i)),
                                )
                            }),
                    )
                    .child(
                        div()
                            .id("reset-tags")
                            .py(px(10.0))
                            .border_t_1()
                            .border_color(c("f_line"))
                            .text_size(px(12.0))
                            .text_color(c("f_muted"))
                            .on_click(cx.listener(|this, _, window, cx| {
                                this.settings.tags = DEFAULT_TAGS
                                    .iter()
                                    .map(|(id, label)| Tag {
                                        id: (*id).into(),
                                        label: (*label).into(),
                                    })
                                    .collect();
                                this.save_general(window, cx);
                            }))
                            .child(t("reset_tags")),
                    ),
            )
    }

    fn knowledge(&self, p: &Project, cx: &mut Context<Self>) -> impl IntoElement {
        let s = &self.settings;
        let show = |k: &Concept| match s.filter {
            KnowledgeFilter::All => true,
            KnowledgeFilter::Deprecated => k.status == "deprecated",
        };
        let filters = [
            (KnowledgeFilter::All, t("all")),
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
        let current = s
            .knowledge
            .iter()
            .filter(|k| show(k))
            .find(|k| k.id == s.concept)
            .or_else(|| s.knowledge.iter().find(|k| show(k)));
        let concept = |k: &Concept| {
            // Agents keep the status; there is no review step, so a draft
            // reads the same as any other live entry.
            let (status, color) = match k.status.as_str() {
                "deprecated" => (t("k_deprecated"), c("f_muted")),
                _ => (t("k_current"), c("ok")),
            };
            v_flex()
                .px(px(18.0))
                .py(px(16.0))
                .flex_1()
                .min_w_0()
                .gap(px(12.0))
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
                                .font_weight(FontWeight::MEDIUM)
                                .child(k.title.clone()),
                        )
                        .child(
                            div()
                                .text_size(px(11.0))
                                .font_weight(FontWeight::MEDIUM)
                                .text_color(color)
                                .child(status.clone()),
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
                    v_flex()
                        .py(px(12.0))
                        .gap(px(8.0))
                        .border_t_1()
                        .border_b_1()
                        .border_color(c("f_line"))
                        .children(
                            [
                                (t("status"), status),
                                (
                                    t("written_by"),
                                    format!(
                                        "{}{}",
                                        sessions_actor(&k.generated_by),
                                        if k.generated_at.is_empty() {
                                            String::new()
                                        } else {
                                            format!(" · {}", k.generated_at)
                                        }
                                    ),
                                ),
                                (t("tags"), k.tags.join(" · ")),
                                (t("based_on"), k.sources.join(" · ")),
                            ]
                            .into_iter()
                            .map(|(label, value)| {
                                h_flex()
                                    .gap(px(12.0))
                                    .items_start()
                                    .text_size(px(12.5))
                                    .child(
                                        div()
                                            .w(px(96.0))
                                            .flex_none()
                                            .text_color(c("f_muted"))
                                            .child(label),
                                    )
                                    .child(div().flex_1().min_w_0().child(value))
                            }),
                        ),
                )
                .when(!k.body.is_empty(), |d| {
                    d.child(
                        gpui_base::text::TextView::markdown(
                            SharedString::from(format!("knowledge-{}", k.id)),
                            k.body.clone(),
                        )
                        .style(ui::markdown_style())
                        .text_size(px(13.5))
                        .text_color(c("f_text")),
                    )
                })
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
            .child(
                h_flex()
                    .gap(px(14.0))
                    .items_start()
                    .min_h(px(420.0))
                    .child(
                        v_flex()
                            .w(px(240.0))
                            .flex_none()
                            .children(sections.iter().map(|(folder, list)| {
                                v_flex()
                                    .child(
                                        div()
                                            .px(px(10.0))
                                            .pt(px(10.0))
                                            .pb(px(3.0))
                                            .text_size(px(11.0))
                                            .text_color(c("f_muted"))
                                            .child(section_name(folder)),
                                    )
                                    .children(list.iter().map(|k| {
                                        let id = k.id.clone();
                                        let selected = current.is_some_and(|c| c.id == k.id);
                                        v_flex()
                                            .id(SharedString::from(format!("concept-{}", k.id)))
                                            .px(px(10.0))
                                            .py(px(8.0))
                                            .gap(px(3.0))
                                            .rounded(px(8.0))
                                            .when(selected, |d| {
                                                d.bg(c("f_card")).shadow(ui::selection_shadow())
                                            })
                                            .hover(|s| s.bg(c("f_line").opacity(0.5)))
                                            .on_click(cx.listener(move |this, _, _, cx| {
                                                this.settings.concept = id.clone();
                                                cx.notify();
                                            }))
                                            .child(
                                                div()
                                                    .text_size(px(13.0))
                                                    .font_weight(FontWeight::MEDIUM)
                                                    .truncate()
                                                    .child(k.title.clone()),
                                            )
                                            .child(
                                                div()
                                                    .text_size(px(11.0))
                                                    .text_color(c("f_muted"))
                                                    .child(k.kind.clone()),
                                            )
                                    }))
                            })),
                    )
                    .when_some(current, |d, k| d.child(concept(k))),
            )
    }

    fn queue_tab(&self, p: &Project, cx: &mut Context<Self>) -> impl IntoElement {
        let limit = p.queue.max(1);
        let paused = p.paused;
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
                .h(px(40.0))
                .px(px(8.0))
                .gap(px(10.0))
                .rounded(px(7.0))
                .hover(|s| s.bg(c("f_line").opacity(0.6)))
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
                        secondary_button(
                            "queue-pause",
                            if paused {
                                t("resume_queue")
                            } else {
                                t("pause_queue")
                            },
                            false,
                        )
                        .on_click(cx.listener(
                            move |this, _, window, cx| {
                                this.patch_project(json!({"paused": !paused}), window, cx)
                            },
                        )),
                    )
                    .child(div().text_size(px(11.5)).text_color(c("f_muted")).child(
                        match p.sync_status.as_str() {
                            "offline" => t("queue_offline"),
                            "pending" => t("queue_pending"),
                            _ => String::new(),
                        },
                    )),
            )
            .child(
                h_flex()
                    .bg(c("f_card"))
                    .border_1()
                    .border_color(c("f_line"))
                    .rounded(px(11.0))
                    .px(px(14.0))
                    .py(px(12.0))
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
                    .child(
                        h_flex()
                            .border_1()
                            .border_color(c("f_field"))
                            .rounded(px(8.0))
                            .overflow_hidden()
                            .child(step("q-less", "−", limit.saturating_sub(1), limit > 1))
                            .child(
                                div()
                                    .w(px(24.0))
                                    .flex()
                                    .justify_center()
                                    .text_size(px(14.0))
                                    .font_weight(FontWeight::MEDIUM)
                                    .child(limit.to_string()),
                            )
                            .child(step("q-more", "+", limit + 1, limit < 10)),
                    ),
            )
            .child(
                v_flex()
                    .gap(px(8.0))
                    .child(
                        div()
                            .text_size(px(13.0))
                            .font_weight(FontWeight::MEDIUM)
                            .child(t("now_heading")),
                    )
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
                    .child(
                        v_flex()
                            .bg(c("f_card"))
                            .border_1()
                            .border_color(c("f_line"))
                            .rounded(px(11.0))
                            .px(px(6.0))
                            .py(px(4.0))
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
                    ),
            )
    }

    fn choose_worker_model(&mut self, index: usize, window: &mut Window, cx: &mut Context<Self>) {
        let Some(project) = self.projects.iter().find(|p| p.id == self.settings.project) else {
            return;
        };
        let Some(worker) = project.workers.get(index) else {
            return;
        };
        let value = worker.model.clone();
        let input = cx.new(|cx| {
            let mut s = InputState::new(window, cx).placeholder(t("default_model"));
            s.set_value(value, window, cx);
            s
        });
        self._subs.push(cx.subscribe_in(
            &input,
            window,
            |this, _, event: &InputEvent, window, cx| {
                if matches!(event, InputEvent::PressEnter { shift: false, .. }) {
                    this.save_worker_model(false, window, cx);
                }
            },
        ));
        self.settings.model_input = Some(input.clone());
        self.settings.model_menu = Some(index);
        input.read(cx).focus_handle(cx).focus(window, cx);
        cx.notify();
    }
    fn save_worker_model(&mut self, default: bool, window: &mut Window, cx: &mut Context<Self>) {
        let Some(index) = self.settings.model_menu.take() else {
            return;
        };
        let Some(project) = self.projects.iter().find(|p| p.id == self.settings.project) else {
            return;
        };
        let mut workers = project.workers.clone();
        let Some(worker) = workers.get_mut(index) else {
            return;
        };
        worker.model = if default {
            String::new()
        } else {
            self.settings
                .model_input
                .as_ref()
                .map(|i| i.read(cx).value().trim().to_owned())
                .unwrap_or_default()
        };
        self.patch_project(json!({"workers":workers}), window, cx);
    }
    fn worker_model_menu(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let index = self.settings.model_menu?;
        let input = self.settings.model_input.as_ref()?;
        Some(
            v_flex()
                .absolute()
                .left(px(142.0))
                .top(px(48.0 * (index + 1) as f32 + 4.0))
                .w(px(280.0))
                .p(px(10.0))
                .gap(px(8.0))
                .bg(c("f_card"))
                .rounded(px(9.0))
                .border_1()
                .border_color(c("f_field"))
                .shadow_lg()
                .occlude()
                .on_mouse_down_out(cx.listener(|this, _, _, cx| {
                    this.settings.model_menu = None;
                    cx.notify();
                }))
                .child(
                    div()
                        .h(px(30.0))
                        .px(px(9.0))
                        .rounded(px(7.0))
                        .border_1()
                        .border_color(c("f_field"))
                        .flex()
                        .items_center()
                        .text_size(px(13.0))
                        .child(Input::new(input)),
                )
                .child(
                    h_flex()
                        .gap(px(8.0))
                        .justify_end()
                        .child(
                            secondary_button("default-worker-model", t("default_model"), false)
                                .on_click(cx.listener(|this, _, window, cx| {
                                    this.save_worker_model(true, window, cx)
                                })),
                        )
                        .child(
                            secondary_button("save-worker-model", t("save_changes"), false)
                                .on_click(cx.listener(|this, _, window, cx| {
                                    this.save_worker_model(false, window, cx)
                                })),
                        ),
                )
                .into_any_element(),
        )
    }

    fn agents(&self, p: &Project, cx: &mut Context<Self>) -> impl IntoElement {
        let model_menu = self.worker_model_menu(cx);
        let workers = p.workers.clone();
        let enabled = workers.iter().filter(|w| w.on).count();
        let rows =
            workers.iter().enumerate().map(|(i, w)| {
                let mut next = workers.clone();
                next[i].on = !w.on;
                let locked = w.on && enabled <= 1;
                let mut removed = workers.clone();
                removed.remove(i);
                h_flex()
                    .h(px(48.0))
                    .gap(px(10.0))
                    .border_b_1()
                    .border_color(c("f_line"))
                    .child(
                        div()
                            .opacity(if w.on { 1.0 } else { 0.45 })
                            .child(ui::agent_icon(&w.agent, 18.0)),
                    )
                    .child(
                        div()
                            .w(px(92.0))
                            .flex_none()
                            .opacity(if w.on { 1.0 } else { 0.45 })
                            .text_size(px(13.0))
                            .font_weight(FontWeight::MEDIUM)
                            .child(crate::sessions::agent_name(&w.agent).to_owned()),
                    )
                    .child(
                        div()
                            .id(SharedString::from(format!("worker-model-{i}")))
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.choose_worker_model(i, window, cx)
                            }))
                            .w(px(150.0))
                            .h(px(28.0))
                            .px(px(9.0))
                            .rounded(px(7.0))
                            .border_1()
                            .border_color(c("f_field"))
                            .flex()
                            .items_center()
                            .opacity(if w.on { 1.0 } else { 0.45 })
                            .text_size(px(12.5))
                            .justify_between()
                            .text_color(c("f_text"))
                            .child(div().flex_1().min_w_0().truncate().child(
                                if w.model.is_empty() {
                                    t("default_model")
                                } else {
                                    w.model.clone()
                                },
                            ))
                            .child(ui::icon("chevron", 13.0, c("f_muted"))),
                    )
                    .child(div().flex_1())
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
                    .child(
                        div()
                            .id(SharedString::from(format!("worker-remove-{i}")))
                            .size(px(28.0))
                            .rounded(px(6.0))
                            .flex()
                            .items_center()
                            .justify_center()
                            .opacity(if locked { 0.35 } else { 1.0 })
                            .when(!locked, |d| {
                                d.on_click(cx.listener(move |this, _, window, cx| {
                                    this.patch_project(json!({"workers":removed}), window, cx)
                                }))
                            })
                            .child(ui::icon("trash", 17.0, c("f_muted"))),
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
                    .h(px(29.0))
                    .px(px(9.0))
                    .gap(px(8.0))
                    .rounded(px(5.0))
                    .text_size(px(12.5))
                    .hover(|s| s.bg(c("f_expanded")))
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.settings.add_worker_open = false;
                        this.patch_project(json!({"workers": next}), window, cx)
                    }))
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
            .child(
                v_flex()
                    .relative()
                    .bg(c("f_card"))
                    .border_1()
                    .border_color(c("f_line"))
                    .rounded(px(11.0))
                    .px(px(14.0))
                    .py(px(4.0))
                    .children(rows)
                    .when_some(model_menu, |d, menu| d.child(menu))
                    .child(
                        div()
                            .relative()
                            .py(px(10.0))
                            .child(
                                h_flex()
                                    .id("add-worker-menu")
                                    .h(px(28.0))
                                    .px(px(9.0))
                                    .gap(px(7.0))
                                    .rounded(px(7.0))
                                    .border_1()
                                    .border_color(c("f_field"))
                                    .bg(c("f_card"))
                                    .text_size(px(12.5))
                                    .on_click(cx.listener(|this, _, _, cx| {
                                        this.settings.add_worker_open =
                                            !this.settings.add_worker_open;
                                        cx.notify();
                                    }))
                                    .child(t("add_worker"))
                                    .child(ui::icon("chevron", 13.0, c("f_muted"))),
                            )
                            .when(self.settings.add_worker_open, |d| {
                                d.child(
                                    v_flex()
                                        .absolute()
                                        .top(px(42.0))
                                        .left(px(0.0))
                                        .min_w(px(180.0))
                                        .p(px(5.0))
                                        .gap(px(2.0))
                                        .rounded(px(9.0))
                                        .border_1()
                                        .border_color(c("f_field"))
                                        .bg(c("f_card"))
                                        .shadow_lg()
                                        .on_mouse_down_out(cx.listener(|this, _, _, cx| {
                                            this.settings.add_worker_open = false;
                                            cx.notify();
                                        }))
                                        .children(add),
                                )
                            }),
                    ),
            )
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
        .bg(if on {
            c("orange")
        } else {
            if crate::settings::get().dark {
                rgba(0x4a4a52ff).into()
            } else {
                rgba(0xd1d1d8ff).into()
            }
        })
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

fn sessions_actor(value: &str) -> String {
    if let Some(person) = value.strip_prefix("human:") {
        return person.to_owned();
    }
    crate::sessions::agent_name(value.split('/').next().unwrap_or(value)).to_owned()
}
