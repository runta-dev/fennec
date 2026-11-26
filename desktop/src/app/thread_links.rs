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
    let when = crate::local_time::month_day_clock(&m.at);
    format!("{who} · {when}")
}

/// The request number a link in a message opens, if it is a KEY-n link.
pub(super) fn request_link(url: &str) -> Option<u64> {
    url.strip_prefix(REQUEST_URL)?.parse().ok()
}

/// Separators of a list of requests after a KEY-n ("THI-107、117、119"); spaces
/// may surround them. The board reads the same grammar
/// (internal/board/refs.go); keep the two in step.
const LIST_SEPS: [&str; 13] = [
    ",", "，", "、", "/", "&", "~", "～", "–", "—", "和", "及", "与", "或",
];

/// Separators that need spaces on both sides.
const LIST_WORDS: [&str; 2] = ["and", "or"];

/// Words that make a number after a reference a count ("THI-41、3 files"
/// written with a Chinese measure word).
const UNITS: &str = "个条次份行项天秒分时处位张件列页字号年月日周倍万千百元块台名人遍轮步%‰°";

fn word(c: u8) -> bool {
    c.is_ascii_alphanumeric() || c == b'_'
}

fn digit_at(s: &str, i: usize) -> bool {
    s.as_bytes().get(i).is_some_and(u8::is_ascii_digit)
}

fn skip_blanks(s: &str, mut i: usize) -> usize {
    while matches!(s.as_bytes().get(i), Some(b' ' | b'\t')) {
        i += 1;
    }
    i
}

/// 1 to 7 digits at `i`: the number and where it ends.
fn number_at(s: &str, i: usize) -> Option<(u64, usize)> {
    let len = s[i..].bytes().take_while(u8::is_ascii_digit).count();
    (len > 0 && len <= 7)
        .then(|| s[i..i + len].parse().ok().map(|n| (n, i + len)))
        .flatten()
}

/// The length of the list separator at `i`, blanks around it included.
fn list_sep(s: &str, i: usize) -> usize {
    let j = skip_blanks(s, i);
    if let Some(sep) = LIST_SEPS.iter().find(|sep| s[j..].starts_with(*sep)) {
        return skip_blanks(s, j + sep.len()) - i;
    }
    if j > i {
        for w in LIST_WORDS {
            if s[j..].starts_with(w) && matches!(s.as_bytes().get(j + w.len()), Some(b' ' | b'\t'))
            {
                return skip_blanks(s, j + w.len()) - i;
            }
        }
    }
    0
}

/// Whether a bare list item may end at `i`: not before a word, a count, or
/// the rest of a date, time or version.
fn list_end(s: &str, i: usize) -> bool {
    let Some(&c) = s.as_bytes().get(i) else {
        return true;
    };
    if word(c) || (b".:-".contains(&c) && digit_at(s, i + 1)) {
        return false;
    }
    if list_sep(s, i) > 0 {
        return true;
    }
    let j = skip_blanks(s, i);
    match s[j..].chars().next() {
        None | Some('\n' | '\r') => true,
        Some(c) if c.is_ascii() && word(c as u8) => false,
        Some(c) => !UNITS.contains(c),
    }
}

