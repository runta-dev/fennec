//! FEN-145: the New Request box picks the agent, model and reasoning effort.
//! A pill in the box's bottom-left corner shows the choice; clicking it opens
//! a popover listing the agents ready on the cloud computer (`GET
//! /v1/agents`), the picked agent's models and the picked model's efforts.
//! This replaced the queue settings' Agents tab. The choice is kept per cloud
//! computer across restarts (local_state) and sent with every new request.

use super::FennecWindow;
use crate::board::{self, AgentOption, Choice, ModelOption};
use crate::i18n::t;
use crate::ui::{self, c};
use gpui::prelude::FluentBuilder as _;
use gpui::*;
use gpui_base::{h_flex, v_flex};
use std::time::{Duration, Instant};

/// The cloud's list is read again when the popover opens after this long.
const STALE: Duration = Duration::from_secs(60);

#[derive(Default)]
pub(crate) struct AgentPick {
    pub open: bool,
    /// The agents the cloud computer offers; `None` until the first answer.
    pub agents: Option<Vec<AgentOption>>,
    loaded: Option<Instant>,
    loading: bool,
    /// When a click outside closed the popover: the same click landing on
    /// the pill must not open it again.
    closed: Option<Instant>,
}

/// What an older cloud computer without `GET /v1/agents` gets: every agent,
/// each with only its default model.
pub(crate) fn fallback_agents() -> Vec<AgentOption> {
    board::AGENTS
        .iter()
        .map(|a| AgentOption {
            agent: a.to_string(),
            models: vec![ModelOption::default()],
        })
        .collect()
}

/// The choice made valid against the offered agents: an unknown agent falls
/// back to `default_agent` (or the first offered), an unknown model or effort
/// to the default.
pub(crate) fn normalize(choice: &Choice, agents: Option<&[AgentOption]>, default_agent: &str) -> Choice {
    let mut out = choice.clone();
    if out.agent.is_empty() {
        out.agent = default_agent.to_owned();
    }
    let Some(agents) = agents.filter(|a| !a.is_empty()) else {
        return out;
    };
    let agent = match agents.iter().find(|a| a.agent == out.agent) {
        Some(a) => a,
        None => {
            let a = agents
                .iter()
                .find(|a| a.agent == default_agent)
                .unwrap_or(&agents[0]);
            out = Choice { agent: a.agent.clone(), ..Default::default() };
            a
        }
    };
    let model = agent.models.iter().find(|m| m.id == out.model);
    if model.is_none() {
        out.model.clear();
    }
    let efforts = model
        .or_else(|| agent.models.iter().find(|m| m.id.is_empty()))
        .map(|m| m.efforts.as_slice())
        .unwrap_or_default();
    if !efforts.contains(&out.effort) {
        out.effort.clear();
    }
    out
}

pub(crate) fn effort_label(effort: &str) -> String {
    match effort {
        "" => t("effort_default"),
        "minimal" | "low" | "medium" | "high" | "xhigh" | "max" | "ultra" => {
            t(&format!("effort_{effort}"))
        }
        other => {
            let mut chars = other.chars();
            chars
                .next()
                .map(|f| f.to_uppercase().collect::<String>() + chars.as_str())
                .unwrap_or_default()
        }
    }
}

fn model_label(agent: Option<&AgentOption>, model: &str) -> String {
    if model.is_empty() {
        return t("default_model");
    }
    agent
        .and_then(|a| a.models.iter().find(|m| m.id == model))
        .map(|m| if m.name.is_empty() { m.id.clone() } else { m.name.clone() })
        .unwrap_or_else(|| model.to_owned())
}

impl FennecWindow {
    /// The agent a queue used before FEN-145: its first turned-on worker.
    fn queue_agent(&self) -> String {
        let id = self.project_id();
        self.projects
            .iter()
            .find(|p| Some(&p.id) == id.as_ref())
            .and_then(|p| p.workers.iter().find(|w| w.on))
            .map(|w| w.agent.clone())
            .unwrap_or_else(|| "claude".to_owned())
    }

    /// What the next new request asks for.
    pub(super) fn agent_choice(&self) -> Choice {
        normalize(
            &self.agent_choice,
            self.agent_pick.agents.as_deref(),
            &self.queue_agent(),
        )
    }

