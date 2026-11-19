//! The window (docs/desktop-app.md): Projects and Sessions on the left; the
//! open project (requests beside a thread) or session (herdr-app's terminal)
//! on the right.

use crate::board::{self, Project, Request};
use crate::hc::{Core, EventLines, Stream};
use crate::i18n::t;
use crate::sessions::{self, Session, State};
use crate::terminal::{TerminalEvent, TerminalView};
use crate::ui::{self, c};
use gpui::prelude::FluentBuilder as _;
use gpui::*;
use gpui_base::input::{Input, InputEvent, InputState, TextareaState};
use gpui_base::{Textarea, h_flex, v_flex};
use gpui_shell::action::ShellAction;
use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

#[derive(Clone, PartialEq)]
pub(crate) enum Route {
    Project(String),
    Session(String),
    /// A project's settings, on one of its tabs.
    Settings(String, crate::project_settings::Tab),
}

#[derive(Clone, Copy, PartialEq)]
enum Menu {
    Org,
    NewSession,
}

/// The New project dialog's fields.
struct NewProject {
    name: Entity<InputState>,
    description: Entity<InputState>,
    repository: Entity<InputState>,
}

pub struct FennecWindow {
    core: Option<Arc<Core>>,
    core_error: Option<String>,
    events: Option<EventLines>,
    sessions: Vec<Session>,
    pub(crate) projects: Vec<Project>,
    pub(crate) requests: HashMap<String, Vec<Request>>,
    thread: Option<Request>,
    route: Option<Route>,
    open: Option<u64>,
    resolved: bool,
    tag: Option<String>,
    terms: HashMap<String, Entity<TerminalView>>,
    search: Entity<InputState>,
    composer: Entity<TextareaState>,
    reply: Entity<TextareaState>,
    notice: Option<String>,
    sidebar: bool,
    /// The signed-in person, as requests and replies are signed.
    pub(crate) me: String,
    orgs: Vec<board::Organization>,
    org: Option<String>,
    menu: Option<Menu>,
    new_project: Option<NewProject>,
    /// A session asked for and not yet listed: shown once it appears.
    starting: Option<String>,
    pub(crate) settings: crate::project_settings::State,
    /// This Mac's own core (Fennec's instance), for local shell tabs.
    local: Option<Arc<Core>>,
    /// Local shells by the session they sit beside (`fennec.shell-of`), in order.
    shells: HashMap<String, Vec<String>>,
    /// The tab shown in each session: a local shell's `local:<address>`, else the agent's.
    tab: HashMap<String, String>,
    pub(crate) _subs: Vec<Subscription>,
    pub(crate) _tasks: Vec<Task<()>>,
}