/// Reads the requests after "KEY-" (THI-117, THI-131): "73", the shorthand
/// "73/103/107", or a list "107、117、119", "41, 59 and 61", "117 and 119" (also
/// with the Chinese conjunctions),
/// "107~121" (both ends), whose items may repeat the key or start with '#'.
/// Returns each number with the bytes of its text, and the bytes used. A
/// number must end at a non-word character ("42x" is not a reference); a
/// bare list item must also not be a count, date or version ("THI-41, 3
/// files"), and the list stops at the first one that is.
fn ref_chain(s: &str, key: &str) -> Option<(Vec<(u64, usize, usize)>, usize)> {
    let tight_end = |end: usize| s.as_bytes().get(end).is_none_or(|c| !word(*c));
    let (first, end) = number_at(s, 0)?;
    if !tight_end(end) {
        return None;
    }
    let mut refs = vec![(first, 0, end)];
    let mut used = end;
    loop {
        let (mut j, mut loose) = (used + 1, false);
        if !s[used..].starts_with('/') {
            let sep = list_sep(s, used);
            if sep == 0 {
                break;
            }
            (j, loose) = (used + sep, true);
        }
        let start = j;
        if s[j..].starts_with(key) && s[j + key.len()..].starts_with('-') {
            (j, loose) = (j + key.len() + 1, false);
        } else if loose && s[j..].starts_with('#') {
            j += 1;
        }
        let Some((n, end)) = number_at(s, j) else {
            break;
        };
        if if loose {
            !list_end(s, end)
        } else {
            !tight_end(end)
        } {
            break;
        }
        refs.push((n, start, end));
        used = end;
    }
    Some((refs, used))
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
                && let Some((refs, used)) = ref_chain(&rest[key.len() + 1..], key)
                && refs.iter().any(|(n, _, _)| known.contains(n))
            {
                let s = &rest[key.len() + 1..];
                let mut from = 0;
                for (j, (n, start, end)) in refs.iter().enumerate() {
                    out.push_str(&s[from..*start]);
                    let text = if j == 0 {
                        &rest[..key.len() + 1 + end]
                    } else {
                        &s[*start..*end]
                    };
                    if known.contains(n) {
                        out.push_str(&format!("[{text}]({REQUEST_URL}{n})"));
                    } else {
                        out.push_str(text);
                    }
                    from = *end;
                }
                i += key.len() + 1 + used;
                prev = Some('0');
                continue;
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
    /// Opens request `n` scrolled to message `message`, highlighted (a
    /// mention row's source). Message 0 is the request's title.
    pub(super) fn open_request_at(
        &mut self,
        n: u64,
        message: u64,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.open_request(n, window, cx);
        if message > 0 {
            self.thread_jump = Some((n, message));
            self.thread_jump_frames.set(6);
        }
        cx.notify();
    }

    /// Links request `from` to `to` (kind "" removes the link), then
    /// refreshes the open thread.
    pub(super) fn set_link(
        &mut self,
        from: u64,
        to: u64,
        kind: &'static str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(project) = self.project_id() else {
            return;
        };
        let me = self.me.clone();
        let Some(scope) = self.board_scope.clone() else { return; };
        let worker_scope = scope.clone();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let done = cx
                .background_spawn(async move { worker_scope.run(|| { board::set_link(&project, from, to, kind, &me) }) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| { if this.board_scope.as_ref() != Some(&scope) { return; }
                if let Err(e) = done {
                    this.say(format!("{e:#}"), cx);
                }
                this.refresh_now(window, cx);
            });
        }));
    }

    pub(super) fn thread_links(
        &self,
        p: &Project,
        r: &Request,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        if r.related.is_empty() {
            return None;
        }
        // Rows with an explicit link end in an × button; the others keep an
        // empty slot of the same width so every status pill lines up.
        let unlinkable = r.related.iter().any(|l| l.kind != "mention");
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
                            .child(ui::status_pill(&link.status))
                            .when(link.kind != "mention", |d| {
                                let (from, to) = if link.incoming {
                                    (n, r.number)
                                } else {
                                    (r.number, n)
                                };
                                d.child(
                                    div()
                                        .id(SharedString::from(format!("unlink-{i}")))
                                        .flex_none()
                                        .size(px(20.0))
                                        .rounded(px(5.0))
                                        .flex()
                                        .items_center()
                                        .justify_center()
                                        .hover(|s| s.bg(c("f_line")))
                                        .on_click(cx.listener(move |this, _, window, cx| {
                                            cx.stop_propagation();
                                            this.set_link(from, to, "", window, cx)
                                        }))
                                        .child(ui::icon("close", 11.0, c("f_muted"))),
                                )
                            })
                            .when(unlinkable && link.kind == "mention", |d| {
                                d.child(div().flex_none().size(px(20.0)))
                            }),
                    )
                    .children(link.mentions.iter().take(SHOWN_MENTIONS).enumerate().map(
                        |(j, m)| {
                            let message = m.message;
                            // A mention line opens its thread at the message that wrote it.
                            h_flex()
                                .id(SharedString::from(format!("thread-link-{i}-{j}")))
                                .rounded(px(4.0))
                                .hover(|s| s.bg(c("f_line")))
                                .on_click(cx.listener(move |this, _, window, cx| {
                                    cx.stop_propagation();
                                    this.open_request_at(n, message, window, cx)
                                }))
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
                        },
                    ))
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
                incoming: false,
                mentions: vec![],
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
        let known: HashSet<u64> = [73, 103, 107].into_iter().collect();
        assert_eq!(
            link_refs(
                "正在跑的请求（THI-73/103/107）。THI-99/103x THI-99/107/",
                "THI",
                &known
            ),
            "正在跑的请求（[THI-73](fennec://request/73)/[103](fennec://request/103)/[107](fennec://request/107)）。THI-99/103x THI-99/[107](fennec://request/107)/"
        );
        let known: HashSet<u64> = [107, 117, 119, 121].into_iter().collect();
        assert_eq!(
            link_refs("4 个提交（THI-107、117、119、121），全是 desktop", "THI", &known),
            "4 个提交（[THI-107](fennec://request/107)、[117](fennec://request/117)、[119](fennec://request/119)、[121](fennec://request/121)），全是 desktop"
        );
        assert_eq!(
            link_refs("THI-107 / 117 和 THI-119 都修了，THI-121, 3 files", "THI", &known),
            "[THI-107](fennec://request/107) / [117](fennec://request/117) 和 [THI-119](fennec://request/119) 都修了，[THI-121](fennec://request/121), 3 files"
        );
        assert_eq!(
            link_refs("THI-107~121，THI-117、2026-11-25，THI-119, 0.1.13", "THI", &known),
            "[THI-107](fennec://request/107)~[121](fennec://request/121)，[THI-117](fennec://request/117)、2026-11-25，[THI-119](fennec://request/119), 0.1.13"
        );
        assert_eq!(
            link_refs("THI-117, 5 and #119; fix/thi-117", "THI", &known),
            "[THI-117](fennec://request/117), 5 and [#119](fennec://request/119); fix/thi-117"
        );
        assert_eq!(request_link("fennec://request/41"), Some(41));
        assert_eq!(request_link("https://example.com"), None);
    }
}
