//! Projects and requests, stored on the cloud computer (docs/desktop-app.md).
//! On a Mac the daemon relays `board.call`; on the cloud computer itself the
//! window runs `fennec-terminal board-call` (FENNEC_BOARD_CMD overrides it).

use anyhow::{Context as _, Result, bail};
use serde::Deserialize;
use serde_json::{Value, json};
use std::io::Write;
use std::process::Stdio;

#[derive(Clone, Debug, Default, Deserialize, serde::Serialize, PartialEq)]
pub struct Tag {
    pub id: String,
    pub label: String,
}

#[derive(Clone, Debug, Default, Deserialize, serde::Serialize, PartialEq)]
#[serde(default)]
pub struct Worker {
    pub agent: String,
    pub model: String,
    pub on: bool,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Project {
    pub icon: String,
    pub id: String,
    pub org: String,
    pub key: String,
    pub name: String,
    pub description: String,
    pub repository: String,
    pub color: String,
    pub tags: Vec<Tag>,
    pub queue: usize,
    pub paused: bool,
    pub sync_status: String,
    /// When this Mac's board replica last synced with the cloud (RFC 3339).
    pub synced_at: String,
    pub workers: Vec<Worker>,
    pub open: usize,
    pub needs_you: usize,
}

/// An OKF concept agents wrote in the project's knowledge bundle.
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Concept {
    pub body: String,
    pub id: String,
    #[serde(rename = "type")]
    pub kind: String,
    pub title: String,
    pub description: String,
    pub tags: Vec<String>,
    pub sources: Vec<String>,
    pub status: String,
    pub generated_by: String,
    pub generated_at: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct QueueProject {
    pub project: String,
    pub limit: usize,
    pub working: usize,
    pub queued: Vec<Request>,
}

/// The project colors new projects take in turn (the mockup's).
pub const COLORS: &[&str] = &[
    "#f38d25", "#d97b6f", "#c9a23d", "#5fb08a", "#4fa3b5", "#6f8fd9", "#9a7bd1", "#8a8f98",
];

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Message {
    pub id: u64,
    pub author: String,
    pub agent: String,
    pub body: String,
    pub at: String,
    pub attachments: Vec<Attachment>,
}

#[derive(Clone, Debug, Default, Deserialize, serde::Serialize)]
#[serde(default)]
pub struct Attachment {
    pub id: String,
    pub name: String,
    pub media_type: String,
    pub size: u64,
    pub width: u32,
    pub height: u32,
    pub thumbnail: String,
}

/// `failure` of a request whose agent stopped without replying
/// (board.StalledFailure); shown localized.
pub const STALLED_FAILURE: &str = "The agent stopped without reporting back";

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Request {
    pub number: u64,
    pub title: String,
    /// The first tag; `tags` has them all once there are several (THI-133).
    /// Read them with `tag_list`.
    pub tag: String,
    pub tags: Vec<String>,
    pub status: String,
    pub author: String,
    pub created: String,
    pub updated: String,
    pub agent: String,
    pub session: String,
    pub run_location: String,
    pub failure: String,
    pub votes: u64,
    pub replies: u64,
    pub latest: Option<Message>,
    pub messages: Vec<Message>,
    pub runs: Vec<Run>,
    /// Links and backlinks to other requests of the queue (THI-103); only
    /// a single-request fetch carries them.
    pub related: Vec<Related>,
}

/// One linked request as the board reports it (board.Related): kind is
/// related, duplicate or mention; incoming means the other one links here.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Related {
    pub number: u64,
    pub title: String,
    pub status: String,
    pub kind: String,
    pub incoming: bool,
    /// Where an incoming mention was written (board.Mention), oldest first.
    pub mentions: Vec<Mention>,
}

/// One message of another request that mentions this one; message 0 is
/// that request's title.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Mention {
    pub message: u64,
    pub author: String,
    pub agent: String,
    pub at: String,
    pub excerpt: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Run {
    pub id: String,
    pub session: String,
    pub location: String,
    pub state: String,
}

impl Request {
    pub fn resolved(&self) -> bool {
        self.status == "resolved"
    }

    /// All of the request's tags, `tag` first (board.Request.TagList): just
    /// `tag` when `tags` is empty or a build without it changed `tag` since.
    pub fn tag_list(&self) -> Vec<String> {
        if self.tags.first() == Some(&self.tag) {
            self.tags.clone()
        } else {
            vec![self.tag.clone()]
        }
    }

