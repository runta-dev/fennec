//! Native presentation and ordered delivery of core events to the local policy service.
use super::*;
use serde_json::{Value, json};

impl FennecWindow {
    pub(super) fn start_activity(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let (send, receive) = async_channel::unbounded::<(Value, Arc<Core>)>();
        self.activity_send = Some(send);
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let mut previous = Value::Null;
            while let Ok((mut input, core)) = receive.recv().await {
                input["previous"] = previous.clone();
                let result = cx.background_spawn(async move {
                    let mut result = crate::control::call("desktop.activity.update", input)?;
                    let writes = result["seen"].as_object().cloned().unwrap_or_default();
                    for (addr, turn) in writes {
                        if core.call("terminal.label", json!({"terminal": addr, "labels": {"herdr.seen": turn.to_string()}})).is_err() {
                            result["previous"][&addr]["looked"] = Value::Null;
                            result["previous"][&addr]["written"] = Value::Null;
                            result["retry"] = json!(true);
                        }
                    }
                    anyhow::Ok(result)
                }).await;
                let Ok(result) = result else {
                    let _ = this.update(cx, |this, _| this.activity_input = Value::Null);
                    continue;
                };
                previous = result["previous"].clone();
                if this.update_in(cx, |this, _, cx| {
                    if result["retry"] == true { this.activity_input = Value::Null; }
                    this.activity = result["terminals"].clone();
                    this.apply_activity();
                    let now = sessions::now_ms();
                    this.agent_notices.retain(|a| now.saturating_sub(a["created_ms"].as_u64().unwrap_or(0)) < if a["kind"] == "blocked" { 8000 } else { 5000 });
                    for alert in result["alerts"].as_array().into_iter().flatten() {
                        if alert["visual"].as_bool() == Some(true) {
                            this.agent_notices.retain(|a| a["address"] != alert["address"]);
                            let mut alert = alert.clone();
                            alert["created_ms"] = json!(now);
                            this.agent_notices.push(alert);
                            if this.agent_notices.len() > 4 { this.agent_notices.remove(0); }
                        }
                    }
                    cx.notify();
                }).is_err() { break; }
            }
        }));
        // Focus and window activation also change shared attention without a core event.
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            loop {
                cx.background_executor()
                    .timer(Duration::from_millis(200))
                    .await;
                if this
                    .update_in(cx, |this, window, cx| {
                        this.queue_activity(window);
                        let count = this.agent_notices.len();
                        this.agent_notices.retain(|a| {
                            sessions::now_ms().saturating_sub(a["created_ms"].as_u64().unwrap_or(0))
                                < if a["kind"] == "blocked" { 8000 } else { 5000 }
                        });
                        if count != this.agent_notices.len() {
                            cx.notify();
                        }
                    })
                    .is_err()
                {
                    break;
                }
            }
        }));
    }

    pub(super) fn queue_activity(&mut self, window: &Window) {
        let looking = if window.is_window_active() {
            match &self.route {
                Some(Route::Session(a)) => {
                    self.shown_tab(a).trim_start_matches("local:").to_owned()
                }
                _ => String::new(),
            }
        } else {
            String::new()
        };
        let (Some(events), Some(core), Some(send)) =
            (&mut self.events, &self.core, &self.activity_send)
        else {
            return;
        };
        let (observed, resynced) = events.take_seen();
        let input = json!({"terminals": events.live_for(&core.hello, &Value::Null)["terminals"], "looking": looking,
            "observed_seen": observed, "resynced": resynced});
        if input == self.activity_input {
            return;
        }
        self.activity_input = input.clone();
        let _ = send.try_send((input, core.clone()));
    }

    pub(super) fn apply_activity(&mut self) {
        for s in &mut self.sessions {
            let p = &self.activity[&s.address];
            if let Some(name) = p["program"]["name"].as_str() {
                s.agent = name.to_owned();
            }
            if p.is_object() {
                s.state = state(p["group_state"].as_str().unwrap_or_default());
            }
        }
    }

    pub(super) fn program_icon(&self, address: &str, fallback: &str, size: f32) -> AnyElement {
        let p = &self.activity[address]["program"];
        let Some(path) = p["icon"].as_str() else {
            return ui::agent_icon(fallback, size);
        };
        let path = SharedString::from(path.replacen("icons/", "programs/", 1));
        if p["tint"].as_bool().unwrap_or(true) {
            svg()
                .path(path)
                .size(px(size))
                .flex_none()
                .text_color(c("foreground"))
                .into_any_element()
        } else {
            img(path).size(px(size)).flex_none().into_any_element()
        }
    }

    pub(super) fn program_name(&self, address: &str, fallback: &str) -> String {
        self.activity[address]["program"]["name"]
            .as_str()
            .unwrap_or(fallback)
            .to_owned()
    }

    pub(super) fn activity_mark(&self, address: &str) -> AnyElement {
        self.activity_mark_for(address, "state")
    }
    pub(super) fn group_activity_mark(&self, address: &str) -> AnyElement {
        self.activity_mark_for(address, "group_state")
    }
    fn activity_mark_for(&self, address: &str, role: &str) -> AnyElement {
        let word = self.activity[address][role].as_str().unwrap_or_default();
        if word == "working" {
            return svg()
                .path("programs/activity-spinner.svg")
                .size(px(12.0))
                .text_color(c("working"))
                .with_animation(
                    ElementId::Name(format!("working-{address}").into()),
                    Animation::new(Duration::from_millis(900)).repeat(),
                    |icon, delta| {
                        icon.with_transformation(Transformation::rotate(percentage(delta)))
                    },
                )
                .into_any_element();
        }
        div()
            .size(px(6.0))
            .rounded_full()
            .when(word == "blocked" || word == "done", |d| d.bg(c(word)))
            .into_any_element()
    }

    pub(super) fn notification_stack(&self, cx: &Context<Self>) -> AnyElement {
        v_flex()
            .absolute()
            .right(px(16.0))
            .bottom(px(16.0))
            .gap(px(6.0))
            .children(self.agent_notices.iter().map(|alert| {
                let address = alert["address"].as_str().unwrap_or_default().to_owned();
                let group = alert["group"].as_str().unwrap_or_default().to_owned();
                let dismiss = address.clone();
                let kind = alert["kind"].as_str().unwrap_or("done");
                h_flex()
                    .id(SharedString::from(format!("agent-notice-{address}")))
                    .occlude()
                    .px(px(12.0))
                    .py(px(9.0))
                    .gap(px(8.0))
                    .rounded(px(8.0))
                    .bg(c("popover"))
                    .border_1()
                    .border_color(c("border"))
                    .text_size(px(12.0))
                    .text_color(c("foreground"))
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(move |this, _, window, cx| {
                            cx.stop_propagation();
                            this.agent_notices.retain(|a| a["address"] != address);
                            if address != group {
                                this.tab.insert(group.clone(), format!("local:{address}"));
                            }
                            this.go(Route::Session(group.clone()), window, cx);
                        }),
                    )
                    .child(self.program_icon(&dismiss, "", 14.0))
                    .child(format!(
                        "{} · {}",
                        alert["name"].as_str().unwrap_or_default(),
                        t(if kind == "blocked" {
                            "needs_you"
                        } else {
                            "done"
                        })
                    ))
                    .child(
                        ui::compact_icon_button(
                            SharedString::from(format!("dismiss-{dismiss}")),
                            "close",
                        )
                        .on_click(cx.listener(move |this, _, _, cx| {
                            cx.stop_propagation();
                            this.agent_notices.retain(|a| a["address"] != dismiss);
                            cx.notify();
                        })),
                    )
            }))
            .into_any_element()
    }
}
fn state(word: &str) -> State {
    match word {
        "blocked" => State::NeedsYou,
        "done" => State::Done,
        "working" => State::Working,
        "idle" => State::Idle,
        _ => State::Unknown,
    }
}
