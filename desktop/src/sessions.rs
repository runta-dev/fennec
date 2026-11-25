//! Fennec sessions are the cloud computer's herdr-core terminals labeled
//! `fennec.session` (and `fennec.agent`), with the status their agent reports.

use serde_json::Value;

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum State {
    NeedsYou,
    Done,
    Working,
    Idle,
    Unknown,
}

#[derive(Clone, Debug)]
pub struct Session {
    /// herdr-core's address of the agent's terminal.
    pub address: String,
    /// Fennec's session name (`fen attach <name>`).
    pub name: String,
    pub agent: String,
    pub title: String,
    pub directory: String,
    pub queue: String,
    pub request: Option<u64>,
    pub state: State,
    /// What the agent says it is doing now (herdr-core's `status.msg`).
    pub activity: String,
    /// When the status last changed, in ms since the epoch.
    pub since_ms: u64,
    /// When the terminal was created, in ms since the epoch (0 if unknown).
    pub created_ms: u64,
}

impl Session {
    pub fn group_key(&self) -> String {
        if self.address.starts_with("cloud:") {
            format!("cloud:{}", self.name)
        } else {
            self.name.clone()
        }
    }
}

/// The sidebar sessions of request `n` in queue `project` that Mark done
/// ends now: every one but a working agent's, which finishes its turn first.
pub fn thread_sessions_to_end(sessions: &[Session], project: &str, n: u64) -> Vec<String> {
    sessions
        .iter()
        .filter(|s| s.queue == project && s.request == Some(n) && s.state != State::Working)
        .map(|s| s.address.clone())
        .collect()
}

pub fn from_terminals(terminals: &[Value]) -> Vec<Session> {
    let mut out: Vec<Session> = terminals
        .iter()
        .filter(|t| t["state"] == "live")
        .filter_map(|t| {
            let labels = &t["labels"];
            let name = labels["fennec.session"].as_str()?.to_owned();
            let agent = t["status"]["app"]
                .as_str()
                .filter(|s| !s.is_empty())
                .map(str::to_owned)
                .unwrap_or_else(|| shell_name(t));
            let directory = terminal_directory(t);
            let state = match t["status"]["state"].as_str().unwrap_or_default() {
                "blocked" | "error" => State::NeedsYou,
                "working" => State::Working,
                "idle" | "done" => State::Idle,
                _ => State::Unknown,
            };
            // Agents put a spinner or a mark before their title ("✳ Fix login").
            let title = t["title"]
                .as_str()
                .map(|s| {
                    s.trim_start_matches(|c: char| !c.is_alphanumeric() && !"~/(".contains(c))
                        .trim()
                })
                .filter(|s| !s.is_empty())
                .map(|s| {
                    let pipe = format!(" | {name}");
                    let dot = format!(" · {name}");
                    s.strip_suffix(&pipe)
                        .or_else(|| s.strip_suffix(&dot))
                        .unwrap_or(s)
                        .to_owned()
                })
                .unwrap_or_else(|| name.clone());
            Some(Session {
                address: t["address"].as_str()?.to_owned(),
                name,
                agent,
                title,
                directory,
                queue: labels["fennec.queue"]
                    .as_str()
                    .unwrap_or_default()
                    .to_owned(),
                request: labels["fennec.request"]
                    .as_str()
                    .and_then(|n| n.parse().ok()),
                state,
                activity: t["status"]["msg"]
                    .as_str()
                    .map(str::trim)
                    .unwrap_or_default()
                    .to_owned(),
                since_ms: t["status_ms"]
                    .as_u64()
                    .or_else(|| t["created_ms"].as_u64())
                    .unwrap_or(0),
                created_ms: t["created_ms"].as_u64().unwrap_or(0),
            })
        })
        .collect();
    out.sort_by(|a, b| a.state.cmp(&b.state).then(b.since_ms.cmp(&a.since_ms)));
    out
}

/// The agent's icon in assets/agents (the iPhone app's), by agent or program name.
pub fn agent_icon(agent: &str) -> Option<&'static str> {
    let a = agent.to_lowercase();
    Some(match () {
        _ if a.contains("claude") => "claudecode",
        _ if a.contains("codex") => "codex",
        _ if a.contains("cursor") => "cursor",
        _ if a.contains("kimi") => "kimi",
        _ if a.contains("opencode") => "opencode",
        _ if a.contains("grok") || a.contains("xai") => "xai",
        _ if a == "pi" => "pi",
        _ if a == "agy" || a.contains("antigravity") => "antigravity",
        _ => return None,
    })
}

pub fn agent_name(agent: &str) -> &str {
    if agent == "shell" {
        return "Terminal";
    }
    match agent_icon(agent) {
        Some("claudecode") => "Claude Code",
        Some("codex") => "Codex",
        Some("cursor") => "Cursor",
        Some("kimi") => "Kimi Code",
        Some("opencode") => "OpenCode",
        Some("xai") => "Grok",
        Some("pi") => "Pi",
        Some("antigravity") => "Antigravity CLI",
        _ => agent,
    }
}

