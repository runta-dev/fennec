//! References to the queue's knowledge in a thread message (THI-117).
//!
//! Agents name the entries they wrote or read the way a shell shows them:
//! `thread-mirror-and-links.md`, `architecture/x.md`, a full
//! `~/.fen/board/projects/<id>/knowledge/…` path, or an OKF `[[name]]` link.
//! Each one that names an entry of the queue's bundle becomes a link that
//! shows the entry's title and opens it in Queue settings › Knowledge.
use super::*;
use std::time::{Duration, Instant};

/// The URL a knowledge reference links to; the concept id follows.
const KNOWLEDGE_URL: &str = "fennec://knowledge/";

/// How long a queue's fetched titles count as current while a thread is open.
const STALE_AFTER: Duration = Duration::from_secs(30);

/// One entry of a queue's knowledge, as references resolve it.
#[derive(Clone, Debug, PartialEq)]
pub(crate) struct KnowledgeRef {
    /// Path inside the bundle without ".md", such as "runbooks/attach-eof".
    pub id: String,
    pub title: String,
}

/// The queue's knowledge titles and when they were fetched.
#[derive(Default)]
pub(crate) struct KnowledgeRefs {
    by_project: HashMap<String, (Instant, Vec<KnowledgeRef>)>,
}

impl KnowledgeRefs {
    pub(crate) fn get(&self, project: &str) -> &[KnowledgeRef] {
        self.by_project
            .get(project)
            .map(|(_, list)| list.as_slice())
            .unwrap_or_default()
    }

    /// Whether a refresh should fetch this queue's knowledge again.
    pub(crate) fn stale(&self, project: &str) -> bool {
        self.by_project
            .get(project)
            .is_none_or(|(at, _)| at.elapsed() >= STALE_AFTER)
    }

    pub(crate) fn store(&mut self, project: String, concepts: &[board::Concept]) {
        let list = concepts
            .iter()
            .map(|k| KnowledgeRef {
                id: k.id.clone(),
                title: k.title.clone(),
            })
            .collect();
        self.by_project.insert(project, (Instant::now(), list));
    }
}

/// The concept id a link in a message opens, if it is a knowledge link.
pub(super) fn knowledge_link(url: &str) -> Option<&str> {
    url.strip_prefix(KNOWLEDGE_URL).filter(|id| !id.is_empty())
}

/// The entry a reference names. `name` is a file name or path ending in
/// ".md" (`bare` false: an OKF `[[name]]`, where ".md" is optional). A path
/// resolves from its last "knowledge/" on and must then be an entry's whole
/// id; a plain name matches the one entry with that file name. Other paths
/// (URLs, files in a repository) never match.
fn resolve<'a>(name: &str, bare: bool, known: &'a [KnowledgeRef]) -> Option<&'a KnowledgeRef> {
    let name = name.trim();
    let stem = match name.strip_suffix(".md") {
        Some(stem) => stem,
        None if !bare => name,
        None => return None,
    };
    let rest = match stem.rfind("knowledge/") {
        Some(i) => &stem[i + "knowledge/".len()..],
        None => stem,
    };
    if rest.is_empty() {
        return None;
    }
    if let Some(k) = known.iter().find(|k| k.id == rest) {
        return Some(k);
    }
    if rest.contains('/') {
        return None;
    }
    let mut named = known
        .iter()
        .filter(|k| k.id.rsplit('/').next() == Some(rest));
    match (named.next(), named.next()) {
        (Some(k), None) => Some(k),
        _ => None,
    }
}

/// The markdown link a resolved reference becomes.
fn link(k: &KnowledgeRef) -> String {
    let mut title = String::with_capacity(k.title.len());
    for ch in if k.title.trim().is_empty() { &k.id } else { k.title.trim() }.chars() {
        if matches!(ch, '\\' | '[' | ']' | '*' | '_' | '`' | '<') {
            title.push('\\');
        }
        title.push(ch);
    }
    format!("[{title}]({KNOWLEDGE_URL}{})", k.id)
}

/// Characters a bare file path is made of.
fn path_char(c: char) -> bool {
    c.is_ascii_alphanumeric() || matches!(c, '_' | '-' | '.' | '/' | '~')
}

