//! Queue cards glide to their new place when a request's status moves them
//! (queued → working → needs you / in review, ...), instead of jumping there,
//! and newly listed cards fade in.
//!
//! Cards differ in height (titles wrap, a reply line or the Run next button
//! comes and goes), so their places can't be predicted the way the sidebar's
//! fixed rows are. Each card instead goes through a [`Glided`] element that,
//! once layout is done, reports where the card actually sits; a card whose
//! place changed is drawn at its old place in that same frame and eases into
//! the new one. A [`origin`] marker at the top of the list goes first, so
//! places are measured from the list's top and scrolling never animates.

use gpui::{
    AnyElement, App, Bounds, Element, ElementId, GlobalElementId, InspectorElementId, IntoElement,
    LayoutId, Pixels, Styled as _, Window, point, px,
};
use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;
use std::time::{Duration, Instant};

/// How long a move takes (as the sidebar's rows).
pub const SECONDS: f32 = 0.28;
/// How long a new card takes to fade in.
pub const FADE_SECONDS: f32 = 0.2;

pub type Shared = Rc<RefCell<Glide>>;

#[derive(Default)]
pub struct Glide {
    /// What the list shows (project, tab, filters). When it or the list's
    /// width changes, everything is placed at once.
    scope: String,
    width: f32,
    /// The window y of the list's top in this frame.
    origin: f32,
    /// The instant this frame is drawn at.
    now: Option<Instant>,
    /// Whether this frame already asked for the next one.
    requested: bool,
    /// Each item's place in the last drawn frame, from the list's top.
    placed: HashMap<String, f32>,
    /// Each item's place in this frame.
    current: HashMap<String, f32>,
    /// Running moves: the offset the item started from, and when.
    moves: HashMap<String, (f32, Instant)>,
    /// Running fade-ins: when the item first appeared.
    appeared: HashMap<String, Instant>,
}

impl Glide {
    /// Forgets every place, so the list is placed at once the next time it is
    /// drawn (it was not shown, and anything may have changed meanwhile).
    pub fn reset(&mut self) {
        *self = Self::default();
    }

    /// While rendering: how opaque `key` is drawn. An item that was not in the
    /// last drawn frame of the same list fades in.
    pub fn appear(&mut self, scope: &str, key: &str, now: Instant) -> f32 {
        let known = scope != self.scope
            || self.current.is_empty()
            || self.current.contains_key(key);
        if !known {
            self.appeared.entry(key.to_owned()).or_insert(now);
        }
        let Some(&start) = self.appeared.get(key) else {
            return 1.0;
        };
        let t = now.duration_since(start).as_secs_f32() / FADE_SECONDS;
        if t >= 1.0 {
            self.appeared.remove(key);
            return 1.0;
        }
        ease_out(t)
    }

    /// Whether any fade-in is still running.
    pub fn fading(&self) -> bool {
        !self.appeared.is_empty()
    }

    /// Starts a drawn frame of the list showing `scope`, whose top is at `origin`.
    fn begin(&mut self, scope: &str, width: f32, origin: f32, now: Instant) {
        if scope != self.scope || width != self.width {
            self.scope = scope.to_owned();
            self.width = width;
            self.placed.clear();
            self.current.clear();
            self.moves.clear();
            self.appeared.clear();
        } else {
            self.placed = std::mem::take(&mut self.current);
        }
        self.origin = origin;
        self.now = Some(now);
        self.requested = false;
    }

    /// Records that `key` is laid out at window y `y` in this frame, starting a
    /// move if its place changed, and returns how far from there it is drawn.
    fn place(&mut self, key: &str, y: f32) -> f32 {
        let now = self.now.unwrap_or_else(Instant::now);
        let y = y - self.origin;
        if let Some(&before) = self.placed.get(key)
            && (before - y).abs() > 0.5
        {
            // Continue from where it is drawn, even if a move was under way.
            let from = before + self.offset(key, now) - y;
            self.moves.insert(key.to_owned(), (from, now));
        }
        self.current.insert(key.to_owned(), y);
        let offset = self.offset(key, now);
        if offset == 0.0 {
            self.moves.remove(key);
        }
        offset
    }

