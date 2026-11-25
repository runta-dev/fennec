//! THI-93: a sent reply shows in the thread at once, before the board
//! confirms it, and new messages ease in instead of popping in.
//!
//! Sending used to wait for the board's `continue` call and then a full
//! refresh (projects, the request list and the thread, one after another)
//! before the reply appeared, which took about a second. Now the reply is
//! drawn as a pending message the moment it is sent, the reply box clears,
//! and the request the `continue` call returns replaces the thread directly.
//! If sending fails, the pending message goes away and the text and images
//! return to the reply box.

use crate::board::{Attachment, Message, Request};
use gpui::{Animation, AnimationExt as _, AnyElement, ElementId, IntoElement, Styled, ease_out_quint, px};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

/// How long a new message takes to ease in.
const EASE_IN: Duration = Duration::from_millis(260);
/// How far below its place a new message starts.
const RISE: f32 = 10.0;

/// A reply drawn in its thread before the board has confirmed it.
#[derive(Clone, Debug)]
pub struct PendingReply {
    pub project: String,
    pub number: u64,
    /// Tells this send apart from others for the same thread.
    pub token: u64,
    pub message: Message,
}

impl PendingReply {
    pub fn new(
        project: String,
        thread: Option<&Request>,
        earlier: &[PendingReply],
        number: u64,
        token: u64,
        author: String,
        body: String,
        attachments: Vec<Attachment>,
    ) -> Self {
        // The id the board will most likely give it, so the confirmed message
        // keeps the same element (and its running ease-in).
        let loaded = thread
            .filter(|t| t.number == number)
            .map_or(0, |t| t.messages.len() as u64);
        let queued = earlier
            .iter()
            .filter(|p| p.project == project && p.number == number)
            .count() as u64;
        Self {
            project,
            number,
            token,
            message: Message {
                id: loaded + queued + 1,
                author,
                body: body.trim().to_owned(),
                at: utc_now(),
                attachments,
                ..Default::default()
            },
        }
    }

    /// Whether `thread` already holds this reply (a refresh landed first).
    pub fn confirmed_in(&self, thread: &Request) -> bool {
        thread.number == self.number
            && thread.messages.iter().any(|m| {
                m.id >= self.message.id
                    && m.agent.is_empty()
                    && m.author == self.message.author
                    && m.body == self.message.body
            })
    }
}

/// The element key of a thread message; its `TextView` uses the same one.
pub fn message_key(number: u64, id: u64) -> String {
    format!("m-{number}-{id}")
}

/// Keys of the messages `new` has beyond `old` (the same thread grew).
pub fn grown_keys(old: &Request, new: &Request) -> Vec<String> {
    if old.number != new.number {
        return Vec::new();
    }
    let known = old.messages.iter().map(|m| m.id).max().unwrap_or(0);
    new.messages
        .iter()
        .filter(|m| m.id > known)
        .map(|m| message_key(new.number, m.id))
        .collect()
}

/// Fades `item` in while it rises into place, once, the first time it is drawn.
pub fn ease_in(key: &str, item: gpui::Div) -> AnyElement {
    item.relative()
        .with_animation(
            ElementId::Name(format!("in-{key}").into()),
            Animation::new(EASE_IN).with_easing(ease_out_quint()),
            |item, t| item.opacity(t).top(px(RISE * (1.0 - t))),
        )
        .into_any_element()
}

/// Now, as the board writes times (RFC 3339, UTC, whole seconds).
pub fn utc_now() -> String {
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| d.as_secs());
    utc_stamp(secs)
}

fn utc_stamp(secs: u64) -> String {
    let days = (secs / 86_400) as i64;
    let rem = secs % 86_400;
    // Civil date from days since 1970-01-01 (Howard Hinnant's algorithm).
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1_460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}Z",
        rem / 3_600,
        rem / 60 % 60,
        rem % 60
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    fn thread(number: u64, bodies: &[(&str, &str)]) -> Request {
        Request {
            number,
            messages: bodies
                .iter()
                .enumerate()
                .map(|(i, (author, body))| Message {
                    id: i as u64 + 1,
                    author: (*author).into(),
                    body: (*body).into(),
                    ..Default::default()
                })
                .collect(),
            ..Default::default()
        }
    }

    #[test]
    fn stamps_utc() {
        assert_eq!(utc_stamp(0), "1970-01-01T00:00:00Z");
        assert_eq!(utc_stamp(1_795_578_534), "2026-11-25T03:48:54Z");
        assert_eq!(utc_stamp(951_782_400), "2000-02-29T00:00:00Z");
    }

    #[test]
    fn predicts_the_next_id() {
        let t = thread(93, &[("a", "one"), ("b", "two")]);
        let first = PendingReply::new("this".into(), Some(&t), &[], 93, 1, "me".into(), " hi ".into(), vec![]);
        assert_eq!(first.message.id, 3);
        assert_eq!(first.message.body, "hi");
        let second = PendingReply::new("this".into(), Some(&t), &[first.clone()], 93, 2, "me".into(), "again".into(), vec![]);
        assert_eq!(second.message.id, 4);
        let other = PendingReply::new("this".into(), Some(&t), &[first], 7, 3, "me".into(), "x".into(), vec![]);
        assert_eq!(other.message.id, 1);
    }

    #[test]
    fn confirmed_once_the_thread_has_it() {
        let t = thread(93, &[("a", "one")]);
        let p = PendingReply::new("this".into(), Some(&t), &[], 93, 1, "me".into(), "hi".into(), vec![]);
        assert!(!p.confirmed_in(&t));
        assert!(p.confirmed_in(&thread(93, &[("a", "one"), ("me", "hi")])));
        // An older identical message is not this one.
        let t = thread(93, &[("me", "hi")]);
        let p = PendingReply::new("this".into(), Some(&t), &[], 93, 1, "me".into(), "hi".into(), vec![]);
        assert!(!p.confirmed_in(&t));
    }

    #[test]
    fn keys_of_grown_messages() {
        let old = thread(93, &[("a", "one")]);
        let new = thread(93, &[("a", "one"), ("b", "two")]);
        assert_eq!(grown_keys(&old, &new), vec!["m-93-2".to_owned()]);
        assert!(grown_keys(&thread(1, &[]), &new).is_empty());
    }
}