    /// Read the cloud's agents and models unless a recent answer is at hand.
    pub(super) fn load_agent_options(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let fresh = self.agent_pick.loaded.is_some_and(|at| at.elapsed() < STALE);
        if self.agent_pick.loading || fresh {
            return;
        }
        let Some(scope) = self.board_scope.clone() else {
            return;
        };
        self.agent_pick.loading = true;
        let worker_scope = scope.clone();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let listed = cx
                .background_spawn(async move { worker_scope.run(board::agents) })
                .await;
            let _ = this.update(cx, |this, cx| {
                if this.board_scope.as_ref() != Some(&scope) {
                    return;
                }
                this.agent_pick.loading = false;
                this.agent_pick.loaded = Some(Instant::now());
                this.agent_pick.agents = Some(match listed {
                    Ok(list) if !list.is_empty() => list,
                    Ok(_) => fallback_agents(),
                    Err(e) => {
                        tracing::info!("agent list unavailable, offering every agent: {e:#}");
                        fallback_agents()
                    }
                });
                cx.notify();
            });
        }));
    }

    fn pick(&mut self, choice: Choice, cx: &mut Context<Self>) {
        self.agent_choice = normalize(
            &choice,
            self.agent_pick.agents.as_deref(),
            &self.queue_agent(),
        );
        cx.notify();
    }

    /// The pill in the New Request box's bottom-left corner.
    pub(super) fn agent_pill(&self, cx: &mut Context<Self>) -> Stateful<Div> {
        let choice = self.agent_choice();
        let agents = self.agent_pick.agents.as_deref();
        let agent = agents.and_then(|a| a.iter().find(|a| a.agent == choice.agent));
        let mut label = if choice.model.is_empty() {
            crate::sessions::agent_name(&choice.agent).to_owned()
        } else {
            model_label(agent, &choice.model)
        };
        if !choice.effort.is_empty() {
            label = format!("{label} · {}", effort_label(&choice.effort));
        }
        let open = self.agent_pick.open;
        h_flex()
            .id("agent-pick")
            .debug_selector(|| "agent-pick".into())
            .absolute()
            .left(px(7.0))
            .bottom(px(6.0))
            .max_w(px(260.0))
            .h(px(24.0))
            .px(px(7.0))
            .gap(px(6.0))
            .rounded(px(6.0))
            .text_size(px(12.0))
            .text_color(c("f_muted"))
            .when(open, |d| d.bg(c("f_line")))
            .hover(|s| s.bg(c("f_line")).text_color(c("f_text")))
            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
            .on_click(cx.listener(|this, _, window, cx| {
                let just_closed = this
                    .agent_pick
                    .closed
                    .is_some_and(|at| at.elapsed() < Duration::from_millis(250));
                this.agent_pick.open = !this.agent_pick.open && !just_closed;
                if this.agent_pick.open {
                    this.load_agent_options(window, cx);
                }
                cx.notify();
            }))
            .child(ui::agent_icon(&choice.agent, 14.0))
            .child(div().min_w_0().truncate().child(label))
            .child(ui::icon("chevron", 12.0, c("f_muted")))
    }

    /// The popover under the New Request box, while the pill has it open.
    pub(super) fn agent_popover(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        if !self.agent_pick.open {
            return None;
        }
        let choice = self.agent_choice();
        let agents = self
            .agent_pick
            .agents
            .clone()
            .unwrap_or_else(fallback_agents);
        let agent = agents.iter().find(|a| a.agent == choice.agent).cloned();
        let models = agent.as_ref().map(|a| a.models.clone()).unwrap_or_default();
        let efforts = models
            .iter()
            .find(|m| m.id == choice.model)
            .map(|m| m.efforts.clone())
            .unwrap_or_default();
        let caption = |key: &str| {
            div()
                .px(px(8.0))
                .pt(px(6.0))
                .pb(px(3.0))
                .text_size(px(11.0))
                .font_weight(FontWeight::MEDIUM)
                .text_color(c("f_muted"))
                .child(t(key))
        };
        let row = |id: SharedString, selected: bool| {
            h_flex()
                .id(id)
                .h(px(28.0))
                .px(px(8.0))
                .gap(px(8.0))
                .rounded(px(5.0))
                .text_size(px(12.5))
                .text_color(c("f_text"))
                .hover(|s| s.bg(c("f_expanded")))
                .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                .when(selected, |d| d.font_weight(FontWeight::MEDIUM))
        };
        let check = |selected: bool| {
            div()
                .size(px(14.0))
                .flex_none()
                .when(selected, |d| d.child(ui::icon("check", 14.0, c("f_text"))))
        };
        let agent_rows = agents.iter().map(|a| {
            let selected = a.agent == choice.agent;
            let id = a.agent.clone();
            row(SharedString::from(format!("agent-pick-{}", a.agent)), selected)
                .on_click(cx.listener(move |this, _, _, cx| {
                    if this.agent_choice().agent != id {
                        this.pick(Choice { agent: id.clone(), ..Default::default() }, cx)
                    }
                }))
                .child(ui::agent_icon(&a.agent, 14.0))
                .child(div().flex_1().min_w_0().truncate().child(crate::sessions::agent_name(&a.agent).to_owned()))
                .child(check(selected))
        });
        let model_rows = models.iter().map(|m| {
            let selected = m.id == choice.model;
            let mut next = choice.clone();
            next.model = m.id.clone();
            row(SharedString::from(format!("model-pick-{}", m.id)), selected)
                .on_click(cx.listener(move |this, _, _, cx| this.pick(next.clone(), cx)))
                .child(div().flex_1().min_w_0().truncate().child(model_label(agent.as_ref(), &m.id)))
                .child(check(selected))
        });
        let effort_chips = std::iter::once(String::new())
            .chain(efforts.iter().cloned())
            .map(|e| {
                let selected = e == choice.effort;
                let mut next = choice.clone();
                next.effort = e.clone();
                div()
                    .id(SharedString::from(format!("effort-pick-{e}")))
                    .h(px(24.0))
                    .px(px(8.0))
                    .flex()
                    .items_center()
                    .rounded(px(6.0))
                    .border_1()
                    .text_size(px(12.0))
                    .when(selected, |d| d.border_color(c("f_text").opacity(0.5)).bg(c("f_expanded")).text_color(c("f_text")))
                    .when(!selected, |d| d.border_color(c("f_field")).text_color(c("f_muted")).hover(|s| s.bg(c("f_expanded"))))
                    .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                    .on_click(cx.listener(move |this, _, _, cx| this.pick(next.clone(), cx)))
                    .child(effort_label(&e))
            });
        let divider = || div().my(px(4.0)).h(px(1.0)).bg(c("f_line"));
        Some(
            deferred(
                v_flex()
                    .id("agent-popover")
                    .debug_selector(|| "agent-popover".into())
                    .absolute()
                    .top(relative(1.0))
                    .left(px(0.0))
                    .mt(px(4.0))
                    .w(px(280.0))
                    .max_h(px(440.0))
                    .overflow_y_scroll()
                    .p(px(4.0))
                    .rounded(px(9.0))
                    .bg(c("f_card"))
                    .border_1()
                    .border_color(c("f_field"))
                    .shadow_lg()
                    .occlude()
                    .on_mouse_down_out(cx.listener(|this, _, _, cx| {
                        this.agent_pick.open = false;
                        this.agent_pick.closed = Some(Instant::now());
                        cx.notify();
                    }))
                    .child(caption("pick_agent"))
                    .children(agent_rows)
                    .when(models.len() > 1, |d| {
                        d.child(divider()).child(caption("pick_model")).children(model_rows)
                    })
                    .when(!efforts.is_empty(), |d| {
                        d.child(divider()).child(caption("pick_effort")).child(
                            h_flex()
                                .flex_wrap()
                                .gap(px(5.0))
                                .px(px(8.0))
                                .pt(px(2.0))
                                .pb(px(6.0))
                                .children(effort_chips),
                        )
                    }),
            )
            .with_priority(1)
            .into_any_element(),
        )
    }
}

