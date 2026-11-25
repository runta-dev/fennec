//! The one-line preview under a queue card's title (THI-171).
//!
//! The card shows the first line of the request's latest message. Messages
//! are markdown, so showing that line verbatim put `**结论：…**` markers on
//! the card. This turns the line into plain text plus the inline styles
//! (bold, italic, code, strikethrough) to draw it with, and drops the block
//! markers (headings, quotes, list bullets) that mean nothing on one line.

use std::ops::Range;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Mark {
    Bold,
    Italic,
    Code,
    Strike,
}

#[derive(Debug, Default, PartialEq)]
pub(super) struct Preview {
    pub text: String,
    pub marks: Vec<(Range<usize>, Mark)>,
}

impl Preview {
    /// Text that is not markdown, like a stalled request's reason.
    pub fn plain(text: String) -> Self {
        Self { text, marks: Vec::new() }
    }

    /// The line as a text element; color and size come from the parent.
    pub fn styled(self) -> gpui::StyledText {
        use crate::ui::c;
        use gpui::{FontStyle, FontWeight, HighlightStyle, StrikethroughStyle, px};
        let highlights = self.marks.into_iter().map(|(range, mark)| {
            let style = match mark {
                // Bold also takes the full text color so it reads on the muted line.
                Mark::Bold => HighlightStyle {
                    font_weight: Some(FontWeight::SEMIBOLD),
                    color: Some(c("f_text")),
                    ..Default::default()
                },
                Mark::Italic => HighlightStyle {
                    font_style: Some(FontStyle::Italic),
                    ..Default::default()
                },
                // Same chip as inline code in thread messages (ui::markdown_style).
                Mark::Code => HighlightStyle {
                    color: Some(c("f_text")),
                    background_color: Some(c("f_line")),
                    ..Default::default()
                },
                Mark::Strike => HighlightStyle {
                    strikethrough: Some(StrikethroughStyle {
                        thickness: px(1.0),
                        color: None,
                    }),
                    ..Default::default()
                },
            };
            (range, style)
        });
        gpui::StyledText::new(self.text).with_highlights(highlights.collect::<Vec<_>>())
    }
}

/// The first line of `body` with content, rendered from markdown.
pub(super) fn preview(body: &str) -> Preview {
    let mut in_fence = false;
    for line in body.lines() {
        let t = line.trim();
        if t.starts_with("```") || t.starts_with("~~~") {
            // A fence's own line has no text; its first code line does.
            in_fence = !in_fence;
            continue;
        }
        if t.is_empty() || (!in_fence && is_rule(t)) {
            continue;
        }
        if in_fence {
            return Preview {
                text: t.to_owned(),
                marks: vec![(0..t.len(), Mark::Code)],
            };
        }
        let p = inline(strip_block(t));
        if !p.text.trim().is_empty() {
            return p;
        }
    }
    Preview::default()
}

/// `---`, `***`, `___` and table separators like `|---|:--:|`.
fn is_rule(t: &str) -> bool {
    let c: String = t.chars().filter(|c| !c.is_whitespace()).collect();
    (c.len() >= 3 && ["-", "*", "_"].iter().any(|m| c.chars().all(|x| x.to_string() == *m)))
        || (c.contains('-') && c.chars().all(|x| matches!(x, '|' | '-' | ':')))
}

/// Drop heading, quote, list and task markers from the start of a line.
fn strip_block(mut t: &str) -> &str {
    loop {
        let before = t;
        if let Some(rest) = t.strip_prefix('>') {
            t = rest.trim_start();
        }
        let hashes = t.len() - t.trim_start_matches('#').len();
        if (1..=6).contains(&hashes) && t[hashes..].starts_with(' ') {
            t = t[hashes..].trim_start();
        }
        for bullet in ["- ", "* ", "+ "] {
            if let Some(rest) = t.strip_prefix(bullet) {
                t = rest.trim_start();
            }
        }
        let digits = t.len() - t.trim_start_matches(|c: char| c.is_ascii_digit()).len();
        if digits > 0 && (t[digits..].starts_with(". ") || t[digits..].starts_with(") ")) {
            t = t[digits + 1..].trim_start();
        }
        for task in ["[ ] ", "[x] ", "[X] "] {
            if let Some(rest) = t.strip_prefix(task) {
                t = rest.trim_start();
            }
        }
        if t == before {
            return t;
        }
    }
}

