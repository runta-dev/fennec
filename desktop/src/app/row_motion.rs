//! Sidebar rows glide to their new place when a session's state moves them
//! (working → idle, ...), instead of jumping there.
//!
//! Each frame the sidebar hands over where every row and group header now sits
//! (`update`). A row whose place changed starts from where it was drawn and
//! eases into the new place (`offset`, added to its layout position).

use std::collections::HashMap;
use std::time::{Duration, Instant};

/// How long a move takes.
pub const SECONDS: f32 = 0.28;

#[derive(Default)]
pub struct RowMotion {
    /// Each row's layout position in the last frame.
    placed: HashMap<String, f32>,
    /// Running moves: the offset the row started from, and when.
    moves: HashMap<String, (f32, Instant)>,
    /// The search the last frame was filtered by.
    query: String,
}

impl RowMotion {
    /// Takes this frame's layout (row key, top) and starts a move for each row
    /// that changed place. Returns whether any row is still moving.
    pub fn update(&mut self, layout: &[(String, f32)], query: &str, now: Instant) -> bool {
        // Typing a search reflows the whole list; that should be immediate.
        let animate = query == self.query && !self.placed.is_empty();
        self.query = query.to_owned();
        let mut placed = HashMap::with_capacity(layout.len());
        for (key, y) in layout {
            if animate
                && let Some(&before) = self.placed.get(key)
                && before != *y
            {
                let from = before + self.offset(key, now) - y;
                self.moves.insert(key.clone(), (from, now));
            }
            placed.insert(key.clone(), *y);
        }
        if !animate {
            self.moves.clear();
        }
        self.placed = placed;
        let placed = &self.placed;
        self.moves.retain(|key, (_, start)| {
            placed.contains_key(key)
                && now.duration_since(*start) < Duration::from_secs_f32(SECONDS)
        });
        !self.moves.is_empty()
    }

    /// How far `key` is drawn from its layout position at `now`.
    pub fn offset(&self, key: &str, now: Instant) -> f32 {
        let Some(&(from, start)) = self.moves.get(key) else {
            return 0.0;
        };
        let t = (now.duration_since(start).as_secs_f32() / SECONDS).min(1.0);
        // Ease out (cubic), like the sidebar's slide.
        from * (1.0 - t).powi(3)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn layout(rows: &[(&str, f32)]) -> Vec<(String, f32)> {
        rows.iter().map(|(k, y)| (k.to_string(), *y)).collect()
    }

    fn at(start: Instant, seconds: f32) -> Instant {
        start + Duration::from_secs_f32(seconds)
    }

    #[test]
    fn first_frame_does_not_move() {
        let mut m = RowMotion::default();
        let now = Instant::now();
        assert!(!m.update(&layout(&[("a", 0.0), ("b", 31.0)]), "", now));
        assert_eq!(m.offset("a", now), 0.0);
    }

    #[test]
    fn a_moved_row_starts_where_it_was_and_eases_in() {
        let mut m = RowMotion::default();
        let t0 = Instant::now();
        m.update(&layout(&[("a", 0.0), ("b", 31.0)]), "", t0);
        // a goes below b: it is laid out at 31 but must first be drawn at 0.
        assert!(m.update(&layout(&[("b", 0.0), ("a", 31.0)]), "", t0));
        assert_eq!(m.offset("a", t0), -31.0);
        assert_eq!(m.offset("b", t0), 31.0);
        let half = m.offset("a", at(t0, SECONDS / 2.0));
        assert!(half > -31.0 * 0.2 && half < 0.0, "{half}");
        assert_eq!(m.offset("a", at(t0, SECONDS)), 0.0);
        assert!(!m.update(&layout(&[("b", 0.0), ("a", 31.0)]), "", at(t0, SECONDS)));
    }

    #[test]
    fn a_move_redirected_midway_continues_from_where_it_is_drawn() {
        let mut m = RowMotion::default();
        let t0 = Instant::now();
        m.update(&layout(&[("a", 0.0)]), "", t0);
        m.update(&layout(&[("a", 100.0)]), "", t0);
        let mid = at(t0, SECONDS / 2.0);
        let drawn = 100.0 + m.offset("a", mid);
        m.update(&layout(&[("a", 40.0)]), "", mid);
        assert!((40.0 + m.offset("a", mid) - drawn).abs() < 1e-3);
    }

    #[test]
    fn searching_and_new_rows_do_not_animate() {
        let mut m = RowMotion::default();
        let t0 = Instant::now();
        m.update(&layout(&[("a", 0.0), ("b", 31.0)]), "", t0);
        assert!(!m.update(&layout(&[("b", 0.0)]), "b", t0));
        assert_eq!(m.offset("b", t0), 0.0);
        assert!(!m.update(&layout(&[("b", 0.0), ("c", 31.0)]), "b", t0));
        assert_eq!(m.offset("c", t0), 0.0);
    }
}