/// "3m", "2h", "5d": the age shown beside a row.
pub fn age(ms: u64, now_ms: u64) -> String {
    let s = now_ms.saturating_sub(ms) / 1000;
    let n = |key: &str, v: u64| crate::i18n::tf(key, &[("n", &v.to_string())]);
    match s {
        0..60 => crate::i18n::t("now"),
        60..3600 => n("minutes", s / 60),
        3600..86400 => n("hours", s / 3600),
        _ => n("days", s / 86400),
    }
}

pub fn now_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

/// Board timestamps (RFC 3339) as ms since the epoch, for `age`.
pub fn rfc3339_ms(s: &str) -> u64 {
    // YYYY-MM-DDTHH:MM:SS(.fff...)Z, UTC: enough for ages.
    let n = |r: std::ops::Range<usize>| s.get(r).and_then(|v| v.parse::<i64>().ok());
    let (Some(y), Some(mo), Some(d), Some(h), Some(mi), Some(se)) =
        (n(0..4), n(5..7), n(8..10), n(11..13), n(14..16), n(17..19))
    else {
        return 0;
    };
    let (y, m) = if mo <= 2 { (y - 1, mo + 12) } else { (y, mo) };
    let days = 365 * y + y / 4 - y / 100 + y / 400 + (153 * (m - 3) + 2) / 5 + d - 719469;
    ((days * 86400 + h * 3600 + mi * 60 + se) * 1000).max(0) as u64
}

pub fn shell_name(terminal: &Value) -> String {
    let base = |path: &str| {
        std::path::Path::new(path)
            .file_name()
            .and_then(|p| p.to_str())
            .unwrap_or(path)
            .trim_start_matches('-')
            .to_owned()
    };
    let foreground = terminal["foreground"]["name"].as_str().unwrap_or_default();
    if [
        "zsh",
        "bash",
        "fish",
        "sh",
        "nu",
        "pwsh",
        "powershell",
        "xonsh",
        "dash",
        "ksh",
        "tcsh",
        "elvish",
    ]
    .contains(&foreground)
    {
        return foreground.to_owned();
    }
    terminal["argv"]
        .get(0)
        .and_then(Value::as_str)
        .map(base)
        .or_else(|| std::env::var("SHELL").ok().map(|p| base(&p)))
        .unwrap_or_else(|| "sh".into())
}
pub fn terminal_directory(terminal: &Value) -> String {
    terminal["foreground"]["cwd"]
        .as_str()
        .or_else(|| terminal["cwd"].as_str())
        .unwrap_or_default()
        .to_owned()
}
pub fn directory_label(path: &str) -> String {
    if std::env::var("HOME").ok().as_deref() == Some(path) {
        return "~".into();
    }
    std::path::Path::new(path)
        .file_name()
        .and_then(|p| p.to_str())
        .filter(|p| !p.is_empty())
        .unwrap_or(path)
        .to_owned()
}

/// Child tabs have their own OSC title even though they share a sidebar group.
pub fn agent_terminal_title(terminal: &Value) -> Option<String> {
    agent_icon(terminal["status"]["app"].as_str()?)?;
    let title = terminal["title"]
        .as_str()?
        .trim_start_matches(|c: char| !c.is_alphanumeric() && !"~/(".contains(c))
        .trim();
    let directory = terminal_directory(terminal);
    let cwd = std::path::Path::new(&directory)
        .file_name()
        .and_then(|s| s.to_str());
    let mut title = title;
    for suffix in [terminal["labels"]["fennec.session"].as_str(), cwd]
        .into_iter()
        .flatten()
        .filter(|s| !s.is_empty())
    {
        for separator in [" | ", " · "] {
            if let Some(clean) = title.strip_suffix(&format!("{separator}{suffix}")) {
                title = clean;
            }
        }
    }
    (!title.is_empty()).then(|| title.to_owned())
}

#[cfg(test)]
mod title_tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn child_agent_titles_do_not_require_a_parent_session_label() {
        let mut child = json!({"status":{"app":"codex"},"title":"⠙ Fix login | repo", "cwd":"/work/repo", "labels":{"fennec.shell-of":"parent"}});
        assert_eq!(agent_terminal_title(&child).as_deref(), Some("Fix login"));
        child["title"] = json!("Review changes | repo");
        assert_eq!(
            agent_terminal_title(&child).as_deref(),
            Some("Review changes")
        );
        child["status"]["app"] = json!("zsh");
        assert_eq!(agent_terminal_title(&child), None);
    }
}

#[cfg(test)]
mod mark_done_tests {
    use super::*;
    use serde_json::json;

    fn terminal(address: &str, request: &str, status: &str) -> Value {
        json!({"address": address, "state": "live", "status": {"app": "claude-code", "state": status},
               "labels": {"fennec.session": address, "fennec.queue": "this", "fennec.request": request}})
    }

    // THI-47: Mark done ends the thread's own sessions at once, except an agent
    // still working, which the queue ends after its turn.
    #[test]
    fn mark_done_ends_only_the_threads_idle_sessions() {
        let sessions = from_terminals(&[
            terminal("t1", "47", "done"),
            terminal("t2", "47", "working"),
            terminal("t3", "48", "done"),
            terminal("t4", "47", "blocked"),
        ]);
        let mut keys = thread_sessions_to_end(&sessions, "this", 47);
        keys.sort();
        assert_eq!(keys, ["t1", "t4"]);
        assert!(thread_sessions_to_end(&sessions, "other", 47).is_empty());
    }
}
