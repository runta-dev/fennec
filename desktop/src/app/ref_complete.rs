//! `#` completion in the New Request box (THI-209). Typing `#` shows the
//! queue's requests under the box. Digits after it narrow by number and other
//! text by title, the way GitHub suggests issues. ↑/↓ choose, Enter or Tab
//! insert `KEY-n `, which the board reads as a mention (a linked request),
//! and Esc hides the list until the next `#`.
use super::*;
use std::ops::Range;

/// Suggestions shown at once.
const SHOWN: usize = 6;
/// A longer run after `#` is prose, not a lookup.
const MAX_QUERY: usize = 40;

/// The `#query` the caret is in: the byte offset of its `#` and the text
/// typed after it. None when the `#` follows an ASCII word or `#`/`/` (`C#`, `##`,
/// `a/#b`), or a space or newline ends the query.
pub(super) fn ref_query(text: &str, cursor: usize) -> Option<(usize, &str)> {
    let before = text.get(..cursor)?;
    let hash = before.rfind('#')?;
    let query = &before[hash + 1..];
    if query.chars().count() > MAX_QUERY || query.chars().any(char::is_whitespace) {
        return None;
    }
    match before[..hash].chars().next_back() {
        Some(c) if c.is_ascii_alphanumeric() || matches!(c, '_' | '#' | '/' | '&' | '-') => None,
        _ => Some((hash, query)),
    }
}

/// The requests matching `query`: numbers starting with it first (an exact
/// number on top), then titles containing it, newest first in each group.
/// `THI-12` and `thi-12` read as `12`.
pub(super) fn ref_candidates<'a>(list: &'a [Request], key: &str, query: &str) -> Vec<&'a Request> {
    let q = query.to_lowercase();
    let prefix = format!("{}-", key.to_lowercase());
    let q = q.strip_prefix(&prefix).unwrap_or(&q);
    let rank = |r: &Request| -> Option<u8> {
        let n = r.number.to_string();
        if q.is_empty() {
            Some(2)
        } else if n == q {
            Some(0)
        } else if n.starts_with(q) {
            Some(1)
        } else if r.title.to_lowercase().contains(q) {
            Some(2)
        } else {
            None
        }
    };
    let mut rows: Vec<(u8, &Request)> = list.iter().filter_map(|r| Some((rank(r)?, r))).collect();
    rows.sort_by(|a, b| a.0.cmp(&b.0).then(b.1.number.cmp(&a.1.number)));
    rows.truncate(SHOWN);
    rows.into_iter().map(|(_, r)| r).collect()
}

/// What a picked suggestion becomes: `KEY-n`, plus a space unless one follows.
pub(super) fn ref_insert(key: &str, number: u64, after: &str) -> String {
    let space = if after.starts_with(char::is_whitespace) { "" } else { " " };
    format!("{key}-{number}{space}")
}

/// The open suggestion list.
pub(super) struct RefMenu {
    /// The `#query` text to replace.
    range: Range<usize>,
    key: String,
    rows: Vec<(u64, String, String)>,
}

impl FennecWindow {
    /// The list for the caret's `#query`, while the New Request box has focus.
    pub(super) fn ref_menu(&self, window: &Window, cx: &App) -> Option<RefMenu> {
        let state = self.composer.read(cx);
        if !state.focus_handle(cx).is_focused(window) {
            return None;
        }
        let range = state.selected_range();
        if !range.is_empty() {
            return None;
        }
        let text = state.value();
        let (hash, query) = ref_query(&text, range.end)?;
        if self.ref_closed == Some(hash) {
            return None;
        }
        let p = self
            .project_id()
            .and_then(|id| self.projects.iter().find(|p| p.id == id))?;
        let list = self.requests.get(&p.id).map(Vec::as_slice).unwrap_or_default();
        let rows: Vec<_> = ref_candidates(list, &p.key, query)
            .into_iter()
            .map(|r| (r.number, r.title.clone(), r.status.clone()))
            .collect();
        if rows.is_empty() {
            return None;
        }
        Some(RefMenu { range: hash..range.end, key: p.key.clone(), rows })
    }

    /// Replaces the `#query` with `KEY-n `.
    fn ref_pick_row(&mut self, ix: usize, window: &mut Window, cx: &mut Context<Self>) {
        let Some(menu) = self.ref_menu(window, cx) else {
            return;
        };
        let Some((number, ..)) = menu.rows.get(ix.min(menu.rows.len() - 1)) else {
            return;
        };
        let after = self.composer.read(cx).value()[menu.range.end..].to_string();
        let text = ref_insert(&menu.key, *number, &after);
        self.composer.update(cx, |s, cx| {
            s.set_selected_range(menu.range.clone(), cx);
            s.replace(text, window, cx);
        });
        self.ref_pick = 0;
        cx.notify();
    }

