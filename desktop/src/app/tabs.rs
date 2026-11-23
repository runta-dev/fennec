//! Tab placement is owned by the daemon; terminals and their views keep their identity.
use super::*;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

#[derive(Clone, Default, Deserialize)]
pub(super) struct Group {
    pub id: String,
    pub tabs: Vec<String>,
    pub active: String,
    pub custom: bool,
}
#[derive(Default, Deserialize)]
pub(super) struct Layout {
    pub revision: u64,
    #[serde(default)]
    pub groups: Vec<Group>,
    #[serde(skip)]
    pub members: HashMap<String, Session>,
    #[serde(skip)]
    pub snapshot: Value,
    #[serde(skip)]
    pub busy: bool,
    #[serde(skip)]
    pub pending: Option<Value>,
    #[serde(skip)]
    pub selection: Option<String>,
    #[serde(skip)]
    pub intent: u64,
    #[serde(skip)]
    pub error: Option<String>,
    #[serde(skip)]
    pub hover: Option<(String, String)>,
    #[serde(skip)]
    pub hover_group: Option<(String, std::time::Instant)>,
    #[serde(skip)]
    pub motion: row_motion::RowMotion,
    #[serde(skip)]
    pub frame: Option<std::time::Instant>,
    #[serde(skip)]
    pub displayed: String,
    #[serde(skip)]
    pub content_started: Option<std::time::Instant>,
    #[serde(skip)]
    pub content_opacity: f32,
}
#[derive(Clone, Serialize)]
struct Descriptor {
    id: String,
    key: String,
    group: String,
}
#[derive(Clone)]
pub(super) struct TabDrag {
    pub key: String,
    pub title: String,
}
impl Render for TabDrag {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        div()
            .px(px(12.0))
            .py(px(7.0))
            .max_w(px(240.0))
            .truncate()
            .rounded(px(7.0))
            .bg(c("f_card"))
            .border_1()
            .border_color(c("f_field"))
            .shadow(ui::selection_shadow())
            .text_size(px(12.5))
            .text_color(c("f_text"))
            .child(self.title.clone())
    }
}

