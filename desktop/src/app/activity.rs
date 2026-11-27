//! Native presentation and ordered delivery of core events to the local policy service.
use super::*;
use serde_json::{Value, json};

impl FennecWindow {
    pub(super) fn start_activity(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let (send, receive) = async_channel::unbounded::<(Value, Option<Arc<Core>>)>();
        self.activity_send = Some(send);
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let mut previous = Value::Null;
            while let Ok((mut input, core)) = receive.recv().await {
                input["previous"] = previous.clone();
                let result = cx.background_spawn(async move {
                    let records = input["terminals"].clone();
                    let runtime = input["cloud_runtime"].clone();
                    let mut result = crate::control::call("desktop.activity.update", input)?;
                    let writes = result["seen"].as_object().cloned().unwrap_or_default();
                    for (addr, turn) in writes {
                        let failed = if let Some(address) = addr.strip_prefix("cloud:") {
                            let session = records.as_array().into_iter().flatten().find(|r| r["address"] == addr).map(|r| r["labels"]["fennec.session"].clone()).unwrap_or_default();
                            crate::control::call("desktop.session.seen", json!({"address":address,"turn":turn,"session_id":session,"expected_runtime_id":runtime})).is_err()
                        } else {
                            core.as_ref().is_none_or(|source| source.call("terminal.label", json!({"terminal":addr,"labels":{"herdr.seen":turn.to_string()}})).is_err())
                        };
                        if failed {
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
                        if this.expire_thread_notices() {
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
        self.window_active = window.is_window_active();
        let looking = if window.is_window_active() {
            match &self.route {
                Some(Route::Session(a)) => {
                    self.shown_tab(a).trim_start_matches("local:").to_owned()
                }
                // An open queue thread shows its agent's reply (and peek), so
                // opening the thread counts as looking at that agent, the same
                // as opening its session: a finished agent goes done → idle.
                Some(Route::Project(_)) => self
                    .thread_looking_session()
                    .map(|a| self.shown_tab(&a).trim_start_matches("local:").to_owned())
                    .unwrap_or_default(),
                _ => String::new(),
            }
        } else {
            String::new()
        };
        let Some(send) = &self.activity_send else {
            return;
        };
        let mut observed = HashMap::new();
        let mut resynced = false;
        if let Some(events) = &mut self.events {
            let (seen, reset) = events.take_seen();
            observed.extend(seen);
            resynced |= reset;
        }
        let mut quiet = Vec::new();
        if let Some(events) = &mut self.cloud_events {
            let (seen, reset) = events.take_seen();
            observed.extend(seen.into_iter().map(|(a, t)| (format!("cloud:{a}"), t)));
            resynced |= reset;
            if reset {
                quiet.push("cloud:");
            }
        }
        for terminal in self.cloud_listing["terminals"]
            .as_array()
            .into_iter()
            .flatten()
        {
            if let (Some(address), Some(seen)) = (
                terminal["address"].as_str(),
                terminal["labels"]["herdr.seen"]
                    .as_str()
                    .and_then(|s| s.parse::<u64>().ok()),
            ) {
                observed.insert(format!("cloud:{address}"), seen);
            }
        }
        let input = json!({"terminals": self.terminal_records(), "looking": looking,
            "observed_seen": observed, "resynced": resynced, "quiet_prefixes": quiet, "cloud_runtime": self.cloud_listing["runtime_id"]});
        if input == self.activity_input {
            return;
        }
        self.activity_input = input.clone();
        let _ = send.try_send((input, self.core.clone()));
    }

    pub(super) fn apply_activity(&mut self) {
        for s in &mut self.sessions {
            let p = &self.activity[s.address.strip_prefix("local:").unwrap_or(&s.address)];
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

    pub(super) fn terminal_title(&self, address: &str, fallback: &str) -> String {
        let address = address.strip_prefix("local:").unwrap_or(address);
        // Sidebar rows and tabs ask for a title on every frame (a working
        // spinner animates), so look the record up in place rather than
        // copying every terminal through `terminal_records`.
        let record = match address.strip_prefix("cloud:") {
            Some(raw) => self.cloud_events.as_ref().and_then(|e| e.live_terminal(raw)),
            None => self.events.as_ref().and_then(|e| e.live_terminal(address)),
        };
        record
            .and_then(sessions::agent_terminal_title)
            .unwrap_or_else(|| self.program_name(address, fallback))
    }

    pub(super) fn activity_mark(&self, address: &str) -> AnyElement {
        self.activity_mark_for(address, "state")
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
                    // One shared phase: every running spinner turns in step,
                    // however late its row appeared (THI-111).
                    |icon, _| icon.with_transformation(Transformation::rotate(percentage(spinner_phase()))),
                )
                .into_any_element();
        }
        div()
            .size(px(6.0))
            .rounded_full()
            .when(word == "blocked" || word == "done", |d| d.bg(c(word)))
            .into_any_element()
    }

    /// Queues a notice for each request of `project` whose status just
    /// became Needs you, In review or Failed (FEN-137). These replace the
    /// per-agent "Done" / "Needs you" toasts: the thread is what the person
    /// acts on. `before` is the list as last fetched; nothing is said about
    /// the first fetch, nor about the thread the person is looking at.
    pub(super) fn notice_thread_changes(
        &mut self,
        project: &str,
        before: Option<&[Request]>,
        after: &[Request],
        active: bool,
    ) {
        let Some(before) = before else { return };
        let key = self
            .projects
            .iter()
            .find(|p| p.id == project)
            .map(|p| p.key.clone())
            .unwrap_or_default();
        let now = sessions::now_ms();
        for r in thread_status_changes(before, after) {
            let looking = active
                && self.open == Some(r.number)
                && self.project_id().as_deref() == Some(project);
            if looking {
                continue;
            }
            self.thread_notices
                .retain(|n| !(n.project == project && n.number == r.number));
            self.thread_notices.push(ThreadNotice {
                project: project.to_owned(),
                number: r.number,
                reference: if key.is_empty() { format!("#{}", r.number) } else { format!("{key}-{}", r.number) },
                title: r.title.clone(),
                status: r.status.clone(),
                created_ms: now,
            });
            if self.thread_notices.len() > 4 {
                self.thread_notices.remove(0);
            }
            let status = r.status.clone();
            std::thread::spawn(move || {
                let _ = crate::control::call("desktop.notice.sound", json!({ "status": status }));
            });
        }
    }

    /// Drops notices shown long enough; true when one went.
    pub(super) fn expire_thread_notices(&mut self) -> bool {
        let now = sessions::now_ms();
        let count = self.thread_notices.len();
        self.thread_notices
            .retain(|n| now.saturating_sub(n.created_ms) < n.lifetime_ms());
        count != self.thread_notices.len()
    }

    pub(super) fn notification_stack(&self, cx: &Context<Self>) -> AnyElement {
        v_flex()
            .absolute()
            .right(px(16.0))
            .bottom(px(16.0))
            .gap(px(6.0))
            .children(self.thread_notices.iter().map(|notice| {
                let project = notice.project.clone();
                let number = notice.number;
                let dismiss = (project.clone(), number);
                h_flex()
                    .id(SharedString::from(format!("thread-notice-{project}-{number}")))
                    .occlude()
                    .max_w(px(380.0))
                    .px(px(12.0))
                    .py(px(9.0))
                    .gap(px(8.0))
                    .rounded(px(8.0))
                    .bg(c("popover"))
                    .border_1()
                    .border_color(c("border"))
                    .text_size(px(12.0))
                    .text_color(c("foreground"))
                    .cursor_pointer()
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(move |this, _, window, cx| {
                            cx.stop_propagation();
                            this.thread_notices
                                .retain(|n| !(n.project == project && n.number == number));
                            this.go_request(project.clone(), number, window, cx);
                        }),
                    )
                    .child(ui::status_pill(&notice.status).flex_none())
                    .child(
                        div()
                            .flex_none()
                            .text_color(c("f_muted"))
                            .child(notice.reference.clone()),
                    )
                    .child(
                        div()
                            .min_w_0()
                            .flex_1()
                            .overflow_hidden()
                            .text_ellipsis()
                            .whitespace_nowrap()
                            .child(notice.title.clone()),
                    )
                    .child(
                        ui::compact_icon_button(
                            SharedString::from(format!("dismiss-{}-{}", dismiss.0, dismiss.1)),
                            "close",
                        )
                        .flex_none()
                        .on_click(cx.listener(move |this, _, _, cx| {
                            cx.stop_propagation();
                            this.thread_notices
                                .retain(|n| !(n.project == dismiss.0 && n.number == dismiss.1));
                            cx.notify();
                        })),
                    )
            }))
            .into_any_element()
    }
}

/// A queue thread that now needs the person, is in review, or failed (FEN-137).
pub(super) struct ThreadNotice {
    project: String,
    number: u64,
    /// "FEN-137".
    reference: String,
    title: String,
    /// The request status: "you", "review" or "failed".
    status: String,
    created_ms: u64,
}

impl ThreadNotice {
    fn lifetime_ms(&self) -> u64 {
        if self.status == "review" { 6000 } else { 10000 }
    }
}

/// The request statuses worth telling the person about.
fn noticed(status: &str) -> bool {
    matches!(status, "you" | "review" | "failed")
}

/// Requests of `after` that moved into a noticed status since `before`.
/// A request new to the list counts only when it is not already there
/// (it was just filed, or the list was cut short before).
pub(super) fn thread_status_changes<'a>(before: &[Request], after: &'a [Request]) -> Vec<&'a Request> {
    after
        .iter()
        .filter(|r| noticed(&r.status))
        .filter(|r| {
            before
                .iter()
                .find(|b| b.number == r.number)
                .is_some_and(|b| b.status != r.status)
        })
        .collect()
}

pub(super) fn state(word: &str) -> State {
    match word {
        "blocked" => State::NeedsYou,
        "done" => State::Done,
        "working" => State::Working,
        "idle" => State::Idle,
        _ => State::Unknown,
    }
}

/// Where every activity spinner is in its 900 ms turn, from one shared clock.
fn spinner_phase() -> f32 {
    static START: std::sync::OnceLock<std::time::Instant> = std::sync::OnceLock::new();
    let elapsed = START.get_or_init(std::time::Instant::now).elapsed().as_millis();
    (elapsed % 900) as f32 / 900.0
}

#[cfg(test)]
mod notice_tests {
    use super::thread_status_changes;
    use crate::board::Request;

    fn req(n: u64, status: &str) -> Request {
        Request { number: n, status: status.into(), ..Default::default() }
    }

    #[test]
    fn only_moves_into_you_review_failed_are_noticed() {
        let before = [req(1, "working"), req(2, "working"), req(3, "you"), req(4, "queued"), req(5, "working")];
        let after = [req(1, "you"), req(2, "review"), req(3, "you"), req(4, "failed"), req(5, "resolved"), req(6, "you")];
        let got: Vec<u64> = thread_status_changes(&before, &after).iter().map(|r| r.number).collect();
        assert_eq!(got, vec![1, 2, 4]);
    }

    #[test]
    fn review_to_needs_you_is_noticed_again() {
        let after = [req(1, "you")];
        assert_eq!(thread_status_changes(&[req(1, "review")], &after).len(), 1);
    }
}
