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
    pub id: String,
    pub org: String,
    pub key: String,
    pub name: String,
    pub description: String,
    pub repository: String,
    pub color: String,
    pub tags: Vec<Tag>,
    pub queue: usize,
    pub workers: Vec<Worker>,
    pub open: usize,
    pub needs_you: usize,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Actor {
    pub by: String,
    pub at: String,
}

/// An OKF concept agents wrote in the project's knowledge bundle.
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Concept {
    pub id: String,
    #[serde(rename = "type")]
    pub kind: String,
    pub title: String,
    pub description: String,
    pub tags: Vec<String>,
    pub sources: Vec<String>,
    pub status: String,
    pub generated_by: String,
    pub verified: Vec<Actor>,
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
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Request {
    pub number: u64,
    pub title: String,
    pub tag: String,
    pub status: String,
    pub author: String,
    pub created: String,
    pub updated: String,
    pub agent: String,
    pub session: String,
    pub votes: u64,
    pub replies: u64,
    pub latest: Option<Message>,
    pub messages: Vec<Message>,
}

impl Request {
    pub fn resolved(&self) -> bool {
        self.status == "resolved"
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

/// A new request: its first line is the title; with more lines the whole
/// text is its first message.
pub fn post(project: &str, text: &str, author: &str) -> Result<Request> {
    let text = text.trim();
    let (title, rest) = text.split_once('\n').unwrap_or((text, ""));
    let title: String = title.trim().chars().take(200).collect();
    let body = json!({"title": title, "body": if rest.trim().is_empty() { "" } else { text }, "author": author});
    Ok(serde_json::from_value(call(
        "POST",
        &format!("/v1/projects/{project}/requests"),
        Some(body),
    )?)?)
}

pub fn reply(project: &str, n: u64, text: &str, author: &str) -> Result<Value> {
    call(
        "POST",
        &format!("/v1/projects/{project}/requests/{n}/messages"),
        Some(json!({"body": text.trim(), "author": author})),
    )
}

pub fn set_status(project: &str, n: u64, status: &str) -> Result<Value> {
    call(
        "PATCH",
        &format!("/v1/projects/{project}/requests/{n}"),
        Some(json!({"status": status})),
    )
}

pub fn create_project(
    name: &str,
    org: &str,
    description: &str,
    repository: &str,
    color: &str,
) -> Result<Project> {
    let body = json!({"name": name, "org": org, "description": description, "repository": repository, "color": color, "workers": [{"agent": "claude", "model": "", "on": true}]});
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

/// `action`: "review", "deprecate" or "restore".
pub fn review(id: &str, concept: &str, action: &str, person: &str) -> Result<Value> {
    call(
        "POST",
        &format!("/v1/projects/{id}/knowledge/review"),
        Some(json!({"id": concept, "action": action, "person": person})),
    )
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

/// The agents a session or worker can run (internal/board Agents).
pub const AGENTS: &[&str] = &[
    "claude", "codex", "kimi", "opencode", "cursor", "grok", "pi",
];