impl FennecWindow {
    pub(super) fn layout_group(&self, root: &str) -> Option<&Group> {
        self.layout
            .groups
            .iter()
            .find(|g| g.tabs.first().is_some_and(|k| k == root))
    }
    pub(super) fn group_tabs(&self, root: &str) -> Vec<String> {
        if let Some(group) = self.layout_group(root) {
            return group
                .tabs
                .iter()
                .filter(|k| self.layout.members.contains_key(*k))
                .cloned()
                .collect();
        }
        let mut tabs = vec![root.to_owned()];
        if let Some(s) = self.sessions.iter().find(|s| s.address == root) {
            tabs.extend(
                self.shells
                    .get(&s.group_key())
                    .into_iter()
                    .flatten()
                    .map(|k| format!("local:{k}")),
            );
        }
        tabs
    }
    pub(super) fn layout_snapshot(&mut self) -> Value {
        let records = self.terminal_records();
        let runtime = self.cloud_listing["runtime_id"]
            .as_str()
            .unwrap_or("unknown");
        let mut descriptors = Vec::new();
        let mut roots = HashMap::new();
        let mut members = HashMap::new();
        for record in &records {
            let Some(address) = record["address"].as_str() else {
                continue;
            };
            let root = record["labels"]["fennec.session"]
                .as_str()
                .filter(|s| !s.is_empty());
            let child = record["labels"]["fennec.shell-of"]
                .as_str()
                .filter(|s| !s.is_empty());
            if root.is_none() && child.is_none() {
                continue;
            }
            let key = if root.is_none() && !address.starts_with("cloud:") {
                format!("local:{address}")
            } else {
                address.to_owned()
            };
            let scope = if address.starts_with("cloud:") {
                runtime
            } else {
                "local"
            };
            let id = format!(
                "{scope}:{}:{}",
                root.unwrap_or(address),
                record["created_ms"].as_u64().unwrap_or(0)
            );
            let mut copy = record.clone();
            copy["address"] = json!(key);
            if root.is_none() {
                copy["labels"]["fennec.session"] = json!(address);
            }
            if let Some(session) = sessions::from_terminals(&[copy]).pop() {
                members.insert(key.clone(), session);
            }
            if let Some(name) = root {
                roots.insert(
                    if address.starts_with("cloud:") {
                        format!("cloud:{name}")
                    } else {
                        name.to_owned()
                    },
                    id.clone(),
                );
            }
            descriptors.push((
                Descriptor {
                    id: id.clone(),
                    key,
                    group: id,
                },
                child.map(str::to_owned),
            ));
        }
        for (tab, parent) in &mut descriptors {
            if let Some(group) = parent.as_ref().and_then(|p| roots.get(p)) {
                tab.group = group.clone();
            }
        }
        self.layout.members = members;
        json!(descriptors.into_iter().map(|(d, _)| d).collect::<Vec<_>>())
    }
    pub(super) fn apply_layout(&mut self) {
        if self.layout.groups.is_empty() {
            return;
        }
        let selected = match &self.route {
            Some(Route::Session(root)) => Some(self.shown_tab(root)),
            _ => None,
        };
        let mut sessions = Vec::new();
        for group in &self.layout.groups {
            let Some(root) = group
                .tabs
                .iter()
                .find(|k| self.layout.members.contains_key(*k))
            else {
                continue;
            };
            let Some(mut session) = self.layout.members.get(root).cloned() else {
                continue;
            };
            let active = self
                .tab
                .get(root)
                .filter(|k| group.tabs.contains(k) && self.layout.members.contains_key(*k))
                .cloned()
                .unwrap_or_else(|| {
                    if self.layout.members.contains_key(&group.active) {
                        group.active.clone()
                    } else {
                        root.clone()
                    }
                });
            self.tab.insert(root.clone(), active.clone());
            if group.custom {
                session.title = self.terminal_title(&active, &session.title);
                session.state = group
                    .tabs
                    .iter()
                    .filter_map(|k| {
                        self.layout.members.get(k).map(|s| {
                            let word =
                                self.activity[k.strip_prefix("local:").unwrap_or(k)]["state"]
                                    .as_str();
                            word.map(activity::state).unwrap_or(s.state)
                        })
                    })
                    .min()
                    .unwrap_or(session.state);
            }
            sessions.push(session);
            if selected.as_ref().is_some_and(|k| group.tabs.contains(k)) {
                self.route = Some(Route::Session(root.clone()));
            }
        }
        // A new session can precede the next layout snapshot by one event.
        for s in &self.sessions {
            if !self
                .layout
                .groups
                .iter()
                .any(|g| g.tabs.contains(&s.address))
            {
                sessions.push(s.clone());
            }
        }
        self.sessions = sessions;
        if let Some(Route::Session(root)) = &self.route {
            if !self.sessions.iter().any(|s| &s.address == root) {
                self.route = self
                    .sessions
                    .first()
                    .map(|s| Route::Session(s.address.clone()));
            }
        }
    }
    pub(super) fn start_layout(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            loop {
                cx.background_executor()
                    .timer(Duration::from_millis(100))
                    .await;
                let request = this.update(cx, |this, _| {
                    if this.layout.busy {
                        return None;
                    }
                    let snapshot = this.layout_snapshot();
                    if snapshot == this.layout.snapshot
                        && this.layout.pending.is_none()
                        && this.layout.selection.is_none()
                    {
                        return None;
                    }
                    let mut request = this.layout.pending.take().unwrap_or_else(|| {
                        this.layout
                            .selection
                            .take()
                            .map(|tab| json!({"operation":"activate","tab":tab}))
                            .unwrap_or_else(|| json!({}))
                    });
                    request["tabs"] = snapshot.clone();
                    request["revision"] = json!(this.layout.revision);
                    this.layout.busy = true;
                    let intent = request
                        .get("intent")
                        .and_then(Value::as_u64)
                        .unwrap_or(this.layout.intent);
                    Some((snapshot, request, intent))
                });
                let Ok(request) = request else { break };
                let Some((snapshot, request, intent)) = request else {
                    continue;
                };
                let select = request["tab"]
                    .as_str()
                    .filter(|_| request["operation"] == "move" || request["operation"] == "detach")
                    .map(str::to_owned);
                let mutation = request["operation"].as_str().is_some();
                let result = cx
                    .background_spawn(
                        async move { crate::control::call("desktop.layout", request) },
                    )
                    .await;
                if this
                    .update_in(cx, |this, window, cx| {
                        this.layout.busy = false;
                        match result.and_then(|value| Ok(serde_json::from_value::<Layout>(value)?))
                        {
                            Ok(layout) => {
                                if this.layout.groups.is_empty() {
                                    for g in &layout.groups {
                                        if let Some(root) = g.tabs.first() {
                                            this.tab.insert(root.clone(), g.active.clone());
                                        }
                                    }
                                }
                                this.layout.revision = layout.revision;
                                this.layout.groups = layout.groups;
                                this.layout.snapshot = snapshot;
                                this.layout.error = None;
                                // Rebuild metadata from the latest event stream, not the saved snapshot.
                                this.layout_snapshot();
                                if let Some(key) = select.filter(|_| this.layout.intent == intent) {
                                    if let Some(g) =
                                        this.layout.groups.iter().find(|g| g.tabs.contains(&key))
                                    {
                                        if let Some(root) = g.tabs.first() {
                                            this.tab.insert(root.clone(), key.clone());
                                            this.route = Some(Route::Session(root.clone()));
                                            this.pending_focus = Some(key);
                                        }
                                    }
                                }
                                this.terminals_changed(window, cx);
                            }
                            Err(_) => {
                                this.layout.error = Some(t("tabs_save_failed"));
                                this.layout.snapshot =
                                    if mutation { Value::Null } else { snapshot };
                            }
                        }
                        cx.notify();
                    })
                    .is_err()
                {
                    break;
                }
            }
        }));
    }
    pub(super) fn move_tab(
        &mut self,
        drag: &TabDrag,
        target: String,
        before: String,
        cx: &mut Context<Self>,
    ) {
        if self.layout.pending.is_some() {
            return;
        }
        self.layout.selection = None;
        self.layout.intent += 1;
        self.layout.hover = None;
        self.layout.pending = Some(
            json!({"operation":if target.is_empty(){"detach"}else{"move"},"tab":drag.key,"target":target,"before":before,"intent":self.layout.intent}),
        );
        cx.notify();
    }
    pub(super) fn drag_tab(&self, key: &str) -> TabDrag {
        TabDrag {
            key: key.to_owned(),
            title: self.terminal_title(key, "Terminal"),
        }
    }
    pub(super) fn tab_strip(&self, root: &str, cx: &Context<Self>) -> AnyElement {
        let active = self.shown_tab(root);
        let group = self
            .layout_group(root)
            .map(|g| g.id.clone())
            .unwrap_or_default();
        let keys = self.group_tabs(root);
        let mut preview = keys.clone();
        if let Some((drag, before)) = &self.layout.hover {
            if preview.contains(before) && drag != before {
                preview.retain(|k| k != drag);
                if let Some(at) = preview.iter().position(|k| k == before) {
                    preview.insert(at, drag.clone());
                }
            }
        }
        let end_group = group.clone();
        h_flex()
            .id("session-tab-strip")
            .flex_1()
            .min_w_0()
            .overflow_x_scroll()
            .gap(px(6.0))
            .children(preview.iter().enumerate().map(|(index, key)| {
                let show = root.to_owned();
                let open = key.clone();
                let close = key.clone();
                let target = group.clone();
                let before = key.clone();
                let hover_key = key.clone();
                let title = self.terminal_title(key, "Terminal");
                let offset = self.layout.motion.offset(
                    key,
                    self.layout.frame.unwrap_or_else(std::time::Instant::now),
                );
                self.context_row(
                    h_flex()
                        .id(SharedString::from(format!("tab-{key}")))
                        .debug_selector(|| format!("tab-{key}"))
                        .relative()
                        .left(px(offset))
                        .w(px(196.0))
                        .h(px(28.0))
                        .px(px(9.0))
                        .gap(px(6.0))
                        .flex_none()
                        .rounded(px(7.0))
                        .bg(if &active == key {
                            c("f_card")
                        } else {
                            c("f_bg")
                        })
                        .border_1()
                        .border_color(if &active == key {
                            c("f_field")
                        } else {
                            transparent_black()
                        })
                        .text_size(px(12.5))
                        .text_color(c("f_text"))
                        .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation()),
                    context_menu::Target::Terminal {
                        session: root.to_owned(),
                        tab: key.clone(),
                    },
                    cx,
                )
                .on_drag(self.drag_tab(key), |drag, _, _, cx| {
                    cx.new(|_| drag.clone())
                })
                .drag_over::<TabDrag>(|style, _, _, _| style.border_color(c("orange")))
                .on_drag_move::<TabDrag>(cx.listener(
                    move |this, event: &DragMoveEvent<TabDrag>, _, cx| {
                        if !event.bounds.contains(&event.event.position) {
                            return;
                        }
                        let drag = event.drag(cx).key.clone();
                        let next = Some((drag, hover_key.clone()));
                        if this.layout.hover != next {
                            this.layout.hover = next;
                            cx.notify();
                        }
                    },
                ))
                .on_drop(cx.listener(move |this, drag: &TabDrag, _, cx| {
                    cx.stop_propagation();
                    this.move_tab(drag, target.clone(), before.clone(), cx)
                }))
                .on_click(cx.listener(move |this, event: &ClickEvent, _, cx| {
                    if !event.is_right_click() && !event.modifiers().control {
                        this.set_shown_tab(show.clone(), open.clone(), cx);
                    }
                }))
                .child(self.program_icon(key.strip_prefix("local:").unwrap_or(key), "shell", 14.0))
                .child(div().flex_1().min_w_0().truncate().child(title))
                .child(self.activity_mark(key.strip_prefix("local:").unwrap_or(key)))
                .child(
                    div()
                        .id(SharedString::from(format!("tab-close-{index}")))
                        .flex()
                        .items_center()
                        .justify_center()
                        .flex_none()
                        .size(px(15.0))
                        .rounded(px(3.0))
                        .hover(|s| s.bg(c("f_line")))
                        .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                        .on_click(cx.listener(move |this, _, window, cx| {
                            cx.stop_propagation();
                            this.close_tab_key(close.clone(), window, cx)
                        }))
                        .child(ui::icon("close", 10.0, c("f_muted"))),
                )
            }))
            .child(
                div()
                    .id("tab-append")
                    .debug_selector(|| "tab-append".into())
                    .w(px(28.0))
                    .h(px(28.0))
                    .flex_none()
                    .rounded(px(5.0))
                    .drag_over::<TabDrag>(|s, _, _, _| s.bg(c("f_line")))
                    .on_drag_move::<TabDrag>(cx.listener(
                        |this, event: &DragMoveEvent<TabDrag>, _, cx| {
                            if event.bounds.contains(&event.event.position)
                                && this.layout.hover.take().is_some()
                            {
                                cx.notify();
                            }
                        },
                    ))
                    .on_drop(cx.listener(move |this, drag: &TabDrag, _, cx| {
                        cx.stop_propagation();
                        this.move_tab(drag, end_group.clone(), String::new(), cx)
                    })),
            )
            .into_any_element()
    }
    pub(super) fn step_tabs(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let now = std::time::Instant::now();
        self.layout.frame = Some(now);
        let shown = match &self.route {
            Some(Route::Session(root)) => self.shown_tab(root),
            _ => String::new(),
        };
        if shown != self.layout.displayed {
            self.layout.displayed = shown;
            self.layout.content_started = Some(now);
        }
        let progress = self
            .layout
            .content_started
            .map(|start| (now.duration_since(start).as_secs_f32() / 0.16).min(1.0))
            .unwrap_or(1.0);
        self.layout.content_opacity = 0.85 + 0.15 * (1.0 - (1.0 - progress).powi(3));
        if progress < 1.0 {
            window.request_animation_frame();
        }
        if !cx.has_active_drag() {
            self.layout.hover = None;
            self.layout.hover_group = None;
        }
        if let Some((root, since)) = &self.layout.hover_group {
            if since.elapsed() >= Duration::from_millis(450) {
                if self.route != Some(Route::Session(root.clone())) {
                    self.route = Some(Route::Session(root.clone()));
                    cx.notify();
                }
            } else {
                window.request_animation_frame();
            }
        }
        let mut keys = self.tab_keys();
        if let Some((drag, before)) = &self.layout.hover {
            if keys.contains(before) && drag != before {
                keys.retain(|k| k != drag);
                if let Some(at) = keys.iter().position(|k| k == before) {
                    keys.insert(at, drag.clone());
                }
            }
        }
        let positions: Vec<_> = keys
            .iter()
            .enumerate()
            .map(|(i, k)| (k.clone(), i as f32 * 202.0))
            .collect();
        if self.layout.motion.update(&positions, "", now) {
            window.request_animation_frame();
            cx.notify();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::{EventLines, FennecWindow, Group, Route, json, sessions};
    use gpui::{Entity, Modifiers, MouseButton, TestAppContext, VisualTestContext, point, px};

    fn window(cx: &mut TestAppContext) -> (Entity<FennecWindow>, &mut VisualTestContext) {
        cx.update(gpui_base::init);
        cx.add_window_view(|window,cx| {
            let (_,receive)=async_channel::unbounded();
            let mut app=FennecWindow::build(receive,window,cx,false);
            let records=json!([
                {"address":"a","state":"live","title":"First agent","labels":{"fennec.session":"alpha"},"status":{"app":"codex","state":"idle"},"created_ms":1},
                {"address":"b","state":"live","title":"Second agent","labels":{"fennec.shell-of":"alpha"},"status":{"app":"codex","state":"idle"},"created_ms":2},
                {"address":"c","state":"live","title":"Third agent","labels":{"fennec.session":"gamma"},"status":{"app":"codex","state":"working"},"created_ms":3}
            ]);
            app.events=Some(EventLines::new(&json!({"terminals":records,"seq":1})));
            app.sessions=sessions::from_terminals(records.as_array().unwrap());
            app.layout_snapshot();
            app.layout.groups=vec![Group{id:"one".into(),tabs:vec!["a".into(),"local:b".into()],active:"a".into(),custom:false},Group{id:"two".into(),tabs:vec!["c".into()],active:"c".into(),custom:false}];
            app.activity=json!({"a":{"state":"idle","program":{"name":"Codex","agent":true}},"b":{"state":"idle","program":{"name":"Codex","agent":true}},"c":{"state":"working","program":{"name":"Codex","agent":true}}});
            app.route=Some(Route::Session("a".into()));
            app.apply_layout();
            app
        })
    }
    fn drag(cx: &mut VisualTestContext, from: &'static str, to: &'static str) {
        cx.run_until_parked();
        let start = cx
            .debug_bounds(from)
            .unwrap_or_else(|| panic!("missing {from}"))
            .center();
        let end = cx
            .debug_bounds(to)
            .unwrap_or_else(|| panic!("missing {to}"))
            .center();
        cx.simulate_mouse_move(start, None, Modifiers::default());
        cx.simulate_mouse_down(start, MouseButton::Left, Modifiers::default());
        cx.simulate_mouse_move(
            start + point(px(12.0), px(0.0)),
            Some(MouseButton::Left),
            Modifiers::default(),
        );
        cx.simulate_mouse_move(end, Some(MouseButton::Left), Modifiers::default());
        cx.simulate_mouse_up(end, MouseButton::Left, Modifiers::default());
        cx.run_until_parked();
    }
    #[gpui::test]
    fn sidebar_drag_into_tab_strip_submits_a_move(cx: &mut TestAppContext) {
        let (app, cx) = window(cx);
        drag(cx, "session-row-c", "tab-append");
        app.read_with(cx, |app, _| {
            let request = app
                .layout
                .pending
                .as_ref()
                .expect("drop must submit a move");
            assert_eq!(request["operation"], "move");
            assert_eq!(request["tab"], "c");
            assert_eq!(request["target"], "one");
            assert_eq!(
                app.layout.groups[1].tabs,
                vec!["c"],
                "wait for the daemon's confirmation"
            );
        });
    }
    #[gpui::test]
    fn tab_drag_to_sidebar_submits_detach(cx: &mut TestAppContext) {
        let (app, cx) = window(cx);
        drag(cx, "tab-local:b", "detach-tab-area");
        app.read_with(cx, |app, _| {
            let request = app
                .layout
                .pending
                .as_ref()
                .expect("drop must submit detach");
            assert_eq!(request["operation"], "detach");
            assert_eq!(request["tab"], "local:b");
        });
    }
    #[gpui::test]
    fn tab_drag_reorders_at_the_target_slot(cx: &mut TestAppContext) {
        let (app, cx) = window(cx);
        drag(cx, "tab-local:b", "tab-a");
        app.read_with(cx, |app, _| {
            let request = app
                .layout
                .pending
                .as_ref()
                .expect("drop must submit reorder");
            assert_eq!(request["operation"], "move");
            assert_eq!(request["tab"], "local:b");
            assert_eq!(request["target"], "one");
            assert_eq!(request["before"], "a");
        });
    }
    #[gpui::test]
    fn invalid_drop_leaves_layout_unchanged(cx: &mut TestAppContext) {
        let (app, cx) = window(cx);
        cx.run_until_parked();
        let start = cx.debug_bounds("tab-local:b").unwrap().center();
        cx.simulate_mouse_down(start, MouseButton::Left, Modifiers::default());
        cx.simulate_mouse_move(
            point(px(600.0), px(350.0)),
            Some(MouseButton::Left),
            Modifiers::default(),
        );
        cx.simulate_mouse_up(
            point(px(600.0), px(350.0)),
            MouseButton::Left,
            Modifiers::default(),
        );
        app.read_with(cx, |app, _| assert!(app.layout.pending.is_none()));
    }
    #[gpui::test]
    fn escape_cancels_drag_before_mouse_release(cx: &mut TestAppContext) {
        let (app, cx) = window(cx);
        cx.run_until_parked();
        cx.update(|window, cx| {
            let focus = app.read(cx).focus.clone();
            focus.focus(window, cx)
        });
        let start = cx.debug_bounds("tab-local:b").unwrap().center();
        let end = cx.debug_bounds("tab-a").unwrap().center();
        cx.simulate_mouse_down(start, MouseButton::Left, Modifiers::default());
        cx.simulate_mouse_move(end, Some(MouseButton::Left), Modifiers::default());
        cx.simulate_keystrokes("escape");
        cx.simulate_mouse_up(end, MouseButton::Left, Modifiers::default());
        app.read_with(cx, |app, _| assert!(app.layout.pending.is_none()));
    }
    #[gpui::test]
    fn grouped_tabs_remain_individual_sidebar_sessions(cx: &mut TestAppContext) {
        let (app, cx) = window(cx);
        app.update(cx, |app, _| {
            app.layout.groups = vec![Group {
                id: "merged".into(),
                tabs: vec!["a".into(), "local:b".into(), "c".into()],
                active: "a".into(),
                custom: true,
            }];
            app.apply_layout();
        });
        app.read_with(cx, |app, _| {
            let rows = app.session_list("");
            assert_eq!(rows.len(), 3, "grouping must not hide sessions");
            let working = rows.iter().find(|r| r.tab.as_deref() == Some("c")).unwrap();
            assert!(working.state == sessions::State::Working);
            let idle = rows
                .iter()
                .find(|r| r.tab.as_deref() == Some("local:b"))
                .unwrap();
            assert!(idle.state == sessions::State::Idle);
            let filtered = app.session_list("second agent");
            assert_eq!(filtered.len(), 1);
            assert_eq!(filtered[0].tab.as_deref(), Some("local:b"));
        });
    }
}
