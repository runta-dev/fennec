//! The window (docs/desktop-app.md): Projects and Sessions on the left; the
//! open project (requests beside a thread) or session (herdr-app's terminal)
//! on the right.

mod activity;
mod cloud;
mod context_menu;
mod glide;
mod row_motion;
mod request_attachments;
mod sidebar_resize;
mod tab_close;
mod tabs;
mod thread_peek;
mod thread_wait;
#[cfg(test)]
mod reply_tests;

use crate::board::{self, Project, Request};
use crate::hc::{Core, EventLines, Stream};
use crate::i18n::{t, tf};
use crate::sessions::{self, Session, State, thread_sessions_to_end};
use crate::terminal::{TerminalEvent, TerminalView};
use crate::ui::{self, c};
use gpui::prelude::FluentBuilder as _;
use gpui::*;
use gpui_base::input::{Input, InputEditorStyle, InputEvent, InputState, TextareaState};
use gpui_base::{Textarea, h_flex, v_flex};
use gpui_shell::action::ShellAction;
use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

#[derive(Clone, PartialEq)]
pub(crate) enum Route {
    AppSettings,
    Project(String),
    Session(String),
    /// A project's settings, on one of its tabs.
    Settings(String, crate::project_settings::Tab),
}

/// One sidebar row: a session, or one of the agents a session runs.
struct SidebarRow<'a> {
    session: &'a Session,
    /// The tab key of this row's agent when the session is split into its agents.
    tab: Option<String>,
    state: State,
    since_ms: u64,
    /// An agent's row (or any cloud session's) is grouped by its state; a plain
    /// local shell or ssh session stays at the top under its directory.
    agent: bool,
}

#[derive(Clone, Copy, PartialEq)]
enum Menu {
    NewSession,
    RequestTag,
}

/// The New project dialog's fields.
struct NewProject {
    name: Entity<InputState>,
    description: Entity<InputState>,
    repository: Entity<InputState>,
    agent: String,
}

#[derive(Clone)]
struct QueuedRequestDrag {
    project: String,
    number: u64,
    title: String,
}
impl Render for QueuedRequestDrag {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        div()
            .px(px(12.0))
            .py(px(8.0))
            .bg(c("f_card"))
            .text_color(c("f_text"))
            .child(self.title.clone())
    }
}

pub struct FennecWindow {
    activity_send: Option<async_channel::Sender<(serde_json::Value, Option<Arc<Core>>)>>,
    activity_input: serde_json::Value,
    activity: serde_json::Value,
    agent_notices: Vec<serde_json::Value>,
    cloud_listing: serde_json::Value,
    cloud_generation: u64,
    cloud_views: HashMap<String, String>,
    cloud_attach_pending: std::collections::HashSet<String>,
    cloud_attach_failed: std::collections::HashSet<String>,
    cloud_events: Option<EventLines>,
    core: Option<Arc<Core>>,
    core_error: Option<String>,
    events: Option<EventLines>,
    sessions: Vec<Session>,
    pub(crate) projects: Vec<Project>,
    deleted_projects: std::collections::HashSet<String>,
    pub(crate) requests: HashMap<String, Vec<Request>>,
    thread: Option<Request>,
    /// Threads already fetched, by (project, number): switching back to one
    /// shows it at once while a refresh runs.
    thread_cache: HashMap<(String, u64), Request>,
    /// A clicked request whose thread is still loading. The card is selected,
    /// but the pane keeps the current thread (and its actions) until the new
    /// one arrives, so the pane never blanks between threads.
    opening: Option<u64>,
    /// The open thread's message list, kept at its newest message.
    thread_scroll: ScrollHandle,
    /// Scroll the thread to its newest message once a sent reply shows up.
    thread_follow: bool,
    /// The latest lines the open thread's working agent printed.
    /// Each agent's last peek, by session address: a thread switched back to
    /// shows it at once instead of growing when the first read lands.
    thread_peek: HashMap<String, thread_peek::Peek>,
    pub(crate) route: Option<Route>,
    open: Option<u64>,
    resolved: bool,
    tag: Option<String>,
    pub(crate) terms: HashMap<String, Entity<TerminalView>>,
    pub(crate) search: Entity<InputState>,
    pub(crate) composer: Entity<TextareaState>,
    request_images: request_attachments::Drafts,
    image_viewer: Option<request_attachments::Viewer>,
    posting: bool,
    replying: bool,
    pub(crate) reply: Entity<TextareaState>,
    pub(crate) reply_drafts: HashMap<String, String>,
    opening_thread_session: bool,
    notice: Option<String>,
    pub(crate) sidebar: bool,
    /// How far the sidebar is shown (0 hidden, 1 shown) in this frame.
    sidebar_shown: f32,
    /// The running sidebar animation: the target, the starting amount, and when it started.
    sidebar_motion: Option<(bool, f32, std::time::Instant)>,
    /// Queue cards easing to a new place after their status moved them.
    queue_glide: glide::Shared,
    pub(crate) sidebar_resize: sidebar_resize::State,
    focus: FocusHandle,
    pending_focus: Option<String>,
    confirmed_session: Option<String>,
    prefix: bool,
    close_others: Option<Vec<String>>,
    /// The signed-in person, as requests and replies are signed.
    pub(crate) me: String,
    menu: Option<Menu>,
    context_menu: Option<context_menu::ContextMenu>,
    menu_anchor: Point<Pixels>,
    new_project: Option<NewProject>,
    /// A session asked for and not yet listed: shown once it appears.
    starting: Option<String>,
    pub(crate) settings: crate::project_settings::State,
    pub(crate) app_preferences: crate::app_preferences::State,
    creating_local: bool,
    has_connected: bool,
    /// This Mac's own core (Fennec's instance), for local shell tabs.
    local: Option<Arc<Core>>,
    /// Local shells by the session they sit beside (`fennec.shell-of`), in order.
    shells: HashMap<String, Vec<String>>,
    shell_names: HashMap<String, String>,
    directories: HashMap<String, String>,
    /// The tab shown in each session: a local shell's `local:<address>`, else the agent's.
    tab: HashMap<String, String>,
    layout: tabs::Layout,
    pub(crate) _subs: Vec<Subscription>,
    pub(crate) _tasks: Vec<Task<()>>,
}