    /// How far `key` is drawn from its layout place at `now`.
    fn offset(&self, key: &str, now: Instant) -> f32 {
        let Some(&(from, start)) = self.moves.get(key) else {
            return 0.0;
        };
        let elapsed = now.duration_since(start);
        if elapsed >= Duration::from_secs_f32(SECONDS) {
            return 0.0;
        }
        from * (1.0 - ease_out(elapsed.as_secs_f32() / SECONDS))
    }
}

/// Ease out (cubic): quick to respond, gentle to land.
fn ease_out(t: f32) -> f32 {
    1.0 - (1.0 - t.clamp(0.0, 1.0)).powi(3)
}

/// The marker at the top of the list: place it before the glided items.
pub fn origin(glide: &Shared, scope: String) -> Origin {
    Origin {
        glide: glide.clone(),
        scope,
        child: gpui::div().w_full().h(px(0.0)).into_any_element(),
    }
}

/// `child`, drawn gliding to its place under `key`.
pub fn glided(glide: &Shared, key: String, child: impl IntoElement) -> Glided {
    Glided {
        glide: glide.clone(),
        key,
        child: child.into_any_element(),
    }
}

pub struct Origin {
    glide: Shared,
    scope: String,
    child: AnyElement,
}

pub struct Glided {
    glide: Shared,
    key: String,
    child: AnyElement,
}

impl Element for Origin {
    type RequestLayoutState = ();
    type PrepaintState = ();

    fn id(&self) -> Option<ElementId> {
        None
    }

    fn source_location(&self) -> Option<&'static core::panic::Location<'static>> {
        None
    }

    fn request_layout(
        &mut self,
        _id: Option<&GlobalElementId>,
        _inspector_id: Option<&InspectorElementId>,
        window: &mut Window,
        cx: &mut App,
    ) -> (LayoutId, ()) {
        (self.child.request_layout(window, cx), ())
    }

    fn prepaint(
        &mut self,
        _id: Option<&GlobalElementId>,
        _inspector_id: Option<&InspectorElementId>,
        bounds: Bounds<Pixels>,
        _: &mut (),
        window: &mut Window,
        cx: &mut App,
    ) {
        // A new width rewraps titles and moves every card: place them at once.
        self.glide.borrow_mut().begin(
            &self.scope,
            f32::from(bounds.size.width).round(),
            f32::from(bounds.origin.y),
            Instant::now(),
        );
        self.child.prepaint(window, cx);
    }

    fn paint(
        &mut self,
        _id: Option<&GlobalElementId>,
        _inspector_id: Option<&InspectorElementId>,
        _bounds: Bounds<Pixels>,
        _: &mut (),
        _: &mut (),
        window: &mut Window,
        cx: &mut App,
    ) {
        self.child.paint(window, cx);
    }
}

impl Element for Glided {
    type RequestLayoutState = ();
    type PrepaintState = ();

    fn id(&self) -> Option<ElementId> {
        None
    }

    fn source_location(&self) -> Option<&'static core::panic::Location<'static>> {
        None
    }

    fn request_layout(
        &mut self,
        _id: Option<&GlobalElementId>,
        _inspector_id: Option<&InspectorElementId>,
        window: &mut Window,
        cx: &mut App,
    ) -> (LayoutId, ()) {
        (self.child.request_layout(window, cx), ())
    }

    fn prepaint(
        &mut self,
        _id: Option<&GlobalElementId>,
        _inspector_id: Option<&InspectorElementId>,
        bounds: Bounds<Pixels>,
        _: &mut (),
        window: &mut Window,
        cx: &mut App,
    ) {
        let dy = {
            let mut glide = self.glide.borrow_mut();
            let dy = glide.place(&self.key, f32::from(bounds.origin.y));
            if dy != 0.0 && !glide.requested {
                glide.requested = true;
                window.request_animation_frame();
            }
            dy
        };
        window.with_element_offset(point(px(0.0), px(dy)), |window| {
            self.child.prepaint(window, cx);
        });
    }

    fn paint(
        &mut self,
        _id: Option<&GlobalElementId>,
        _inspector_id: Option<&InspectorElementId>,
        _bounds: Bounds<Pixels>,
        _: &mut (),
        _: &mut (),
        window: &mut Window,
        cx: &mut App,
    ) {
        self.child.paint(window, cx);
    }
}

impl IntoElement for Origin {
    type Element = Self;

    fn into_element(self) -> Self {
        self
    }
}

impl IntoElement for Glided {
    type Element = Self;

