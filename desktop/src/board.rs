//! Projects and requests, stored on the cloud computer (docs/desktop-app.md).
//! On a Mac the daemon relays `board.call`; on the cloud computer itself the
//! window runs `fennec-terminal board-call` (FENNEC_BOARD_CMD overrides it).

use anyhow::{Context as _, Result, bail};
use serde::Deserialize;
use serde_json::{Value, json};
use std::io::Write;
use std::process::Stdio;

/// Capture at the UI action, then keep every RPC in that action on its board.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Scope(pub String);

thread_local! {
    static CALL_SCOPE: std::cell::RefCell<Option<String>> = const { std::cell::RefCell::new(None) };
}

impl Scope {
    pub fn current() -> Result<Self> {
        if crate::preview::active() || !crate::control::available() || crate::core_bin::cloud() {
            return Ok(Self::default());
        }
        let value = crate::control::call("board.context", json!({}))?;
        let scope = value["scope"].as_str().context("missing queue scope")?;
        if scope != "local" && (scope.len() != 64 || !scope.bytes().all(|b| b.is_ascii_hexdigit())) {
            bail!("invalid queue scope");
        }
        Ok(Self(scope.to_owned()))
    }

    pub fn run<T>(&self, action: impl FnOnce() -> T) -> T {
        struct Restore(Option<String>);
        impl Drop for Restore {
            fn drop(&mut self) { CALL_SCOPE.with(|s| { s.replace(self.0.take()); }); }
        }
        let _restore = Restore(CALL_SCOPE.with(|s| s.replace(Some(self.0.clone()))));
        action()
    }
}

pub fn control_call(method: &str, mut params: Value) -> Result<Value> {
    if !crate::core_bin::cloud() {
        let scope = CALL_SCOPE.with(|s| s.borrow().clone()).context("queue scope not loaded")?;
        params["scope"] = json!(scope);
    }
    crate::control::call(method, params)
}

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

impl Project {
    pub fn display_name(&self) -> String {
        self.display_copy("starter_queue_name", &self.name, crate::i18n::language())
    }

    pub fn display_description(&self) -> String {
        self.display_copy("starter_queue_description", &self.description, crate::i18n::language())
    }

    fn display_copy(&self, key: &str, stored: &str, language: &str) -> String {
        if self.id == "starter" {
            crate::i18n::default_copy(key, stored, language)
        } else {
            stored.to_owned()
        }
    }

    pub fn default_icon(&self, dark: bool) -> Option<&'static str> {
        (self.id == "starter" && self.icon.is_empty()).then_some(if dark {
            "fennec-ios-dark.png"
        } else {
            "fennec-ios-light.png"
        })
    }
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
    /// Set on a person's answer to a permission prompt (THI-197): the thread
    /// draws it as a status line instead of the body.
    pub permission: Option<PermissionAnswer>,
}

#[derive(Clone, Debug, Default, Deserialize, serde::Serialize)]
#[serde(default)]
pub struct PermissionAnswer {
    pub allow: bool,
    /// What the agent asked to run: "Bash: rm -rf build".
    pub command: String,
    /// What to do instead, given with a denial.
    pub text: String,
}

#[derive(Clone, Debug, Default, Deserialize, serde::Serialize, PartialEq)]
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

/// `failure` of a request whose agent is waiting for an answer in its session
/// (board.AskingFailure, THI-163); shown localized.
pub const ASKING_FAILURE: &str = "The agent is asking a question in its session";

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
    /// What the agent of an asking run is asking in its session, and the
    /// answer given to it from the thread (board.Question/Answer, THI-176).
    pub question: Option<Question>,
    pub answer: Option<Answer>,
}