impl FennecWindow {
    pub fn new(
        requests: async_channel::Receiver<crate::mods::Request>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Self {
        Self::build(requests, window, cx, true)
    }

    fn build(
        requests: async_channel::Receiver<crate::mods::Request>,
        window: &mut Window,
        cx: &mut Context<Self>,
        connect: bool,
    ) -> Self {
        let search = cx.new(|cx| {
            InputState::new(window, cx)
                .placeholder(t("search"))
                .clean_on_escape()
        });
        let composer = cx.new(|cx| {
            TextareaState::new(window, cx)
                .auto_grow(2, 12)
                .submit_on_enter(true)
                .placeholder(t("new_request"))
        });
        let reply = cx.new(|cx| {
            TextareaState::new(window, cx)
                .auto_grow(1, 8)
                .submit_on_enter(true)
                .placeholder(t("reply"))
        });
        let mut subs = vec![
            cx.subscribe_in(&search, window, |_, _, ev: &InputEvent, _, cx| {
                if matches!(ev, InputEvent::Change) {
                    cx.notify();
                }
            }),
            cx.subscribe_in(
                &composer,
                window,
                |this, _, ev: &InputEvent, window, cx| match ev {
                    InputEvent::PressEnter { shift: false, .. } => this.post(window, cx),
                    InputEvent::Change => cx.notify(),
                    _ => {}
                },
            ),
            cx.subscribe_in(
                &reply,
                window,
                |this, _, ev: &InputEvent, window, cx| match ev {
                    InputEvent::PressEnter { shift: false, .. } => this.send_reply(window, cx),
                    InputEvent::Change => cx.notify(),
                    _ => {}
                },
            ),
        ];
        subs.push(cx.observe_window_activation(window, |this, window, cx| {
            if window.is_window_active()
                && !this.prefix
                && this.close_others.is_none()
                && this.new_project.is_none()
                && this.image_viewer.is_none()
                && this.menu.is_none()
                && this.context_menu.is_none()
            {
                if let Some(Route::Session(id)) = &this.route {
                    this.pending_focus = Some(this.shown_tab(id));
                    cx.notify();
                }
            }
        }));
        subs.push(cx.observe_window_appearance(window, |this, window, cx| {
            if crate::preview::active() && std::env::var("FENNEC_PREVIEW_THEME").is_ok() {
                return;
            }
            if this.app_preferences.value.theme != "system" {
                return;
            }
            let dark = matches!(
                window.appearance(),
                WindowAppearance::Dark | WindowAppearance::VibrantDark
            );
            if crate::settings::set_dark(dark) {
                for t in this.terms.values() {
                    t.read(cx).refresh_theme();
                }
                cx.notify();
            }
        }));
        // The window shows at once; the cloud computer's core is reached meanwhile
        // (over SSH from a Mac, a second or more), and sessions appear with it.
        let (core, core_error): (Option<Arc<Core>>, Option<String>) = (None, None);
        let mut this = FennecWindow {
            activity_send: None,
            activity_input: serde_json::Value::Null,
            activity: serde_json::Value::Null,
            agent_notices: Vec::new(),
            cloud_listing: serde_json::Value::Null,
            cloud_generation: 0,
            cloud_views: HashMap::new(),
            cloud_attach_pending: Default::default(),
            cloud_attach_failed: Default::default(),
            cloud_events: None,
            core,
            core_error,
            events: None,
            sessions: Vec::new(),
            projects: Vec::new(),
            deleted_projects: Default::default(),
            requests: HashMap::new(),
            thread: None,
            thread_peek: HashMap::new(),
            route: None,
            open: None,
            resolved: false,
            tag: None,
            terms: HashMap::new(),
            search,
            composer,
            request_images: Default::default(),
            image_viewer: None,
            posting: false,
            replying: false,
            thread_scroll: ScrollHandle::new(),
            thread_follow: false,
            thread_cache: HashMap::new(),
            opening: None,
            reply,
            reply_drafts: HashMap::new(),
		    opening_thread_session: false,
            notice: None,
            sidebar: true,
            sidebar_shown: 1.0,
            sidebar_motion: None,
            queue_glide: Default::default(),
            sidebar_resize: Default::default(),
            focus: cx.focus_handle(),
            pending_focus: None,
            confirmed_session: None,
            prefix: false,
            close_others: None,
            me: std::env::var("USER").unwrap_or_else(|_| "me".into()),
            menu: None,
            context_menu: None,
            menu_anchor: point(px(120.0), px(150.0)),
            new_project: None,
            starting: None,
            settings: Default::default(),
            app_preferences: if connect {crate::app_preferences::State::load()} else {Default::default()},
            creating_local: false,
            has_connected: false,
            local: None,
            shells: HashMap::new(),
            shell_names: HashMap::new(),
            directories: HashMap::new(),
            tab: HashMap::new(),
            layout: tabs::Layout::default(),
            _subs: subs,
            _tasks: Vec::new(),
        };
        if connect && !crate::preview::active() {
            this.apply_desktop_preferences(window, cx);
            this.start_activity(window, cx);
            this.start_layout(window, cx);
            this.follow_cloud(window, cx);
        }
        if crate::preview::active() {
            this.request_images.by_queue=crate::preview::request_images();
            this.sessions = sessions::from_terminals(&crate::preview::terminals());
            this.shells = crate::preview::shells();
            this.activity = crate::preview::activity();
            this.apply_activity();
            this.core_error = Some(String::new());
        } else if connect {
            this._tasks.push(cx.spawn_in(window, async move |this, cx| {
                let connected = cx.background_spawn(async move { Core::connect() }).await;
                let _ = this.update_in(cx, |this, window, cx| match connected {
                    Ok(core) => {
                        this.core = Some(core.clone());
                        if !crate::core_bin::cloud() {
                            this.local = Some(core);
                        }
                        this.follow_core(window, cx);
                        this.has_connected = true;
                        if !crate::core_bin::cloud() {
                            if let Some(session) = this
                                .sessions
                                .iter()
                                .find(|s| !s.address.starts_with("cloud:"))
                            {
                                if this.route == Some(Route::AppSettings) {
                                    cx.notify();
                                    return;
                                }
                                this.pending_focus = Some(session.address.clone());
                                this.route = Some(Route::Session(session.address.clone()));
                            } else {
                                this.start_session_selection(
                                    String::new(),
                                    this.route != Some(Route::AppSettings),
                                    window,
                                    cx,
                                );
                            }
                        }
                        cx.notify();
                    }
                    Err(e) => {
                        this.core_error = Some(format!("{e:#}"));
                        cx.notify();
                    }
                });
            }));
        }
        if let Some(path) = crate::shot::path() {
            this._tasks.push(cx.spawn_in(window, async move |this, cx| {
                loop {
                    cx.background_executor()
                        .timer(Duration::from_millis(500))
                        .await;
                    let Some(what) = crate::shot::request(&path) else {
                        continue;
                    };
                    if this
                        .update_in(cx, |this, window, cx| this.show(&what, window, cx))
                        .is_err()
                    {
                        break;
                    }
                    // Data the view loads (a thread, settings) arrives within a few frames.
                    cx.background_executor()
                        .timer(Duration::from_millis(2500))
                        .await;
                    let handle = cx.window_handle();
                    if let Err(e) = crate::shot::save(handle, cx, &path) {
                        tracing::warn!("screenshot failed: {e:#}");
                    }
                }
            }));
        }
        if connect {this.follow_board(window, cx); this.follow_thread_peek(window, cx);}
        // Toasts the terminal core asks for (a refused file drop).
        this._tasks.push(cx.spawn_in(window, async move |this, cx| {
            while let Ok(crate::mods::Request::OpenOverlay { spec, id: _ }) = requests.recv().await
            {
                let n = &spec["notification"];
                let text = [n["title"].as_str(), n["body"].as_str()]
                    .into_iter()
                    .flatten()
                    .collect::<Vec<_>>()
                    .join(": ");
                if this.update(cx, |this, cx| this.say(text, cx)).is_err() {
                    break;
                }
            }
        }));
        this
    }

    /// Shows what a screenshot request names (shot.rs).
    fn show(&mut self, what: &str, window: &mut Window, cx: &mut Context<Self>) {
        use crate::project_settings::Tab;
        let mut parts = what.splitn(3, ':');
        match (parts.next(), parts.next(), parts.next()) {
            (Some("app-settings"), _, _) => self.open_app_settings(window, cx),
            (Some("sidebar"), Some(state), _) => self.sidebar = state != "hidden",
            (Some("menu"), Some(kind), _) if crate::preview::active() => {
                self.menu = Some(if kind == "tags" {
                    Menu::RequestTag
                } else {
                    Menu::NewSession
                });
                self.menu_anchor = if kind == "tags" {
                    point(px(498.0), px(158.0))
                } else {
                    point(px(120.0), px(230.0))
                };
            }
            (Some("project"), Some(id), _) => self.go(Route::Project(id.into()), window, cx),
            (Some("request"), Some(n), _) => {
                if let Ok(n) = n.parse() {
                    self.open_request(n, window, cx);
                }
            }
            (Some("session"), Some(a), _) => self.go(Route::Session(a.into()), window, cx),
            (Some("settings"), Some(id), tab) => {
                let tab = match tab.unwrap_or_default() {
                    "knowledge" => Tab::Knowledge,
                    "queue" => Tab::Queue,
                    "agents" => Tab::Agents,
                    _ => Tab::General,
                };
                self.go(Route::Settings(id.into(), tab), window, cx);
            }
            _ => {}
        }
        cx.notify();
    }

    pub(crate) fn say(&mut self, text: String, cx: &mut Context<Self>) {
        tracing::warn!("desktop action: {text}");
        self.notice = Some(t("action_failed"));
        cx.notify();
        self._tasks.push(cx.spawn(async move |this, cx| {
            cx.background_executor().timer(Duration::from_secs(5)).await;
            let _ = this.update(cx, |this, cx| {
                this.notice = None;
                cx.notify();
            });
        }));
    }

    /// The cloud computer's terminals, live: which ones are Fennec sessions.
    fn follow_core(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(core) = self.core.clone() else {
            return;
        };
        let (channel, stream, snapshot) = match core.subscribe() {
            Ok(s) => s,
            Err(e) => {
                self.core_error = Some(format!("{e:#}"));
                return;
            }
        };
        self.events = Some(EventLines::new(&snapshot));
        self.terminals_changed(window, cx);
        let core = core.clone();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            while let Ok(item) = stream.recv().await {
                let gone = matches!(item, Stream::Closed(_) | Stream::Lost);
                let ok = this.update_in(cx, |this, window, cx| {
                    if let Stream::Data(bytes) = &item {
                        core.credit(channel, bytes.len());
                        // Preserve intermediate status transitions even within one transport frame.
                        for line in bytes.split_inclusive(|b| *b == b'\n') {
                            if this
                                .events
                                .as_mut()
                                .is_some_and(|events| events.push(line).terminals)
                            {
                                this.terminals_changed(window, cx);
                            }
                        }
                    }
                    if gone {
                        this.core_error = Some(t("terminal_closed"));
                        cx.notify();
                    }
                });
                if ok.is_err() || gone {
                    break;
                }
            }
        }));
    }

    fn terminals_changed(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let previously_shown = match &self.route {Some(Route::Session(root))=>Some(self.shown_tab(root)),_=>None};
        let live = serde_json::json!({"terminals": self.terminal_records()});
        if !crate::core_bin::cloud() {
            self.shells.clear();
        }
        for terminal in live["terminals"].as_array().into_iter().flatten() {
            if let Some(address) = terminal["address"].as_str() {
                self.shell_names
                    .insert(address.to_owned(), sessions::shell_name(terminal));
                self.directories
                    .insert(address.to_owned(), sessions::terminal_directory(terminal));
                if !address.starts_with("cloud:")
                    && let Some(parent) = terminal["labels"]["fennec.shell-of"].as_str()
                {
                    let list = self.shells.entry(parent.to_owned()).or_default();
                    if !list.iter().any(|a| a == address) {
                        list.push(address.to_owned());
                    }
                }
            }
        }

        self.queue_activity(window);
        crate::mods::publish_terminals(live["terminals"].clone());
        self.sessions = sessions::from_terminals(
            live["terminals"]
                .as_array()
                .map(Vec::as_slice)
                .unwrap_or_default(),
        );
        self.layout_snapshot();
        self.apply_layout();
        // `apply_layout` rebuilds sessions from raw terminal labels; overlay the
        // attention state afterwards, or every core event drops rows into their
        // raw group until the next activity result moves them back (flicker).
        self.apply_activity();
        if previously_shown.as_ref().is_some_and(|key| !self.layout.members.contains_key(key)) {
            self.focus.focus(window, cx);
        }
        if let Some(Route::Session(a)) = &self.route
            && self.confirmed_session.as_ref() != Some(a)
            && !a.starts_with("local:")
            && !self.sessions.iter().any(|s| &s.address == a)
        {
            self.route = None;
        }
        if self
            .confirmed_session
            .as_ref()
            .is_some_and(|id| self.sessions.iter().any(|s| &s.address == id))
        {
            self.confirmed_session = None;
        }
        // A session opens only when it is picked: attaching resizes its terminal.
        if let Some(name) = self.starting.clone()
            && let Some(s) = self.sessions.iter().find(|s| s.name == name)
        {
            self.starting = None;
            self.pending_focus = Some(s.address.clone());
            self.route = Some(Route::Session(s.address.clone()));
        }
        if self.has_connected {
            self.ensure_local_terminal(window, cx);
        }
        cx.notify();
    }

    /// Projects and the open project's requests, refreshed every few seconds:
    /// agents answer on the cloud computer.
    fn follow_board(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            loop {
                let project = this.update(cx, |this, _| this.project_id()).ok().flatten();
                let open = this.update(cx, |this, _| this.open).ok().flatten();
                let fetched = cx
                    .background_spawn(async move {
                        let projects = board::projects();
                        let requests = project.as_ref().map(|p| (p.clone(), board::requests(p)));
                        let thread = project
                            .as_ref()
                            .zip(open)
                            .map(|(p, n)| board::request(p, n));
                        (projects, requests, thread)
                    })
                    .await;
                if this
                    .update(cx, |this, cx| this.board_loaded(fetched, cx))
                    .is_err()
                {
                    break;
                }
                cx.background_executor().timer(Duration::from_secs(4)).await;
            }
        }));
    }

    #[allow(clippy::type_complexity)]
    fn board_loaded(
        &mut self,
        (projects, requests, thread): (
            anyhow::Result<Vec<Project>>,
            Option<(String, anyhow::Result<Vec<Request>>)>,
            Option<anyhow::Result<Request>>,
        ),
        cx: &mut Context<Self>,
    ) {
        match projects {
            Ok(p) => {
                self.projects = p
                    .into_iter()
                    .filter(|p| !self.deleted_projects.contains(&p.id))
                    .collect()
            }
            Err(e) if self.projects.is_empty() => tracing::debug!("board unavailable: {e:#}"),
            Err(_) => {}
        }
        let fetched_project = requests.as_ref().map(|(id, _)| id.clone());
        if let Some((id, Ok(list))) = requests {
            self.requests.insert(id, list);
        }
        let thread = thread.and_then(Result::ok);
        if let (Some(project), Some(t)) = (&fetched_project, &thread) {
            self.thread_cache
                .insert((project.clone(), t.number), t.clone());
        }
        if let Some(t) = thread
            && Some(t.number) == self.open
            && fetched_project == self.project_id()
        {
            // Follow new messages to the bottom when the thread was just opened,
            // a reply was just sent, or the list was already at its newest
            // message; leave it alone while reading older ones.
            let grew = self.thread.as_ref().is_none_or(|old| {
                old.number != t.number || t.messages.len() > old.messages.len()
            });
            if grew {
                if self.thread_follow || self.thread.is_none() || self.thread_at_bottom() {
                    self.thread_scroll.scroll_to_bottom();
                }
                self.thread_follow = false;
            }
            self.thread = Some(t);
        }
        if self.route.is_none() {
            self.route = self
                .visible_projects()
                .first()
                .map(|p| Route::Project(p.id.clone()));
        }
        cx.notify();
    }

    /// Whether the thread's message list shows its newest message.
    fn thread_at_bottom(&self) -> bool {
        let max = self.thread_scroll.max_offset().y;
        -self.thread_scroll.offset().y >= max - px(24.0)
    }

    pub(crate) fn visible_projects(&self) -> Vec<&Project> {
        self.projects.iter().collect()
    }

    pub(crate) fn project_id(&self) -> Option<String> {
        match &self.route {
            Some(Route::Project(id) | Route::Settings(id, _)) => Some(id.clone()),
            _ => None,
        }
    }

    pub(crate) fn refresh_now(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let project = self.project_id();
        let open = self.open;
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let fetched = cx
                .background_spawn(async move {
                    let projects = board::projects();
                    let requests = project.as_ref().map(|p| (p.clone(), board::requests(p)));
                    let thread = project
                        .as_ref()
                        .zip(open)
                        .map(|(p, n)| board::request(p, n));
                    (projects, requests, thread)
                })
                .await;
            let _ = this.update(cx, |this, cx| this.board_loaded(fetched, cx));
        }));
    }

    pub(crate) fn go(&mut self, mut route: Route, window: &mut Window, cx: &mut Context<Self>) {
        self.close_others = None;
        self.prefix = false;
        self.layout.intent += 1;
        self.context_menu = None;
        if let Route::Session(address) = &route {
            if !address.starts_with("local:")
                && !self.sessions.iter().any(|s| &s.address == address)
            {
                if let Some(first) = self.sessions.first() {
                    route = Route::Session(first.address.clone());
                }
            }
        }
        if let Route::Session(address) = &route {
            self.cloud_attach_failed.remove(address);
            self.pending_focus = Some(self.shown_tab(address));
        }
        self.menu = None;
        if self.route.as_ref() != Some(&route) {
            self.save_reply_draft(cx);
            // Only the session on screen streams: every attached terminal shares one
            // paced connection to the cloud computer, so a busy hidden agent would
            // delay the echo of what is typed here. Coming back attaches again
            // (the core repaints the screen and its history).
            if let Some(Route::Session(old)) = &self.route {
                let shown = self.shown_tab(old);
                if let Some(view) = self.terms.remove(&shown) {
                    view.read(cx).detach();
                }
            }
            if let Route::Settings(id, _) = &route {
                self.open_settings(id.clone(), window, cx);
            }
            if let Route::Project(_) = route {
                self.open = None;
                self.opening = None;
                self.thread = None;
                self.tag = None;
            }
            if let Route::Session(address) = &route {
                self.pending_focus = Some(self.shown_tab(address));
            }
            self.route = Some(route);
            self.refresh_now(window, cx);
        }
        cx.notify();
    }

    /// Open a request's thread. The pane swaps from the current thread to the
    /// new one in a single frame: a thread fetched before shows at once (and
    /// refreshes), otherwise the card is selected while the thread loads and
    /// the pane changes when it arrives. Clearing the pane first would flash
    /// the empty placeholder between the two threads.
    pub(crate) fn open_request(&mut self, n: u64, window: &mut Window, cx: &mut Context<Self>) {
        let Some(project) = self.project_id() else {
            return;
        };
        if let Some(t) = self.thread_cache.get(&(project.clone(), n)).cloned() {
            self.opening = None;
            self.show_thread(n, Some(t), window, cx);
            self.refresh_now(window, cx);
            return;
        }
        self.opening = Some(n);
        cx.notify();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let fetched = {
                let project = project.clone();
                cx.background_spawn(async move { board::request(&project, n) })
                    .await
            };
            let _ = this.update_in(cx, |this, window, cx| {
                if let Ok(t) = &fetched {
                    this.thread_cache.insert((project.clone(), n), t.clone());
                }
                // A later click, or leaving the queue, supersedes this one.
                if this.opening != Some(n) || this.project_id().as_ref() != Some(&project) {
                    return;
                }
                this.opening = None;
                // On failure open it empty; the board refresh fills it in.
                this.show_thread(n, fetched.ok(), window, cx);
                this.refresh_now(window, cx);
            });
        }));
    }

    /// Make `n` the open thread, with `thread` (if loaded) on screen at its
    /// newest message and its own reply draft in the reply box.
    fn show_thread(
        &mut self,
        n: u64,
        thread: Option<Request>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if self.open != Some(n) {
            self.save_reply_draft(cx);
            self.open = Some(n);
            let draft = self
                .project_id()
                .and_then(|p| self.reply_drafts.get(&format!("reply:{p}:{n}")).cloned())
                .unwrap_or_default();
            self.reply
                .update(cx, |s, cx| s.set_value(draft, window, cx));
        }
        if let Some(t) = thread {
            if self.thread.as_ref().is_none_or(|old| old.number != n) {
                self.thread_scroll.scroll_to_bottom();
            }
            self.thread = Some(t);
        } else if self.thread.as_ref().is_some_and(|old| old.number != n) {
            self.thread = None;
        }
        cx.notify();
    }

    /// The board's loaded request a session was launched for, with its project.
    fn loaded_session_request(&self, s: &Session) -> Option<(&str, &Request)> {
        self.requests.iter().find_map(|(pid, list)| {
            list.iter()
                .find(|r| r.session == s.name || r.runs.iter().any(|run| run.session == s.name))
                .map(|r| (pid.as_str(), r))
        })
    }

    /// The queue request a session was launched for, as (project, number, title):
    /// from the board's loaded requests, or else the session's own
    /// `fennec.queue`/`fennec.request` labels.
    pub(super) fn session_request(&self, s: &Session) -> Option<(String, u64, String)> {
        self.loaded_session_request(s)
            .map(|(pid, r)| (pid.to_owned(), r.number, r.title.clone()))
            .or_else(|| {
                s.request
                    .filter(|_| !s.queue.is_empty())
                    .map(|n| (s.queue.clone(), n, t("view_thread")))
            })
    }

    /// Show a request's thread on its project page.
    pub(super) fn go_request(
        &mut self,
        project: String,
        n: u64,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.go(Route::Project(project), window, cx);
        self.open_request(n, window, cx);
    }

    fn save_reply_draft(&mut self, cx: &Context<Self>) {
        if let (Some(project), Some(n)) = (self.project_id(), self.open) {
            self.reply_drafts.insert(
                format!("reply:{project}:{n}"),
                self.reply.read(cx).value().to_string(),
            );
        }
    }

    fn post(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.posting
            || self
                .composer
                .update(cx, |s, cx| s.marked_text_range(window, cx).is_some())
        {
            return;
        }
        let text = self.composer.read(cx).value().to_string();
        let Some(project) = self.project_id() else {
            return;
        };
        let images=self.request_images.by_queue.get(&project).cloned().unwrap_or_default();
        if self.request_images.busy.contains(&project)||(text.trim().is_empty()&&images.is_empty()) {
            return;
        }
        self.posting = true;
        let me = self.me.clone();
        let submitted = text.clone();
        let submitted_project = project.clone();
        let submitted_images:Vec<String>=images.iter().map(|image|image.id.clone()).collect();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let posted = cx
                .background_spawn(async move { board::post(&project, &text, &me, &images) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| {
                this.posting = false;
                match posted {
                    Ok(r) => {
                        this.request_images.errors.remove(&submitted_project);
                        if let Some(images)=this.request_images.by_queue.get_mut(&submitted_project){images.retain(|image|!submitted_images.contains(&image.id));}
                        if this.composer.read(cx).value().as_ref() == submitted {
                            this.composer
                                .update(cx, |s, cx| s.set_value("", window, cx));
                        }
                        if this.project_id().as_deref() == Some(&submitted_project) {
                            this.resolved = false;
                            if !r.messages.is_empty() {
                                this.thread_cache
                                    .insert((submitted_project.clone(), r.number), r.clone());
                            }
                            this.open_request(r.number, window, cx);
                        }
                    }
                    Err(e) => this.say(format!("{e:#}"), cx),
                }
                cx.notify();
            });
        }));
    }

    fn send_reply(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.submit_reply(window, cx);
    }

    fn open_thread_session(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.opening_thread_session {
            return;
        }
        let (Some(project), Some(number)) = (self.project_id(), self.open) else {
            return;
        };
        self.opening_thread_session = true;
        let origin = project.clone();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let result = cx
                .background_spawn(async move {
                    crate::control::call(
                        "board.session",
                        serde_json::json!({"project":project,"number":number}),
                    )
                })
                .await;
            let _ = this.update_in(cx, |this, window, cx| {
                this.opening_thread_session = false;
                match result {
                    Ok(result) => {
                        let mut record = result["terminal"].clone();
                        if let Some(address) = record["address"].as_str().map(str::to_owned) {
                            let address =
                                if crate::core_bin::cloud() || result["location"] == "local" {
                                    address
                                } else {
                                    format!("cloud:{address}")
                                };
                            record["address"] = serde_json::json!(address);
                            this.adopt_local_terminal(record, false, cx);
                            if this.project_id().as_deref() == Some(&origin)
                                && this.open == Some(number)
                            {
                                this.go(Route::Session(address), window, cx);
                            }
                            this.refresh_now(window, cx);
                        }
                    }
                    Err(e) => this.say(format!("{e:#}"), cx),
                }
                cx.notify();
            });
        }));
    }

    fn submit_reply(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.replying {
            return;
        }
        let text = self.reply.read(cx).value().to_string();
        let (Some(project), Some(n)) = (self.project_id(), self.open) else {
            return;
        };
        let key = format!("reply:{project}:{n}");
        let images = self
            .request_images
            .by_queue
            .get(&key)
            .cloned()
            .unwrap_or_default();
        if self.request_images.busy.contains(&key) || (text.trim().is_empty() && images.is_empty())
        {
            return;
        }
        self.replying = true;
        self.thread_follow = true;
        self.thread_scroll.scroll_to_bottom();
        let me = self.me.clone();
        let submitted = text.clone();
        let submitted_images: Vec<String> = images.iter().map(|image| image.id.clone()).collect();
        let origin = project.clone();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let sent = cx
                .background_spawn(async move {
                    board::continue_request(&project, n, &text, &me, &images)
                })
                .await;
            let _ = this.update_in(cx, |this, window, cx| {
                this.replying = false;
                match sent {
                    Ok(_) => {
                        if let Some(images) = this.request_images.by_queue.get_mut(&key) {
                            images.retain(|image| !submitted_images.contains(&image.id));
                        }
                        if this
                            .reply_drafts
                            .get(&key)
                            .is_some_and(|draft| draft == &submitted)
                        {
                            this.reply_drafts.remove(&key);
                        }
                        if this.project_id().as_deref() == Some(&origin)
                            && this.open == Some(n)
                            && this.reply.read(cx).value().as_ref() == submitted
                        {
                            this.reply.update(cx, |s, cx| s.set_value("", window, cx));
                        }
                        if this.project_id().as_deref() == Some(&origin) && this.open == Some(n) {
                            this.refresh_now(window, cx);
                        }
                    }
                    Err(e) => {
                        this.thread_follow = false;
                        this.say(format!("{e:#}"), cx)
                    }
                }
                cx.notify();
            });
        }));
    }

    fn set_status(&mut self, status: &'static str, window: &mut Window, cx: &mut Context<Self>) {
        let (Some(project), Some(n)) = (self.project_id(), self.open) else {
            return;
        };
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let changed = project.clone();
            let done = cx
                .background_spawn(async move { board::set_status(&changed, n, status) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| match done {
                Ok(_) => {
                    if status == "resolved" {
                        this.end_thread_sessions(&project, n, window, cx);
                    }
                    this.refresh_now(window, cx)
                }
                Err(e) => this.say(format!("{e:#}"), cx),
            });
        }));
    }

    /// THI-47: the queue ends a done thread's sessions (queuescheduler
    /// reclaim), but only after the Mac's board syncs to the cloud and the
    /// cloud's scheduler and session list catch up, so the sidebar kept the
    /// row for several seconds. End them here, the way closing the tab does,
    /// so the row goes at once. A working agent is left to finish its turn,
    /// as the queue does; the queue ends it afterwards.
    fn end_thread_sessions(&mut self, project: &str, n: u64, window: &mut Window, cx: &mut Context<Self>) {
        for key in thread_sessions_to_end(&self.sessions, project, n) {
            self.close_tab_key(key, window, cx);
        }
    }

    fn move_request(
        &mut self,
        project: String,
        number: u64,
        before: u64,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let result = cx
                .background_spawn(async move { board::move_request(&project, number, before) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| match result {
                Ok(_) => this.refresh_now(window, cx),
                Err(e) => this.say(format!("{e:#}"), cx),
            });
        }));
    }

    /// The terminal view of a cloud terminal, or of a local shell (`local:<address>`).
    fn terminal(
        &mut self,
        key: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Option<Entity<TerminalView>> {
        if let Some(t) = self.terms.get(key) {
            return Some(t.clone());
        }
        let (core, address) = if let Some(a) = key.strip_prefix("cloud:") {
            let _ = a;
            self.ensure_cloud_view(key, window, cx);
            (self.local.clone()?, self.cloud_views.get(key)?.clone())
        } else if let Some(a) = key.strip_prefix("local:") {
            (self.local.clone()?, a.to_owned())
        } else {
            (self.core.clone()?, key.to_owned())
        };
        let addr = key.to_owned();
        let view = cx.new(|cx| TerminalView::attach(core, address, (120, 40), cx));
        let sub = cx.subscribe_in(
            &view,
            window,
            move |this, _, ev: &TerminalEvent, window, cx| match ev {
                TerminalEvent::Link(click) => {
                    if let Some(target @ crate::links::Target::Url(_)) = &click.native {
                        crate::links::open(target);
                    }
                }
                TerminalEvent::Notice { title, body } => this.say(format!("{title}: {body}"), cx),
                TerminalEvent::Ended => {
                    if addr.starts_with("cloud:") {
                        if matches!(&this.route,Some(Route::Session(root)) if this.shown_tab(root)==addr) {
                            this.focus.focus(window, cx);
                        }
                        this.terms.remove(&addr);
                        this.cloud_views.remove(&addr);
                        this.cloud_attach_failed.insert(addr.clone());
                    } else {
                        this.tab_closed(&addr, window, cx);
                    }
                    cx.notify();
                }
                TerminalEvent::TitleChanged => cx.notify(),
                _ => {}
            },
        );
        self._subs.push(sub);
        self.terms.insert(key.to_owned(), view.clone());
        Some(view)
    }

    /// The key of the tab shown in a session (its agent's address, or `local:<shell>`).
    fn shown_tab(&self, session: &str) -> String {
        self.tab
            .get(session)
            .cloned()
            .unwrap_or_else(|| session.to_owned())
    }

    fn shown_terminal(&self) -> Option<Entity<TerminalView>> {
        match &self.route {
            Some(Route::Session(a)) => self.terms.get(&self.shown_tab(a)).cloned(),
            _ => None,
        }
    }

    // ---------- local shells ----------

    /// Connects to this Mac's core once, and finds the shells left from before.
    fn with_local(
        &mut self,
        window: &mut Window,
        cx: &mut Context<Self>,
        then: impl FnOnce(&mut Self, &mut Window, &mut Context<Self>) + 'static,
    ) {
        if self.local.is_some() {
            return then(self, window, cx);
        }
        let Some(program) = crate::core_bin::local() else {
            return;
        };
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let connected = cx
                .background_spawn(async move {
                    let core = crate::core_bin::using(program, Core::connect)?;
                    let listed = core.call("terminal.list", serde_json::json!({}))?;
                    anyhow::Ok((core, listed))
                })
                .await;
            let _ = this.update_in(cx, |this, window, cx| match connected {
                Ok((core, listed)) => {
                    for t in listed["terminals"].as_array().into_iter().flatten() {
                        if let (Some(of), Some(a)) = (
                            t["labels"]["fennec.shell-of"].as_str(),
                            t["address"].as_str(),
                        ) {
                            this.shell_names
                                .insert(a.to_owned(), sessions::shell_name(t));
                            this.directories
                                .insert(a.to_owned(), sessions::terminal_directory(t));
                            let list = this.shells.entry(of.to_owned()).or_default();
                            if !list.iter().any(|x| x == a) {
                                list.push(a.to_owned());
                            }
                        }
                    }
                    this.local = Some(core);
                    then(this, window, cx);
                    cx.notify();
                }
                Err(e) => {
                    this.creating_local = false;
                    this.say(format!("{e:#}"), cx)
                }
            });
        }));
    }

    fn new_shell(&mut self, session: Session, window: &mut Window, cx: &mut Context<Self>) {
        if crate::preview::active() {
            return;
        }
        self.new_shell_once(session, true, window, cx);
    }

    /// `retry`: a core left running by an earlier Fennec.app refuses new terminals
    /// once the app (and its herdr-core) was replaced ("no holder ... is not
    /// current"); Fennec's own instance is restarted and asked once more.
    fn new_shell_once(
        &mut self,
        session: Session,
        retry: bool,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.with_local(window, cx, move |this, window, cx| {
            let Some(core) = this.local.clone() else {
                return;
            };
            let display = crate::terminal::initial_display(window);
            let name = session.group_key();
            this._tasks.push(cx.spawn_in(window, async move |this, cx| {
                let started = cx
                    .background_spawn(async move {
                        let shell = std::env::var("SHELL").unwrap_or_else(|_| "/bin/zsh".into());
                        let home = std::env::var("HOME").ok();
                        core.run(
                            vec!["/usr/bin/env".into(), "-u".into(), "NO_COLOR".into(), shell, "-l".into()],
                            home,
                            serde_json::json!({"fennec.shell-of": name}),
                            None,
                            None,
                            display,
                        )
                    })
                    .await;
                let stale = matches!(&started, Err(e) if format!("{e:#}").contains("no holder") || format!("{e:#}").contains("is not current"));
                if stale && retry {
                    let restarted = cx
                        .background_spawn(async move {
                            let program = crate::core_bin::local()?;
                            crate::spawn::command(program).args(["server", "restart"]).output().ok()
                        })
                        .await;
                    let _ = this.update_in(cx, |this, window, cx| {
                        if restarted.is_none() {
                            return this.say(t("try_again"), cx);
                        }
                        this.local = None;
                        this.new_shell_once(session, false, window, cx);
                    });
                    return;
                }
                let _ = this.update_in(cx, |this, window, cx| match started {
                    Ok(address) => {
                        if let Some(group) = this.layout_group(&session.address) {
                            this.layout.pending = Some(serde_json::json!({"operation":"move","tab":format!("local:{address}"),"target":group.id}));
                        }
                        let list=this.shells.entry(session.group_key()).or_default();
                        if !list.contains(&address){list.push(address.clone());}
                        this.shell_names.entry(address.clone()).or_insert_with(||sessions::shell_name(&serde_json::Value::Null));
                        this.set_shown_tab(session.address.clone(), format!("local:{address}"), cx);
                        this.route = Some(Route::Session(session.address.clone()));
                        this.pending_focus = Some(format!("local:{address}"));
                        this.focus.focus(window, cx);
                        cx.notify();
                    }
                    Err(e) => this.say(format!("{e:#}"), cx),
                });
            }));
        });
    }

    fn close_shell(&mut self, address: String, window: &mut Window, cx: &mut Context<Self>) {
        let Some(core) = self.local.clone() else {
            return;
        };
        let target = address.clone();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let result = cx
                .background_spawn(async move {
                    core.call("terminal.kill", serde_json::json!({"terminal":target}))
                })
                .await;
            let _ = this.update_in(cx, |this, window, cx| {
                if result.is_ok() {
                    this.tab_closed(&format!("local:{address}"), window, cx);
                } else {
                    this.say(t("try_again"), cx);
                }
                cx.notify();
            });
        }));
    }
    fn open_app_settings(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.route != Some(Route::AppSettings) {
            self.app_preferences.previous = self.route.clone();
        }
        self.go(Route::AppSettings, window, cx);
        self.focus.focus(window, cx);
    }

    fn new_tab(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let context = if self.route == Some(Route::AppSettings) {
            &self.app_preferences.previous
        } else {
            &self.route
        };
        if let Some(Route::Session(id)) = context {
            if let Some(session) = self.sessions.iter().find(|s| &s.address == id).cloned() {
                self.new_shell(session, window, cx);
                return;
            }
        }
        self.start_session(String::new(), window, cx);
    }
    fn tab_keys(&self) -> Vec<String> {
        let Some(Route::Session(id)) = &self.route else {
            return Vec::new();
        };
        self.group_tabs(id)
    }
    fn pick_tab(&mut self, index: usize, cx: &mut Context<Self>) {
        let Some(Route::Session(id)) = &self.route else {
            return;
        };
        if let Some(key) = self.tab_keys().get(index) {
            self.set_shown_tab(id.clone(), key.clone(), cx);
            self.pending_focus = Some(key.clone());
            cx.notify();
        }
    }
    fn step_tab(&mut self, delta: isize, cx: &mut Context<Self>) {
        let Some(Route::Session(id)) = &self.route else {
            return;
        };
        let keys = self.tab_keys();
        if keys.is_empty() {
            return;
        }
        let current = keys
            .iter()
            .position(|k| k == &self.shown_tab(id))
            .unwrap_or(0);
        self.pick_tab(
            (current as isize + delta).rem_euclid(keys.len() as isize) as usize,
            cx,
        );
    }
    fn adopt_local_terminal(
        &mut self,
        record: serde_json::Value,
        select: bool,
        cx: &mut Context<Self>,
    ) {
        let Some(terminal) = sessions::from_terminals(&[record]).pop() else {
            return;
        };
        let address = terminal.address.clone();
        if let Some(view) = self.terms.remove(&format!("local:{address}")) {
            view.read(cx).detach();
        }
        if let Some(tabs) = self.shells.get_mut(&terminal.group_key()) {
            tabs.retain(|a| a != &address);
        }
        self.sessions.retain(|s| s.address != address);
        self.sessions.push(terminal);
        if select {
            self.confirmed_session = Some(address.clone());
            self.pending_focus = Some(address.clone());
            self.route = Some(Route::Session(address));
        }
        cx.notify();
    }
    fn ensure_local_terminal(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if crate::core_bin::cloud()
            || crate::preview::active()
            || self
                .sessions
                .iter()
                .any(|s| !s.address.starts_with("cloud:"))
            || self.creating_local
        {
            return;
        }
        let select = self.route.is_none()
            || matches!(&self.route, Some(Route::Session(id)) if !id.starts_with("cloud:"));
        let remaining = self
            .shells
            .iter()
            .filter(|(group, _)| !group.starts_with("cloud:"))
            .find_map(|(group, tabs)| tabs.first().map(|address| (group.clone(), address.clone())));
        if let (Some((group, address)), Some(core)) = (remaining, self.local.clone()) {
            self.creating_local = true;
            self._tasks.push(cx.spawn_in(window,async move|this,cx|{
                let result=cx.background_spawn(async move{core.call("terminal.label",serde_json::json!({"terminal":address,"labels":{"fennec.session":group,"fennec.agent":"shell","fennec.shell-of":""}}))}).await;
                let _=this.update(cx,|this,cx|{this.creating_local=false;match result{Ok(record)=>this.adopt_local_terminal(record,select&&matches!(this.route,None|Some(Route::Session(_))),cx),Err(e)=>this.say(format!("{e:#}"),cx)}});
            }));
        } else {
            self.start_session_selection(String::new(), select, window, cx);
        }
    }
    fn close_tab_key(&mut self, key: String, window: &mut Window, cx: &mut Context<Self>) {
        if matches!(&self.route,Some(Route::Session(root)) if self.shown_tab(root)==key) {
            self.focus.focus(window, cx);
        }
        if let Some(address) = key.strip_prefix("local:") {
            self.close_shell(address.to_owned(), window, cx);
            return;
        }
        if key.starts_with("cloud:") {
            self.close_cloud_session(key, window, cx);
            return;
        }
        let remote = crate::core_bin::cloud();
        let Some(core) = self.core.clone() else {
            return;
        };
        let address = key.clone();
        let root = self.layout.members.get(&key).or_else(|| self.sessions.iter().find(|s| s.address == key));
        let remote_name = if remote {
            root.map(|s| s.name.clone())
        } else {
            None
        };
        let promotion = if remote || !self.layout.groups.is_empty() {
            None
        } else {
            root.and_then(|s| {
                self.shells
                    .get(&s.group_key())
                    .and_then(|tabs| tabs.first())
                    .map(|a| (s.name.clone(), a.clone()))
            })
        };
        self._tasks.push(cx.spawn_in(window,async move|this,cx|{
            let result=cx.background_spawn(async move{
                let promoted=if let Some((name,child))=promotion{Some(core.call("terminal.label",serde_json::json!({"terminal":child,"labels":{"fennec.session":name,"fennec.agent":"shell","fennec.shell-of":""}}))?)}else{None};
                if let Some(name)=remote_name{crate::control::call("sessions.stop",serde_json::json!({"session_id":name}))?;}else{core.call("terminal.kill",serde_json::json!({"terminal":address}))?;}
                anyhow::Ok(promoted)
            }).await;
            let _=this.update_in(cx,|this,window,cx|match result{
                Ok(promoted)=>{
                    let select=this.route==Some(Route::Session(key.clone()))||this.route.is_none();
                    this.tab_closed(&key,window,cx);
                    if let Some(record)=promoted{this.adopt_local_terminal(record,select,cx);}
                    this.ensure_local_terminal(window,cx);cx.notify();
                },Err(e)=>this.say(format!("{e:#}"),cx)
            });
        }));
    }
    fn step_session(&mut self, delta: isize, window: &mut Window, cx: &mut Context<Self>) {
        if self.sessions.is_empty() {
            return;
        }
        let current = self
            .sessions
            .iter()
            .position(|s| self.route == Some(Route::Session(s.address.clone())))
            .unwrap_or(0);
        let index = (current as isize + delta).rem_euclid(self.sessions.len() as isize) as usize;
        self.go(
            Route::Session(self.sessions[index].address.clone()),
            window,
            cx,
        );
    }

    /// Key bindings the terminal core sends up (keys.rs).
    fn on_shell_action(&mut self, a: &ShellAction, window: &mut Window, cx: &mut Context<Self>) {
        if self.context_menu.is_some() {
            return;
        }
        match a.id() {
            "app.settings" => {
                self.open_app_settings(window, cx);
                return;
            }
            "tab.new" => {
                self.new_tab(window, cx);
                return;
            }
            "tab.next" => {
                self.step_tab(1, cx);
                return;
            }
            "tab.prev" => {
                self.step_tab(-1, cx);
                return;
            }
            "tab.close" => {
                if let Some(Route::Session(id)) = &self.route {
                    self.close_tab_key(self.shown_tab(id), window, cx);
                }
                return;
            }
            "space.next" | "agent.next" => {
                self.step_session(1, window, cx);
                return;
            }
            "space.prev" | "agent.prev" => {
                self.step_session(-1, window, cx);
                return;
            }
            "layout.toggle:sidebar" => {
                self.sidebar = !self.sidebar;
                cx.notify();
                return;
            }
            "mod.open:picker" => {
                self.sidebar = true;
                self.search.read(cx).focus_handle(cx).focus(window, cx);
                cx.notify();
                return;
            }
            "mod.open:tui-prefix" => {
                self.prefix = true;
                self.focus.focus(window, cx);
                cx.notify();
                return;
            }
            "mod.open:tui-close-others" => {
                self.confirm_close_other_tabs(window, cx);
                return;
            }
            "app.zoom_in" | "app.zoom_out" | "app.zoom_reset" => {
                let mut settings = (*crate::settings::get()).clone();
                settings.zoom = match a.id() {
                    "app.zoom_in" => (settings.zoom + 0.1).min(2.0),
                    "app.zoom_out" => (settings.zoom - 0.1).max(0.5),
                    _ => 1.0,
                };
                crate::settings::set(settings);
                cx.notify();
                return;
            }
            _ => {}
        }
        if let Some(index) = a
            .id()
            .strip_prefix("tab.select:")
            .and_then(|n| n.parse::<usize>().ok())
        {
            self.pick_tab(index.saturating_sub(1), cx);
            return;
        }
        if let Some(index) = a
            .id()
            .strip_prefix("space.select:")
            .and_then(|n| n.parse::<usize>().ok())
        {
            if let Some(s) = self.sessions.get(index.saturating_sub(1)) {
                self.go(Route::Session(s.address.clone()), window, cx);
            }
            return;
        }
        let Some(t) = self.shown_terminal() else {
            return cx.propagate();
        };
        match a.id() {
            "terminal.copy" => {
                t.update(cx, |t, cx| t.copy(cx));
            }
            "terminal.paste" => t.update(cx, |t, cx| t.paste(cx)),
            "terminal.paste_image" => t.update(cx, |t, cx| t.paste_image(cx)),
            "terminal.select_all" => t.update(cx, |t, cx| t.select_all(cx)),
            "terminal.search" => t.update(cx, |t, cx| t.open_search(window, cx)),
            _ => cx.propagate(),
        }
    }

    fn session_directory(&self, session: &Session) -> String {
        let tab = self.shown_tab(&session.address);
        let key = tab.strip_prefix("local:").unwrap_or(&tab);
        self.directories
            .get(key)
            .filter(|p| !p.is_empty())
            .cloned()
            .unwrap_or_else(|| session.directory.clone())
    }

    fn query(&self, cx: &App) -> String {
        self.search.read(cx).value().trim().to_lowercase()
    }

    // ---------- organization, new project, new session ----------

    /// Advances the sidebar's slide toward `self.sidebar`, wherever that was
    /// changed, and keeps frames coming until it settles.
    fn step_sidebar(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let target = if self.sidebar { 1.0 } else { 0.0 };
        if self
            .sidebar_motion
            .is_none_or(|(to, _, _)| to != self.sidebar)
            && self.sidebar_shown != target
        {
            self.sidebar_motion =
                Some((self.sidebar, self.sidebar_shown, std::time::Instant::now()));
            // Animation frames can pause (a hidden window); one late redraw still lands the slide.
            cx.spawn(async move |this, cx| {
                cx.background_executor()
                    .timer(Duration::from_secs_f32(SIDEBAR_SLIDE_SECONDS + 0.05))
                    .await;
                let _ = this.update(cx, |_, cx| cx.notify());
            })
            .detach();
        }
        let Some((_, from, start)) = self.sidebar_motion else {
            return;
        };
        let t = (start.elapsed().as_secs_f32() / SIDEBAR_SLIDE_SECONDS).min(1.0);
        // Ease out (cubic): quick to respond, gentle to land.
        let eased = 1.0 - (1.0 - t).powi(3);
        self.sidebar_shown = from + (target - from) * eased;
        if t < 1.0 {
            window.request_animation_frame();
        } else {
            self.sidebar_shown = target;
            self.sidebar_motion = None;
        }
    }

    /// The sidebar's session rows in order: plain shells first, then agents,
    /// local or cloud, grouped by state.
    fn session_list<'a>(&'a self, q: &str) -> Vec<SidebarRow<'a>> {
        // A cloud session a local terminal already shows is listed once, as that terminal.
        let shown_locally = self.shown_locally();
        let mut list: Vec<SidebarRow> = self
            .sessions
            .iter()
            .filter(|s| self.layout_group(&s.address).is_some_and(|g|g.custom) || !(s.address.starts_with("cloud:") && shown_locally.contains(&s.name)))
            .flat_map(|s| self.sidebar_rows(s))
            .filter(|r| {
                let s = r.session;
                let tab = r.tab.clone().unwrap_or_else(|| self.shown_tab(&s.address));
                q.is_empty()
                    || self.terminal_title(&tab, &s.title).to_lowercase().contains(q)
                    || s.name.contains(q)
                    || self.session_directory(s).to_lowercase().contains(q)
            })
            .collect();
        // Activity timestamps and snapshot order change independently of the
        // visible group. Keep peers stable rather than shuffling every poll:
        // newest-created first, so a new session opens at the top of its group.
        list.sort_by(|a, b| {
            (a.agent, if a.agent { a.state } else { State::Unknown })
                .cmp(&(b.agent, if b.agent { b.state } else { State::Unknown }))
                .then_with(|| b.session.created_ms.cmp(&a.session.created_ms))
                .then_with(|| a.session.address.cmp(&b.session.address))
                .then_with(|| a.tab.cmp(&b.tab))
        });
        list
    }

    /// The left inset of a title row whose own inset is `base`: while the
    /// sidebar is hidden it clears the traffic lights and the toggle, and it
    /// moves with the sidebar's slide.
    pub(crate) fn title_inset(&self, base: f32, gap: f32) -> Pixels {
        let collapsed = SIDEBAR_TOGGLE_LEFT + SIDEBAR_TOGGLE_SIZE + gap;
        px(base + (collapsed - base) * (1.0 - self.sidebar_shown))
    }

    /// The sidebar toggle sits beside the traffic lights at the same place
    /// whether the sidebar is shown or hidden; title rows leave room for it.
    fn sidebar_toggle(&self, cx: &mut Context<Self>) -> AnyElement {
        div()
            .absolute()
            .left(px(SIDEBAR_TOGGLE_LEFT))
            .top(px(10.0))
            .child(
                ui::icon_button("sidebar-toggle", "sidebar")
                    .size(px(SIDEBAR_TOGGLE_SIZE))
                    .on_click(cx.listener(|this, _, _, cx| {
                        this.sidebar = !this.sidebar;
                        cx.notify();
                    })),
            )
            .into_any_element()
    }

    fn menu(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        self.menu?;
        if self.menu == Some(Menu::RequestTag) {
            let p = self
                .project_id()
                .and_then(|id| self.projects.iter().find(|p| p.id == id))?;
            let options = std::iter::once((None, t("all_tags"))).chain(
                p.tags
                    .iter()
                    .map(|tag| (Some(tag.id.clone()), tag.label.clone())),
            );
            return Some(
                div()
                    .absolute()
                    .top(self.menu_anchor.y)
                    .left(self.menu_anchor.x)
                    .w(px(170.0))
                    .p(px(5.0))
                    .rounded(px(9.0))
                    .bg(c("f_card"))
                    .border_1()
                    .border_color(c("f_field"))
                    .shadow_lg()
                    .on_mouse_down_out(cx.listener(|this, _, _, cx| {
                        this.menu = None;
                        cx.notify();
                    }))
                    .occlude()
                    .children(options.enumerate().map(|(i, (id, label))| {
                        let selected = self.tag == id;
                        h_flex()
                            .id(SharedString::from(format!("tag-option-{i}")))
                            .h(px(29.0))
                            .px(px(9.0))
                            .gap(px(8.0))
                            .rounded(px(5.0))
                            .hover(|s| s.bg(c("f_expanded")))
                            .text_size(px(12.5))
                            .on_click(cx.listener(move |this, _, _, cx| {
                                this.tag = id.clone();
                                this.menu = None;
                                cx.notify();
                            }))
                            .child(div().flex_1().child(label))
                            .when(selected, |d| d.child(ui::icon("check", 13.0, c("orange"))))
                    }))
                    .into_any_element(),
            );
        }

        let item = |id: SharedString| {
            h_flex()
                .id(id)
                .h(px(34.0))
                .px(px(9.0))
                .gap(px(8.0))
                .rounded(px(5.0))
                .text_size(px(12.5))
                .font_weight(FontWeight::MEDIUM)
                .text_color(c("f_text"))
                .hover(|s| s.bg(c("f_expanded")))
        };
        let mut items =
            vec![
                item("new-terminal".into())
                    .on_click(cx.listener(|this, _, window, cx| {
                        this.start_session(String::new(), window, cx)
                    }))
                    .child(ui::icon("term", 15.0, c("f_text")))
                    .child(div().flex_1().child(t("terminal")))
                    .child(div().text_color(c("f_muted")).child("⌘T"))
                    .into_any_element(),
                div()
                    .px(px(9.0))
                    .pt(px(10.0))
                    .pb(px(5.0))
                    .text_size(px(11.0))
                    .text_color(c("f_muted"))
                    .child(t("start_agent"))
                    .into_any_element(),
            ];
        for (i, agent) in [
            "claude", "codex", "pi", "opencode", "cursor", "grok", "kimi",
        ]
        .iter()
        .enumerate()
        {
            let a = agent.to_string();
            items.push(
                item(SharedString::from(format!("agent-{agent}")))
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.start_session(a.clone(), window, cx)
                    }))
                    .child(ui::agent_icon(agent, 15.0))
                    .child(div().flex_1().child(sessions::agent_name(agent).to_owned()))
                    .child(div().text_color(c("f_muted")).child((i + 1).to_string()))
                    .into_any_element(),
            );
        }
        Some(
            div()
                .absolute()
                .top(self.menu_anchor.y)
                .left(self.menu_anchor.x)
                .w(px(230.0))
                .p(px(5.0))
                .rounded(px(9.0))
                .bg(c("f_card"))
                .border_1()
                .border_color(c("f_field"))
                .shadow_lg()
                .on_mouse_down_out(cx.listener(|this, _, _, cx| {
                    this.menu = None;
                    cx.notify();
                }))
                .occlude()
                .children(items)
                .into_any_element(),
        )
    }

    fn start_session(&mut self, agent: String, window: &mut Window, cx: &mut Context<Self>) {
        self.start_session_selection(agent, true, window, cx);
    }
    fn start_session_selection(
        &mut self,
        agent: String,
        select: bool,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if crate::preview::active() {
            self.menu = None;
            cx.notify();
            return;
        }
        self.menu = None;
        if !crate::core_bin::cloud() || agent.is_empty() {
            if self.creating_local {
                return;
            }
            self.creating_local = true;
            let origin = self.route.clone();
            let display = crate::terminal::initial_display(window);
            self.with_local(window, cx, move |this, window, cx| {
                let Some(core) = this.local.clone() else { return; };
                this._tasks.push(cx.spawn_in(window, async move |this, cx| {
                    let started = cx.background_spawn(async move {
                        let shell = std::env::var("SHELL").unwrap_or_else(|_| "/bin/zsh".into());
                        let command = if agent == "cursor" { "cursor-agent" } else { &agent };
                        let argv = if agent.is_empty() { vec![shell, "-l".into()] }
                            else { vec![shell, "-ilc".into(), format!("exec {command}")] };
                        let name = format!("desktop-{}", sessions::now_ms());
                        // A long-lived core can retain NO_COLOR from a noninteractive launcher.
                        let argv = [vec!["/usr/bin/env".into(), "-u".into(), "NO_COLOR".into()], argv].concat();
                        let cwd = if agent.is_empty() { None } else { board::local_folder(&agent, &name) }
                            .or_else(|| std::env::var("HOME").ok());
                        core.run(argv, cwd, serde_json::json!({"fennec.session":name, "fennec.agent":if agent.is_empty() { "shell" } else { &agent }}), None, None, display)
                    }).await;
                    let _ = this.update(cx, |this, cx| {this.creating_local=false;match started {
                        Ok(address) => { let key = if crate::core_bin::cloud() { format!("local:{address}") } else { address }; this.confirmed_session = Some(key.clone()); if select && (this.route==origin || this.route.is_none()) {this.pending_focus = Some(key.clone()); this.route = Some(Route::Session(key));} cx.notify(); },
                        Err(e) => this.say(format!("{e:#}"), cx),
                    }});
                }));
            });
            return;
        }
        let origin = self.route.clone();
        self.starting = Some(String::new());
        cx.notify();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let started = cx
                .background_spawn(async move { board::start_session(&agent) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| match started {
                Ok(name) => {
                    this.starting = if this.route == origin {
                        Some(name)
                    } else {
                        None
                    };
                    this.terminals_changed(window, cx);
                }
                Err(e) => {
                    this.starting = None;
                    this.say(format!("{e:#}"), cx);
                }
            });
        }));
    }

    fn open_new_project(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let name = cx.new(|cx| InputState::new(window, cx).placeholder(t("name")));
        let description = cx.new(|cx| InputState::new(window, cx).placeholder(t("description")));
        let repository = cx.new(|cx| InputState::new(window, cx).placeholder("owner/repo"));
        name.read(cx).focus_handle(cx).focus(window, cx);
        self.new_project = Some(NewProject {
            name,
            description,
            repository,
            agent: "claude".into(),
        });
        self.menu = None;
        cx.notify();
    }

    fn create_project(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(np) = self.new_project.as_ref() else {
            return;
        };
        let (name, description, repository) = (
            np.name.read(cx).value().trim().to_owned(),
            np.description.read(cx).value().trim().to_owned(),
            np.repository.read(cx).value().trim().to_owned(),
        );
        if name.is_empty() {
            return;
        }
        let org = String::new();
        let agent = np.agent.clone();
        let color = board::COLORS[self.projects.len() % board::COLORS.len()];
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let created = cx
                .background_spawn(async move {
                    board::create_project(&name, &org, &description, &repository, color, &agent)
                })
                .await;
            let _ = this.update_in(cx, |this, window, cx| match created {
                Ok(p) => {
                    this.new_project = None;
                    this.projects.push(p.clone());
                    this.go(Route::Project(p.id), window, cx);
                }
                Err(_) => this.say(t("check_repository"), cx),
            });
        }));
    }

    fn new_project_dialog(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let np = self.new_project.as_ref()?;
        let field = |label: String, input: &Entity<InputState>| {
            v_flex()
                .gap(px(5.0))
                .child(
                    div()
                        .text_size(px(11.5))
                        .text_color(c("f_muted"))
                        .child(label),
                )
                .child(
                    div()
                        .h(px(32.0))
                        .px(px(9.0))
                        .rounded(px(8.0))
                        .border_1()
                        .border_color(c("f_field"))
                        .bg(c("f_card"))
                        .flex()
                        .items_center()
                        .text_size(px(13.0))
                        .child(Input::new(input)),
                )
        };
        Some(
            div()
                .id("scrim")
                .absolute()
                .inset_0()
                .bg(hsla(0.0, 0.0, 0.0, 0.35))
                .flex()
                .items_center()
                .justify_center()
                .occlude()
                .on_click(cx.listener(|this, _, _, cx| {
                    this.new_project = None;
                    cx.notify();
                }))
                .child(
                    v_flex()
                        .id("new-project")
                        .w(px(400.0))
                        .p(px(16.0))
                        .gap(px(12.0))
                        .rounded(px(12.0))
                        .bg(c("f_card"))
                        .shadow_lg()
                        .on_click(|_, _, cx| cx.stop_propagation())
                        .child(
                            div()
                                .text_size(px(14.0))
                                .font_weight(FontWeight::MEDIUM)
                                .child(t("new_project")),
                        )
                        .child(field(t("name"), &np.name))
                        .child(field(t("description"), &np.description))
                        .child(field(t("repository_optional"), &np.repository))
                        .child(
                            div()
                                .text_size(px(12.0))
                                .text_color(c("f_muted"))
                                .child(t("default_agent")),
                        )
                        .child(h_flex().gap(px(4.0)).flex_wrap().children(
                            board::AGENTS.iter().map(|agent| {
                                let agent = agent.to_string();
                                secondary_button(
                                    SharedString::from(format!("new-worker-{agent}")),
                                    sessions::agent_name(&agent).to_owned(),
                                    np.agent == agent,
                                )
                                .on_click(cx.listener(
                                    move |this, _, _, cx| {
                                        if let Some(np) = &mut this.new_project {
                                            np.agent = agent.clone();
                                        }
                                        cx.notify();
                                    },
                                ))
                            }),
                        ))
                        .child(
                            div()
                                .text_size(px(11.0))
                                .text_color(c("f_muted"))
                                .child(t("run_location_hint")),
                        )
                        .child(
                            h_flex()
                                .justify_end()
                                .gap(px(8.0))
                                .child(secondary_button("np-cancel", t("cancel"), false).on_click(
                                    cx.listener(|this, _, _, cx| {
                                        this.new_project = None;
                                        cx.notify();
                                    }),
                                ))
                                .child(primary_button("np-create", t("create")).on_click(
                                    cx.listener(|this, _, window, cx| {
                                        this.create_project(window, cx)
                                    }),
                                )),
                        ),
                )
                .into_any_element(),
        )
    }

    // ---------- sidebar ----------

    /// A session's sidebar rows: one for the session, or, when it runs several
    /// agents (its own terminal and agent tabs), one per agent with that agent's state.
    fn sidebar_rows<'a>(&self, s: &'a Session) -> Vec<SidebarRow<'a>> {
        let keys = self.group_tabs(&s.address).into_iter();
        if let Some(group) = self.layout_group(&s.address).filter(|g| g.custom) {
            return group.tabs.iter().map(|key| {
                let address = key.strip_prefix("local:").unwrap_or(key);
                let member = self.layout.members.get(key).unwrap_or(s);
                let observed = &self.activity[address];
                SidebarRow {
                    session: s,
                    tab: Some(key.clone()),
                    state: observed["state"].as_str().map(activity::state).unwrap_or(member.state),
                    since_ms: self.events.as_ref().and_then(|e| e.status_ms(address)).unwrap_or(member.since_ms),
                    agent: key.starts_with("cloud:") || observed["program"]["agent"] == true || observed["state"].as_str().is_some_and(|w| !w.is_empty()),
                }
            }).collect();
        }
        let agents: Vec<String> = keys
            .filter(|key| {
                let p = &self.activity[key.strip_prefix("local:").unwrap_or(key)];
                p["program"]["agent"] == true || p["state"].as_str().is_some_and(|w| !w.is_empty())
            })
            .collect();
        if agents.len() < 2 {
            return vec![SidebarRow {
                session: s,
                tab: None,
                state: s.state,
                since_ms: s.since_ms,
                agent: s.address.starts_with("cloud:") || !agents.is_empty(),
            }];
        }
        agents
            .into_iter()
            .map(|key| {
                let address = key.strip_prefix("local:").unwrap_or(&key);
                let since_ms = if key == s.address {
                    s.since_ms
                } else {
                    self.events
                        .as_ref()
                        .and_then(|e| e.status_ms(address))
                        .unwrap_or(s.since_ms)
                };
                SidebarRow {
                    session: s,
                    state: activity::state(
                        self.activity[address]["state"].as_str().unwrap_or_default(),
                    ),
                    since_ms,
                    tab: Some(key),
                    agent: true,
                }
            })
            .collect()
    }

    fn sidebar(&self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let q = self.query(cx);
        let route = self.route.clone();
        let header = |label: String, plus: Stateful<Div>| {
            h_flex()
                .h(px(41.0))
                .pt(px(14.0))
                .pb(px(5.0))
                .px(px(8.0))
                .justify_between()
                .text_size(px(11.5))
                .font_weight(FontWeight::MEDIUM)
                .text_color(c("f_muted"))
                .child(label)
                .child(plus)
        };
        let plus = |id: &'static str, open: bool| {
            ui::compact_icon_button(id, "plus")
                .mr(px(-6.0))
                .my(px(-3.0))
                .when(open, |d| d.bg(c("f_line")))
        };
        let row = |id: SharedString, selected: bool| {
            h_flex()
                .id(id)
                .h(px(ROW_HEIGHT))
                .mb(px(ROW_GAP))
                .px(px(8.0))
                .gap(px(9.0))
                .rounded(px(7.0))
                .text_size(px(13.0))
                .font_weight(FontWeight::MEDIUM)
                .text_color(c("f_text"))
                .when(selected, |d| {
                    d.bg(c("f_card")).shadow(ui::selection_shadow())
                })
                .when(!selected, |d| d.hover(|s| s.bg(c("f_line").opacity(0.5))))
        };
        let projects = self.visible_projects().into_iter().filter(|p| q.is_empty() || p.name.to_lowercase().contains(&q)).map(|p| {
            let selected = matches!(&route, Some(Route::Project(id) | Route::Settings(id, _)) if id == &p.id);
            let id = p.id.clone();
            self.context_row(row(SharedString::from(format!("p-{}", p.id)), selected), context_menu::Target::Project(p.id.clone()), cx)
                .on_click(cx.listener(move |this, event: &ClickEvent, window, cx| { if !event.is_right_click() && !event.modifiers().control { this.go(Route::Project(id.clone()), window, cx); } }))
                .child(ui::project_icon(p, 18.0))
                .child(div().flex_1().min_w_0().truncate().child(p.name.clone()))
                .when(p.needs_you > 0, |d| {
                    d.child(div().min_w(px(18.0)).h(px(17.0)).px(px(5.0)).rounded(px(9.0)).bg(c("tint")).border_1().border_color(c("orange").opacity(0.3)).text_color(c("accent_text")).text_size(px(10.5)).font_weight(FontWeight::MEDIUM).flex().items_center().justify_center().child(p.needs_you.to_string()))
                })
        })
            .collect::<Vec<_>>();
        let now = sessions::now_ms();
        let mut groups = Vec::new();
        let list = self.session_list(&q);
        let mut age_font = window.text_style().font();
        age_font.weight = FontWeight::MEDIUM;
        let age_width = list.iter().map(|row| {
            let age = sessions::age(row.since_ms, now);
            let run = TextRun {len:age.len(),font:age_font.clone(),..Default::default()};
            window.text_system().shape_line(age.into(),px(11.0),&[run],None).width()
        }).fold(px(0.0), |width,next|width.max(next));
        let mut section = None;
        for (i, r) in list.iter().enumerate() {
            let s = r.session;
            let grouped = r.agent;
            if grouped && section != Some(r.state) {
                section = Some(r.state);
                let (label, color) = state_label(r.state);
                let count = list[i..]
                    .iter()
                    .take_while(|o| o.agent && o.state == r.state)
                    .count();
                groups.push(
                    h_flex()
                        .h(px(GROUP_HEADER_HEIGHT))
                        .flex_none()
                        .line_height(px(18.0))
                        .px(px(8.0))
                        .pt(px(10.0))
                        .pb(px(4.0))
                        .gap(px(6.0))
                        .text_size(px(11.5))
                        .font_weight(FontWeight::MEDIUM)
                        .child(div().text_color(c(color)).child(t(label)))
                        .child(div().text_color(c("f_muted")).child(count.to_string()))
                        .into_any_element(),
                );
            }
            // A row for one of several agents opens that agent's tab and shows its own state.
            let split = r.tab.is_some();
            let tab = r.tab.clone().unwrap_or_else(|| self.shown_tab(&s.address));
            let activity = tab.strip_prefix("local:").unwrap_or(&tab).to_owned();
            let selected = route == Some(Route::Session(s.address.clone()))
                && (!split || self.shown_tab(&s.address) == tab);
            let address = s.address.clone();
            let open = r.tab.clone();
            let drop_group = self.layout_group(&s.address).map(|g| g.id.clone()).unwrap_or_default();
            let custom = self.layout_group(&s.address).is_some_and(|g| g.custom);
            let hover_root = s.address.clone();
            let mark = |this: &Self| {
                if split {
                    this.activity_mark(&activity)
                } else {
                    this.group_activity_mark(&s.address)
                }
            };
            let id = row_key(r);
            groups.push(
                self.context_row(
                    row(SharedString::from(id), selected).flex_none().debug_selector(||format!("session-row-{}",tab)),
                    context_menu::Target::Terminal {
                        session: s.address.clone(),
                        tab: tab.clone(),
                    },
                    cx,
                )
                .on_drag(self.drag_tab(&tab), |drag,_,_,cx|cx.new(|_|drag.clone()))
                .drag_over::<tabs::TabDrag>(|s,_,_,_|s.bg(c("f_line")))
                .on_drag_move::<tabs::TabDrag>(cx.listener(move |this,event:&DragMoveEvent<tabs::TabDrag>,_,cx| {
                    if event.bounds.contains(&event.event.position) {
                        if this.layout.hover_group.as_ref().is_none_or(|(root,_)|root!=&hover_root) {
                            this.layout.hover_group=Some((hover_root.clone(),std::time::Instant::now()));cx.notify();
                        }
                    } else if this.layout.hover_group.as_ref().is_some_and(|(root,_)|root==&hover_root) {
                        this.layout.hover_group=None;
                    }
                }))
                .on_drop(cx.listener(move |this,drag:&tabs::TabDrag,_,cx| {
                    cx.stop_propagation();
                    this.move_tab(drag,drop_group.clone(),String::new(),cx);
                }))
                .on_click(cx.listener(move |this, event: &ClickEvent, window, cx| {
                    if !event.is_right_click() && !event.modifiers().control {
                        if let Some(tab) = &open {
                            this.set_shown_tab(address.clone(), tab.clone(), cx);
                        }
                        this.go(Route::Session(address.clone()), window, cx);
                    }
                }))
                .child(
                    div()
                        .relative()
                        .size(px(16.0))
                        .flex_none()
                        .child(self.program_icon(
                            if split || custom { &activity } else { &s.address },
                            &s.agent,
                            16.0,
                        ))
                        .child(
                            div()
                                .absolute()
                                .right(px(-1.0))
                                .bottom(px(-1.0))
                                .when(!grouped, |d| d.child(mark(self))),
                        ),
                )
                .child(div().flex_1().min_w_0().truncate().child({
                    let key = tab.strip_prefix("local:").unwrap_or(&tab);
                    let path = self
                        .directories
                        .get(key)
                        .map(String::as_str)
                        .filter(|p| !p.is_empty())
                        .unwrap_or(&s.directory);
                    if custom || (grouped && (split || !tab.starts_with("local:"))) || path.is_empty() {
                        self.terminal_title(&tab, &s.title)
                    } else {
                        sessions::directory_label(path)
                    }
                }))
                .child(
                    h_flex()
                        .flex_none()
                        .gap(px(6.0))
                        .when(grouped, |d| d.child(h_flex()
                            .debug_selector(|| format!("session-spinner-{}", row_key(r)))
                            .w(px(12.0)).flex_none().justify_center().child(mark(self))))
                        .text_size(px(11.0))
                        .text_color(c("f_muted"))
                        .child(h_flex().w(age_width).flex_none().justify_end()
                            .child(div()
                                .debug_selector(|| format!("session-age-{}", row_key(r)))
                                .child(sessions::age(r.since_ms, now)))),
                )
                .into_any_element(),
            );
        }
        v_flex()
            .w(px(self.sidebar_width(window)))
            .h_full()
            .flex_none()
            .bg(c("f_bg"))
            .border_r_1()
            .border_color(c("f_line"))
            .child(
                // Title row: the traffic lights sit here; the row drags the window.
                h_flex()
                    .id("titlebar")
                    .h(px(46.0))
                    .flex_none()
                    .pl(px(84.0))
                    .pr(px(10.0))
                    .gap(px(7.0))
                    .on_mouse_down(MouseButton::Left, ui::drag_titlebar),
            )
            .child(
                div().px(px(10.0)).pt(px(2.0)).pb(px(6.0)).child(
                    h_flex()
                        .h(px(30.0))
                        .px(px(8.0))
                        .gap(px(7.0))
                        .rounded(px(7.0))
                        .bg(c("f_line"))
                        .child(ui::icon("search", 14.0, c("f_muted")))
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .text_size(px(13.0))
                                .child(Input::new(&self.search)),
                        )
                        .child(
                            div()
                                .text_size(px(11.0))
                                .text_color(c("f_muted"))
                                .child("⌘K"),
                        ),
                ),
            )
            .child(
                v_flex()
                    .id("side-scroll")
                    .py(px(4.0))
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .px(px(8.0))
                    .child(header(
                        t("projects"),
                        plus("new-project", self.new_project.is_some()).on_click(
                            cx.listener(|this, _, window, cx| this.open_new_project(window, cx)),
                        ),
                    ))
                    .children(projects)
                    .child(header(
                        t("sessions"),
                        plus("new-session", self.menu == Some(Menu::NewSession)).on_click(
                            cx.listener(|this, event: &ClickEvent, _, cx| {
                                this.menu_anchor = point(px(120.0), event.position().y + px(14.0));
                                this.menu = if this.menu == Some(Menu::NewSession) {
                                    None
                                } else {
                                    Some(Menu::NewSession)
                                };
                                cx.notify();
                            }),
                        ),
                    ))
                    .when_some(self.starting.as_ref(), |d, _| {
                        d.child(
                            h_flex()
                                .h(px(30.0))
                                .px(px(8.0))
                                .gap(px(9.0))
                                .text_size(px(13.0))
                                .text_color(c("f_muted"))
                                .child(t("starting")),
                        )
                    })
                    .when(self.core.is_none() && self.core_error.is_none(), |d| {
                        d.child(
                            div()
                                .px(px(8.0))
                                .py(px(6.0))
                                .text_size(px(12.0))
                                .text_color(c("f_muted"))
                                .child(t(if crate::core_bin::cloud() {
                                    "connecting"
                                } else {
                                    "loading"
                                })),
                        )
                    })
                    .children(groups)
                    .child(div().id("detach-tab-area").debug_selector(||"detach-tab-area".into()).min_h(px(56.0)).w_full().flex_1().rounded(px(7.0))
                        .drag_over::<tabs::TabDrag>(|s,_,_,_|s.bg(c("f_line")))
                        .on_drop(cx.listener(|this,drag:&tabs::TabDrag,_,cx| {
                            cx.stop_propagation();this.move_tab(drag,String::new(),String::new(),cx);
                        }))
                        .when(cx.has_active_drag(), |d| d.child(div().p(px(10.0)).text_size(px(11.0)).text_color(c("f_muted")).child(t("tabs_drop_split")))))
                    .when_some(self.layout.error.as_ref(), |d,error| d.child(div().p(px(8.0)).text_size(px(11.0)).text_color(c("f_muted")).child(error.clone()))),
            )
            .child(div().h(px(1.0)).bg(c("f_line")))
            .child(
                div().px(px(8.0)).pt(px(6.0)).pb(px(10.0)).child(
                    h_flex()
                        .id("app-settings")
                        .h(px(30.0))
                        .w_full()
                        .px(px(8.0))
                        .gap(px(9.0))
                        .rounded(px(7.0))
                        .text_size(px(13.0))
                        .font_weight(FontWeight::MEDIUM)
                        .text_color(c("f_muted"))
                        .when(route == Some(Route::AppSettings), |d| {
                            d.bg(c("f_card"))
                                .shadow(ui::selection_shadow())
                                .text_color(c("f_text"))
                        })
                        .when(route != Some(Route::AppSettings), |d| {
                            d.hover(|s| s.bg(c("f_line").opacity(0.5)))
                        })
                        .on_click(
                            cx.listener(|this, _, window, cx| this.open_app_settings(window, cx)),
                        )
                        .child(ui::icon("gear", 15.0, c("f_muted")))
                        .child(t("settings")),
                ),
            )
    }

    // ---------- project ----------

    fn project(&self, p: &Project, window: &Window, cx: &mut Context<Self>) -> impl IntoElement {
        let q = self.query(cx);
        let all = self.requests.get(&p.id).cloned().unwrap_or_default();
        let mut shown: Vec<&Request> = all
            .iter()
            .filter(|r| r.resolved() == self.resolved)
            .filter(|r| self.tag.as_ref().is_none_or(|t| &r.tag == t))
            .filter(|r| q.is_empty() || r.title.to_lowercase().contains(&q))
            .collect();
        let group = |status: &str| match status {
            "you" => 0,
            "failed" => 1,
            "review" => 2,
            "unknown" => 3,
            "working" => 4,
            "queued" => 5,
            _ => 6,
        };
        shown.sort_by_key(|r| group(&r.status));
        let open_count = all.iter().filter(|r| !r.resolved()).count();
        let resolved_count = all.len() - open_count;
        let tag_label = |id: &str| {
            p.tags
                .iter()
                .find(|t| t.id == id)
                .map(|t| t.label.clone())
                .unwrap_or_else(|| id.to_owned())
        };
        let now = sessions::now_ms();
        let image_status = self.request_image_status(&p.id);
        let composer_has = !self.composer.read(cx).value().trim().is_empty()
            || self.request_images.by_queue.get(&p.id).is_some_and(|images|!images.is_empty());
        let segment = |label: String, count: usize, on: bool, resolved: bool| {
            div()
                .id(SharedString::from(format!("seg-{resolved}")))
                .h(px(24.0))
                .px(px(10.0))
                .rounded(px(6.0))
                .flex()
                .items_center()
                .text_size(px(12.0))
                .font_weight(FontWeight::MEDIUM)
                .text_color(if on { c("f_text") } else { c("f_muted") })
                .when(on, |d| {
                    d.bg(c("f_card")).border_1().border_color(c("f_field"))
                })
                .when(!on, |d| d.border_1().border_color(transparent_black()))
                .on_click(cx.listener(move |this, _, _, cx| {
                    this.resolved = resolved;
                    cx.notify();
                }))
                .gap(px(6.0))
                .child(label)
                .child(
                    div()
                        .text_size(px(11.5))
                        .font_weight(FontWeight::NORMAL)
                        .text_color(c("f_muted"))
                        .child(count.to_string()),
                )
        };
        let glide_scope = format!("{}|{}|{:?}|{q}", p.id, self.resolved, self.tag);
        let frame_at = std::time::Instant::now();
        let appear = |key: &str| self.queue_glide.borrow_mut().appear(&glide_scope, key, frame_at);
        let cards = shown.iter().enumerate().map(|(index, r)| {
            let selected = self.opening.or(self.open) == Some(r.number);
            let n = r.number;
            let latest = r
                .latest
                .as_ref()
                .map(|m| {
                    m.body
                        .lines()
                        .find(|l| !l.trim().is_empty())
                        .unwrap_or_default()
                        .to_owned()
                })
                .unwrap_or_default();
            // Why an open request is not progressing (stalled agent,
            // unconfirmed delivery) says more than its last message.
            let latest = match r.failure.as_str() {
                _ if r.resolved() => latest,
                "" => latest,
                board::STALLED_FAILURE => t("stalled"),
                failure => failure.to_owned(),
            };
            let project = p.id.clone();
            let drop_project = project.clone();
            let next_project = project.clone();
            let card_key = format!("req-{n}");
            let card = v_flex()
                .id(SharedString::from(card_key.clone()))
                .opacity(appear(&card_key))
                .w_full()
                .px(px(13.0))
                .py(px(11.0))
                .mb(px(4.0))
                .gap(px(4.0))
                .rounded(px(10.0))
                .border_1()
                .border_color(if selected {
                    c("f_field")
                } else {
                    transparent_black()
                })
                .when(selected, |d| d.bg(c("f_card")))
                .when(!selected, |d| d.hover(|s| s.bg(c("f_line").opacity(0.5))))
                .on_click(cx.listener(move |this, _, window, cx| this.open_request(n, window, cx)))
                .when(r.status == "queued", |d| {
                    d.on_drag(
                        QueuedRequestDrag {
                            project,
                            number: n,
                            title: r.title.clone(),
                        },
                        |drag, _, _, cx| cx.new(|_| drag.clone()),
                    )
                    .on_drop(cx.listener(
                        move |this, drag: &QueuedRequestDrag, window, cx| {
                            if drag.project == drop_project && drag.number != n {
                                this.move_request(drop_project.clone(), drag.number, n, window, cx);
                            }
                        },
                    ))
                })
                .child(
                    h_flex()
                        .items_start()
                        .gap(px(8.0))
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .text_size(px(13.5))
                                .line_height(relative(1.35))
                                .font_weight(if r.resolved() {
                                    FontWeight::MEDIUM
                                } else {
                                    FontWeight::MEDIUM
                                })
                                .text_color(if r.resolved() {
                                    c("f_muted")
                                } else {
                                    c("f_text")
                                })
                                .child(r.title.clone()),
                        )
                        .child(ui::status_pill(&r.status)),
                )
                .when(!latest.is_empty(), |d| {
                    d.child(
                        div()
                            .text_size(px(12.0))
                            .text_color(c("f_muted"))
                            .truncate()
                            .child(latest),
                    )
                })
                .child(
                    h_flex()
                        .mt(px(5.0))
                        .gap(px(10.0))
                        .text_size(px(11.0))
                        .text_color(c("f_muted"))
                        .child(format!("{}-{}", p.key, r.number))
                        .child(ui::tag_chip(&r.tag, &tag_label(&r.tag)))
                        .when(!r.agent.is_empty(), |d| {
                            d.child(ui::agent_icon(&r.agent, 14.0))
                        })
                        .child(
                            h_flex()
                                .gap(px(3.0))
                                .child(ui::icon("chat", 12.0, c("f_muted")))
                                .child(r.replies.to_string()),
                        )
                        .when(r.votes > 0, |d| d.child(format!("+{}", r.votes)))
                        .child(sessions::age(sessions::rfc3339_ms(&r.updated), now)),
                )
                .when(r.status == "queued", |d| {
                    d.child(
                        h_flex().child(
                            secondary_button(
                                SharedString::from(format!("next-{n}")),
                                t("run_next"),
                                false,
                            )
                            .on_click(cx.listener(
                                move |this, _, window, cx| {
                                    cx.stop_propagation();
                                    this.move_request(next_project.clone(), n, 0, window, cx);
                                },
                            )),
                        ),
                    )
                })
                .into_any_element();
            v_flex()
                .when(index == 0 || shown[index - 1].status != r.status, |d| {
                    let header_key = format!("group-{}", r.status);
                    d.child(glide::glided(
                        &self.queue_glide,
                        header_key.clone(),
                        div()
                            .opacity(appear(&header_key))
                            .px(px(13.0))
                            .pt(px(12.0))
                            .pb(px(6.0))
                            .text_size(px(11.5))
                            .text_color(c("f_muted"))
                            .child(t(match r.status.as_str() {
                                "you" => "needs_you",
                                "review" => "review",
                                "working" => "working",
                                "queued" => "queued",
                                "failed" => "failed",
                                "unknown" => "unknown",
                                _ => "resolved",
                            })),
                    ))
                })
                .child(glide::glided(&self.queue_glide, card_key, card))
                .into_any_element()
        })
        .collect::<Vec<_>>();
        if self.queue_glide.borrow().fading() {
            window.request_animation_frame();
        }
        v_flex()
            .w(relative(0.42))
            .max_w(px(460.0))
            .min_w_0()
            .h_full()
            .flex_none()
            .bg(c("background"))
            .border_r_1()
            .border_color(c("f_line"))
            .child(
                h_flex()
                    .id("project-head")
                    .h(px(58.0))
                    .flex_none()
                    .px(px(18.0))
                    .gap(px(11.0))
                    .border_b_1()
                    .border_color(c("f_line"))
                    .on_mouse_down(MouseButton::Left, ui::drag_titlebar)
                    .pl(self.title_inset(18.0, 11.0))
                    .child(ui::project_icon(p, 30.0))
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .gap(px(2.0))
                            .child(
                                div()
                                    .text_size(px(13.5))
                                    .font_weight(FontWeight::MEDIUM)
                                    .text_color(c("f_text"))
                                    .truncate()
                                    .child(p.name.clone()),
                            )
                            .when(!p.description.is_empty(), |d| {
                                d.child(
                                    div()
                                        .text_size(px(11.5))
                                        .text_color(c("f_muted"))
                                        .truncate()
                                        .child(p.description.clone()),
                                )
                            }),
                    )
                    .child({
                        let id = p.id.clone();
                        div()
                            .id("project-settings")
                            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                            .size(px(28.0))
                            .rounded(px(6.0))
                            .flex()
                            .items_center()
                            .justify_center()
                            .hover(|s| s.bg(c("f_line")))
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.go(
                                    Route::Settings(
                                        id.clone(),
                                        crate::project_settings::Tab::General,
                                    ),
                                    window,
                                    cx,
                                )
                            }))
                            .child(ui::icon("gear", 15.0, c("f_muted")))
                    }),
            )
            .child(
                v_flex()
                    .id("requests")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .p(px(14.0))
                    .when(p.sync_status == "offline", |d| {
                        d.child(stale_board_banner(&p.synced_at, now))
                    })
                    .child(
                        // New Request: one plain box; the first line is the title.
                        v_flex()
                            .flex_none()
                            .w_full()
                            .relative()
                            .on_mouse_down(
                                MouseButton::Left,
                                cx.listener(|this, _, window, cx| {
                                    this.composer.read(cx).focus_handle(cx).focus(window, cx)
                                }),
                            )
                            .rounded(px(10.0))
                            .bg(c("f_card"))
                            .border_1()
                            .border_color(
                                if self.composer.read(cx).focus_handle(cx).is_focused(window) {
                                    c("f_text").opacity(0.22)
                                } else {
                                    c("f_field")
                                },
                            )
                            .px(px(13.0))
                            .py(px(9.0))
                            .pb(px(34.0))
                            .min_h(px(84.0))
                            .text_size(px(13.5))
                            .line_height(relative(1.6))
                            .child(div().w_full().flex_none().child(Textarea::new(&self.composer)))
                            .when_some(self.draft_images(&p.id,cx),|d,images|d.child(images))
                            .when_some(image_status, |d, key| d.child(div().absolute().left(px(13.0)).right(px(80.0)).bottom(px(8.0)).h(px(16.0)).truncate().text_size(px(11.0)).text_color(c("f_muted")).child(t(key))))
                            .when(!composer_has && image_status.is_none(), |d| {
                                d.child(
                                    div()
                                        .absolute()
                                        .right(px(10.0))
                                        .bottom(px(8.0))
                                        .text_size(px(11.0))
                                        .text_color(c("f_muted"))
                                        .child(t("compose_shortcuts")),
                                )
                            })
                            .when(composer_has, |d| {
                                d.child(div().absolute().right(px(8.0)).bottom(px(8.0)).child(
                                    primary_button("post", t("post")).on_click(
                                        cx.listener(|this, _, window, cx| this.post(window, cx)),
                                    ),
                                ))
                            }),
                    )
                    .child(
                        h_flex()
                            .mt(px(12.0))
                            .mb(px(10.0))
                            .gap(px(8.0))
                            .child(
                                h_flex()
                                    .p(px(2.0))
                                    .rounded(px(8.0))
                                    .bg(c("f_line"))
                                    .child(segment(t("active"), open_count, !self.resolved, false))
                                    .child(segment(
                                        t("resolved"),
                                        resolved_count,
                                        self.resolved,
                                        true,
                                    )),
                            )
                            .child(div().flex_1())
                            .child(
                                h_flex()
                                    .id("request-tag")
                                    .h(px(28.0))
                                    .px(px(9.0))
                                    .gap(px(7.0))
                                    .rounded(px(7.0))
                                    .border_1()
                                    .border_color(c("f_field"))
                                    .bg(c("f_card"))
                                    .text_size(px(12.0))
                                    .text_color(c("f_text"))
                                    .on_click(cx.listener(|this, event: &ClickEvent, _, cx| {
                                        this.menu_anchor = point(
                                            event.position().x - px(70.0),
                                            event.position().y + px(18.0),
                                        );
                                        this.menu = if this.menu == Some(Menu::RequestTag) {
                                            None
                                        } else {
                                            Some(Menu::RequestTag)
                                        };
                                        cx.notify();
                                    }))
                                    .child(
                                        self.tag
                                            .as_ref()
                                            .map(|id| tag_label(id))
                                            .unwrap_or_else(|| t("all_tags")),
                                    )
                                    .child(ui::icon("chevron", 13.0, c("f_muted"))),
                            ),
                    )
                    .when(shown.is_empty(), |d| {
                        d.child(
                            div()
                                .py(px(40.0))
                                .flex()
                                .justify_center()
                                .text_size(px(12.5))
                                .text_color(c("f_muted"))
                                .child(if self.resolved {
                                    t("nothing_resolved")
                                } else {
                                    t("no_open_requests")
                                }),
                        )
                    })
                    .child(glide::origin(&self.queue_glide, glide_scope))
                    .children(cards),
            )
    }

    fn thread(&self, p: &Project, window: &Window, cx: &mut Context<Self>) -> impl IntoElement {
        let Some(r) = self.thread.as_ref().filter(|t| Some(t.number) == self.open) else {
            return v_flex()
                .flex_1()
                .h_full()
                .bg(c("background"))
                .items_center()
                .justify_center()
                .child(ui::icon("chat", 16.0, c("f_muted").opacity(0.5)))
                .into_any_element();
        };
        let tag = p
            .tags
            .iter()
            .find(|t| t.id == r.tag)
            .map(|t| t.label.clone())
            .unwrap_or_else(|| r.tag.clone());
        let mut last_agent = String::new();
        let mut items = Vec::new();
        for (i, m) in r.messages.iter().enumerate() {
            let agent = !m.agent.is_empty();
            if agent && m.agent != last_agent {
                items.push(
                    div()
                        .my(px(10.0))
                        .flex()
                        .justify_center()
                        .text_size(px(11.5))
                        .text_color(c("f_muted"))
                        .child(sessions::agent_name(&m.agent).to_owned())
                        .into_any_element(),
                );
                last_agent = m.agent.clone();
            }
            let name = if agent {
                sessions::agent_name(&m.agent).to_owned()
            } else {
                m.author.clone()
            };
            let time = m.at.get(11..16).unwrap_or_default().to_owned();
            items.push(
                h_flex()
                    .items_start()
                    .gap(px(12.0))
                    .py(px(10.0))
                    .when(i == 0, |d| {
                        d.pb(px(16.0))
                            .mb(px(6.0))
                            .border_b_1()
                            .border_color(c("f_line"))
                    })
                    .child(if agent {
                        div()
                            .size(px(32.0))
                            .flex_none()
                            .rounded(px(9.0))
                            .bg(c("f_expanded"))
                            .border_1()
                            .border_color(c("f_line"))
                            .flex()
                            .items_center()
                            .justify_center()
                            .child(ui::agent_icon(&m.agent, 19.0))
                            .into_any_element()
                    } else {
                        div()
                            .size(px(32.0))
                            .flex_none()
                            .rounded(px(9.0))
                            .bg(c("orange"))
                            .text_color(white())
                            .text_size(px(14.0))
                            .font_weight(FontWeight::MEDIUM)
                            .flex()
                            .items_center()
                            .justify_center()
                            .child(
                                name.chars()
                                    .next()
                                    .map(|ch| ch.to_uppercase().to_string())
                                    .unwrap_or_default(),
                            )
                            .into_any_element()
                    })
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .gap(px(3.0))
                            .child(
                                h_flex()
                                    .gap(px(6.0))
                                    .child(
                                        div()
                                            .text_size(px(13.0))
                                            .font_weight(FontWeight::MEDIUM)
                                            .text_color(c("f_text"))
                                            .child(name),
                                    )
                                    .child(
                                        div()
                                            .text_size(px(11.0))
                                            .text_color(c("f_muted"))
                                            .child(time),
                                    ),
                            )
                            .child(
                                gpui_base::text::TextView::markdown(
                                    SharedString::from(format!("m-{}-{}", r.number, m.id)),
                                    m.body.clone(),
                                )
                                .style(ui::markdown_style())
                                .text_size(px(13.5))
                                .text_color(c("f_text")),
                            )
                            .when(!m.attachments.is_empty(), |d| {
                                d.child(self.stored_images(&m.attachments, m.id, cx))
                            }),
                    )
                    .into_any_element(),
            );
        }
        let reply_key = format!("reply:{}:{}", p.id, r.number);
        let reply_has = !self.replying
            && !self.request_images.busy.contains(&reply_key)
            && (!self.reply.read(cx).value().trim().is_empty()
                || self
                    .request_images
                    .by_queue
                    .get(&reply_key)
                    .is_some_and(|images| !images.is_empty()));
        let resolved = r.resolved();
        let session_route = r
            .active_session(&self.sessions)
            .map(|s| s.address.clone());
        let peek = self
            .peek_target()
            .map(|s| self.thread_peek_card(s, &r.status))
            .or_else(|| self.thread_wait_card(p, r));
        let can_resolve = resolved || matches!(r.status.as_str(), "you" | "review" | "failed");
        v_flex()
            .flex_1()
            .min_w_0()
            .h_full()
            .bg(c("background"))
            .child(
                h_flex()
                    .id("thread-head")
                    .h(px(58.0))
                    .flex_none()
                    .px(px(18.0))
                    .gap(px(8.0))
                    .border_b_1()
                    .border_color(c("f_line"))
                    .on_mouse_down(MouseButton::Left, ui::drag_titlebar)
                    .child(
                        div()
                            .flex_none()
                            .text_size(px(12.0))
                            .text_color(c("f_muted"))
                            .child(format!("{}-{}", p.key, r.number)),
                    )
                    .child(
                        div()
                            .min_w_0()
                            .text_size(px(13.5))
                            .font_weight(FontWeight::MEDIUM)
                            .text_color(c("f_text"))
                            .truncate()
                            .child(r.title.clone()),
                    )
                    .child(ui::tag_chip(&r.tag, &tag))
                    .child(ui::status_pill(&r.status))
                    .child(div().flex_1())
                    .when(session_route.is_none() && !r.runs.is_empty(), |d| {
                        d.child(
                            secondary_button("resume-thread-session", t("view_session"), false)
                                .on_click(cx.listener(|this, _, window, cx| {
                                    this.open_thread_session(window, cx)
                                })),
                        )
                    })
                    .when_some(session_route, |d, address| {
                        d.child(
                            secondary_button("request-session", t("view_session"), false).on_click(
                                cx.listener(move |this, _, window, cx| {
                                    this.go(Route::Session(address.clone()), window, cx)
                                }),
                            ),
                        )
                    })
                    .when(can_resolve, |d| {
                        d.child(
                            secondary_button(
                                "resolve",
                                if resolved { t("reopen") } else { t("resolve") },
                                !resolved,
                            )
                            .on_click(cx.listener(
                                move |this, _, window, cx| {
                                    this.set_status(
                                        if resolved { "you" } else { "resolved" },
                                        window,
                                        cx,
                                    )
                                },
                            )),
                        )
                    }),
            )
            .child(
                v_flex()
                    .id("thread")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .track_scroll(&self.thread_scroll)
                    .px(px(22.0))
                    .py(px(14.0))
                    .children(items)
                    .when_some(peek, |d, card| d.child(card)),
            )
            .child(
                div().flex_none().px(px(18.0)).pb(px(16.0)).child(
                    v_flex()
                        .relative()
                        .on_mouse_down(
                            MouseButton::Left,
                            cx.listener(|this, _, window, cx| {
                                this.reply.read(cx).focus_handle(cx).focus(window, cx)
                            }),
                        )
                        .rounded(px(10.0))
                        .bg(c("f_card"))
                        .border_1()
                        .border_color(if self.reply.read(cx).focus_handle(cx).is_focused(window) {
                            c("f_text").opacity(0.22)
                        } else {
                            c("f_field")
                        })
                        .px(px(13.0))
                        .py(px(9.0))
                        .min_h(px(42.0))
                        .text_size(px(13.5))
                        .line_height(relative(1.6))
                        .pb(px(38.0))
                        // No min height here: the textarea's root is h_full and clips, so a
                        // definite min height on this wrapper would cap it at one line.
                        .child(
                            div()
                                .w_full()
                                .flex_none()
                                .debug_selector(|| "thread-reply-text".into())
                                .child(Textarea::new(&self.reply)),
                        )
                        .when_some(self.draft_images(&reply_key, cx), |d, images| {
                            d.child(images)
                        })
                        .when_some(self.request_image_status(&reply_key), |d, status| {
                            d.child(
                                div()
                                    .text_size(px(11.0))
                                    .text_color(c("f_muted"))
                                    .child(t(status)),
                            )
                        })
                        .child(
                            h_flex()
                                .absolute()
                                .right(px(8.0))
                                .bottom(px(8.0))
                                .gap(px(8.0))
                                .when(reply_has, |d| {
                                    d.child(primary_button("send", t("send")).on_click(
                                        cx.listener(|this, _, window, cx| {
                                            this.send_reply(window, cx)
                                        }),
                                    ))
                                }),
                        ),
                ),
            )
            .into_any_element()
    }

    // ---------- session ----------

    fn session(
        &mut self,
        address: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> impl IntoElement {
        let s = self.sessions.iter().find(|s| s.address == address).cloned();
        let shown = self.shown_tab(address);
        let view = self.terminal(&shown, window, cx);
        // The terminal schedules output paints only while marked in this frame.
        if let Some(view) = &view {
            view.read(cx).mark_shown();
        }
        let cloud_failed = self.cloud_attach_failed.contains(&shown);
        let connecting_cloud = shown.starts_with("cloud:") && view.is_none() && !cloud_failed;
        if self.pending_focus.as_deref() == Some(&shown) {
            if let Some(view) = &view {
                view.read(cx).focus_target(cx).focus(window, cx);
                self.pending_focus = None;
            }
        }
        let request = s.as_ref().and_then(|s| self.session_request(s));
        // The request's status beside its button, as on its card and thread (THI-85).
        let request_status = s
            .as_ref()
            .and_then(|s| self.loaded_session_request(s))
            .map(|(_, r)| r.status.clone());
        v_flex()
            .flex_1()
            .min_w_0()
            .h_full()
            .bg(c("background"))
            .child(
                h_flex()
                    .id("tabs")
                    // Taller while the sidebar is hidden, to hold the traffic lights.
                    .h(px(40.0 + 6.0 * (1.0 - self.sidebar_shown)))
                    .flex_none()
                    .px(px(10.0))
                    .pl(self.title_inset(10.0, 8.0))
                    .gap(px(8.0))
                    .bg(c("f_bg"))
                    .border_b_1()
                    .border_color(c("f_line"))
                    .on_mouse_down(MouseButton::Left, ui::drag_titlebar)
                    .child(self.tab_strip(address, cx))
                    .child(div().flex_1())
                    .when_some(request_status, |d, status| {
                        d.child(
                            ui::status_pill(&status)
                                .debug_selector(|| "session-request-status".into()),
                        )
                    })
                    .when_some(request, |d, (pid, n, title)| {
                        d.child(secondary_button("goto-request", title, false).on_click(
                            cx.listener(move |this, _, window, cx| {
                                this.go_request(pid.clone(), n, window, cx)
                            }),
                        ))
                    }),
            )
            .child(
                div()
                    .flex_1()
                    .min_h_0()
                    .when(connecting_cloud, |d| {
                        d.flex().items_center().justify_center().child(
                            svg()
                                .path("programs/activity-spinner.svg")
                                .size(px(18.0))
                                .text_color(c("muted"))
                                .with_animation(
                                    "cloud-connecting",
                                    Animation::new(Duration::from_millis(900)).repeat(),
                                    |icon, delta| {
                                        icon.with_transformation(Transformation::rotate(
                                            percentage(delta),
                                        ))
                                    },
                                ),
                        )
                    })
                    .when(cloud_failed, |d| {
                        let key = shown.clone();
                        d.flex().items_center().justify_center().child(
                            secondary_button("retry-cloud-attach", t("try_again"), false).on_click(
                                cx.listener(move |this, _, _, cx| {
                                    this.cloud_attach_failed.remove(&key);
                                    cx.notify();
                                }),
                            ),
                        )
                    })
                    .opacity(self.layout.content_opacity.max(0.85))
                    .when_some(view, |d, v| d.child(v)),
            )
    }
}