/// Inline markdown of one line: emphasis, code spans, links, escapes.
fn inline(src: &str) -> Preview {
    let mut out = Preview::default();
    // Open delimiters: (marker, mark, start offset in out.text).
    let mut open: Vec<(&'static str, Mark, usize)> = Vec::new();
    let mut i = 0;
    while i < src.len() {
        let rest = &src[i..];
        // `\*` is a literal star.
        if let Some(c) = rest.strip_prefix('\\').and_then(|r| r.chars().next())
            && c.is_ascii_punctuation()
        {
            out.text.push(c);
            i += 1 + c.len_utf8();
            continue;
        }
        // `code` runs to the matching run of backticks.
        if rest.starts_with('`') {
            let ticks = rest.len() - rest.trim_start_matches('`').len();
            let fence = &rest[..ticks];
            if let Some(end) = rest[ticks..].find(fence) {
                let code = rest[ticks..ticks + end].trim();
                let start = out.text.len();
                out.text.push_str(code);
                out.marks.push((start..out.text.len(), Mark::Code));
                i += ticks + end + ticks;
                continue;
            }
        }
        // [text](url) and ![alt](url) keep only their text.
        let bang = rest.starts_with("![");
        if (bang || rest.starts_with('['))
            && let Some((label, used)) = link(&rest[bang as usize..])
        {
            let inner = inline(label);
            let base = out.text.len();
            out.text.push_str(&inner.text);
            out.marks
                .extend(inner.marks.into_iter().map(|(r, m)| (r.start + base..r.end + base, m)));
            i += bang as usize + used;
            continue;
        }
        // <https://…> autolinks.
        if rest.starts_with('<')
            && let Some(end) = rest.find('>')
            && rest[1..end].contains("://")
            && !rest[1..end].contains(' ')
        {
            out.text.push_str(&rest[1..end]);
            i += end + 1;
            continue;
        }
        if let Some((marker, mark)) = delimiter(src, i, &open) {
            if let Some(pos) = open.iter().rposition(|(m, _, _)| *m == marker) {
                let (_, mark, start) = open.remove(pos);
                open.truncate(pos);
                if out.text.len() > start {
                    out.marks.push((start..out.text.len(), mark));
                }
            } else {
                open.push((marker, mark, out.text.len()));
            }
            i += marker.len();
            continue;
        }
        let c = rest.chars().next().unwrap_or_default();
        out.text.push(c);
        i += c.len_utf8();
    }
    // An unclosed marker was literal text after all: put it back.
    for (marker, _, start) in open.into_iter().rev() {
        out.text.insert_str(start, marker);
        for (r, _) in &mut out.marks {
            if r.start >= start {
                r.start += marker.len();
            }
            if r.end > start {
                r.end += marker.len();
            }
        }
    }
    out.marks.sort_by_key(|(r, _)| r.start);
    out
}

/// An emphasis marker at `i` that may open or close a span.
fn delimiter(src: &str, i: usize, open: &[(&'static str, Mark, usize)]) -> Option<(&'static str, Mark)> {
    const MARKERS: [(&str, Mark); 6] = [
        ("***", Mark::Bold),
        ("**", Mark::Bold),
        ("__", Mark::Bold),
        ("~~", Mark::Strike),
        ("*", Mark::Italic),
        ("_", Mark::Italic),
    ];
    let rest = &src[i..];
    let (marker, mark) = MARKERS.into_iter().find(|(m, _)| rest.starts_with(m))?;
    // `***x***` is bold italic; keep it simple and treat it as bold.
    let before = src[..i].chars().next_back();
    let after = rest[marker.len()..].chars().next();
    let closing = open.iter().any(|(m, _, _)| *m == marker);
    if closing {
        // A closer follows text, not space: `**a **` stays literal.
        return before.filter(|c| !c.is_whitespace()).map(|_| (marker, mark));
    }
    // An opener precedes text.
    after.filter(|c| !c.is_whitespace())?;
    // snake_case and 2*3*4 are not emphasis.
    if before.is_some_and(|c| c.is_alphanumeric()) && (marker.starts_with('_') || marker == "*") {
        return None;
    }
    Some((marker, mark))
}

/// `[label](target)` at the start of `s`: the label and bytes consumed.
fn link(s: &str) -> Option<(&str, usize)> {
    let mut depth = 0;
    let mut close = None;
    for (j, c) in s.char_indices() {
        match c {
            '[' => depth += 1,
            ']' => {
                depth -= 1;
                if depth == 0 {
                    close = Some(j);
                    break;
                }
            }
            _ => {}
        }
    }
    let close = close?;
    let after = s[close + 1..].strip_prefix('(')?;
    let end = after.find(')')?;
    Some((&s[1..close], close + 1 + 1 + end + 1))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn styled(p: &Preview) -> Vec<(&str, Mark)> {
        p.marks.iter().map(|(r, m)| (&p.text[r.clone()], *m)).collect()
    }

    #[test]
    fn bold_markers_become_a_bold_run() {
        let p = preview("**结论：只解决了一半。** \"派出去\"这一半做得很好");
        assert_eq!(p.text, "结论：只解决了一半。 \"派出去\"这一半做得很好");
        assert_eq!(styled(&p), vec![("结论：只解决了一半。", Mark::Bold)]);
    }

    #[test]
    fn inline_code_italic_strike_and_links() {
        let p = preview("Use `fen start` and *then* ~~not~~ see [the docs](https://x.y/z).");
        assert_eq!(p.text, "Use fen start and then not see the docs.");
        assert_eq!(
            styled(&p),
            vec![("fen start", Mark::Code), ("then", Mark::Italic), ("not", Mark::Strike)]
        );
    }

    #[test]
    fn block_markers_and_blank_lines_are_skipped() {
        assert_eq!(preview("\n\n## Summary\nbody").text, "Summary");
        assert_eq!(preview("> - [x] **done** it").text, "done it");
        assert_eq!(preview("1. first").text, "first");
        assert_eq!(preview("---\n\nreal").text, "real");
        assert_eq!(preview("|---|---|\n| a |").text, "| a |");
    }

    #[test]
    fn code_fence_shows_its_first_line() {
        let p = preview("```sh\nfen start\n```");
        assert_eq!(p.text, "fen start");
        assert_eq!(styled(&p), vec![("fen start", Mark::Code)]);
    }

    #[test]
    fn literal_markers_stay() {
        assert_eq!(preview("snake_case_name").text, "snake_case_name");
        assert_eq!(preview("2*3*4 = 24").text, "2*3*4 = 24");
        assert_eq!(preview("a ** b").text, "a ** b");
        assert_eq!(preview("**unclosed bold").text, "**unclosed bold");
        assert_eq!(preview(r"\*not italic\*").text, "*not italic*");
        let p = preview("**open** and **unclosed");
        assert_eq!(p.text, "open and **unclosed");
        assert_eq!(styled(&p), vec![("open", Mark::Bold)]);
    }
}
