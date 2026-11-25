//! An agent asking a question in its session (THI-176): the card's preview
//! line says what it asks, and the open thread ends with an "Agent is asking
//! you" card listing the question and its options. Picking an option (or
//! typing in the reply box) answers it: the board stores the answer and the
//! scheduler running the session types it there, so no new run starts.
use super::*;
use crate::board::Question;

/// The card's preview line for an asking request: the question and its
/// options, or the command a permission prompt wants to run.
pub(super) fn question_preview(q: Option<&Question>) -> String {
    let Some(q) = q else {
        return t("asking");
    };
    if q.permission() {
        return tf("asking_permission_preview", &[("command", q.command())]);
    }
    if let Some(first) = q.items.first() {
        let options = first
            .options
            .iter()
            .map(|o| o.label.as_str())
            .collect::<Vec<_>>()
            .join(" / ");
        let mut line = if options.is_empty() {
            tf("asking_question_preview", &[("question", &first.question)])
        } else {
            tf(
                "asking_question_options_preview",
                &[("question", &first.question), ("options", &options)],
            )
        };
        if q.items.len() > 1 {
            line.push_str(&format!(" · +{}", q.items.len() - 1));
        }
        return line;
    }
    if !q.text.is_empty() {
        return tf("asking_question_preview", &[("question", &q.text)]);
    }
    t("asking")
}

/// Chosen options per question, for the picks a click leaves.
pub(super) fn toggle_pick(picks: &mut Vec<Vec<usize>>, item: usize, option: usize, multi: bool) {
    if picks.len() <= item {
        picks.resize(item + 1, Vec::new());
    }
    let chosen = &mut picks[item];
    if multi {
        if let Some(i) = chosen.iter().position(|&k| k == option) {
            chosen.remove(i);
        } else {
            chosen.push(option);
            chosen.sort_unstable();
        }
    } else {
        *chosen = vec![option];
    }
}

/// A single single-select question is answered by its first click.
pub(super) fn answers_on_click(q: &Question) -> bool {
    q.items.len() == 1 && !q.items[0].multi_select
}

/// Whether every question has a pick.
pub(super) fn complete(q: &Question, picks: &[Vec<usize>]) -> bool {
    !q.items.is_empty() && (0..q.items.len()).all(|i| picks.get(i).is_some_and(|p| !p.is_empty()))
}

impl FennecWindow {
    pub(super) fn question_key(&self, n: u64, q: &Question) -> String {
        format!("{}:{n}:{}", self.project_id().unwrap_or_default(), q.id)
    }

