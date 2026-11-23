//! Native confirmation for ending other shell tabs; terminal input never owns it.
use super::*;

impl FennecWindow {
    pub(super) fn confirm_close_other_tabs(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.close_others.is_some() {
            return;
        }
        let Some(Route::Session(root)) = &self.route else {
            return;
        };
        let shown = self.shown_tab(root);
        // Retain the group's agent/root and the selected tab. An empty target
        // set is a no-op, not a confirmation that can obscure a lone terminal.
        let targets: Vec<String> = self
            .tab_keys()
            .into_iter()
            .skip(1)
            .filter(|key| key != &shown)
            .collect();
        if targets.is_empty() {
            self.prefix = false;
            self.pending_focus = Some(shown);
            cx.notify();
            return;
        }
        self.prefix = false;
        self.close_others = Some(targets);
        let answer = window.prompt(
            PromptLevel::Warning,
            &t("close_other_tabs_confirm"),
            None,
            &[t("cancel").as_str(), t("close_other_tabs_action").as_str()],
            cx,
        );
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let confirmed = answer.await.ok() == Some(1);
            let _ = this.update_in(cx, |this, window, cx| {
                let targets = this.close_others.take().unwrap_or_default();
                if confirmed {
                    // Do not act on replacement tabs or a different group if
                    // a session disappeared while the native prompt was open.
                    let current: Vec<_> = this.tab_keys().into_iter().skip(1).collect();
                    let selected = match &this.route {
                        Some(Route::Session(root)) => Some(this.shown_tab(root)),
                        _ => None,
                    };
                    for key in targets {
                        if current.contains(&key) && selected.as_ref() != Some(&key) {
                            this.close_tab_key(key, window, cx);
                        }
                    }
                }
                if let Some(Route::Session(root)) = &this.route {
                    this.pending_focus = Some(this.shown_tab(root));
                }
                cx.notify();
            });
        }));
        cx.notify();
    }
}
