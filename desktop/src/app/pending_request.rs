//! FEN-133: a new request shows as a card the moment it is sent, before the
//! board answers, the way a sent reply does (THI-93, `pending_reply`).
//!
//! Sending used to keep the text in the New Request box and show no card
//! until the board's POST came back through the cloud link, so a send felt
//! slow. Now the box clears at once and a faded Queued card stands in for
//! the request. The board's answer replaces it with the real card. If the
//! board refuses it, the card goes away and the text and images come back
//! to the box.
//!
//! Stand-ins live only here, never in the board's lists, so a refresh can't
//! drop them and their stand-in numbers never reach the board.

use crate::board::{Attachment, Message, Request};

/// Stand-in numbers start here; no board number gets this high.
const STANDIN_FROM: u64 = u64::MAX / 2;

/// Whether `number` belongs to a request the board hasn't answered yet.
pub fn is_standin(number: u64) -> bool {
    number >= STANDIN_FROM
}

/// A new request drawn in its queue before the board has it.
#[derive(Clone, Debug)]
pub struct PendingRequest {
    pub project: String,
    /// Tells this send apart from others.
    pub token: u64,
    /// What was sent, to give back to the box if the board refuses it.
    pub text: String,
    pub images: Vec<Attachment>,
    /// The stand-in card.
    pub request: Request,
}

impl PendingRequest {
    pub fn new(project: String, token: u64, author: String, text: String, images: Vec<Attachment>) -> Self {
        let at = super::pending_reply::utc_now();
        let body = text.trim().to_owned();
        let message = Message {
            id: 1,
            author: author.clone(),
            body: body.clone(),
            at: at.clone(),
            attachments: images.clone(),
            ..Default::default()
        };
        let request = Request {
            number: STANDIN_FROM + token,
            title: crate::board::title_for(&body),
            status: "queued".into(),
            author,
            created: at.clone(),
            updated: at,
            latest: Some(message.clone()),
            messages: vec![message],
            ..Default::default()
        };
        Self { project, token, text, images, request }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_stand_in_is_queued_under_its_first_line_with_a_number_the_board_never_uses() {
        let p = PendingRequest::new("fennec".into(), 3, "me".into(), "  Fix send\nmore  ".into(), Vec::new());
        assert!(is_standin(p.request.number));
        assert!(!is_standin(1_000_000));
        assert_eq!(p.request.title, "Fix send");
        assert_eq!(p.request.status, "queued");
        assert_eq!(p.request.latest.as_ref().map(|m| m.body.as_str()), Some("Fix send\nmore"));
    }
}