    pub fn has_tag(&self, id: &str) -> bool {
        self.tag_list().iter().any(|t| t == id)
    }

    /// Stores `tags` (non-empty) the way the board does.
    pub fn set_tags(&mut self, tags: &[String]) {
        self.tag = tags[0].clone();
        self.tags = if tags.len() > 1 {
            tags.to_vec()
        } else {
            Vec::new()
        };
    }

    pub fn active_session<'a>(
        &self,
        sessions: &'a [crate::sessions::Session],
    ) -> Option<&'a crate::sessions::Session> {
        self.runs
            .iter()
            .rev()
            .find_map(|run| sessions.iter().find(|s| s.name == run.session))
            .or_else(|| sessions.iter().find(|s| s.name == self.session))
    }
}

#[cfg(test)]
mod thread_tests {
    #[test]
    fn thread_link_prefers_latest_active_conversation_over_sidebar_order() {
        let request: super::Request = serde_json::from_value(
            serde_json::json!({"runs":[{"session":"older"},{"session":"latest"}]}),
        )
        .unwrap();
        let sessions = crate::sessions::from_terminals(&[
            serde_json::json!({"address":"t1","state":"live","labels":{"fennec.session":"older"}}),
            serde_json::json!({"address":"t2","state":"live","labels":{"fennec.session":"latest"}}),
        ]);
        assert_eq!(request.active_session(&sessions).unwrap().address, "t2");
        assert_eq!(
            request.active_session(&sessions[..1]).unwrap().address,
            "t1"
        );
        assert!(request.active_session(&[]).is_none());
    }

    #[test]
    fn tag_list_reads_old_and_new_requests() {
        let old: super::Request = serde_json::from_value(serde_json::json!({"tag":"bug"})).unwrap();
        assert_eq!(old.tag_list(), ["bug"]);
        let mut r: super::Request =
            serde_json::from_value(serde_json::json!({"tag":"bug","tags":["bug","desktop"]}))
                .unwrap();
        assert_eq!(r.tag_list(), ["bug", "desktop"]);
        assert!(r.has_tag("desktop") && !r.has_tag("ios"));
        r.tag = "ios".into(); // changed by a build without tags
        assert_eq!(r.tag_list(), ["ios"]);
        r.set_tags(&["feature".into()]);
        assert!(r.tags.is_empty() && r.tag == "feature");
    }
}

fn command() -> Option<String> {
    if let Some(c) = std::env::var_os("FENNEC_BOARD_CMD") {
        return Some(c.to_string_lossy().into_owned());
    }
    let home = std::env::var_os("HOME")?;
    let local = std::path::Path::new(&home).join(".local/fennec/bin/fennec-terminal");
    local.exists().then(|| local.to_string_lossy().into_owned())
}

/// One board call. Blocking: run it from a background task.
pub fn call(method: &str, path: &str, body: Option<Value>) -> Result<Value> {
    if let Some(result) = crate::preview::call(method, path) {
        return result;
    }
    if crate::control::available() {
        return crate::control::call(
            if crate::core_bin::cloud() {
                "board.call"
            } else {
                "board.local.call"
            },
            json!({"method": method, "path": path, "body": body}),
        );
    }
    let Some(program) = command() else {
        bail!("Fennec isn't running")
    };
    let mut child = crate::spawn::command(&program)
        .args(["board-call", method, path])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .with_context(|| format!("cannot run {program}"))?;
    if let Some(body) = body {
        child
            .stdin
            .take()
            .unwrap()
            .write_all(&serde_json::to_vec(&body)?)?;
    }
    drop(child.stdin.take());
    let out = child.wait_with_output()?;
    if !out.status.success() {
        bail!(
            "{}",
            String::from_utf8_lossy(&out.stderr)
                .trim()
                .trim_start_matches("board-call: ")
        );
    }
    Ok(serde_json::from_slice(&out.stdout)?)
}

pub fn projects() -> Result<Vec<Project>> {
    Ok(serde_json::from_value(call("GET", "/v1/projects", None)?)?)
}

pub fn requests(project: &str) -> Result<Vec<Request>> {
    Ok(serde_json::from_value(call(
        "GET",
        &format!("/v1/projects/{project}/requests"),
        None,
    )?)?)
}

pub fn request(project: &str, n: u64) -> Result<Request> {
    Ok(serde_json::from_value(call(
        "GET",
        &format!("/v1/projects/{project}/requests/{n}"),
        None,
    )?)?)
}