pub(crate) fn primary_button(id: &'static str, label: String) -> Stateful<Div> {
    div()
        .id(id)
        .h(px(25.0))
        .px(px(10.0))
        .rounded(px(7.0))
        .bg(c("f_text"))
        .text_color(c("f_card"))
        .text_size(px(12.0))
        .font_weight(FontWeight::MEDIUM)
        .flex()
        .items_center()
        .child(label)
}

pub(crate) fn secondary_button(
    id: impl Into<ElementId>,
    label: impl Into<SharedString>,
    check: bool,
) -> Stateful<Div> {
    h_flex()
        .id(id)
        .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
        .h(px(25.0))
        .px(px(9.0))
        .gap(px(5.0))
        .rounded(px(7.0))
        .bg(c("f_card"))
        .border_1()
        .border_color(c("f_field"))
        .text_size(px(12.0))
        .font_weight(FontWeight::MEDIUM)
        .text_color(c("f_text"))
        .hover(|s| s.bg(c("f_expanded")))
        .when(check, |d| d.child(ui::icon("check", 13.0, c("f_text"))))
        .child(label.into())
}

impl FennecWindow {
    /// gpui-kit inputs fall back to its own theme, not the app palette, so the
    /// caret and text would stay dark on a dark card. Re-applied each frame to
    /// follow theme switches.
    fn sync_input_styles(&mut self, cx: &mut Context<Self>) {
        let style = InputEditorStyle {
            foreground: c("f_text"),
            muted_foreground: c("f_muted"),
            selection: c("selection"),
            caret: c("f_text"),
            ..InputEditorStyle::default()
        };
        self.search
            .update(cx, |s, _| s.set_editor_style(style.clone()));
        self.composer
            .update(cx, |s, _| s.set_editor_style(style.clone()));
        self.reply
            .update(cx, |s, _| s.set_editor_style(style.clone()));
        self.settings.sync_input_styles(&style, cx);
        if let Some(project) = &self.new_project {
            for input in [&project.name, &project.description, &project.repository] {
                input.update(cx, |s, _| s.set_editor_style(style.clone()));
            }
        }
    }
}

