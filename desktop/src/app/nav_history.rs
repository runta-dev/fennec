//! FEN-203: back and forward through the places visited, as in Slack. A
//! place is the route on screen and the thread open in it. Every render
//! notes the place, so any way of getting there (rail, cards, search,
//! links, shortcuts) adds to the history without each caller recording it.
use super::*;

/// How many places back are kept.
const LIMIT: usize = 100;

#[derive(Clone, PartialEq)]
pub(crate) struct Place {
    route: Route,
    thread: Option<u64>,
}

#[derive(Default)]
pub(crate) struct History {
    back: Vec<Place>,
    forward: Vec<Place>,
    current: Option<Place>,
}

impl History {
    /// The place now on screen. A new one moves the old one back and drops
    /// the places ahead, as a browser does.
    pub(crate) fn note(&mut self, place: Place) {
        if self.current.as_ref() == Some(&place) {
            return;
        }
        if let Some(old) = self.current.replace(place) {
            self.back.push(old);
            if self.back.len() > LIMIT {
                self.back.remove(0);
            }
        }
        self.forward.clear();
    }

    pub(crate) fn can_go_back(&self) -> bool {
        !self.back.is_empty()
    }

    pub(crate) fn can_go_forward(&self) -> bool {
        !self.forward.is_empty()
    }

    /// Step back (or forward) one place and return it; the place left is
    /// kept on the other side.
    fn step(&mut self, back: bool) -> Option<Place> {
        let (from, to) = if back {
            (&mut self.back, &mut self.forward)
        } else {
            (&mut self.forward, &mut self.back)
        };
        let place = from.pop()?;
        if let Some(old) = self.current.replace(place.clone()) {
            to.push(old);
        }
        Some(place)
    }

    pub(crate) fn clear(&mut self) {
        *self = Self::default();
    }
}

impl FennecWindow {
    pub(super) fn current_place(&self) -> Option<Place> {
        let route = self.route.clone()?;
        let thread = match route {
            Route::Project(_) => self.opening.or(self.open),
            _ => None,
        };
        Some(Place { route, thread })
    }

    /// Called from render: records where the window is now.
    pub(super) fn note_place(&mut self) {
        if let Some(place) = self.current_place() {
            self.history.note(place);
        }
    }

    /// ⌘[ and the title bar's back arrow (FEN-203); `back` false is ⌘] and
    /// the forward arrow.
    pub(super) fn go_history(&mut self, back: bool, window: &mut Window, cx: &mut Context<Self>) {
        // Leave the current place recorded before stepping off it.
        self.note_place();
        let Some(place) = self.history.step(back) else {
            return;
        };
        self.go(place.route.clone(), window, cx);
        if self.route.as_ref() != Some(&place.route) {
            return;
        }
        if let Route::Project(_) = place.route {
            match place.thread {
                Some(n) if self.opening.or(self.open) != Some(n) => self.open_request(n, window, cx),
                Some(_) => {}
                None => {
                    self.save_reply_draft(cx);
                    self.open = None;
                    self.opening = None;
                    self.thread = None;
                    self.tag = None;
                }
            }
        }
        cx.notify();
    }

    /// The back and forward arrows left of the title bar's search.
    pub(super) fn history_buttons(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let arrow = |id: &'static str, icon: &str, enabled: bool, back: bool| {
            if enabled {
                // The chrome's icon button: a faint square and brighter glyph on hover.
                ui::icon_button(id, icon)
                    .debug_selector(move || id.into())
                    .on_click(cx.listener(move |this, _, window, cx| this.go_history(back, window, cx)))
            } else {
                // Nowhere to go: the same metrics, dimmed, with no hover (as in Slack).
                div()
                    .id(id)
                    .debug_selector(move || id.into())
                    .size(px(28.0))
                    .flex_none()
                    .flex()
                    .items_center()
                    .justify_center()
                    .opacity(0.45)
                    .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                    .child(ui::icon(icon, 17.0, c("f_muted")))
            }
        };
        h_flex()
            .flex_none()
            .gap(px(2.0))
            .child(arrow("history-back", "arrow-left", self.history.can_go_back(), true))
            .child(arrow("history-forward", "arrow-right", self.history.can_go_forward(), false))
    }
}

#[cfg(test)]
mod tests {
    use super::{History, Place, Route, LIMIT};

    fn at(id: &str, thread: Option<u64>) -> Place {
        Place { route: Route::Project(id.into()), thread }
    }

    #[test]
    fn back_and_forward_walk_the_places_visited() {
        let mut h = History::default();
        h.note(at("a", None));
        assert!(!h.can_go_back());
        h.note(at("a", Some(1)));
        h.note(at("a", Some(1)));
        h.note(at("b", None));
        assert!(h.step(true) == Some(at("a", Some(1))));
        assert!(h.step(true) == Some(at("a", None)));
        assert!(h.step(true).is_none());
        assert!(h.can_go_forward());
        assert!(h.step(false) == Some(at("a", Some(1))));
        // Noting the place stepped to keeps what is ahead.
        h.note(at("a", Some(1)));
        assert!(h.can_go_forward());
        // A new place drops it.
        h.note(at("c", None));
        assert!(!h.can_go_forward());
        assert!(h.step(true) == Some(at("a", Some(1))));
    }

    #[test]
    fn history_keeps_a_bounded_number_of_places() {
        let mut h = History::default();
        for n in 0..(LIMIT as u64 + 20) {
            h.note(at("a", Some(n)));
        }
        assert_eq!(h.back.len(), LIMIT);
    }
}