/// `body` with every reference to an entry of `known` turned into a link
/// showing its title. Fenced code, the text and target of existing links,
/// and inline code naming anything else are left as written.
pub(super) fn link_knowledge(body: &str, known: &[KnowledgeRef]) -> String {
    if known.is_empty() || !(body.contains(".md") || body.contains("[[")) {
        return body.to_owned();
    }
    let mut out = String::with_capacity(body.len());
    let mut fence = false;
    for (index, line) in body.split('\n').enumerate() {
        if index > 0 {
            out.push('\n');
        }
        if line.trim_start().starts_with("```") {
            fence = !fence;
            out.push_str(line);
            continue;
        }
        if fence {
            out.push_str(line);
            continue;
        }
        let (mut bracket, mut target) = (0usize, false);
        let mut prev: Option<char> = None;
        let mut i = 0;
        while i < line.len() {
            let rest = &line[i..];
            let ch = rest.chars().next().unwrap_or_default();
            let free = bracket == 0 && !target;
            // Inline code: the whole span, backticks included, is the reference.
            if ch == '`' {
                let ticks = rest.chars().take_while(|c| *c == '`').count();
                let fence = &rest[..ticks];
                if let Some(end) = rest[ticks..].find(fence) {
                    let span = &rest[..ticks + end + ticks];
                    match resolve(&rest[ticks..ticks + end], true, known) {
                        Some(k) if free => out.push_str(&link(k)),
                        _ => out.push_str(span),
                    }
                    i += span.len();
                    prev = Some('`');
                    continue;
                }
            }
            if free && rest.starts_with("[[") {
                if let Some(end) = rest[2..].find("]]")
                    && let Some(k) = resolve(&rest[2..2 + end], false, known)
                {
                    out.push_str(&link(k));
                    i += 2 + end + 2;
                    prev = Some(']');
                    continue;
                }
            }
            if free && path_char(ch) && prev.is_none_or(|p| !path_char(p) && p != ':') {
                let run = rest.find(|c| !path_char(c)).unwrap_or(rest.len());
                let token = rest[..run].trim_end_matches('.');
                if let Some(k) = resolve(token, true, known) {
                    out.push_str(&link(k));
                    i += token.len();
                    prev = Some('d');
                    continue;
                }
                out.push_str(&rest[..run]);
                prev = rest[..run].chars().last();
                i += run;
                continue;
            }
            match ch {
                '\\' => {
                    // An escaped character is plain text.
                    out.push(ch);
                    i += 1;
                    if let Some(next) = line[i..].chars().next() {
                        out.push(next);
                        i += next.len_utf8();
                        prev = Some(next);
                    }
                    continue;
                }
                '[' if !target => bracket += 1,
                ']' if bracket > 0 => {
                    bracket -= 1;
                    target = bracket == 0 && rest[1..].starts_with('(');
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
    /// Opens an entry of a queue's knowledge in Queue settings › Knowledge.
    pub(crate) fn open_concept(
        &mut self,
        project: String,
        id: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.go(
            Route::Settings(project, crate::project_settings::Tab::Knowledge),
            window,
            cx,
        );
        self.settings.show_concept(id);
        cx.notify();
    }
}

#[cfg(test)]
mod tests {
    use super::{KnowledgeRef, knowledge_link, link_knowledge};

    fn known() -> Vec<KnowledgeRef> {
        [
            ("architecture/thread-mirror-and-links", "Threads link [like] GitHub"),
            ("runbooks/install-desktop-on-mac", "Installing on the Mac"),
            ("architecture/install-desktop-on-mac", "Same file name elsewhere"),
            ("decisions/finish-means-merged", "Merge then install"),
        ]
        .map(|(id, title)| KnowledgeRef {
            id: id.into(),
            title: title.into(),
        })
        .into_iter()
        .collect()
    }

    #[test]
    fn knowledge_references_show_their_title() {
        let k = known();
        let threads = r"[Threads link \[like\] GitHub](fennec://knowledge/architecture/thread-mirror-and-links)";
        assert_eq!(
            link_knowledge("知识库 `thread-mirror-and-links.md` 已补充。", &k),
            format!("知识库 {threads} 已补充。")
        );
        assert_eq!(
            link_knowledge(
                "See ~/.fen/board/projects/this/knowledge/architecture/thread-mirror-and-links.md.",
                &k
            ),
            format!("See {threads}.")
        );
        assert_eq!(
            link_knowledge("[[finish-means-merged]] and decisions/finish-means-merged.md", &k),
            "[Merge then install](fennec://knowledge/decisions/finish-means-merged) and [Merge then install](fennec://knowledge/decisions/finish-means-merged)"
        );
        // Ambiguous names, other files, URLs, links and fenced code stay.
        let kept = "`install-desktop-on-mac.md` AGENTS.md `x/thread-mirror-and-links.md` \
                    https://x.dev/thread-mirror-and-links.md [a](b/thread-mirror-and-links.md)";
        assert_eq!(link_knowledge(kept, &k), kept);
        let fenced = "```\nthread-mirror-and-links.md\n```";
        assert_eq!(link_knowledge(fenced, &k), fenced);
        assert_eq!(
            link_knowledge("runbooks/install-desktop-on-mac.md", &k),
            "[Installing on the Mac](fennec://knowledge/runbooks/install-desktop-on-mac)"
        );
        assert_eq!(
            knowledge_link("fennec://knowledge/runbooks/x"),
            Some("runbooks/x")
        );
        assert_eq!(knowledge_link("fennec://request/4"), None);
    }
}