impl FennecWindow {
    pub fn new(
        core: anyhow::Result<Arc<Core>>,
        requests: async_channel::Receiver<crate::mods::Request>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Self {
        let search = cx.new(|cx| {
            InputState::new(window, cx)
                .placeholder(t("search"))
                .clean_on_escape()
        });
        let composer = cx.new(|cx| {
            TextareaState::new(window, cx)
                .auto_grow(1, 12)
                .placeholder(t("new_request"))
        });
        let reply = cx.new(|cx| {
            TextareaState::new(window, cx)
                .auto_grow(1, 8)
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
                    InputEvent::PressEnter {
                        secondary: true, ..
                    } => this.post(window, cx),
                    InputEvent::Change => cx.notify(),
                    _ => {}
                },
            ),
            cx.subscribe_in(
                &reply,
                window,
                |this, _, ev: &InputEvent, window, cx| match ev {
                    InputEvent::PressEnter {
                        secondary: true, ..
                    } => this.send_reply(window, cx),
                    InputEvent::Change => cx.notify(),
                    _ => {}
                },
            ),
        ];
        subs.push(cx.observe_window_appearance(window, |this, window, cx| {
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
        let (core, core_error) = match core {
            Ok(core) => (Some(core), None),
            Err(e) => (None, Some(format!("{e:#}"))),
        };
        let mut this = FennecWindow {
            core,
            core_error,
            events: None,
            sessions: Vec::new(),
            projects: Vec::new(),
            requests: HashMap::new(),
            thread: None,
            route: None,
            open: None,
            resolved: false,
            tag: None,
            terms: HashMap::new(),
            search,
            composer,
            reply,
            notice: None,
            sidebar: true,
            me: String::from("me"),
            orgs: Vec::new(),
            org: None,
            menu: None,
            new_project: None,
            starting: None,
            settings: Default::default(),
            local: None,
            shells: HashMap::new(),
            tab: HashMap::new(),
            _subs: subs,
            _tasks: Vec::new(),
        };
        this._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let (me, orgs) = cx
                .background_spawn(async move {
                    (board::person(), board::organizations().unwrap_or_default())
                })
                .await;
            let _ = this.update(cx, |this, cx| {
                this.me = me;
                this.org = orgs
                    .iter()
                    .find(|o| o.current)
                    .or(orgs.first())
                    .map(|o| o.id.clone());
                this.orgs = orgs;
                cx.notify();
            });
        }));
        this.follow_core(window, cx);
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
        this.follow_board(window, cx);
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
        self.notice = Some(text);
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
        self.terminals_changed(cx);
        let core = core.clone();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            while let Ok(item) = stream.recv().await {
                let gone = matches!(item, Stream::Closed(_) | Stream::Lost);
                let ok = this.update(cx, |this, cx| {
                    if let (Stream::Data(bytes), Some(events)) = (&item, this.events.as_mut()) {
                        core.credit(channel, bytes.len());
                        if events.push(bytes).terminals {
                            this.terminals_changed(cx);
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

    fn terminals_changed(&mut self, cx: &mut Context<Self>) {
        let Some(events) = self.events.as_ref() else {
            return;
        };
        let live = events.live();
        crate::mods::publish_terminals(live["terminals"].clone());
        self.sessions = sessions::from_terminals(
            live["terminals"]
                .as_array()
                .map(Vec::as_slice)
                .unwrap_or_default(),
        );
        if let Some(Route::Session(a)) = &self.route
            && !self.sessions.iter().any(|s| &s.address == a)
        {
            self.route = None;
        }
        // A session opens only when it is picked: attaching resizes its terminal.
        if let Some(name) = self.starting.clone()
            && let Some(s) = self.sessions.iter().find(|s| s.name == name)
        {
            self.starting = None;
            self.route = Some(Route::Session(s.address.clone()));
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
            Ok(p) => self.projects = p,
            Err(e) if self.projects.is_empty() => self.notice = Some(format!("{e:#}")),
            Err(_) => {}
        }
        if let Some((id, Ok(list))) = requests {
            self.requests.insert(id, list);
        }
        if let Some(Ok(t)) = thread
            && Some(t.number) == self.open
        {
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

    /// The current organization's projects (all of them before organizations load).
    pub(crate) fn visible_projects(&self) -> Vec<&Project> {
        self.projects
            .iter()
            .filter(|p| {
                self.org
                    .as_ref()
                    .is_none_or(|o| p.org.is_empty() || &p.org == o)
            })
            .collect()
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

    pub(crate) fn go(&mut self, route: Route, window: &mut Window, cx: &mut Context<Self>) {
        self.menu = None;
        if self.route.as_ref() != Some(&route) {
            // Only the session on screen streams: every attached terminal shares one
            // paced connection to the cloud computer, so a busy hidden agent would
            // delay the echo of what is typed here. Coming back attaches again
            // (the core repaints the screen and its history).
            if let Some(Route::Session(old)) = &self.route {
                if let Some(view) = self.terms.remove(old) {
                    view.read(cx).detach();
                }
            }
            if let Route::Settings(id, _) = &route {
                self.open_settings(id.clone(), window, cx);
            }
            if let Route::Project(_) = route {
                self.open = None;
                self.thread = None;
                self.tag = None;
            }
            self.route = Some(route);
            self.refresh_now(window, cx);
        }
        cx.notify();
    }

    pub(crate) fn open_request(&mut self, n: u64, window: &mut Window, cx: &mut Context<Self>) {
        self.open = Some(n);
        self.thread = None;
        self.refresh_now(window, cx);
        cx.notify();
    }

    fn post(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let text = self.composer.read(cx).value().to_string();
        let Some(project) = self.project_id() else {
            return;
        };
        if text.trim().is_empty() {
            return;
        }
        self.composer
            .update(cx, |s, cx| s.set_value("", window, cx));
        let me = self.me.clone();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let posted = cx
                .background_spawn(async move { board::post(&project, &text, &me) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| match posted {
                Ok(r) => {
                    this.resolved = false;
                    this.open_request(r.number, window, cx);
                }
                Err(e) => this.say(format!("{e:#}"), cx),
            });
        }));
    }

    fn send_reply(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let text = self.reply.read(cx).value().to_string();
        let (Some(project), Some(n)) = (self.project_id(), self.open) else {
            return;
        };
        if text.trim().is_empty() {
            return;
        }
        self.reply.update(cx, |s, cx| s.set_value("", window, cx));
        let me = self.me.clone();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let sent = cx
                .background_spawn(async move { board::reply(&project, n, &text, &me) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| match sent {
                Ok(_) => this.refresh_now(window, cx),
                Err(e) => this.say(format!("{e:#}"), cx),
            });
        }));
    }

    fn set_status(&mut self, status: &'static str, window: &mut Window, cx: &mut Context<Self>) {
        let (Some(project), Some(n)) = (self.project_id(), self.open) else {
            return;
        };
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let done = cx
                .background_spawn(async move { board::set_status(&project, n, status) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| match done {
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
        let (core, address) = match key.strip_prefix("local:") {
            Some(a) => (self.local.clone()?, a.to_owned()),
            None => (self.core.clone()?, key.to_owned()),
        };
        let addr = key.to_owned();
        let view = cx.new(|cx| TerminalView::attach(core, address, (120, 40), cx));
        let sub = cx.subscribe_in(
            &view,
            window,
            move |this, _, ev: &TerminalEvent, _, cx| match ev {
                TerminalEvent::Link(click) => {
                    if let Some(target @ crate::links::Target::Url(_)) = &click.native {
                        crate::links::open(target);
                    }
                }
                TerminalEvent::Notice { title, body } => this.say(format!("{title}: {body}"), cx),
                TerminalEvent::Ended => {
                    this.terms.remove(&addr);
                    if let Some(a) = addr.strip_prefix("local:") {
                        this.forget_shell(a);
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
                Err(e) => this.say(format!("{e:#}"), cx),
            });
        }));
    }

    fn new_shell(&mut self, session: Session, window: &mut Window, cx: &mut Context<Self>) {
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
            let name = session.name.clone();
            this._tasks.push(cx.spawn_in(window, async move |this, cx| {
                let started = cx
                    .background_spawn(async move {
                        let shell = std::env::var("SHELL").unwrap_or_else(|_| "/bin/zsh".into());
                        let home = std::env::var("HOME").ok();
                        core.run(
                            vec![shell, "-l".into()],
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
                let _ = this.update(cx, |this, cx| match started {
                    Ok(address) => {
                        this.shells
                            .entry(session.name.clone())
                            .or_default()
                            .push(address.clone());
                        this.tab
                            .insert(session.address.clone(), format!("local:{address}"));
                        cx.notify();
                    }
                    Err(e) => this.say(format!("{e:#}"), cx),
                });
            }));
        });
    }

    fn close_shell(&mut self, address: String, cx: &mut Context<Self>) {
        if let Some(core) = self.local.clone() {
            let a = address.clone();
            cx.background_spawn(async move {
                let _ = core.call("terminal.kill", serde_json::json!({"terminal": a}));
            })
            .detach();
        }
        self.forget_shell(&address);
        cx.notify();
    }

    fn forget_shell(&mut self, address: &str) {
        let key = format!("local:{address}");
        self.terms.remove(&key);
        for list in self.shells.values_mut() {
            list.retain(|a| a != address);
        }
        self.tab.retain(|_, t| t != &key);
    }

    /// Key bindings the terminal core sends up (keys.rs).
    fn on_shell_action(&mut self, a: &ShellAction, window: &mut Window, cx: &mut Context<Self>) {
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

    fn query(&self, cx: &App) -> String {
        self.search.read(cx).value().trim().to_lowercase()
    }

    // ---------- organization, new project, new session ----------

    fn org_switcher(&self, cx: &mut Context<Self>) -> AnyElement {
        let current = self
            .org
            .as_ref()
            .and_then(|id| self.orgs.iter().find(|o| &o.id == id));
        let Some(org) = current else {
            return h_flex()
                .gap(px(7.0))
                .child(img("fennec.svg").size(px(18.0)).flex_none())
                .child(
                    div()
                        .text_size(px(13.0))
                        .font_weight(FontWeight::SEMIBOLD)
                        .text_color(c("f_text"))
                        .child("Fennec"),
                )
                .into_any_element();
        };
        h_flex()
            .id("org")
            .h(px(26.0))
            .px(px(6.0))
            .gap(px(6.0))
            .rounded(px(6.0))
            .min_w_0()
            .when(self.menu == Some(Menu::Org), |d| d.bg(c("f_line")))
            .hover(|s| s.bg(c("f_line")))
            .on_click(cx.listener(|this, _, _, cx| {
                this.menu = if this.menu == Some(Menu::Org) {
                    None
                } else {
                    Some(Menu::Org)
                };
                cx.notify();
            }))
            .child(ui::project_mark(&org.name, "#f38d25", 18.0))
            .child(
                div()
                    .min_w_0()
                    .truncate()
                    .text_size(px(13.0))
                    .font_weight(FontWeight::SEMIBOLD)
                    .text_color(c("f_text"))
                    .child(org.name.clone()),
            )
            .child(ui::icon("chevron", 13.0, c("f_muted")))
            .into_any_element()
    }

    /// The open dropdown (organizations, or agents for a new session) under its button.
    fn menu(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let item = |id: SharedString, checked: bool| {
            h_flex()
                .id(id)
                .h(px(29.0))
                .px(px(9.0))
                .gap(px(8.0))
                .rounded(px(5.0))
                .text_size(px(12.5))
                .font_weight(FontWeight::MEDIUM)
                .text_color(c("f_text"))
                .hover(|s| s.bg(c("f_expanded")))
                .when(checked, |d| d.font_weight(FontWeight::SEMIBOLD))
        };
        let (top, items): (f32, Vec<AnyElement>) = match self.menu? {
            Menu::Org => {
                let mut list: Vec<AnyElement> = self
                    .orgs
                    .iter()
                    .map(|o| {
                        let id = o.id.clone();
                        let on = self.org.as_ref() == Some(&o.id);
                        item(SharedString::from(format!("org-{}", o.id)), on)
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.org = Some(id.clone());
                                this.menu = None;
                                this.route = this
                                    .visible_projects()
                                    .first()
                                    .map(|p| Route::Project(p.id.clone()));
                                this.open = None;
                                this.thread = None;
                                this.refresh_now(window, cx);
                            }))
                            .child(div().flex_1().child(o.name.clone()))
                            .when(on, |d| d.child(ui::icon("check", 13.0, c("orange"))))
                            .into_any_element()
                    })
                    .collect();
                list.push(
                    div()
                        .h(px(1.0))
                        .my(px(4.0))
                        .bg(c("f_line"))
                        .into_any_element(),
                );
                list.push(
                    item("org-new".into(), false)
                        .on_click(cx.listener(|this, _, _, cx| {
                            this.menu = None;
                            crate::links::open(&crate::links::Target::Url(
                                "https://dashboard.runta.com".into(),
                            ));
                            cx.notify();
                        }))
                        .child(t("new_organization"))
                        .into_any_element(),
                );
                (40.0, list)
            }
            Menu::NewSession => {
                let list = board::AGENTS
                    .iter()
                    .map(|agent| {
                        let a = agent.to_string();
                        item(SharedString::from(format!("agent-{agent}")), false)
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.start_session(a.clone(), window, cx)
                            }))
                            .child(ui::agent_icon(agent, 15.0))
                            .child(crate::sessions::agent_name(agent).to_owned())
                            .into_any_element()
                    })
                    .collect();
                let projects = self.visible_projects().len() as f32;
                (150.0 + projects * 30.0, list)
            }
        };
        Some(
            div()
                .absolute()
                .top(px(top))
                .left(px(if self.menu == Some(Menu::Org) {
                    80.0
                } else {
                    120.0
                }))
                .w(px(220.0))
                .p(px(5.0))
                .rounded(px(9.0))
                .bg(c("f_card"))
                .border_1()
                .border_color(c("f_field"))
                .shadow_lg()
                .occlude()
                .children(items)
                .into_any_element(),
        )
    }

    fn start_session(&mut self, agent: String, window: &mut Window, cx: &mut Context<Self>) {
        self.menu = None;
        self.starting = Some(String::new());
        cx.notify();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let started = cx
                .background_spawn(async move { board::start_session(&agent) })
                .await;
            let _ = this.update(cx, |this, cx| match started {
                Ok(name) => {
                    this.starting = Some(name);
                    this.terminals_changed(cx);
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
        let org = self.org.clone().unwrap_or_default();
        let color = board::COLORS[self.projects.len() % board::COLORS.len()];
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let created = cx
                .background_spawn(async move {
                    board::create_project(&name, &org, &description, &repository, color)
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
                                .font_weight(FontWeight::SEMIBOLD)
                                .child(t("new_project")),
                        )
                        .child(field(t("name"), &np.name))
                        .child(field(t("description"), &np.description))
                        .child(field(t("repository"), &np.repository))
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

    fn sidebar(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let q = self.query(cx);
        let route = self.route.clone();
        let header = |label: String, plus: Stateful<Div>| {
            h_flex()
                .h(px(28.0))
                .mt(px(10.0))
                .px(px(8.0))
                .justify_between()
                .text_size(px(11.5))
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(c("f_muted"))
                .child(label)
                .child(plus)
        };
        let plus = |id: &'static str, open: bool| {
            div()
                .id(id)
                .size(px(20.0))
                .rounded(px(6.0))
                .flex()
                .items_center()
                .justify_center()
                .when(open, |d| d.bg(c("f_line")))
                .hover(|s| s.bg(c("f_line")))
                .child(ui::icon("plus", 13.0, c("f_muted")))
        };
        let row = |id: SharedString, selected: bool| {
            h_flex()
                .id(id)
                .h(px(30.0))
                .px(px(8.0))
                .gap(px(9.0))
                .rounded(px(7.0))
                .text_size(px(13.0))
                .font_weight(FontWeight::MEDIUM)
                .text_color(c("f_text"))
                .when(selected, |d| {
                    d.bg(c("f_card"))
                        .border_1()
                        .border_color(c("f_line"))
                        .shadow(vec![BoxShadow {
                            color: hsla(0.0, 0.0, 0.0, 0.07),
                            offset: point(px(0.0), px(1.0)),
                            blur_radius: px(2.0),
                            spread_radius: px(0.0),
                            inset: false,
                        }])
                })
                .when(!selected, |d| {
                    d.border_1()
                        .border_color(transparent_black())
                        .hover(|s| s.bg(c("f_line")))
                })
        };
        let projects = self.visible_projects().into_iter().filter(|p| q.is_empty() || p.name.to_lowercase().contains(&q)).map(|p| {
            let selected = matches!(&route, Some(Route::Project(id) | Route::Settings(id, _)) if id == &p.id);
            let id = p.id.clone();
            row(SharedString::from(format!("p-{}", p.id)), selected)
                .on_click(cx.listener(move |this, _, window, cx| this.go(Route::Project(id.clone()), window, cx)))
                .child(ui::project_mark(&p.name, &p.color, 18.0))
                .child(div().flex_1().min_w_0().truncate().child(p.name.clone()))
                .when(p.needs_you > 0, |d| {
                    d.child(div().min_w(px(18.0)).h(px(17.0)).px(px(5.0)).rounded(px(9.0)).bg(c("tint")).border_1().border_color(c("orange").opacity(0.3)).text_color(c("accent_text")).text_size(px(10.5)).font_weight(FontWeight::SEMIBOLD).flex().items_center().justify_center().child(p.needs_you.to_string()))
                })
        })
            .collect::<Vec<_>>();
        let org_switcher = self.org_switcher(cx);
        let now = sessions::now_ms();
        let mut groups = Vec::new();
        for state in [State::NeedsYou, State::Working, State::Idle] {
            let list: Vec<&Session> = self
                .sessions
                .iter()
                .filter(|s| {
                    s.state == state
                        && (q.is_empty()
                            || s.title.to_lowercase().contains(&q)
                            || s.name.contains(&q))
                })
                .collect();
            if list.is_empty() {
                continue;
            }
            groups.push(
                h_flex()
                    .px(px(8.0))
                    .pt(px(7.0))
                    .pb(px(3.0))
                    .gap(px(4.0))
                    .text_size(px(11.0))
                    .font_weight(FontWeight::MEDIUM)
                    .text_color(c("f_muted"))
                    .child(state.label())
                    .child(div().opacity(0.7).child(list.len().to_string()))
                    .into_any_element(),
            );
            for s in list {
                let selected = route == Some(Route::Session(s.address.clone()));
                let address = s.address.clone();
                groups.push(
                    row(SharedString::from(format!("s-{}", s.address)), selected)
                        .on_click(cx.listener(move |this, _, window, cx| {
                            this.go(Route::Session(address.clone()), window, cx)
                        }))
                        .child(
                            div()
                                .relative()
                                .size(px(16.0))
                                .flex_none()
                                .child(ui::agent_icon(&s.agent, 16.0))
                                .child(
                                    div()
                                        .absolute()
                                        .right(px(-1.0))
                                        .bottom(px(-1.0))
                                        .child(ui::status_dot(s.state)),
                                ),
                        )
                        .child(div().flex_1().min_w_0().truncate().child(s.title.clone()))
                        .child(
                            div()
                                .text_size(px(11.0))
                                .text_color(c("f_muted"))
                                .child(sessions::age(s.since_ms, now)),
                        )
                        .into_any_element(),
                );
            }
        }
        v_flex()
            .w(px(252.0))
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
                    .on_mouse_down(MouseButton::Left, |_, window, _| window.start_window_move())
                    .child(org_switcher)
                    .child(div().flex_1())
                    .child(
                        div()
                            .id("hide-sidebar")
                            .size(px(26.0))
                            .rounded(px(6.0))
                            .flex()
                            .items_center()
                            .justify_center()
                            .hover(|s| s.bg(c("f_line")))
                            .on_click(cx.listener(|this, _, _, cx| {
                                this.sidebar = false;
                                cx.notify();
                            }))
                            .child(ui::icon("sidebar", 15.0, c("f_muted"))),
                    ),
            )
            .child(
                div().px(px(10.0)).pb(px(2.0)).child(
                    h_flex()
                        .h(px(28.0))
                        .px(px(8.0))
                        .gap(px(6.0))
                        .rounded(px(7.0))
                        .bg(c("f_line"))
                        .child(ui::icon("search", 13.0, c("f_muted")))
                        .child(
                            div()
                                .flex_1()
                                .text_size(px(12.5))
                                .child(Input::new(&self.search)),
                        ),
                ),
            )
            .child(
                v_flex()
                    .id("side-scroll")
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
                            cx.listener(|this, _, _, cx| {
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
                    .children(groups),
            )
            .child(div().h(px(1.0)).bg(c("f_line")))
            .child(
                h_flex()
                    .h(px(40.0))
                    .px(px(16.0))
                    .gap(px(9.0))
                    .text_size(px(13.0))
                    .font_weight(FontWeight::MEDIUM)
                    .text_color(c("f_muted"))
                    .child(ui::icon("gear", 15.0, c("f_muted")))
                    .child(t("settings")),
            )
    }

    // ---------- project ----------

    fn project(&self, p: &Project, window: &Window, cx: &mut Context<Self>) -> impl IntoElement {
        let q = self.query(cx);
        let all = self.requests.get(&p.id).cloned().unwrap_or_default();
        let shown: Vec<&Request> = all
            .iter()
            .filter(|r| r.resolved() == self.resolved)
            .filter(|r| self.tag.as_ref().is_none_or(|t| &r.tag == t))
            .filter(|r| q.is_empty() || r.title.to_lowercase().contains(&q))
            .collect();
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
        let composer_has = !self.composer.read(cx).value().trim().is_empty();
        let segment = |label: String, on: bool, resolved: bool| {
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
                .child(label)
        };
        let tags = std::iter::once(None)
            .chain(p.tags.iter().map(|t| Some(t.clone())))
            .map(|choice| {
                let on = self.tag.as_deref() == choice.as_ref().map(|c| c.id.as_str());
                let (key, label) = choice
                    .as_ref()
                    .map(|c| (c.id.clone(), c.label.clone()))
                    .unwrap_or(("all".into(), t("all_tags")));
                let pick = choice.map(|c| c.id);
                div()
                    .id(SharedString::from(format!("tag-{key}")))
                    .h(px(24.0))
                    .px(px(9.0))
                    .rounded(px(12.0))
                    .flex()
                    .items_center()
                    .text_size(px(12.0))
                    .font_weight(FontWeight::MEDIUM)
                    .text_color(if on { c("f_text") } else { c("f_muted") })
                    .when(on, |d| d.bg(c("f_line")))
                    .hover(|s| s.bg(c("f_line")))
                    .on_click(cx.listener(move |this, _, _, cx| {
                        this.tag = pick.clone();
                        cx.notify();
                    }))
                    .child(label)
            });
        let cards = shown.iter().map(|r| {
            let selected = self.open == Some(r.number);
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
            v_flex()
                .id(SharedString::from(format!("req-{n}")))
                .w_full()
                .px(px(13.0))
                .py(px(11.0))
                .mb(px(8.0))
                .gap(px(4.0))
                .rounded(px(10.0))
                .bg(c("f_card"))
                .border_1()
                .border_color(if selected { c("orange") } else { c("f_line") })
                .when(!selected, |d| d.hover(|s| s.border_color(c("f_field"))))
                .on_click(cx.listener(move |this, _, window, cx| this.open_request(n, window, cx)))
                .child(
                    h_flex()
                        .items_start()
                        .gap(px(8.0))
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .text_size(px(13.5))
                                .font_weight(if r.resolved() {
                                    FontWeight::MEDIUM
                                } else {
                                    FontWeight::SEMIBOLD
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
                .into_any_element()
        });
        v_flex()
            .w(relative(0.42))
            .max_w(px(480.0))
            .min_w(px(340.0))
            .h_full()
            .flex_none()
            .bg(c("f_expanded"))
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
                    .on_mouse_down(MouseButton::Left, |_, window, _| window.start_window_move())
                    .child(ui::project_mark(&p.name, &p.color, 30.0))
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .gap(px(2.0))
                            .child(
                                div()
                                    .text_size(px(13.5))
                                    .font_weight(FontWeight::SEMIBOLD)
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
                    .child(
                        // New Request: one plain box; the first line is the title.
                        div()
                            .relative()
                            .rounded(px(10.0))
                            .bg(c("f_card"))
                            .border_1()
                            .border_color(
                                if self.composer.read(cx).focus_handle(cx).is_focused(window) {
                                    c("orange")
                                } else {
                                    c("f_field")
                                },
                            )
                            .px(px(4.0))
                            .py(px(3.0))
                            .text_size(px(13.5))
                            .child(Textarea::new(&self.composer))
                            .when(composer_has, |d| {
                                d.pb(px(38.0)).child(
                                    div().absolute().right(px(8.0)).bottom(px(8.0)).child(
                                        primary_button("post", t("post")).on_click(
                                            cx.listener(|this, _, window, cx| {
                                                this.post(window, cx)
                                            }),
                                        ),
                                    ),
                                )
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
                                    .child(segment(
                                        format!("{} {open_count}", t("open")),
                                        !self.resolved,
                                        false,
                                    ))
                                    .child(segment(
                                        format!("{} {resolved_count}", t("resolved")),
                                        self.resolved,
                                        true,
                                    )),
                            )
                            .child(h_flex().gap(px(2.0)).children(tags)),
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
                    .children(cards),
            )
    }

    fn thread(&self, p: &Project, window: &Window, cx: &mut Context<Self>) -> impl IntoElement {
        let Some(r) = self.thread.as_ref().filter(|t| Some(t.number) == self.open) else {
            return v_flex()
                .flex_1()
                .h_full()
                .bg(c("f_card"))
                .items_center()
                .justify_center()
                .child(ui::icon("chat", 32.0, c("f_muted").opacity(0.5)))
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
                            .font_weight(FontWeight::SEMIBOLD)
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
                                            .font_weight(FontWeight::SEMIBOLD)
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
                                .text_size(px(13.5))
                                .text_color(c("f_text")),
                            ),
                    )
                    .into_any_element(),
            );
        }
        let reply_has = !self.reply.read(cx).value().trim().is_empty();
        let resolved = r.resolved();
        v_flex()
            .flex_1()
            .min_w_0()
            .h_full()
            .bg(c("f_card"))
            .child(
                h_flex()
                    .id("thread-head")
                    .h(px(58.0))
                    .flex_none()
                    .px(px(18.0))
                    .gap(px(8.0))
                    .border_b_1()
                    .border_color(c("f_line"))
                    .on_mouse_down(MouseButton::Left, |_, window, _| window.start_window_move())
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
                            .font_weight(FontWeight::SEMIBOLD)
                            .text_color(c("f_text"))
                            .truncate()
                            .child(r.title.clone()),
                    )
                    .child(ui::tag_chip(&r.tag, &tag))
                    .child(ui::status_pill(&r.status))
                    .child(div().flex_1())
                    .child(
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
                    ),
            )
            .child(
                v_flex()
                    .id("thread")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .px(px(22.0))
                    .py(px(14.0))
                    .children(items),
            )
            .child(
                div().px(px(18.0)).pb(px(16.0)).child(
                    div()
                        .relative()
                        .rounded(px(10.0))
                        .bg(c("f_card"))
                        .border_1()
                        .border_color(if self.reply.read(cx).focus_handle(cx).is_focused(window) {
                            c("orange")
                        } else {
                            c("f_field")
                        })
                        .px(px(4.0))
                        .py(px(3.0))
                        .text_size(px(13.5))
                        .when(reply_has, |d| d.pb(px(38.0)))
                        .child(Textarea::new(&self.reply))
                        .when(reply_has, |d| {
                            d.child(div().absolute().right(px(8.0)).bottom(px(8.0)).child(
                                primary_button("send", t("send")).on_click(
                                    cx.listener(|this, _, window, cx| this.send_reply(window, cx)),
                                ),
                            ))
                        }),
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
        let shells = s
            .as_ref()
            .and_then(|s| self.shells.get(&s.name))
            .cloned()
            .unwrap_or_default();
        let request = s.as_ref().and_then(|s| {
            self.requests.iter().find_map(|(pid, list)| {
                list.iter()
                    .find(|r| r.session == s.name)
                    .map(|r| (pid.clone(), r.number, r.title.clone()))
            })
        });
        let tab = |id: SharedString, on: bool| {
            h_flex()
                .id(id)
                .h(px(28.0))
                .px(px(10.0))
                .gap(px(7.0))
                .rounded(px(7.0))
                .text_size(px(12.5))
                .font_weight(FontWeight::MEDIUM)
                .text_color(if on { c("f_text") } else { c("f_muted") })
                .when(on, |d| {
                    d.bg(c("f_card")).border_1().border_color(c("f_line"))
                })
                .when(!on, |d| {
                    d.border_1()
                        .border_color(transparent_black())
                        .hover(|s| s.bg(c("f_line")))
                })
        };
        let session_address = address.to_owned();
        let shell_tabs = shells.iter().enumerate().map(|(i, a)| {
            let key = format!("local:{a}");
            let (show, close) = (session_address.clone(), a.clone());
            let open_key = key.clone();
            tab(SharedString::from(format!("shell-{a}")), shown == key)
                .on_click(cx.listener(move |this, _, _, cx| {
                    this.tab.insert(show.clone(), open_key.clone());
                    cx.notify();
                }))
                .child(if shells.len() > 1 {
                    format!("{} {}", t("shell"), i + 1)
                } else {
                    t("shell")
                })
                .child(
                    div()
                        .id(SharedString::from(format!("close-{a}")))
                        .rounded(px(4.0))
                        .hover(|s| s.bg(c("f_line")))
                        .on_click(cx.listener(move |this, _, _, cx| {
                            cx.stop_propagation();
                            this.close_shell(close.clone(), cx);
                        }))
                        .child(ui::icon("close", 11.0, c("f_muted"))),
                )
        });
        let agent_on = shown == address;
        v_flex()
            .flex_1()
            .min_w_0()
            .h_full()
            .bg(c("background"))
            .child(
                h_flex()
                    .id("tabs")
                    .h(px(40.0))
                    .flex_none()
                    .px(px(10.0))
                    .gap(px(4.0))
                    .bg(c("f_bg"))
                    .border_b_1()
                    .border_color(c("f_line"))
                    .on_mouse_down(MouseButton::Left, |_, window, _| window.start_window_move())
                    .when_some(s.as_ref(), |d, s| {
                        let back = s.address.clone();
                        d.child(
                            tab("agent-tab".into(), agent_on)
                                .on_click(cx.listener(move |this, _, _, cx| {
                                    this.tab.remove(&back);
                                    cx.notify();
                                }))
                                .child(ui::agent_icon(&s.agent, 14.0))
                                .child(sessions::agent_name(&s.agent).to_owned()),
                        )
                    })
                    .children(shell_tabs)
                    .when(crate::core_bin::local().is_some(), |d| {
                        let session = s.clone();
                        d.child(
                            div()
                                .id("new-tab")
                                .size(px(26.0))
                                .rounded(px(6.0))
                                .flex()
                                .items_center()
                                .justify_center()
                                .hover(|s| s.bg(c("f_line")))
                                .on_click(cx.listener(move |this, _, window, cx| {
                                    if let Some(s) = session.clone() {
                                        this.new_shell(s, window, cx);
                                    }
                                }))
                                .child(ui::icon("plus", 13.0, c("f_muted"))),
                        )
                    })
                    .child(div().flex_1())
                    .when_some(request, |d, (pid, n, title)| {
                        d.child(secondary_button("goto-request", title, false).on_click(
                            cx.listener(move |this, _, window, cx| {
                                this.go(Route::Project(pid.clone()), window, cx);
                                this.open_request(n, window, cx);
                            }),
                        ))
                    }),
            )
            .child(div().flex_1().min_h_0().when_some(view, |d, v| d.child(v)))
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
        .font_weight(FontWeight::SEMIBOLD)
        .flex()
        .items_center()
        .child(label)
}

pub(crate) fn secondary_button(
    id: &'static str,
    label: impl Into<SharedString>,
    check: bool,
) -> Stateful<Div> {
    h_flex()
        .id(id)
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

impl Render for FennecWindow {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        crate::terminal::next_frame();
        window.set_rem_size(px(crate::settings::ui_font_size()));
        let route = self.route.clone();
        let main = match &route {
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
        let dialog = self.new_project_dialog(cx);
        h_flex()
            .relative()
            .size_full()
            .font_family(crate::settings::get().ui_font_family.clone())
            .bg(c("f_card"))
            .text_color(c("f_text"))
            .on_action(cx.listener(Self::on_shell_action))
            // ⌘K (Ctrl-K elsewhere) goes to search; Esc there clears it.
            .on_key_down(cx.listener(|this, e: &KeyDownEvent, window, cx| {
                let k = &e.keystroke;
                if k.key == "k"
                    && (k.modifiers.platform
                        || (cfg!(not(target_os = "macos")) && k.modifiers.control))
                {
                    this.sidebar = true;
                    this.search.read(cx).focus_handle(cx).focus(window, cx);
                    cx.stop_propagation();
                }
            }))
            .when(self.sidebar, |d| d.child(self.sidebar(cx)))
            .child(
                div()
                    .relative()
                    .flex_1()
                    .min_w_0()
                    .h_full()
                    .flex()
                    .child(main),
            )
            .when_some(menu, |d, m| d.child(m))
            .when_some(dialog, |d, m| d.child(m))
            .when_some(
                self.core_error.clone().or(self.notice.clone()),
                |d, text| {
                    d.child(
                        div()
                            .absolute()
                            .bottom(px(16.0))
                            .right(px(16.0))
                            .max_w(px(420.0))
                            .px(px(12.0))
                            .py(px(9.0))
                            .rounded(px(9.0))
                            .bg(c("f_card"))
                            .border_1()
                            .border_color(c("f_field"))
                            .text_size(px(12.0))
                            .child(text),
                    )
                },
            )
    }
}

fn empty() -> AnyElement {
    div().flex_1().h_full().bg(c("f_card")).into_any_element()
}
