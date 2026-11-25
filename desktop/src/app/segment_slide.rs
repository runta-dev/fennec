//! The Active / Done switch above the queue list: the highlight slides to the
//! chosen tab (eased) instead of jumping there.
//!
//! The tabs' widths depend on their labels and counts, so the highlight is
//! placed from where the tabs were laid out in the last frame. Until they have
//! been laid out once, the chosen tab draws its own background instead.

use gpui::{
    Animation, AnimationExt as _, AnyElement, Bounds, Div, ElementId, IntoElement, Pixels,
    Styled as _, ease_out_quint, px,
};
use std::cell::RefCell;
use std::rc::Rc;
use std::time::Duration;

/// How long the highlight takes to reach the chosen tab.
const SLIDE: Duration = Duration::from_millis(260);

#[derive(Default)]
struct State {
    /// Each tab's bounds in the last laid-out frame.
    tabs: Vec<Bounds<Pixels>>,
    /// The tab the latest switch started from.
    from: usize,
    /// Counts switches, so every switch starts a fresh animation.
    switches: u64,
}

#[derive(Clone, Default)]
pub struct Slide(Rc<RefCell<State>>);

/// Where the highlight sits over one tab, relative to the row of tabs.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Place {
    pub left: f32,
    pub width: f32,
    pub height: f32,
}

impl Slide {
    /// Called when the chosen tab changes from `from` to another one.
    pub fn switch(&self, from: usize) {
        let mut s = self.0.borrow_mut();
        s.from = from;
        s.switches += 1;
    }

    /// Whether the highlight can be drawn (the tabs were laid out once).
    pub fn ready(&self) -> bool {
        !self.0.borrow().tabs.is_empty()
    }

    /// Records where `row`'s children (the tabs) are laid out. Redraws when
    /// they moved, so the highlight follows a count that changed width.
    pub fn track(&self, row: Div) -> Div {
        let state = self.0.clone();
        row.on_children_prepainted(move |tabs, window, _| {
            let mut s = state.borrow_mut();
            if s.tabs != tabs {
                s.tabs = tabs;
                window.refresh();
            }
        })
    }

    fn place(&self, index: usize) -> Option<Place> {
        let s = self.0.borrow();
        let first = s.tabs.first()?;
        let tab = s.tabs.get(index)?;
        Some(Place {
            left: f32::from(tab.origin.x - first.origin.x),
            width: f32::from(tab.size.width),
            height: f32::from(tab.size.height),
        })
    }

    /// The highlight under tab `chosen`, positioned absolutely from `inset`
    /// (the row's padding). `style` gives its look. None until the tabs were
    /// laid out.
    pub fn highlight(
        &self,
        chosen: usize,
        inset: f32,
        style: impl Fn(Div) -> Div,
    ) -> Option<AnyElement> {
        let to = self.place(chosen)?;
        let (from, switches) = {
            let s = self.0.borrow();
            (self.place(s.from).unwrap_or(to), s.switches)
        };
        let pill = |p: Place| {
            style(gpui::div())
                .absolute()
                .top(px(inset))
                .left(px(inset + p.left))
                .w(px(p.width))
                .h(px(p.height))
        };
        if switches == 0 || from == to {
            return Some(pill(to).into_any_element());
        }
        Some(
            pill(from)
                .with_animation(
                    ElementId::Name(format!("tab-slide-{switches}").into()),
                    Animation::new(SLIDE).with_easing(ease_out_quint()),
                    move |pill, t| {
                        let p = between(from, to, t);
                        pill.left(px(inset + p.left)).w(px(p.width))
                    },
                )
                .into_any_element(),
        )
    }
}

/// The highlight `t` of the way (0..=1) from `a` to `b`.
pub fn between(a: Place, b: Place, t: f32) -> Place {
    let t = t.clamp(0.0, 1.0);
    let mix = |x: f32, y: f32| x + (y - x) * t;
    Place {
        left: mix(a.left, b.left),
        width: mix(a.width, b.width),
        height: mix(a.height, b.height),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn between_eases_from_one_tab_to_the_other() {
        let a = Place { left: 0.0, width: 60.0, height: 24.0 };
        let b = Place { left: 64.0, width: 72.0, height: 24.0 };
        assert_eq!(between(a, b, 0.0), a);
        assert_eq!(between(a, b, 1.0), b);
        assert_eq!(between(a, b, 2.0), b);
        let mid = between(a, b, 0.5);
        assert_eq!(mid.left, 32.0);
        assert_eq!(mid.width, 66.0);
    }

    #[test]
    fn no_highlight_until_tabs_are_laid_out() {
        let slide = Slide::default();
        assert!(!slide.ready());
        assert!(slide.highlight(0, 2.0, |d| d).is_none());
    }
}