    /// Sends an answer for the open thread's question. `text` comes from the
    /// reply box; it answers every question without a pick, or tells a denied
    /// permission prompt what to do instead.
    pub(super) fn submit_answer(
        &mut self,
        picks: Vec<Vec<usize>>,
        allow: Option<bool>,
        text: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let (Some(project), Some(n)) = (self.project_id(), self.open) else {
            return;
        };
        let Some(q) = self.thread.as_ref().and_then(|r| r.answerable()).cloned() else {
            return;
        };
        if self.answering {
            return;
        }
        self.answering = true;
        let me = self.me.clone();
        let origin = project.clone();
        let key = self.question_key(n, &q);
        cx.notify();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let sent = cx
                .background_spawn(async move {
                    board::answer(&project, n, &me, &q.id, &picks, allow, &text)
                })
                .await;
            let _ = this.update_in(cx, |this, window, cx| {
                this.answering = false;
                let here = this.project_id().as_deref() == Some(&origin) && this.open == Some(n);
                match sent {
                    Ok(value) => {
                        this.question_picks.remove(&key);
                        if let Ok(mut updated) = serde_json::from_value::<Request>(value)
                            && updated.number == n
                        {
                            if let Some(old) = this.thread_cache.get(&(origin.clone(), n)) {
                                updated.related = old.related.clone();
                            }
                            if here {
                                if let Some(old) = &this.thread {
                                    this.fresh_messages
                                        .extend(pending_reply::grown_keys(old, &updated));
                                }
                                this.scroll_thread_to_bottom();
                                this.thread = Some(updated.clone());
                            }
                            this.thread_cache.insert((origin.clone(), n), updated);
                        }
                        if here {
                            this.refresh_now(window, cx);
                        }
                    }
                    Err(e) => this.say(format!("{e:#}"), cx),
                }
                cx.notify();
            });
        }));
    }

    /// The reply box answers the open thread's question when there is one to
    /// answer (THI-176); true when it took the text.
    pub(super) fn reply_answers(
        &mut self,
        text: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> bool {
        let Some(n) = self.open else {
            return false;
        };
        let Some(q) = self.thread.as_ref().and_then(|r| r.answerable()).cloned() else {
            return false;
        };
        if text.trim().is_empty() {
            return false;
        }
        let picks = self
            .question_picks
            .get(&self.question_key(n, &q))
            .cloned()
            .unwrap_or_default();
        let allow = q.permission().then_some(false);
        self.reply.update(cx, |s, cx| s.set_value("", window, cx));
        self.submit_answer(picks, allow, text.trim().to_owned(), window, cx);
        true
    }

    /// The "Agent is asking you" card at the end of an asking thread.
    pub(super) fn thread_question_card(
        &self,
        r: &Request,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        let q = r.asking()?.clone();
        let answered = r.answer.as_ref().is_some_and(|a| a.question == q.id);
        let answerable = q.answerable() && !answered;
        let key = self.question_key(r.number, &q);
        let picks = self.question_picks.get(&key).cloned().unwrap_or_default();
        let busy = self.answering;
        let mut body = v_flex().gap(px(10.0)).min_w_0();
        if q.permission() {
            body = body.child(
                div()
                    .rounded(px(8.0))
                    .bg(c("f_card"))
                    .border_1()
                    .border_color(c("f_line"))
                    .px(px(10.0))
                    .py(px(7.0))
                    .font_family(crate::settings::get().font_family.clone())
                    .text_size(px(12.0))
                    .text_color(c("f_text"))
                    .child(q.command().to_owned()),
            );
        } else if q.items.is_empty() {
            body = body.child(
                div()
                    .text_size(px(13.5))
                    .text_color(c("f_text"))
                    .child(q.text.clone()),
            );
        }
        for (i, item) in q.items.iter().enumerate() {
            let mut block = v_flex().gap(px(6.0)).min_w_0().child(
                h_flex()
                    .gap(px(6.0))
                    .min_w_0()
                    .when(!item.header.is_empty(), |d| {
                        d.child(
                            div()
                                .flex_none()
                                .px(px(6.0))
                                .rounded(px(5.0))
                                .bg(c("f_expanded"))
                                .text_size(px(11.0))
                                .text_color(c("f_muted"))
                                .child(item.header.clone()),
                        )
                    })
                    .child(
                        div()
                            .min_w_0()
                            .text_size(px(13.5))
                            .font_weight(FontWeight::MEDIUM)
                            .text_color(c("f_text"))
                            .child(item.question.clone()),
                    )
                    .when(item.multi_select, |d| {
                        d.child(
                            div()
                                .flex_none()
                                .text_size(px(11.0))
                                .text_color(c("f_muted"))
                                .child(t("asking_multi")),
                        )
                    }),
            );
            for (k, option) in item.options.iter().enumerate() {
                let chosen = picks.get(i).is_some_and(|p| p.contains(&k));
                let multi = item.multi_select;
                let row = h_flex()
                    .id(SharedString::from(format!("question-{i}-{k}")))
                    .items_start()
                    .gap(px(8.0))
                    .px(px(10.0))
                    .py(px(6.0))
                    .rounded(px(8.0))
                    .border_1()
                    .border_color(if chosen {
                        c("orange").opacity(0.6)
                    } else {
                        c("f_line")
                    })
                    .bg(if chosen { c("tint") } else { c("f_card") })
                    .when(answerable && !busy, |d| {
                        d.cursor_pointer().hover(|s| s.bg(c("f_expanded")))
                    })
                    .when(!answerable, |d| d.opacity(0.6))
                    .child(
                        div()
                            .flex_none()
                            .mt(px(3.0))
                            .size(px(12.0))
                            .when(multi, |d| d.rounded(px(3.0)))
                            .when(!multi, |d| d.rounded_full())
                            .border_1()
                            .border_color(if chosen { c("orange") } else { c("f_field") })
                            .when(chosen, |d| d.bg(c("orange"))),
                    )
                    .child(
                        v_flex()
                            .min_w_0()
                            .child(
                                div()
                                    .text_size(px(13.0))
                                    .text_color(c("f_text"))
                                    .child(option.label.clone()),
                            )
                            .when(!option.description.is_empty(), |d| {
                                d.child(
                                    div()
                                        .text_size(px(11.5))
                                        .text_color(c("f_muted"))
                                        .child(option.description.clone()),
                                )
                            }),
                    )
                    .when(answerable && !busy, |d| {
                        let q = q.clone();
                        let key = key.clone();
                        d.on_click(cx.listener(move |this, _, window, cx| {
                            let picks = this.question_picks.entry(key.clone()).or_default();
                            toggle_pick(picks, i, k, multi);
                            if answers_on_click(&q) {
                                let picks = picks.clone();
                                this.submit_answer(picks, None, String::new(), window, cx);
                            }
                            cx.notify();
                        }))
                    });
                block = block.child(row);
            }
            body = body.child(block);
        }
        let actions = if !answerable {
            None
        } else if q.permission() {
            Some(
                h_flex()
                    .gap(px(8.0))
                    .child(
                        primary_button("question-allow", t("asking_allow")).when(!busy, |d| {
                            d.cursor_pointer()
                                .on_click(cx.listener(|this, _, window, cx| {
                                    this.submit_answer(
                                        Vec::new(),
                                        Some(true),
                                        String::new(),
                                        window,
                                        cx,
                                    )
                                }))
                        }),
                    )
                    .child(
                        secondary_button("question-deny", t("asking_deny"), false).when(
                            !busy,
                            |d| {
                                d.on_click(cx.listener(|this, _, window, cx| {
                                    this.submit_answer(
                                        Vec::new(),
                                        Some(false),
                                        String::new(),
                                        window,
                                        cx,
                                    )
                                }))
                            },
                        ),
                    )
                    .into_any_element(),
            )
        } else if !answers_on_click(&q) {
            let ready = complete(&q, &picks) && !busy;
            Some(
                h_flex()
                    .child(
                        primary_button("question-submit", t("answer_question"))
                            .when(!ready, |d| d.opacity(0.4))
                            .when(ready, |d| {
                                let picks = picks.clone();
                                d.cursor_pointer().on_click(cx.listener(
                                    move |this, _, window, cx| {
                                        this.submit_answer(
                                            picks.clone(),
                                            None,
                                            String::new(),
                                            window,
                                            cx,
                                        )
                                    },
                                ))
                            }),
                    )
                    .into_any_element(),
            )
        } else {
            None
        };
        let hint = if answered || busy {
            t("asking_answered")
        } else if !q.answerable() {
            t("asking_in_session")
        } else if q.permission() {
            t("asking_permission_hint")
        } else if q.items.iter().any(|i| i.multi_select) {
            t("asking_reply_hint_multi")
        } else {
            t("asking_reply_hint")
        };
        Some(
            h_flex()
                .id("thread-question")
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
                        .child(ui::agent_icon(&r.agent, 19.0)),
                )
                .child(
                    v_flex()
                        .flex_1()
                        .min_w_0()
                        .gap(px(8.0))
                        .p(px(12.0))
                        .rounded(px(10.0))
                        .bg(c("tint"))
                        .border_1()
                        .border_color(c("orange").opacity(0.35))
                        .child(
                            h_flex()
                                .gap(px(6.0))
                                .child(
                                    div()
                                        .flex_none()
                                        .size(px(6.0))
                                        .rounded_full()
                                        .bg(c("orange")),
                                )
                                .child(
                                    div()
                                        .text_size(px(13.0))
                                        .font_weight(FontWeight::MEDIUM)
                                        .text_color(c("f_text"))
                                        .child(if q.permission() {
                                            t("asking_permission_title")
                                        } else {
                                            t("asking_title")
                                        }),
                                ),
                        )
                        .child(body)
                        .when_some(actions, |d, a| d.child(a))
                        .child(
                            div()
                                .text_size(px(11.5))
                                .text_color(c("f_muted"))
                                .child(hint),
                        ),
                )
                .into_any_element(),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    // An explicit import beats the glob: `gpui::*` brings gpui's own `test`, which expands to `#[test]` again forever.
    use std::prelude::v1::test;

    fn q(items: serde_json::Value) -> Question {
        serde_json::from_value(
            serde_json::json!({"id":"q","app":"claude-code","kind":"question","items":items}),
        )
        .unwrap()
    }

    #[test]
    fn picks_toggle_by_kind() {
        let mut picks = Vec::new();
        toggle_pick(&mut picks, 1, 2, true);
        toggle_pick(&mut picks, 1, 0, true);
        assert_eq!(picks, vec![vec![], vec![0, 2]]);
        toggle_pick(&mut picks, 1, 2, true);
        assert_eq!(picks[1], vec![0]);
        toggle_pick(&mut picks, 0, 1, false);
        toggle_pick(&mut picks, 0, 0, false);
        assert_eq!(picks[0], vec![0]);
    }

    #[test]
    fn one_click_answers_only_a_single_choice() {
        let single = q(serde_json::json!([{"question":"A?","options":[{"label":"x"}]}]));
        let multi =
            q(serde_json::json!([{"question":"A?","multi_select":true,"options":[{"label":"x"}]}]));
        let two = q(serde_json::json!([{"question":"A?"},{"question":"B?"}]));
        assert!(answers_on_click(&single));
        assert!(!answers_on_click(&multi) && !answers_on_click(&two));
        assert!(!complete(&two, &[vec![0]]) && complete(&two, &[vec![0], vec![1]]));
    }
}
