//! A queue thread whose agent hasn't started yet ends with a card that says so:
//! its place in line and how many agents are busy, or that the agent is
//! starting. Without it a new request's thread is just the user's message,
//! which reads as a rendering bug (THI-95).
use super::*;

/// Where a queued request stands, by the board's own start rule
/// (`Store.Claim` in internal/board/store.go): the first queued request in
/// list order starts once the queue isn't paused and fewer than `limit`
/// requests are working (or unknown).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Wait {
    Paused,
    /// `ahead` queued requests start before it; `working` of `limit` slots are taken.
    InLine {
        ahead: usize,
        working: usize,
        limit: usize,
    },
}

pub(super) fn queue_wait(list: &[Request], number: u64, limit: usize, paused: bool) -> Wait {
    if paused {
        return Wait::Paused;
    }
    let working = list
        .iter()
        .filter(|r| matches!(r.status.as_str(), "working" | "unknown"))
        .count();
    let ahead = list
        .iter()
        .take_while(|r| r.number != number)
        .filter(|r| r.status == "queued")
        .count();
    Wait::InLine {
        ahead,
        working,
        limit: limit.max(1),
    }
}

impl FennecWindow {
    /// The card ending a thread whose agent isn't on screen yet: waiting in
    /// the queue, or dispatched and still starting. None otherwise.
    pub(super) fn thread_wait_card(&self, p: &Project, r: &Request) -> Option<AnyElement> {
        let starting = matches!(r.status.as_str(), "working" | "unknown")
            && r.active_session(&self.sessions).is_none()
            && r.runs.last().is_some_and(|run| run.state == "starting");
        if r.status != "queued" && !starting {
            return None;
        }
        // The agent that will take it: the dispatched one, else the queue's first worker.
        let agent = Some(r.agent.clone())
            .filter(|a| !a.is_empty() && starting)
            .or_else(|| p.workers.iter().find(|w| w.on).map(|w| w.agent.clone()));
        let (title, detail, hint) = if starting {
            let name = agent
                .as_deref()
                .map(|a| sessions::agent_name(a).to_owned())
                .unwrap_or_default();
            (name, t("queued_starting"), None)
        } else {
            let list = self.requests.get(&p.id).map(Vec::as_slice).unwrap_or_default();
            match queue_wait(list, r.number, p.queue, p.paused) {
                Wait::Paused => (
                    t("queued_wait"),
                    t("queued_paused"),
                    Some(t("queued_hint_paused")),
                ),
                Wait::InLine {
                    ahead,
                    working,
                    limit,
                } => {
                    let place = if ahead == 0 {
                        t("queued_next")
                    } else {
                        tf("queued_ahead", &[("n", &ahead.to_string())])
                    };
                    let busy = tf(
                        "queued_busy",
                        &[
                            ("working", &working.min(limit).to_string()),
                            ("limit", &limit.to_string()),
                        ],
                    );
                    (
                        t("queued_wait"),
                        format!("{place} · {busy}"),
                        Some(t("queued_hint")),
                    )
                }
            }
        };
        Some(
            h_flex()
                .id("thread-wait")
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
                        .child(match (&agent, starting) {
                            (Some(a), true) => ui::agent_icon(a, 19.0),
                            _ => ui::icon("clock", 16.0, c("f_muted")).into_any_element(),
                        }),
                )
                .child(
                    v_flex()
                        .flex_1()
                        .min_w_0()
                        .gap(px(3.0))
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
                                        .child(title),
                                )
                                .child(
                                    div()
                                        .flex_none()
                                        .size(px(6.0))
                                        .rounded_full()
                                        .bg(if starting { c("ok") } else { c("f_muted") }),
                                )
                                .child(
                                    div()
                                        .min_w_0()
                                        .truncate()
                                        .text_size(px(11.5))
                                        .text_color(c("f_muted"))
                                        .child(detail),
                                ),
                        )
                        .when_some(hint, |d, hint| {
                            d.child(
                                h_flex()
                                    .gap(px(8.0))
                                    .min_w_0()
                                    .when_some(agent.filter(|_| !starting), |d, a| {
                                        d.child(ui::agent_icon(&a, 14.0))
                                    })
                                    .child(
                                        div()
                                            .min_w_0()
                                            .text_size(px(12.5))
                                            .text_color(c("f_muted"))
                                            .child(hint),
                                    ),
                            )
                        }),
                )
                .into_any_element(),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::{Wait, queue_wait};
    use crate::board::Request;

    fn req(number: u64, status: &str) -> Request {
        Request {
            number,
            status: status.into(),
            ..Default::default()
        }
    }

    #[test]
    fn counts_queued_ahead_and_busy_slots() {
        let list = [
            req(1, "working"),
            req(2, "unknown"),
            req(3, "you"),
            req(4, "queued"),
            req(5, "resolved"),
            req(6, "queued"),
        ];
        assert_eq!(
            queue_wait(&list, 6, 3, false),
            Wait::InLine {
                ahead: 1,
                working: 2,
                limit: 3
            }
        );
        assert_eq!(
            queue_wait(&list, 4, 3, false),
            Wait::InLine {
                ahead: 0,
                working: 2,
                limit: 3
            }
        );
    }

    #[test]
    fn paused_wins() {
        assert_eq!(queue_wait(&[req(1, "queued")], 1, 3, true), Wait::Paused);
    }
}