impl Render for FennecWindow {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        crate::terminal::next_frame();
        window.set_rem_size(px(crate::settings::ui_font_size()));
        self.sync_input_styles(cx);
        self.step_sidebar(window, cx);
        let sidebar_width=self.sidebar_width(window);
        self.step_tabs(window, cx);
        let route = self.route.clone();
        if !matches!(route, Some(Route::Project(_))) {
            // Hidden meanwhile: the queue's cards are placed at once when it shows again.
            self.queue_glide.borrow_mut().reset();
        }
        let main = match &route {
            Some(Route::AppSettings) => self.desktop_preferences_view(cx).into_any_element(),
            Some(Route::Project(id)) => match self.projects.iter().find(|p| &p.id == id).cloned() {
                Some(p) => h_flex()
                    .flex_1()
                    .min_w_0()
                    .h_full()
                    .child(self.project(&p, window, cx))
                    .child(self.thread(&p, window, cx))
                    .into_any_element(),
                None => empty(),
            },
            Some(Route::Session(a)) => {
                let a = a.clone();
                self.session(&a, window, cx).into_any_element()
            }
            Some(Route::Settings(id, tab)) => {
                match self.projects.iter().find(|p| &p.id == id).cloned() {
                    Some(p) => self
                        .project_settings(&p, *tab, window, cx)
                        .into_any_element(),
                    None => empty(),
                }
            }
            None => empty(),
        };
        let menu = self.menu(cx);
        let context_menu = self.context_menu_view(window, cx);
        let dialog = self.new_project_dialog(cx);
        let image_viewer = self.image_viewer_view(cx);
        h_flex()
            .relative()
            .track_focus(&self.focus)
            .key_context("FennecWindow")
            .size_full()
            .font_family(crate::settings::get().ui_font_family.clone())
            .text_size(px(13.0))
            .font_weight(FontWeight::NORMAL)
            .line_height(relative(1.45))
            .bg(c("f_card"))
            .text_color(c("f_text"))
            .on_action(cx.listener(Self::on_shell_action))
            .capture_action(cx.listener(|this,_:&gpui_base::input::Paste,window,cx|{
                if this.paste_request_image(window,cx){cx.stop_propagation();}
            }))
            .capture_action(cx.listener(|this,action:&ShellAction,window,cx|{
                if matches!(action.id(),"terminal.paste"|"terminal.paste_image")&&this.paste_request_image(window,cx){cx.stop_propagation();}
            }))
            .on_drag_move::<sidebar_resize::SidebarDrag>(cx.listener(Self::drag_sidebar))
            .on_drop(cx.listener(|this,_:&sidebar_resize::SidebarDrag,window,cx|this.finish_sidebar_resize(window,cx)))
            .capture_any_mouse_up(cx.listener(|this,event:&MouseUpEvent,window,cx| {
                if event.button==MouseButton::Left {this.finish_sidebar_resize(window,cx);}
            }))
            .on_mouse_up_out(MouseButton::Left,cx.listener(|this,_,window,cx|this.finish_sidebar_resize(window,cx)))
            // ⌘K (Ctrl-K elsewhere) goes to search; Esc there clears it.
            .on_key_down(cx.listener(|this, e: &KeyDownEvent, window, cx| {
                if this.image_viewer_key(&e.keystroke.key, cx) {
                    return;
                }
                if this.context_key(&e.keystroke.key, window, cx) {
                    return;
                }
                let k = &e.keystroke;
                if k.key == "escape" && cx.has_active_drag() {
                    if let Some((_,width))=this.sidebar_resize.drag.take() {
                        this.app_preferences.value.sidebar_width=width;
                        this.restore_sidebar_focus(window,cx);
                    }
                    cx.stop_active_drag(window);
                    this.layout.hover = None;
                    cx.stop_propagation();
                    cx.notify();
                    return;
                }
                if this.prefix {
                    this.prefix = false;
                    match k.key.as_str() {
                        "s" => {
                            this.sidebar = true;
                            this.search.read(cx).focus_handle(cx).focus(window, cx);
                        }
                        "." => {
                            this.open_app_settings(window, cx);
                        }
                        "b" => {
                            this.sidebar = !this.sidebar;
                        }
                        _ => {}
                    }
                    if k.key != "s" {
                        if let Some(Route::Session(id)) = &this.route {
                            this.pending_focus = Some(this.shown_tab(id));
                        }
                    }
                    cx.stop_propagation();
                    cx.notify();
                    return;
                }
                if this.menu.is_some() && k.key == "escape" {
                    this.menu = None;
                    cx.notify();
                    return;
                }
                if this.menu == Some(Menu::NewSession) {
                    if k.key == "escape" {
                        this.menu = None;
                        cx.notify();
                        return;
                    }
                    if let Ok(n) = k.key.parse::<usize>() {
                        if let Some(agent) = [
                            "claude", "codex", "pi", "opencode", "cursor", "grok", "kimi",
                        ]
                        .get(n.wrapping_sub(1))
                        {
                            this.start_session((*agent).into(), window, cx);
                            cx.stop_propagation();
                            return;
                        }
                    }
                }
                if k.key == "k"
                    && (k.modifiers.platform
                        || (cfg!(not(target_os = "macos")) && k.modifiers.control))
                {
                    this.sidebar = true;
                    this.search.read(cx).focus_handle(cx).focus(window, cx);
                    cx.stop_propagation();
                }
            }))
            // The sidebar slides in from the left edge: its column widens while
            // the sidebar itself, still full width, moves into view.
            .when(self.sidebar_shown > 0.0, |d| {
                let shown = self.sidebar_shown;
                d.child(
                    div()
                        .relative()
                        .debug_selector(||"sidebar-column".into())
                        .w(px(sidebar_width * shown))
                        .h_full()
                        .flex_none()
                        .overflow_hidden()
                        .child(
                            div()
                                .h_full()
                                .ml(px(-sidebar_width * (1.0 - shown)))
                                .child(self.sidebar(window, cx)),
                        )
                        .when(shown >= 1.0,|d|d.child(self.sidebar_resize_handle(cx))
                        ),
                )
            })
            .child(
                div()
                    .relative()
                    .flex_1()
                    .min_w_0()
                    .h_full()
                    .flex()
                    .flex_col()
                    .when(self.sidebar_shown < 1.0 && route.is_none(), |d| {
                        d.child(
                            h_flex()
                                .id("collapsed-titlebar")
                                .h(px(46.0 * (1.0 - self.sidebar_shown)))
                                .overflow_hidden()
                                .flex_none()
                                .pr(px(10.0))
                                .bg(c("f_bg"))
                                .border_b_1()
                                .border_color(c("f_line"))
                                .on_mouse_down(MouseButton::Left, ui::drag_titlebar),
                        )
                    })
                    .child(div().flex_1().min_h_0().flex().child(main))
                    .when_some(self.notice.clone(), |d, text| {
                        d.child(
                            div()
                                .absolute()
                                .bottom(px(2.0))
                                .left(px(18.0))
                                .text_size(px(11.0))
                                .text_color(c("f_muted"))
                                .child(text),
                        )
                    }),
            )
            .child(self.sidebar_toggle(cx))
            .child(self.notification_stack(cx))
            .when_some(menu, |d, m| d.child(m))
            .when_some(context_menu, |d, m| d.child(m))
            .when_some(dialog, |d, m| d.child(m))
            .when_some(image_viewer, |d, m| d.child(m))
            .when(self.prefix, |d| {
                d.child(
                    div()
                        .absolute()
                        .top(px(50.0))
                        .left(px(270.0))
                        .p(px(16.0))
                        .rounded(px(9.0))
                        .bg(c("f_card"))
                        .border_1()
                        .border_color(c("f_line"))
                        .text_size(px(13.0))
                        .child(t("keyboard_prefix")),
                )
            })
    }
}