#[cfg(test)]
mod tests {
    // Not `super::*`: that brings in gpui's `test` macro, which shadows `#[test]`.
    use super::{effort_label, normalize};
    use crate::board::{AgentOption, Choice, ModelOption};

    fn agents() -> Vec<AgentOption> {
        vec![
            AgentOption {
                agent: "claude".into(),
                models: vec![
                    ModelOption { id: "".into(), name: "Default".into(), efforts: vec!["low".into(), "high".into()] },
                    ModelOption { id: "opus".into(), name: "Opus".into(), efforts: vec!["low".into(), "max".into()] },
                    ModelOption { id: "haiku".into(), name: "Haiku".into(), efforts: vec![] },
                ],
            },
            AgentOption { agent: "codex".into(), models: vec![ModelOption::default()] },
        ]
    }

    fn choice(agent: &str, model: &str, effort: &str) -> Choice {
        Choice { agent: agent.into(), model: model.into(), effort: effort.into() }
    }

    #[test]
    fn a_choice_falls_back_to_what_the_cloud_offers() {
        let list = agents();
        let list = Some(list.as_slice());
        assert_eq!(normalize(&choice("claude", "opus", "max"), list, "codex"), choice("claude", "opus", "max"));
        // Haiku has no efforts; the old effort is dropped.
        assert_eq!(normalize(&choice("claude", "haiku", "max"), list, "codex"), choice("claude", "haiku", ""));
        // Default model: the default's efforts.
        assert_eq!(normalize(&choice("claude", "", "high"), list, "codex"), choice("claude", "", "high"));
        assert_eq!(normalize(&choice("claude", "", "max"), list, "codex"), choice("claude", "", ""));
        // Unknown model, unknown agent, nothing picked yet.
        assert_eq!(normalize(&choice("claude", "gone", "low"), list, "codex"), choice("claude", "", "low"));
        assert_eq!(normalize(&choice("pi", "x", "low"), list, "codex"), choice("codex", "", ""));
        assert_eq!(normalize(&Choice::default(), list, "claude"), choice("claude", "", ""));
        // Before the cloud answers the choice stands as saved.
        assert_eq!(normalize(&choice("pi", "x", "low"), None, "codex"), choice("pi", "x", "low"));
    }

    #[test]
    fn efforts_have_readable_labels() {
        assert_eq!(effort_label("turbo"), "Turbo");
        assert!(!effort_label("xhigh").contains("effort_"));
    }
}
