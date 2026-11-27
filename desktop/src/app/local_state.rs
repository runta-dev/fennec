//! What the window keeps across restarts (THI-121): the last queue shown and
//! the text not yet sent from the new-request composer and the thread reply
//! boxes, isolated by cloud computer in desktop-board-state.json. The old
//! unscoped desktop-state.json is preserved, never assigned to a new computer.
use super::{FennecWindow, Route};
use gpui::*;
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::time::Duration;

#[derive(Clone, Default, Deserialize, PartialEq, Serialize)]
#[serde(default)]
pub(crate) struct Saved {
    /// The queue (project id) last shown.
    pub last_queue: Option<String>,
    /// The new-request composer's text.
    pub composer: String,
    /// Reply drafts by `reply:<queue>:<number>`, empty ones left out.
    pub replies: BTreeMap<String, String>,
    pub images: BTreeMap<String, Vec<crate::board::Attachment>>,
    /// FEN-145: the agent, model and effort picked in the composer.
    pub choice: crate::board::Choice,
}

fn path() -> Option<std::path::PathBuf> {
    std::env::var_os("HOME")
        .map(|home| std::path::PathBuf::from(home).join(".config/fennec/desktop-board-state.json"))
}

pub(crate) fn load() -> BTreeMap<String, Saved> {
    path()
        .and_then(|p| std::fs::read(p).ok())
        .and_then(|bytes| serde_json::from_slice(&bytes).ok())
        .unwrap_or_default()
}

/// Write through a temporary file so a crash mid-write keeps the old state.
fn store(saved: &BTreeMap<String, Saved>) -> anyhow::Result<()> {
    let Some(path) = path() else { return Ok(()) };
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)?;
    }
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, serde_json::to_vec_pretty(saved)?)?;
    std::fs::rename(&tmp, &path)?;
    Ok(())
}

impl FennecWindow {
    /// The state as it stands now, including the reply box of the open thread.
    fn local_snapshot(&self, cx: &App) -> Saved {
        let mut replies: BTreeMap<String, String> = self
            .reply_drafts
            .iter()
            .map(|(k, v)| (k.clone(), v.clone()))
            .collect();
        if let (Some(project), Some(n)) = (self.project_id(), self.open) {
            replies.insert(
                format!("reply:{project}:{n}"),
                self.reply.read(cx).value().to_string(),
            );
        }
        replies.retain(|_, v| !v.trim().is_empty());
        let last_queue = match &self.route {
            Some(Route::Project(id) | Route::Settings(id, _)) => Some(id.clone()),
            _ => self.local_state.last_queue.clone(),
        };
        Saved {
            last_queue,
            composer: self.composer.read(cx).value().to_string(),
            replies,
            images: self
                .request_images
                .by_queue
                .iter()
                .map(|(k, v)| (k.clone(), v.clone()))
                .collect(),
            choice: self.agent_choice.clone(),
        }
    }

    /// Restore the saved state into a window being built: open the last queue
    /// with the cursor in its new-request box, and put the drafts back.
    pub(super) fn restore_local_state(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.local_profiles = load();
        let _ = (window, cx);
    }

    pub(super) fn switch_board_scope(
        &mut self,
        scope: crate::board::Scope,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if self.board_scope.as_ref() == Some(&scope) {
            return;
        }
        if let Some(old) = &self.board_scope {
            self.local_profiles
                .insert(old.0.clone(), self.local_snapshot(cx));
            self.local_profiles_dirty = true;
        }
        let saved = self
            .local_profiles
            .get(&scope.0)
            .cloned()
            .unwrap_or_default();
        self.board_scope = Some(scope);
        self.projects.clear();
        self.deleted_projects.clear();
        self.requests.clear();
        self.thread_cache.clear();
        self.status_override.clear();
        self.thread = None;
        self.open = None;
        self.opening = None;
        self.history.clear();
        self.pending_replies.clear();
        self.fresh_messages.clear();
        self.question_picks.clear();
        self.thread_peek.clear();
        self.peek_reads = Default::default();
        self.thread_jump = None;
        self.thread_follow = false;
        self.open_permission_commands.clear();
        self.new_project = None;
        self.knowledge_refs = Default::default();
        self.settings = Default::default();
        self.request_images = Default::default();
        self.request_images.by_queue.extend(saved.images.clone());
        self.image_viewer = None;
        self.context_menu = None;
        self.menu = None;
        self.pending_requests.clear();
        self.agent_choice = saved.choice.clone();
        self.agent_pick = Default::default();
        self.replying = false;
        self.answering = false;
        self.opening_thread_session = false;
        self.reply_drafts.clear();
        self.tag = None;
        self.resolved = false;
        self.reply.update(cx, |s, cx| s.set_value("", window, cx));
        self.composer
            .update(cx, |s, cx| s.set_value(saved.composer.clone(), window, cx));
        if matches!(self.route, Some(Route::Project(_) | Route::Settings(_, _))) {
            self.route = None;
        }
        self.restored_queue = None;
        self.reply_drafts
            .extend(saved.replies.iter().map(|(k, v)| (k.clone(), v.clone())));
        if let Some(queue) = &saved.last_queue {
            self.route = Some(Route::Project(queue.clone()));
            self.restored_queue = Some(queue.clone());
            // gpui keeps focus on a handle whose element is not drawn yet.
            self.composer.read(cx).focus_handle(cx).focus(window, cx);
        }
        self.local_state = saved;
    }

    /// Save the state shortly after it changes, off the UI thread.
    pub(super) fn start_local_state(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            loop {
                cx.background_executor()
                    .timer(Duration::from_millis(400))
                    .await;
                let Ok(changed) = this.update(cx, |this, cx| {
                    let Some(scope) = this.board_scope.clone() else {
                        return None;
                    };
                    let now = this.local_snapshot(cx);
                    (now != this.local_state || this.local_profiles_dirty).then(|| {
                        this.local_state = now.clone();
                        this.local_profiles.insert(scope.0, now);
                        this.local_profiles_dirty = false;
                        this.local_profiles.clone()
                    })
                }) else {
                    break;
                };
                if let Some(saved) = changed {
                    let result = cx.background_spawn(async move { store(&saved) }).await;
                    if let Err(e) = result {
                        tracing::warn!("saving desktop state failed: {e:#}");
                    }
                }
            }
        }));
    }

    /// The restored queue may be gone: once the board lists its queues, fall
    /// back to the first one.
    pub(super) fn check_restored_queue(&mut self) {
        let Some(queue) = self.restored_queue.take() else {
            return;
        };
        if self.route == Some(Route::Project(queue.clone()))
            && !self.projects.iter().any(|p| p.id == queue)
        {
            self.route = self
                .visible_projects()
                .first()
                .map(|p| Route::Project(p.id.clone()));
        }
    }
}
