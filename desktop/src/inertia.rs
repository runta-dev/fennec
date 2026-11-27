//! Trackpad scroll inertia (FEN-205).
//!
//! The user reported that every scroll view in the desktop window stops
//! dead when the fingers lift off the trackpad: it moves only as far as the
//! fingers moved, with no inertia. gpui's scroll containers apply whatever
//! `ScrollWheelEvent`s arrive, so momentum depends on AppKit sending its
//! momentum-phase events. When it doesn't, a flick ends the moment the
//! gesture's `Ended` event arrives.
//!
//! This layer watches the window's scroll events (capture phase, so every
//! scroll container is covered, terminals included). When a precise
//! (trackpad) gesture ends while still moving, it continues the scroll with
//! `UIScrollView`'s deceleration (velocity × 0.998 per millisecond),
//! dispatched as ordinary scroll events at the pointer, the way AppKit's
//! momentum events would be.
//! - Touching the trackpad again (`Started`) or clicking stops the glide.
//! - If AppKit does send momentum events (a scroll event without `Started`
//!   arriving during the glide), the glide stops and the layer stays off for
//!   the rest of the process, so the two never add up.
//! - Mouse wheels (line deltas, no phases) are left alone.
//!
//! One line per launch says which case applies, in the daily log
//! `~/.config/fennec/logs/fennec-<date>.log` (prefixed
//! `fennec-desktop FEN-205 scroll`), so Export logs shows it.

use gpui::{
    AnyElement, App, DispatchPhase, IntoElement, MouseDownEvent, ScrollDelta, ScrollWheelEvent,
    Styled as _, TouchPhase, Window, canvas, div, point, px,
};
use std::cell::RefCell;
use std::collections::VecDeque;
use std::time::{Duration, Instant};

/// `UIScrollView`'s normal deceleration rate: velocity kept per millisecond.
const DECAY_PER_MS: f64 = 0.998;
/// Releases slower than this (px/s) just stop, like a careful drag.
const MIN_FLING: f64 = 120.0;
/// Ceiling on release speed (px/s), as in gpui's touch recognizer.
const MAX_FLING: f64 = 8000.0;
/// The glide ends once it is slower than this (px/s): no longer visible.
const STOP_SPEED: f64 = 12.0;
/// How far back the release velocity looks.
const VELOCITY_WINDOW: Duration = Duration::from_millis(100);
/// No movement for this long before lifting means the fingers had stopped.
const STOPPED_GAP: Duration = Duration::from_millis(45);
/// Glide step; each step lands on a closed-form curve, so late timers don't
/// change the distance, only the granularity.
const TICK: Duration = Duration::from_millis(8);

#[derive(Default)]
struct State {
    /// Precise deltas of the gesture in progress (time, x, y).
    samples: VecDeque<(Instant, f64, f64)>,
    in_gesture: bool,
    glide: Option<Glide>,
    generation: u64,
    /// Set while the layer dispatches its own events.
    dispatching: bool,
    /// AppKit was seen sending momentum events: leave inertia to it.
    native: bool,
    logged: bool,
}

struct Glide {
    generation: u64,
    start: Instant,
    /// Release velocity, px/s.
    vx: f64,
    vy: f64,
    /// Distance already dispatched.
    sent_x: f64,
    sent_y: f64,
}

thread_local! {
    static STATE: RefCell<State> = RefCell::new(State::default());
}

/// A window-wide element that installs the listeners; render it once in the
/// window's root. Off outside macOS.
pub fn layer() -> AnyElement {
    if !cfg!(target_os = "macos") {
        return div().into_any_element();
    }
    canvas(
        |_, _, _| (),
        |_, _, window, _| {
            window.on_mouse_event(|e: &ScrollWheelEvent, phase, window, cx| {
                if phase == DispatchPhase::Capture {
                    on_scroll(e, window, cx);
                }
            });
            window.on_mouse_event(|_: &MouseDownEvent, phase, _, _| {
                if phase == DispatchPhase::Capture {
                    STATE.with_borrow_mut(|s| s.glide = None);
                }
            });
        },
    )
    .absolute()
    .size_0()
    .into_any_element()
}

fn on_scroll(e: &ScrollWheelEvent, window: &mut Window, cx: &mut App) {
    let now = Instant::now();
    let start = STATE.with_borrow_mut(|s| {
        if s.dispatching {
            return None;
        }
        let ScrollDelta::Pixels(delta) = e.delta else {
            // A mouse wheel: no gesture, no inertia.
            s.glide = None;
            return None;
        };
        match e.touch_phase {
            TouchPhase::Started => {
                s.glide = None;
                s.samples.clear();
                s.in_gesture = true;
            }
            TouchPhase::Moved if s.in_gesture => {
                s.samples.push_back((now, f64::from(f32::from(delta.x)), f64::from(f32::from(delta.y))));
                while s.samples.front().is_some_and(|(t, ..)| now.duration_since(*t) > VELOCITY_WINDOW * 2) {
                    s.samples.pop_front();
                }
            }
            TouchPhase::Moved => {
                // Scrolling outside a gesture while gliding: AppKit's own
                // momentum. It carries on from here.
                if s.glide.take().is_some() && !s.native {
                    s.native = true;
                    log("AppKit momentum events present; Fennec inertia off");
                }
            }
            TouchPhase::Ended => {
                s.in_gesture = false;
                let (vx, vy) = release_velocity(&s.samples, now);
                s.samples.clear();
                let speed = vx.hypot(vy);
                if s.native || speed < MIN_FLING {
                    return None;
                }
                let scale = (MAX_FLING / speed).min(1.0);
                s.generation += 1;
                s.glide = Some(Glide {
                    generation: s.generation,
                    start: now,
                    vx: vx * scale,
                    vy: vy * scale,
                    sent_x: 0.0,
                    sent_y: 0.0,
                });
                return Some(s.generation);
            }
            TouchPhase::Cancelled => {
                s.in_gesture = false;
                s.samples.clear();
                s.glide = None;
            }
        }
        None
    });
    if let Some(generation) = start {
        window
            .spawn(cx, async move |cx| {
                loop {
                    cx.background_executor().timer(TICK).await;
                    let more = cx.update(|window, cx| step(generation, window, cx)).unwrap_or(false);
                    if !more {
                        break;
                    }
                }
            })
            .detach();
    }
}