/// An agent's question in its session (board.Question): AskUserQuestion's
/// questions (`items`) or a permission prompt (`kind` "permission", `text`
/// "approve Bash: …").
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Question {
    pub id: String,
    pub app: String,
    pub kind: String,
    pub text: String,
    pub items: Vec<QuestionItem>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct QuestionItem {
    pub header: String,
    pub question: String,
    pub multi_select: bool,
    pub options: Vec<QuestionOption>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct QuestionOption {
    pub label: String,
    pub description: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Answer {
    pub question: String,
    pub sent: bool,
    /// A permission prompt's answer: allowed or denied.
    pub allow: Option<bool>,
}

impl Question {
    /// The scheduler can type an answer into the session (board.Question.Answerable).
    pub fn answerable(&self) -> bool {
        self.app == "claude-code" && matches!(self.kind.as_str(), "question" | "permission")
    }

    pub fn permission(&self) -> bool {
        self.kind == "permission"
    }

    /// What a permission prompt wants to run: "Bash: rm -rf build".
    pub fn command(&self) -> &str {
        self.text.strip_prefix("approve ").unwrap_or(&self.text)
    }

    /// The tool a permission prompt asks about and what it runs or touches.
    pub fn call(&self) -> Option<(String, String)> {
        split_call(self.command())
    }
}

/// "Bash: rm -rf build" as ("Bash", "rm -rf build"); a bare tool name has
/// nothing after it. None when the prompt didn't name its call: Claude Code's
/// status can say only "permission prompt" (THI-205), and answers stored
/// before the scheduler read the transcript keep those words.
pub fn split_call(command: &str) -> Option<(String, String)> {
    let command = command.trim();
    if command.is_empty() || command == "permission prompt" {
        return None;
    }
    let tool_name = |t: &str| !t.is_empty() && !t.contains(char::is_whitespace);
    match command.split_once(": ") {
        Some((tool, what)) if tool_name(tool) => Some((tool.to_owned(), what.to_owned())),
        _ if tool_name(command) => Some((command.to_owned(), String::new())),
        _ => Some((String::new(), command.to_owned())),
    }
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

    /// The question an asking run's agent waits on (THI-176): only while the
    /// request needs you and its run is "asking", so a stale one never shows.
    pub fn asking(&self) -> Option<&Question> {
        (self.status == "you" && self.runs.last().is_some_and(|run| run.state == "asking"))
            .then_some(self.question.as_ref())
            .flatten()
    }

    /// The asking question the thread can answer, unless already answered.
    pub fn answerable(&self) -> Option<&Question> {
        self.asking()
            .filter(|q| q.answerable() && self.answer.as_ref().is_none_or(|a| a.question != q.id))
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
    fn starter_defaults_follow_language_without_changing_saved_copy() {
        let mut project = super::Project {
            id: "starter".into(),
            name: "My tasks".into(),
            description: crate::i18n::text_in("starter_queue_description", "en-US"),
            ..Default::default()
        };
        for language in ["en-US", "en-GB", "zh-CN"] {
            for (key, value) in [("starter_queue_name", &project.name), ("starter_queue_description", &project.description)] {
                assert_eq!(project.display_copy(key, value, language), crate::i18n::text_in(key, language));
            }
        }
        assert_eq!(project.name, "My tasks");
        project.name = crate::i18n::text_in("starter_queue_name", "zh-CN");
        assert_eq!(project.display_copy("starter_queue_name", &project.name, "en-US"), "My tasks");
        project.name = "Personal work".into();
        assert_eq!(project.display_copy("starter_queue_name", &project.name, "zh-CN"), "Personal work");
        project.id = "custom-queue".into();
        project.name = "My tasks".into();
        assert_eq!(project.display_copy("starter_queue_name", &project.name, "zh-CN"), "My tasks");
    }

    #[test]
    fn starter_icon_uses_ios_artwork_unless_customized() {
        let mut project = super::Project { id: "starter".into(), ..Default::default() };
        assert_eq!(project.default_icon(false), Some("fennec-ios-light.png"));
        assert_eq!(project.default_icon(true), Some("fennec-ios-dark.png"));
        project.icon = "data:image/png;base64,custom".into();
        assert!(project.default_icon(false).is_none());
        project.icon.clear();
        project.id = "custom-queue".into();
        assert!(project.default_icon(false).is_none());
    }

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

    #[test]
    fn permission_calls_split_into_tool_and_subject() {
        let call = |s: &str| super::split_call(s);
        assert_eq!(call("Bash: rm -f \"$D\""), Some(("Bash".into(), "rm -f \"$D\"".into())));
        assert_eq!(call("Edit"), Some(("Edit".into(), String::new())));
        assert_eq!(call("trust this folder"), Some((String::new(), "trust this folder".into())));
        assert_eq!(call("permission prompt"), None);
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
        return control_call(
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

/// THI-176: answers the question request `n`'s agent is asking in its
/// session; `picks` holds the chosen option indexes per question, `allow`
/// answers a permission prompt, `text` is typed. No new run starts.
pub fn answer(
    project: &str,
    n: u64,
    author: &str,
    question: &str,
    picks: &[Vec<usize>],
    allow: Option<bool>,
    text: &str,
) -> Result<Value> {
    call(
        "POST",
        &format!("/v1/projects/{project}/requests/{n}/answer"),
        Some(json!({"author": author, "question": question, "picks": picks, "allow": allow, "text": text.trim()})),
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

/// FEN-5: deletes request `n` from its queue on every device.
pub fn delete_request(project: &str, n: u64) -> Result<Value> {
    call("DELETE", &format!("/v1/projects/{project}/requests/{n}"), None)
}

/// FEN-11: puts a failed or unconfirmed request `n` back in the queue; the
/// scheduler starts it in a new session (Store.Retry).
pub fn retry_request(project: &str, n: u64) -> Result<Value> {
    call("POST", &format!("/v1/projects/{project}/requests/{n}/retry"), Some(json!({})))
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