    /// Keys while the list is open; they go to the box otherwise. Typing in
    /// an input method's composition is left alone.
    fn ref_key(
        &mut self,
        key: RefKey,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> bool {
        let Some(menu) = self.ref_menu(window, cx) else {
            return false;
        };
        if self
            .composer
            .update(cx, |s, cx| s.marked_text_range(window, cx).is_some())
        {
            return false;
        }
        let n = menu.rows.len();
        match key {
            RefKey::Up => self.ref_pick = (self.ref_pick.min(n - 1) + n - 1) % n,
            RefKey::Down => self.ref_pick = (self.ref_pick.min(n - 1) + 1) % n,
            RefKey::Pick => self.ref_pick_row(self.ref_pick, window, cx),
            RefKey::Close => self.ref_closed = Some(menu.range.start),
        }
        cx.notify();
        true
    }

    /// The New Request text box, with the keys the list takes first.
    pub(super) fn composer_box(&self, cx: &mut Context<Self>) -> Div {
        use gpui_base::input::{Enter, Escape, IndentInline, MoveDown, MoveUp};
        let this = cx.entity().downgrade();
        let on = move |key: RefKey| {
            let this = this.clone();
            move |window: &mut Window, cx: &mut App| {
                let taken = this
                    .upgrade()
                    .is_some_and(|this| this.update(cx, |this, cx| this.ref_key(key, window, cx)));
                if taken {
                    cx.stop_propagation();
                }
            }
        };
        let (up, down, tab, esc, enter) = (
            on(RefKey::Up),
            on(RefKey::Down),
            on(RefKey::Pick),
            on(RefKey::Close),
            on(RefKey::Pick),
        );
        div()
            .w_full()
            .flex_none()
            .capture_action(move |_: &MoveUp, w, cx| up(w, cx))
            .capture_action(move |_: &MoveDown, w, cx| down(w, cx))
            .capture_action(move |_: &IndentInline, w, cx| tab(w, cx))
            .capture_action(move |_: &Escape, w, cx| esc(w, cx))
            .capture_action(move |e: &Enter, w, cx| {
                if !e.shift && !e.secondary {
                    enter(w, cx)
                }
            })
            .child(Textarea::new(&self.composer))
    }

    /// The suggestions under the New Request box.
    pub(super) fn ref_list(&self, window: &Window, cx: &mut Context<Self>) -> Option<AnyElement> {
        let menu = self.ref_menu(window, cx)?;
        let pick = self.ref_pick.min(menu.rows.len() - 1);
        let key = menu.key.clone();
        Some(
            deferred(
                v_flex()
                    .absolute()
                    .top(relative(1.0))
                    .left(px(0.0))
                    .mt(px(4.0))
                    .w(px(460.0))
                    .max_w(relative(1.0))
                    .p(px(4.0))
                    .rounded(px(9.0))
                    .bg(c("f_card"))
                    .border_1()
                    .border_color(c("f_field"))
                    .shadow_lg()
                    .occlude()
                    .children(menu.rows.into_iter().enumerate().map(|(ix, (n, title, status))| {
                        h_flex()
                            .id(SharedString::from(format!("ref-option-{n}")))
                            .h(px(30.0))
                            .px(px(8.0))
                            .gap(px(8.0))
                            .rounded(px(5.0))
                            .when(ix == pick, |d| d.bg(c("f_expanded")))
                            .hover(|s| s.bg(c("f_expanded")))
                            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.composer.read(cx).focus_handle(cx).focus(window, cx);
                                this.ref_pick_row(ix, window, cx)
                            }))
                            .child(
                                div()
                                    .flex_none()
                                    .text_size(px(12.0))
                                    .text_color(c("f_muted"))
                                    .child(format!("{key}-{n}")),
                            )
                            .child(
                                div()
                                    .flex_1()
                                    .min_w_0()
                                    .truncate()
                                    .text_size(px(12.5))
                                    .text_color(c("f_text"))
                                    .child(title),
                            )
                            .child(ui::status_pill(&status))
                    })),
            )
            .with_priority(1)
            .into_any_element(),
        )
    }
}

#[derive(Clone, Copy)]
enum RefKey {
    Up,
    Down,
    Pick,
    Close,
}

#[cfg(test)]
mod tests {
    use super::{ref_candidates, ref_insert, ref_query};
    use crate::board::Request;
    use std::prelude::v1::test;

    fn req(number: u64, title: &str) -> Request {
        Request { number, title: title.into(), ..Default::default() }
    }

    #[test]
    fn query_starts_at_a_free_hash() {
        assert_eq!(ref_query("#", 1), Some((0, "")));
        assert_eq!(ref_query("see #20", 7), Some((4, "20")));
        assert_eq!(ref_query("\u{89c1}#\u{8f93}\u{5165}", "\u{89c1}#\u{8f93}\u{5165}".len()), Some(("\u{89c1}".len(), "\u{8f93}\u{5165}")));
        assert_eq!(ref_query("(#12", 4), Some((1, "12")));
        assert_eq!(ref_query("C#", 2), None);
        assert_eq!(ref_query("##", 2), None);
        assert_eq!(ref_query("a/#b", 4), None);
        assert_eq!(ref_query("#12 done", 8), None);
        assert_eq!(ref_query("#12 done", 3), Some((0, "12")));
        assert_eq!(ref_query("#1\n2", 4), None);
        assert_eq!(ref_query("no hash", 7), None);
    }

    #[test]
    fn numbers_first_then_titles_newest_first() {
        let list = [req(2, "input box"), req(20, "x"), req(12, "y"), req(209, "#2 hint"), req(7, "z")];
        let nums = |q: &str| ref_candidates(&list, "THI", q).iter().map(|r| r.number).collect::<Vec<_>>();
        assert_eq!(nums("2"), vec![2, 209, 20]);
        assert_eq!(nums("20"), vec![20, 209]);
        assert_eq!(nums("THI-12"), vec![12]);
        assert_eq!(nums("thi-7"), vec![7]);
        assert_eq!(nums("INPUT"), vec![2]);
        assert_eq!(nums(""), vec![209, 20, 12, 7, 2]);
        assert!(nums("nothing").is_empty());
    }

    #[test]
    fn insert_adds_one_space() {
        assert_eq!(ref_insert("THI", 9, ""), "THI-9 ");
        assert_eq!(ref_insert("THI", 9, "x"), "THI-9 ");
        assert_eq!(ref_insert("THI", 9, " x"), "THI-9");
        assert_eq!(ref_insert("THI", 9, "\nx"), "THI-9");
    }
}