/// Velocity (px/s) over the last [`VELOCITY_WINDOW`] of the gesture.
fn release_velocity(samples: &VecDeque<(Instant, f64, f64)>, now: Instant) -> (f64, f64) {
    let Some(&(last, ..)) = samples.back() else { return (0.0, 0.0) };
    if now.duration_since(last) > STOPPED_GAP {
        return (0.0, 0.0);
    }
    let recent: Vec<_> =
        samples.iter().filter(|(t, ..)| last.duration_since(*t) <= VELOCITY_WINDOW).collect();
    let (Some(first), true) = (recent.first(), recent.len() >= 2) else { return (0.0, 0.0) };
    let span = last.duration_since(first.0).as_secs_f64();
    if span < 0.004 {
        return (0.0, 0.0);
    }
    // The first sample's delta happened before the window opened.
    let (dx, dy) = recent[1..].iter().fold((0.0, 0.0), |(x, y), (_, dx, dy)| (x + dx, y + dy));
    (dx / span, dy / span)
}

/// Distance (px) a glide released at `v` px/s has covered after `ms`.
fn glide_offset(v: f64, ms: f64) -> f64 {
    let tau_ms = -1.0 / DECAY_PER_MS.ln();
    v / 1000.0 * tau_ms * (1.0 - DECAY_PER_MS.powf(ms))
}

/// Dispatches the glide's next step; false once it's over or replaced.
fn step(generation: u64, window: &mut Window, cx: &mut App) -> bool {
    let next = STATE.with_borrow_mut(|s| {
        let glide = s.glide.as_mut().filter(|g| g.generation == generation)?;
        let ms = glide.start.elapsed().as_secs_f64() * 1000.0;
        let (x, y) = (glide_offset(glide.vx, ms), glide_offset(glide.vy, ms));
        let delta = (x - glide.sent_x, y - glide.sent_y);
        glide.sent_x = x;
        glide.sent_y = y;
        let done = glide.vx.hypot(glide.vy) * DECAY_PER_MS.powf(ms) < STOP_SPEED;
        if done {
            s.glide = None;
            if !s.logged && !s.native {
                s.logged = true;
                log("no AppKit momentum events after a flick; Fennec inertia on");
            }
        }
        s.dispatching = true;
        Some((delta, done))
    });
    let Some(((dx, dy), done)) = next else { return false };
    let event = ScrollWheelEvent {
        position: window.mouse_position(),
        delta: ScrollDelta::Pixels(point(px(dx as f32), px(dy as f32))),
        modifiers: window.modifiers(),
        touch_phase: TouchPhase::Moved,
    };
    window.dispatch_event(gpui::PlatformInput::ScrollWheel(event), cx);
    STATE.with_borrow_mut(|s| s.dispatching = false);
    !done
}

fn log(line: &str) {
    use std::io::Write as _;
    tracing::info!(target: "fennec_desktop::inertia", "FEN-205 scroll {line}");
    let Some(home) = std::env::var_os("HOME") else { return };
    let now = chrono::Local::now();
    let dir = std::path::Path::new(&home).join(".config/fennec/logs");
    let path = dir.join(format!("fennec-{}.log", now.format("%Y-%m-%d")));
    let _ = std::fs::create_dir_all(&dir);
    if let Ok(mut file) = std::fs::OpenOptions::new().create(true).append(true).open(path) {
        let text = format!("{} fennec-desktop FEN-205 scroll {line}\n", now.format("%Y/%m/%d %H:%M:%S"));
        let _ = file.write_all(text.as_bytes());
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn glide_distance_matches_uiscrollview() {
        // A 1000 px/s release coasts about 500 px.
        let total = glide_offset(1000.0, 10_000.0);
        assert!((total - 499.5).abs() < 1.0, "{total}");
        assert!(glide_offset(1000.0, 100.0) > 80.0);
    }

    #[test]
    fn release_velocity_reads_recent_motion() {
        let t0 = Instant::now();
        let samples: VecDeque<_> =
            (0..6).map(|i| (t0 + Duration::from_millis(i * 10), 0.0, -20.0)).collect();
        let (vx, vy) = release_velocity(&samples, t0 + Duration::from_millis(55));
        assert_eq!(vx, 0.0);
        assert!((vy + 2000.0).abs() < 1.0, "{vy}");
        // Fingers rested before lifting: no fling.
        assert_eq!(release_velocity(&samples, t0 + Duration::from_millis(200)), (0.0, 0.0));
    }
}
