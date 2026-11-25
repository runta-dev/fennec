//! THI-135: switching queue threads eases the new thread's content in instead
//! of swapping it in a single frame.
//!
//! The motion follows the yansu desktop app's session switch: the content
//! fades in while it rises 4 px into place, over 180 ms with an exponential
//! ease-out (CSS `cubic-bezier(0.16, 1, 0.3, 1)`). Yansu also scales from
//! 0.96, which a GPUI div can't do, so only the fade and rise are kept.
//!
//! The whole scroll container moves as one, rather than each message on its
//! own, so the scroll handle's item indices (used to jump to a message) still
//! refer to the messages themselves.

use gpui::{Animation, AnimationExt as _, AnyElement, ElementId, IntoElement, Styled};
use std::time::Duration;

/// How long the new thread's content takes to settle.
pub const ENTER: Duration = Duration::from_millis(180);
/// How far below its place the content starts.
pub const RISE: f32 = 4.0;

/// Exponential ease-out: quick start, long gentle landing.
pub fn ease_out_expo(t: f32) -> f32 {
    if t >= 1.0 {
        1.0
    } else {
        1.0 - 2f32.powf(-10.0 * t)
    }
}

/// Eases `content` in once per thread switch. `switch` counts switches, so
/// every switch (even back to the same thread) starts a fresh animation.
pub fn ease_in<E>(switch: u64, content: E) -> AnyElement
where
    E: IntoElement + Styled + 'static,
{
    content
        .relative()
        .with_animation(
            ElementId::NamedInteger("thread-enter".into(), switch),
            Animation::new(ENTER).with_easing(ease_out_expo),
            |content, t| content.opacity(t).top(gpui::px(RISE * (1.0 - t))),
        )
        .into_any_element()
}

#[cfg(test)]
mod tests {
    use super::ease_out_expo;

    #[test]
    fn expo_runs_from_zero_to_one() {
        assert_eq!(ease_out_expo(0.0), 0.0);
        assert_eq!(ease_out_expo(1.0), 1.0);
        // Most of the way there by half time, as an ease-out should be.
        assert!(ease_out_expo(0.5) > 0.95);
    }
}
