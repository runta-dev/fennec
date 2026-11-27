//! FEN-195: a modal opens with the same motion as the Fennec window itself.
//!
//! The window uses the macOS document-window animation (see the queue
//! knowledge entry "window-animation"): it springs from slightly smaller to
//! full size, reaching it in about 110 ms, overshooting a little and settling
//! by about 270 ms. A GPUI div can't be scaled, so the card's width follows
//! that spring from `FROM` of its size (it stays centred, so it grows from the
//! middle), while the scrim and everything on it fade in.

use gpui::{Animation, AnimationExt as _, AnyElement, ElementId, IntoElement, Styled, px};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

/// The spring's length: the window's open animation settles by ~270 ms.
pub const SPRING: Duration = Duration::from_millis(270);
/// How long the scrim and card take to fade in.
pub const FADE: Duration = Duration::from_millis(140);
/// The card's starting size relative to its full size.
pub const FROM: f32 = 0.95;

static OPENED: AtomicU64 = AtomicU64::new(0);

/// A fresh key per opening, so every opening plays the animation again.
pub fn next_key() -> u64 {
    OPENED.fetch_add(1, Ordering::Relaxed) + 1
}

/// Underdamped spring over `SPRING`: peaks at ~110 ms with a ~10% overshoot
/// of the travel (the window's +0.2% on a 2% travel), then settles.
pub fn spring(t: f32) -> f32 {
    if t >= 1.0 {
        return 1.0;
    }
    const ZETA: f32 = 0.6;
    // Damped frequency puts the first peak at 110 ms.
    let wd = std::f32::consts::PI / 0.110;
    let wn = wd / (1.0 - ZETA * ZETA).sqrt();
    let s = t * SPRING.as_secs_f32();
    let decay = (-ZETA * wn * s).exp();
    1.0 - decay * ((wd * s).cos() + ZETA / (1.0 - ZETA * ZETA).sqrt() * (wd * s).sin())
}

fn ease_out_cubic(t: f32) -> f32 {
    1.0 - (1.0 - t.clamp(0.0, 1.0)).powi(3)
}

/// Fades the scrim (and the card on it) in.
pub fn scrim<E>(key: u64, scrim: E) -> AnyElement
where
    E: IntoElement + Styled + 'static,
{
    scrim
        .with_animation(
            ElementId::NamedInteger("modal-scrim".into(), key),
            Animation::new(FADE).with_easing(ease_out_cubic),
            |scrim, t| scrim.opacity(t),
        )
        .into_any_element()
}

/// Springs the card from `FROM` of `width` to `width`.
pub fn card<E>(key: u64, width: f32, card: E) -> AnyElement
where
    E: IntoElement + Styled + 'static,
{
    card.with_animation(
        ElementId::NamedInteger("modal-card".into(), key),
        Animation::new(SPRING).with_easing(spring),
        move |card, t| card.w(px(width * (FROM + (1.0 - FROM) * t))),
    )
    .into_any_element()
}

#[cfg(test)]
mod tests {
    use super::spring;

    #[test]
    fn spring_starts_overshoots_and_lands() {
        assert_eq!(spring(0.0), 0.0);
        assert_eq!(spring(1.0), 1.0);
        let peak = (1..100).map(|i| spring(i as f32 / 100.0)).fold(0.0, f32::max);
        assert!(peak > 1.05 && peak < 1.15, "peak {peak}");
        // Nearly settled before the end, so the final snap to 1 is invisible.
        assert!((spring(0.99) - 1.0).abs() < 0.01);
    }
}