/// Where the sidebar toggle starts: just right of the traffic lights.
const SIDEBAR_TOGGLE_LEFT: f32 = 84.0;
const SIDEBAR_TOGGLE_SIZE: f32 = 26.0;
const SIDEBAR_SLIDE_SECONDS: f32 = 0.22;
/// A sidebar row's height and the gap below it.
const ROW_HEIGHT: f32 = 30.0;
const ROW_GAP: f32 = 1.0;
/// A state header (Working 2, Idle 1, ...) above its session rows.
const GROUP_HEADER_HEIGHT: f32 = 32.0;

/// A session row's key: its element id, and its identity while it moves.
fn row_key(r: &SidebarRow) -> String {
    match &r.tab {
        Some(tab) => format!("s-{}-{tab}", r.session.address),
        None => format!("s-{}", r.session.address),
    }
}

/// A state header's message ID and color.
fn state_label(state: State) -> (&'static str, &'static str) {
    match state {
        State::NeedsYou => ("needs_you", "blocked"),
        State::Working => ("working", "working"),
        State::Done => ("done", "done"),
        _ => ("idle", "f_muted"),
    }
}

fn empty() -> AnyElement {
    div()
        .flex_1()
        .h_full()
        .bg(c("background"))
        .into_any_element()
}

/// The Mac shows its replica of the board. While the cloud is unreachable that
/// replica is frozen, so "Running" may be hours out of date (THI-55): say so.
fn stale_board_banner(synced_at: &str, now: u64) -> impl IntoElement {
    let since = sessions::rfc3339_ms(synced_at);
    let text = if since == 0 || now.saturating_sub(since) < 60_000 {
        t("board_stale")
    } else {
        tf(
            "board_stale_since",
            &[("age", &sessions::age(since, now))],
        )
    };
    h_flex()
        .w_full()
        .mb(px(10.0))
        .px(px(12.0))
        .py(px(8.0))
        .gap(px(8.0))
        .rounded(px(8.0))
        .border_1()
        .border_color(c("f_field"))
        .text_size(px(12.0))
        .text_color(c("f_muted"))
        .child(div().flex_1().min_w_0().child(text))
}
