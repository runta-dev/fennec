//! A thread's linked requests (THI-103), shown the way GitHub shows an issue's
//! cross-references. Under the opening message there is one row per request
//! that links here or that this one links to explicitly. Each row says how they
//! are linked, with the other request's reference, title and status. Clicking
//! a row opens that thread.
//!
//! The board derives them (internal/board/links.go): explicit links (related,
//! or duplicate when a request was merged into another), and mentions of
//! KEY-n in any message. A mention row shows where it was written: who, when,
//! and the line holding the reference. What this thread mentions itself is
//! not a row; each KEY-n in its text is a link instead (`link_refs`).
use super::*;
use crate::board::{Mention, Related};
use std::collections::HashSet;

/// Mention lines shown per row; the rest are counted.
const SHOWN_MENTIONS: usize = 3;

/// The URL a KEY-n reference in a message links to.
const REQUEST_URL: &str = "fennec://request/";

/// The label of a linked request's row.
pub(super) fn link_label(r: &Related) -> &'static str {
    match (r.kind.as_str(), r.incoming) {
        ("duplicate", false) => "link_duplicate_of",
        ("duplicate", true) => "link_merged_here",
        ("mention", false) => "link_mentions",
        ("mention", true) => "link_mentioned_in",
        _ => "link_related",
    }
}

/// Rows in the order they show: a merge first, then the rest as the board
/// lists them (explicit links, then mentions; outgoing before incoming).
pub(super) fn ordered(related: &[Related]) -> Vec<&Related> {
    let mut rows: Vec<&Related> = related.iter().collect();
    rows.sort_by_key(|r| r.kind != "duplicate");
    rows
}

/// "claude · 11-25 05:01": who wrote a mention, and when.
fn mention_source(m: &Mention) -> String {
    let who = if m.agent.is_empty() {
        m.author.clone()
    } else {
        sessions::agent_name(&m.agent).to_owned()
    };
    let when =
        m.at.get(5..16)
            .map(|s| s.replace('T', " "))
            .unwrap_or_default();
    format!("{who} · {when}")
}

/// The request number a link in a message opens, if it is a KEY-n link.
pub(super) fn request_link(url: &str) -> Option<u64> {
    url.strip_prefix(REQUEST_URL)?.parse().ok()
}

/// `body` with every reference to a known request of the queue (KEY-n, as
/// the board matches mentions) turned into a markdown link that opens it.
/// Code, and the text and target of existing links, are left alone.
pub(super) fn link_refs(body: &str, key: &str, known: &HashSet<u64>) -> String {
    if key.is_empty() || !body.contains(key) {
        return body.to_owned();
    }
    let word = |c: char| c.is_ascii_alphanumeric() || c == '_';
    let mut out = String::with_capacity(body.len());
    let mut fence = false;
    for (index, line) in body.split('\n').enumerate() {
        if index > 0 {
            out.push('\n');
        }
        if line.trim_start().starts_with("```") {
            fence = !fence;
        }
        if fence || line.trim_start().starts_with("```") {
            out.push_str(line);
            continue;
        }
        let (mut code, mut bracket, mut target) = (false, 0usize, false);
        let mut prev: Option<char> = None;
        let mut i = 0;
        while i < line.len() {
            let rest = &line[i..];
            let ch = rest.chars().next().unwrap_or_default();
            let free = !code && bracket == 0 && !target;
            if free
                && rest.starts_with(key)
                && rest[key.len()..].starts_with('-')
                && prev.is_none_or(|p| !word(p) && p != '-')
            {
                let digits: String = rest[key.len() + 1..]
                    .chars()
                    .take_while(|c| c.is_ascii_digit())
                    .collect();
                let after = rest[key.len() + 1 + digits.len()..].chars().next();
                if let Ok(n) = digits.parse::<u64>()
                    && !digits.is_empty()
                    && digits.len() <= 7
                    && after.is_none_or(|c| !word(c))
                    && known.contains(&n)
                {
                    out.push_str(&format!("[{key}-{n}]({REQUEST_URL}{n})"));
                    i += key.len() + 1 + digits.len();
                    prev = Some('0');
                    continue;
                }
            }
            match ch {
                '`' => code = !code,
                '[' if !code => bracket += 1,
                ']' if !code && bracket > 0 => {
                    bracket -= 1;
                    target = rest[1..].starts_with('(');
                }
                ')' if target => target = false,
                _ => {}
            }
            out.push(ch);
            prev = Some(ch);
            i += ch.len_utf8();
        }
    }
    out
}