/// A new request: the whole text is its first message (the request itself);
/// its first line is only a placeholder title until the agent's session
/// names the work, which the queue scheduler then copies onto the thread.
pub fn post(
    project: &str,
    text: &str,
    author: &str,
    attachments: &[Attachment],
) -> Result<Request> {
    let text = text.trim();
    let first = text.lines().next().unwrap_or_default().trim();
    let title: String = if first.is_empty() {
        crate::i18n::t("image_request")
    } else {
        first.chars().take(200).collect()
    };
    let body = json!({"title": title, "body": text, "author": author, "attachments":attachments});
    Ok(serde_json::from_value(call(
        "POST",
        &format!("/v1/projects/{project}/requests"),
        Some(body),
    )?)?)
}

pub fn continue_request(
    project: &str,
    n: u64,
    text: &str,
    author: &str,
    images: &[Attachment],
) -> Result<Value> {
    call(
        "POST",
        &format!("/v1/projects/{project}/requests/{n}/continue"),
        Some(json!({"body":text.trim(),"author":author,"attachments":images})),
    )
}

pub fn move_request(project: &str, n: u64, before: u64) -> Result<Value> {
    call(
        "POST",
        &format!("/v1/projects/{project}/requests/{n}/move"),
        Some(json!({"before":before})),
    )
}

/// THI-133: replaces all of request `n`'s tags.
pub fn set_tags(project: &str, n: u64, tags: &[String]) -> Result<Value> {
    call(
        "PATCH",
        &format!("/v1/projects/{project}/requests/{n}"),
        Some(json!({"tags": tags})),
    )
}

pub fn set_status(project: &str, n: u64, status: &str) -> Result<Value> {
    call(
        "PATCH",
        &format!("/v1/projects/{project}/requests/{n}"),
        Some(json!({"status": status})),
    )
}

/// THI-103: link request `n` to request `to` of the same queue as `kind`
/// ("related" or "duplicate"), or remove that link (kind "").
pub fn set_link(project: &str, n: u64, to: u64, kind: &str, author: &str) -> Result<Value> {
    call(
        "POST",
        &format!("/v1/projects/{project}/requests/{n}/links"),
        Some(json!({"number": to, "kind": kind, "author": author})),
    )
}

pub fn create_project(
    name: &str,
    org: &str,
    description: &str,
    repository: &str,
    color: &str,
    agent: &str,
) -> Result<Project> {
    let body = json!({"name": name, "org": org, "description": description, "repository": repository, "color": color, "workers": [{"agent": agent, "model": "", "on": true}]});
    Ok(serde_json::from_value(call(
        "POST",
        "/v1/projects",
        Some(body),
    )?)?)
}

/// Changes some of a project's settings (only the fields in `change`).
pub fn update_project(id: &str, change: Value) -> Result<Project> {
    Ok(serde_json::from_value(call(
        "PATCH",
        &format!("/v1/projects/{id}"),
        Some(change),
    )?)?)
}

pub fn knowledge(id: &str) -> Result<Vec<Concept>> {
    Ok(serde_json::from_value(call(
        "GET",
        &format!("/v1/projects/{id}/knowledge"),
        None,
    )?)?)
}

pub fn queue() -> Result<Vec<QueueProject>> {
    Ok(serde_json::from_value(call("GET", "/v1/queue", None)?)?)
}

// What the window asks the Mac daemon besides the board.

/// Starts a session on the cloud computer (no Mac folder); its name.
pub fn start_session(agent: &str) -> Result<String> {
    let r = crate::control::call(
        "sessions.start",
        json!({"agent": agent, "project": "", "model": "", "message": ""}),
    )?;
    Ok(r["session_id"].as_str().unwrap_or_default().to_owned())
}

/// The working directory for an agent session started on this Mac: a folder
/// Fennec made and Claude Code trusts, so it doesn't stop at "Quick safety
/// check" (it never remembers trust for the home folder). None keeps the home
/// folder, also when the daemon can't be asked.
pub fn local_folder(agent: &str, name: &str) -> Option<String> {
    let r = crate::control::call(
        "sessions.local_folder",
        json!({"agent": agent, "name": name}),
    )
    .ok()?;
    r["directory"]
        .as_str()
        .filter(|d| !d.is_empty())
        .map(str::to_owned)
}

/// The agents a session or worker can run (internal/board Agents).
pub const AGENTS: &[&str] = &[
    "claude", "codex", "kimi", "opencode", "cursor", "grok", "pi", "agy",
];