    fn into_element(self) -> Self {
        self
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn at(start: Instant, seconds: f32) -> Instant {
        start + Duration::from_secs_f32(seconds)
    }

    fn frame(g: &mut Glide, now: Instant, origin: f32, items: &[(&str, f32)]) -> Vec<f32> {
        g.begin("p", 300.0, origin, now);
        items.iter().map(|(k, y)| g.place(k, origin + y)).collect()
    }

    #[test]
    fn first_frame_does_not_move() {
        let mut g = Glide::default();
        let t0 = Instant::now();
        assert_eq!(frame(&mut g, t0, 0.0, &[("a", 0.0), ("b", 80.0)]), [0.0, 0.0]);
    }

    #[test]
    fn a_card_whose_status_moved_it_starts_where_it_was_and_eases_in() {
        let mut g = Glide::default();
        let t0 = Instant::now();
        frame(&mut g, t0, 0.0, &[("a", 0.0), ("b", 80.0)]);
        // b goes above a (needs you sorts first): laid out at 0, drawn at 80.
        assert_eq!(frame(&mut g, t0, 0.0, &[("b", 0.0), ("a", 60.0)]), [80.0, -60.0]);
        let half = frame(&mut g, at(t0, SECONDS / 2.0), 0.0, &[("b", 0.0), ("a", 60.0)]);
        assert!(half[0] > 0.0 && half[0] < 80.0 * 0.2, "{half:?}");
        assert_eq!(
            frame(&mut g, at(t0, SECONDS), 0.0, &[("b", 0.0), ("a", 60.0)]),
            [0.0, 0.0]
        );
    }

    #[test]
    fn a_move_redirected_midway_continues_from_where_it_is_drawn() {
        let mut g = Glide::default();
        let t0 = Instant::now();
        frame(&mut g, t0, 0.0, &[("a", 0.0)]);
        frame(&mut g, t0, 0.0, &[("a", 100.0)]);
        let mid = at(t0, SECONDS / 2.0);
        let drawn = 100.0 + frame(&mut g, mid, 0.0, &[("a", 100.0)])[0];
        let redirected = 40.0 + frame(&mut g, mid, 0.0, &[("a", 40.0)])[0];
        assert!((redirected - drawn).abs() < 1e-3);
    }

    #[test]
    fn scrolling_does_not_move_cards() {
        let mut g = Glide::default();
        let t0 = Instant::now();
        frame(&mut g, t0, 0.0, &[("a", 0.0), ("b", 80.0)]);
        assert_eq!(frame(&mut g, t0, -120.0, &[("a", 0.0), ("b", 80.0)]), [0.0, 0.0]);
    }

    #[test]
    fn another_list_is_placed_at_once() {
        let mut g = Glide::default();
        let t0 = Instant::now();
        frame(&mut g, t0, 0.0, &[("a", 0.0), ("b", 80.0)]);
        // Rendering comes before the list's frame begins.
        assert_eq!(g.appear("q", "c", t0), 1.0);
        g.begin("q", 300.0, 0.0, t0);
        assert_eq!(g.place("b", 0.0), 0.0);
        assert_eq!(g.place("c", 80.0), 0.0);
    }

    #[test]
    fn a_new_width_places_cards_at_once() {
        let mut g = Glide::default();
        let t0 = Instant::now();
        frame(&mut g, t0, 0.0, &[("a", 0.0), ("b", 80.0)]);
        g.begin("p", 200.0, 0.0, t0);
        assert_eq!(g.place("b", 100.0), 0.0);
    }

    #[test]
    fn a_new_card_fades_in_but_known_ones_do_not() {
        let mut g = Glide::default();
        let t0 = Instant::now();
        // Nothing drawn yet: the first list shows at once.
        assert_eq!(g.appear("p", "a", t0), 1.0);
        frame(&mut g, t0, 0.0, &[("a", 0.0)]);
        assert_eq!(g.appear("p", "a", t0), 1.0);
        assert_eq!(g.appear("p", "b", t0), 0.0);
        assert!(g.fading());
        let half = g.appear("p", "b", at(t0, FADE_SECONDS / 2.0));
        assert!(half > 0.5 && half < 1.0, "{half}");
        assert_eq!(g.appear("p", "b", at(t0, FADE_SECONDS)), 1.0);
        assert!(!g.fading());
    }
}