impl FennecWindow {
    pub(super) fn thread_links(
        &self,
        p: &Project,
        r: &Request,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        if r.related.is_empty() {
            return None;
        }
        let rows = ordered(&r.related)
            .into_iter()
            .enumerate()
            .map(|(i, link)| {
                let n = link.number;
                let more = link.mentions.len().saturating_sub(SHOWN_MENTIONS);
                v_flex()
                    .id(SharedString::from(format!("thread-link-{i}")))
                    .px(px(8.0))
                    .py(px(5.0))
                    .gap(px(2.0))
                    .rounded(px(6.0))
                    .cursor_pointer()
                    .hover(|s| s.bg(c("f_line").opacity(0.5)))
                    .on_click(
                        cx.listener(move |this, _, window, cx| this.open_request(n, window, cx)),
                    )
                    .child(
                        h_flex()
                            .h(px(22.0))
                            .gap(px(8.0))
                            .child(
                                div()
                                    .flex_none()
                                    .w(px(96.0))
                                    .truncate()
                                    .text_size(px(11.5))
                                    .text_color(c("f_muted"))
                                    .child(t(link_label(link))),
                            )
                            .child(
                                div()
                                    .flex_none()
                                    .text_size(px(12.0))
                                    .text_color(c("f_muted"))
                                    .child(format!("{}-{}", p.key, n)),
                            )
                            .child(
                                div()
                                    .flex_1()
                                    .min_w_0()
                                    .truncate()
                                    .text_size(px(12.5))
                                    .text_color(c("f_text"))
                                    .child(link.title.clone()),
                            )
                            .child(ui::status_pill(&link.status)),
                    )
                    .children(link.mentions.iter().take(SHOWN_MENTIONS).map(|m| {
                        h_flex()
                            .pl(px(104.0))
                            .gap(px(8.0))
                            .text_size(px(11.5))
                            .child(
                                div()
                                    .flex_none()
                                    .text_color(c("f_muted"))
                                    .child(mention_source(m)),
                            )
                            .child(
                                div()
                                    .flex_1()
                                    .min_w_0()
                                    .truncate()
                                    .text_color(c("f_text").opacity(0.75))
                                    .child(m.excerpt.clone()),
                            )
                    }))
                    .when(more > 0, |d| {
                        d.child(
                            div()
                                .pl(px(104.0))
                                .text_size(px(11.5))
                                .text_color(c("f_muted"))
                                .child(tf("link_more_mentions", &[("n", &more.to_string())])),
                        )
                    })
                    .into_any_element()
            });
        Some(
            v_flex()
                .id("thread-links")
                .mb(px(6.0))
                .pb(px(10.0))
                .border_b_1()
                .border_color(c("f_line"))
                .child(
                    div()
                        .px(px(8.0))
                        .pb(px(4.0))
                        .text_size(px(11.5))
                        .font_weight(FontWeight::MEDIUM)
                        .text_color(c("f_muted"))
                        .child(t("linked_requests")),
                )
                .children(rows)
                .into_any_element(),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::{link_label, link_refs, ordered, request_link};
    use crate::board::{Related, Request};
    use std::collections::HashSet;

    fn link(n: u64, kind: &str, incoming: bool) -> Related {
        Related {
            number: n,
            kind: kind.into(),
            incoming,
            ..Default::default()
        }
    }

    #[test]
    fn a_merge_leads_and_each_direction_has_its_label() {
        let related = vec![
            link(1, "related", false),
            link(2, "mention", true),
            link(3, "duplicate", true),
        ];
        let rows = ordered(&related);
        assert_eq!(rows.iter().map(|r| r.number).collect::<Vec<_>>(), [3, 1, 2]);
        assert_eq!(link_label(rows[0]), "link_merged_here");
        assert_eq!(
            link_label(&link(4, "duplicate", false)),
            "link_duplicate_of"
        );
        assert_eq!(link_label(rows[2]), "link_mentioned_in");
        assert_eq!(link_label(&link(5, "mention", false)), "link_mentions");
        assert_eq!(link_label(rows[1]), "link_related");
    }

    #[test]
    fn a_thread_reads_its_links_from_the_board() {
        let r: Request = serde_json::from_str(
            r#"{"number":7,"related":[{"number":3,"title":"Sync stalls","status":"resolved","kind":"duplicate"}]}"#,
        )
        .unwrap();
        assert_eq!(
            r.related,
            vec![Related {
                number: 3,
                title: "Sync stalls".into(),
                status: "resolved".into(),
                kind: "duplicate".into(),
                incoming: false
            }]
        );
    }

    #[test]
    fn references_in_text_become_links_outside_code() {
        let known: HashSet<u64> = [41, 59].into_iter().collect();
        assert_eq!(
            link_refs("Waiting on you: THI-41, THI-59, THI-99", "THI", &known),
            "Waiting on you: [THI-41](fennec://request/41), [THI-59](fennec://request/59), THI-99"
        );
        assert_eq!(
            link_refs(
                "XTHI-41 THI-41x `THI-41` [THI-41](https://x/THI-41)",
                "THI",
                &known
            ),
            "XTHI-41 THI-41x `THI-41` [THI-41](https://x/THI-41)"
        );
        assert_eq!(
            link_refs("```\nTHI-41\n```\n(THI-41)", "THI", &known),
            "```\nTHI-41\n```\n([THI-41](fennec://request/41))"
        );
        assert_eq!(request_link("fennec://request/41"), Some(41));
        assert_eq!(request_link("https://example.com"), None);
    }
}
