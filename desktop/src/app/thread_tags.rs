//! A request's tags (THI-133). A request can have several, say Bug and
//! Desktop: the list and the thread show a chip for each, the list's tag
//! filter matches any of them, and clicking the thread's chips opens a menu
//! that checks and unchecks the queue's tags. A request keeps at least one.
use super::*;
use crate::board::Tag;

/// The request's tags after toggling `id`, in the queue's tag order, or None
/// when that would leave it without a tag.
pub(super) fn toggled_tags(tags: &[Tag], current: &[String], id: &str) -> Option<Vec<String>> {
    let on = |tag: &str| (tag == id) != current.iter().any(|c| c == tag);
    let mut next: Vec<String> = tags
        .iter()
        .filter(|t| on(&t.id))
        .map(|t| t.id.clone())
        .collect();
    // Keep a tag the queue no longer has rather than drop it silently.
    next.extend(
        current
            .iter()
            .filter(|c| *c != id && !tags.iter().any(|t| &t.id == *c))
            .cloned(),
    );
    (!next.is_empty()).then_some(next)
}

impl FennecWindow {
    pub(super) fn toggle_tag_menu(&mut self, event: &ClickEvent, cx: &mut Context<Self>) {
        if self.menu == Some(Menu::ThreadTags) {
            self.menu = None;
        } else {
            self.menu = Some(Menu::ThreadTags);
            self.menu_anchor = point(event.position().x - px(20.0), event.position().y + px(18.0));
        }
        cx.notify();
    }

    /// Checks or unchecks tag `id` on the open thread: shown right away, then
    /// written to the board.
    fn toggle_thread_tag(&mut self, id: &str, window: &mut Window, cx: &mut Context<Self>) {
        let (Some(project), Some(n)) = (self.project_id(), self.open) else {
            return;
        };
        let Some(p) = self.projects.iter().find(|p| p.id == project) else {
            return;
        };
        let Some(current) = self
            .thread
            .as_ref()
            .filter(|t| t.number == n)
            .map(Request::tag_list)
        else {
            return;
        };
        let Some(next) = toggled_tags(&p.tags, &current, id) else {
            return;
        };
        if let Some(r) = self.thread.as_mut() {
            r.set_tags(&next);
        }
        if let Some(r) = self
            .requests
            .get_mut(&project)
            .and_then(|l| l.iter_mut().find(|r| r.number == n))
        {
            r.set_tags(&next);
        }
        cx.notify();
        let Some(scope) = self.board_scope.clone() else { return; };
        let worker_scope = scope.clone();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let done = cx
                .background_spawn(async move { worker_scope.run(|| { board::set_tags(&project, n, &next) }) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| { if this.board_scope.as_ref() != Some(&scope) { return; }
                if let Err(e) = done {
                    this.say(format!("{e:#}"), cx);
                }
                this.refresh_now(window, cx);
            });
        }));
    }

    /// The open thread's tag menu: the queue's tags, the request's checked.
    pub(super) fn tag_menu(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let p = self
            .project_id()
            .and_then(|id| self.projects.iter().find(|p| p.id == id))?;
        let current = self
            .thread
            .as_ref()
            .filter(|t| Some(t.number) == self.open)?
            .tag_list();
        let only = current.len() == 1;
        Some(
            div()
                .absolute()
                .top(self.menu_anchor.y)
                .left(self.menu_anchor.x.max(px(8.0)))
                .w(px(180.0))
                .p(px(5.0))
                .rounded(px(9.0))
                .bg(c("f_card"))
                .border_1()
                .border_color(c("f_field"))
                .shadow_lg()
                .on_mouse_down_out(cx.listener(|this, _, _, cx| {
                    this.menu = None;
                    cx.notify();
                }))
                .occlude()
                .children(p.tags.iter().map(|tag| {
                    let selected = current.contains(&tag.id);
                    // The last tag can't be unchecked.
                    let locked = selected && only;
                    let id = tag.id.clone();
                    h_flex()
                        .id(SharedString::from(format!("thread-tag-{}", tag.id)))
                        .h(px(29.0))
                        .px(px(9.0))
                        .gap(px(8.0))
                        .rounded(px(5.0))
                        .text_size(px(12.5))
                        .when(!locked, |d| {
                            d.hover(|s| s.bg(c("f_expanded"))).on_click(cx.listener(
                                move |this, _, window, cx| this.toggle_thread_tag(&id, window, cx),
                            ))
                        })
                        .child(
                            div()
                                .size(px(8.0))
                                .rounded_full()
                                .bg(ui::tag_color(&tag.id)),
                        )
                        .child(div().flex_1().child(tag.label.clone()))
                        .when(selected, |d| {
                            d.child(ui::icon(
                                "check",
                                13.0,
                                c(if locked { "f_muted" } else { "orange" }),
                            ))
                        })
                }))
                .into_any_element(),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::toggled_tags;
    use crate::board::Tag;

    fn tags() -> Vec<Tag> {
        ["bug", "feature", "desktop", "ios"]
            .map(|id| Tag {
                id: id.into(),
                label: id.into(),
            })
            .to_vec()
    }

    #[test]
    fn toggling_keeps_queue_order_and_at_least_one_tag() {
        let s = |v: &[&str]| v.iter().map(|x| x.to_string()).collect::<Vec<_>>();
        assert_eq!(
            toggled_tags(&tags(), &s(&["desktop"]), "bug"),
            Some(s(&["bug", "desktop"]))
        );
        assert_eq!(
            toggled_tags(&tags(), &s(&["bug", "desktop"]), "bug"),
            Some(s(&["desktop"]))
        );
        assert_eq!(toggled_tags(&tags(), &s(&["bug"]), "bug"), None);
        assert_eq!(
            toggled_tags(&tags(), &s(&["bug", "gone"]), "ios"),
            Some(s(&["bug", "ios", "gone"]))
        );
    }
}
