//! While a request's agent works, its thread shows the agent's latest output:
//! a peek at its terminal through herdr-core (`terminal.read` on this core, or
//! `sessions.read` on the cloud computer, which runs `herdr-core read` there).
use super::*;
use serde_json::json;

/// Lines of the agent's output a thread shows.
const LINES: usize = 6;
/// The agent's input box and footer sit in these last lines of its screen.
const CHROME: usize = 8;

#[derive(Clone, Debug, Default, PartialEq)]
pub(super) struct Peek {
    /// The terminal it was read from: a peek is shown only for that one.
    pub address: String,
    pub lines: Vec<String>,
}

impl FennecWindow {
    /// The open thread's session while its agent works on it.
    pub(super) fn peek_target(&self) -> Option<&Session> {
        let r = self
            .thread
            .as_ref()
            .filter(|t| Some(t.number) == self.open)?;
        (r.status == "working").then(|| r.active_session(&self.sessions))?
    }

    /// Read the working agent's screen every few seconds while its thread is open.
    pub(super) fn follow_thread_peek(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            loop {
                let Ok(target) = this.update(cx, |this, _| {
                    this.peek_target().cloned().map(|s| (s, this.core.clone()))
                }) else {
                    break;
                };
                if let Some((session, core)) = target {
                    let address = session.address.clone();
                    let text = cx
                        .background_spawn(async move { read_screen(&session, core) })
                        .await;
                    let applied = this.update(cx, |this, cx| {
                        let Ok(text) = text else { return };
                        if this.peek_target().is_none_or(|s| s.address != address) {
                            return;
                        }
                        let peek = Peek {
                            address,
                            lines: snippet(&text, LINES),
                        };
                        if this.thread_peek.as_ref() != Some(&peek) {
                            this.thread_peek = Some(peek);
                            cx.notify();
                        }
                    });
                    if applied.is_err() {
                        break;
                    }
                }
                cx.background_executor()
                    .timer(Duration::from_millis(2500))
                    .await;
            }
        }));
    }

    /// The working agent's row at the end of the thread: what it says it is
    /// doing and the last lines it printed.
    pub(super) fn thread_peek_card(&self, session: &Session) -> AnyElement {
        let lines = self
            .thread_peek
            .as_ref()
            .filter(|p| p.address == session.address)
            .map(|p| p.lines.clone())
            .unwrap_or_default();
        h_flex()
            .id("thread-peek")
            .items_start()
            .gap(px(12.0))
            .py(px(10.0))
            .child(
                div()
                    .size(px(32.0))
                    .flex_none()
                    .rounded(px(9.0))
                    .bg(c("f_expanded"))
                    .border_1()
                    .border_color(c("f_line"))
                    .flex()
                    .items_center()
                    .justify_center()
                    .child(ui::agent_icon(&session.agent, 19.0)),
            )
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .gap(px(6.0))
                    .child(
                        h_flex()
                            .gap(px(6.0))
                            .min_w_0()
                            .child(
                                div()
                                    .flex_none()
                                    .text_size(px(13.0))
                                    .font_weight(FontWeight::MEDIUM)
                                    .text_color(c("f_text"))
                                    .child(sessions::agent_name(&session.agent).to_owned()),
                            )
                            .child(div().flex_none().size(px(6.0)).rounded_full().bg(c("ok")))
                            .child(
                                div()
                                    .min_w_0()
                                    .truncate()
                                    .text_size(px(11.5))
                                    .text_color(c("f_muted"))
                                    .child(if session.activity.is_empty() {
                                        t("working")
                                    } else {
                                        session.activity.clone()
                                    }),
                            ),
                    )
                    .when(!lines.is_empty(), |d| {
                        d.child(
                            v_flex()
                                .rounded(px(8.0))
                                .bg(c("f_card"))
                                .border_1()
                                .border_color(c("f_line"))
                                .px(px(10.0))
                                .py(px(7.0))
                                .font_family(crate::settings::get().font_family.clone())
                                .text_size(px(11.5))
                                .line_height(relative(1.45))
                                .text_color(c("f_muted"))
                                .children(lines.into_iter().map(|l| div().truncate().child(l))),
                        )
                    }),
            )
            .into_any_element()
    }
}

fn read_screen(session: &Session, core: Option<Arc<Core>>) -> anyhow::Result<String> {
    let screen = if let Some(address) = session.address.strip_prefix("cloud:") {
        let read = crate::control::call(
            "sessions.read",
            json!({"session_id": session.name, "address": address}),
        )?;
        read["screen"].clone()
    } else {
        let core = core.ok_or_else(|| anyhow::anyhow!("not connected"))?;
        core.call(
            "terminal.read",
            json!({"terminal": session.address, "lines": 0}),
        )?
    };
    Ok(screen["text"].as_str().unwrap_or_default().to_owned())
}

/// The last `max` printed lines of an agent's screen, above its input box.
pub(super) fn snippet(text: &str, max: usize) -> Vec<String> {
    let lines: Vec<&str> = text.lines().map(str::trim_end).collect();
    let mut end = lines.len();
    while end > 0 && lines[end - 1].trim().is_empty() {
        end -= 1;
    }
    // Agents draw their input box between rules at the bottom: cut at the topmost.
    if let Some(i) = (end.saturating_sub(CHROME)..end).find(|&i| is_rule(lines[i])) {
        end = i;
    }
    let mut out: Vec<String> = lines[..end]
        .iter()
        .rev()
        .filter(|l| !l.trim().is_empty() && !is_rule(l))
        .take(max)
        .map(|l| l.replace('\u{a0}', " "))
        .collect();
    out.reverse();
    out
}

fn is_rule(line: &str) -> bool {
    let line = line.trim();
    line.chars().count() >= 12 && line.chars().all(|c| "─━═╌┄-▔▁╭╮╰╯┌┐└┘".contains(c))
}

#[cfg(test)]
mod tests {
    use super::snippet;

    #[test]
    fn keeps_the_output_above_claude_codes_input_box() {
        let rule = "─".repeat(80);
        let screen = format!(
            "\n\n● Reading the queue thread code\n  ⎿ Read 40 lines\n\n✽ Undulating… (1m 40s)\n\n{rule}\n❯\u{a0}\n{rule}\n  ⏵⏵ bypass permissions on\n\n"
        );
        assert_eq!(
            snippet(&screen, 6),
            [
                "● Reading the queue thread code",
                "  ⎿ Read 40 lines",
                "✽ Undulating… (1m 40s)"
            ]
        );
    }

    #[test]
    fn keeps_the_last_lines_of_a_screen_without_an_input_box() {
        let screen = (1..=10)
            .map(|n| format!("line {n}"))
            .collect::<Vec<_>>()
            .join("\n");
        assert_eq!(snippet(&screen, 3), ["line 8", "line 9", "line 10"]);
    }

    #[test]
    fn a_rule_far_above_the_bottom_is_output() {
        let rule = "─".repeat(40);
        let mut lines = vec!["before".to_owned(), rule, "after".to_owned()];
        lines.extend((1..=10).map(|n| format!("out {n}")));
        assert_eq!(snippet(&lines.join("\n"), 2), ["out 9", "out 10"]);
        assert!(snippet("", 6).is_empty());
    }
}
