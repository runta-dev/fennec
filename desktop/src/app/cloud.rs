//! Cloud rows load independently; terminal pixels and input use the installed fen attach.
use super::*;
use serde_json::{Value, json};

impl FennecWindow {
    pub(super) fn set_shown_tab(&mut self, session: String, tab: String, cx: &mut Context<Self>) {
        self.close_others = None;
        self.prefix = false;
        self.layout.intent += 1;
        let old = self.shown_tab(&session);
        if old != tab && old.starts_with("cloud:") {
            if let Some(view) = self.terms.remove(&old) {
                view.read(cx).detach();
            }
        }
        self.tab.insert(session, tab.clone());
        if !self.layout.groups.is_empty() {
            self.layout.selection = Some(tab.clone());
        }
        self.pending_focus = Some(tab);
        cx.notify();
    }

    pub(super) fn follow_cloud(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if crate::core_bin::cloud() {
            return;
        }
        // Isolated local tests pass the same core for both endpoints.
        if std::env::var_os("FENNEC_HC") == std::env::var_os("FENNEC_LOCAL_HC") {
            return;
        }
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            loop {
                let generation = match this.update(cx, |this, _| this.cloud_generation) {
                    Ok(v) => v,
                    Err(_) => break,
                };
                let result = cx
                    .background_spawn(async { crate::control::call("sessions.list", json!({})) })
                    .await;
                match result {
                    Ok(mut listing) if listing["loaded"] == true => {
                        if let Some(object) = listing.as_object_mut() {
                            object.remove("refreshing");
                        }
                        if this
                            .update_in(cx, |this, window, cx| {
                                if generation != this.cloud_generation
                                    || this.cloud_listing == listing
                                {
                                    return;
                                }
                                this.set_cloud_listing(listing, window, cx);
                            })
                            .is_err()
                        {
                            break;
                        }
                    }
                    _ => {
                        let signed_out = cx
                            .background_spawn(async {
                                crate::control::call("account.status", json!({}))
                                    .ok()
                                    .is_some_and(|v| v["status"] == "signed_out")
                            })
                            .await;
                        if signed_out
                            && this
                                .update_in(cx, |this, window, cx| {
                                    this.set_cloud_listing(json!({"terminals":[]}), window, cx);
                                })
                                .is_err()
                        {
                            break;
                        }
                    }
                }
                cx.background_executor().timer(Duration::from_secs(2)).await;
            }
        }));
    }

    fn set_cloud_listing(&mut self, listing: Value, window: &mut Window, cx: &mut Context<Self>) {
        let reset = self.cloud_events.is_none()
            || self.cloud_listing["runtime_id"] != listing["runtime_id"];
        if reset {
            self.cloud_generation += 1;
            for key in self.cloud_views.keys().cloned().collect::<Vec<_>>() {
                self.detach_cloud_view(&key, cx);
            }
            self.cloud_attach_failed.clear();
        }
        let mut events = EventLines::new(&listing);
        if !reset {
            let _ = events.take_seen();
        }
        self.cloud_listing = listing;
        self.cloud_events = Some(events);
        self.terminals_changed(window, cx);
    }

    pub(super) fn ensure_cloud_view(
        &mut self,
        key: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if self.cloud_views.contains_key(key)
            || self.cloud_attach_pending.contains(key)
            || self.cloud_attach_failed.contains(key)
        {
            return;
        }
        let (Some(core), Some(session)) = (
            self.local.clone(),
            self.layout.members.get(key).cloned().or_else(|| self.sessions.iter().find(|s| s.address == key).cloned()),
        ) else {
            return;
        };
        let runtime = self.cloud_listing["runtime_id"]
            .as_str()
            .unwrap_or_default()
            .to_owned();
        // Recover a previous view after UI restart, never create a second attach after an uncertain receipt.
        if let Some(address) = self
            .events
            .as_ref()
            .and_then(|events| events.live()["terminals"].as_array().cloned())
            .into_iter()
            .flatten()
            .find(|r| {
                r["labels"]["fennec.cloud-view"] == session.name
                    && r["labels"]["fennec.cloud-runtime"] == runtime
            })
            .and_then(|r| r["address"].as_str().map(str::to_owned))
        {
            self.cloud_views.insert(key.to_owned(), address);
            cx.notify();
            return;
        }
        self.cloud_attach_pending.insert(key.to_owned());
        let key = key.to_owned();
        let display = crate::terminal::initial_display(window);
        let expected_runtime = runtime.clone();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let result = cx.background_spawn(async move {
                let cli = std::env::var_os("FENNEC_CLI").map(std::path::PathBuf::from)
                    .or_else(|| std::env::current_exe().ok().and_then(|p| p.parent().map(|p| p.join("fen"))))
                    .ok_or_else(|| anyhow::anyhow!("Fennec command unavailable"))?;
                let record = core.call("terminal.run", json!({
                    "argv":["/usr/bin/env","-u","NO_COLOR",cli.to_string_lossy(),"attach",session.name],
                    "cwd":std::env::var("HOME").ok(), "keep":false,
                    "env":{"TERM":"xterm-256color","COLORTERM":"truecolor","TERM_PROGRAM":"ghostty"},
                    "labels":{"fennec.cloud-view":session.name,"fennec.cloud-runtime":runtime},"display":display
                }))?;
                record["address"].as_str().map(str::to_owned).ok_or_else(|| anyhow::anyhow!("Attach view not confirmed"))
            }).await;
            let _ = this.update_in(cx, |this, _, cx| {
                this.cloud_attach_pending.remove(&key);
                match result {
                    Ok(address) => {
                        if this.cloud_listing["runtime_id"].as_str().unwrap_or_default() != expected_runtime {
                            if let Some(core) = this.local.clone() { this._tasks.push(cx.spawn(async move |_, cx| { let _=cx.background_spawn(async move { core.call("terminal.kill",json!({"terminal":address})) }).await; })); }
                            cx.notify(); return;
                        }
                        this.cloud_views.insert(key.clone(), address);
                        let selected = matches!(&this.route, Some(Route::Session(id)) if this.shown_tab(id) == key);
                        if selected { this.pending_focus = Some(key.clone()); }
                    }
                    Err(error) => { tracing::warn!("cloud attach view: {error:#}"); this.cloud_attach_failed.insert(key.clone()); }
                }
                cx.notify();
            });
        }));
    }

    pub(super) fn detach_cloud_view(&mut self, key: &str, cx: &mut Context<Self>) {
        if let Some(view) = self.terms.remove(key) {
            view.read(cx).detach();
        }
        if let (Some(address), Some(core)) = (self.cloud_views.remove(key), self.local.clone()) {
            // This terminates only the local fen attach process; the cloud agent stays alive.
            self._tasks.push(cx.spawn(async move |_, cx| {
                let _ = cx
                    .background_spawn(async move {
                        core.call("terminal.kill", json!({"terminal":address}))
                    })
                    .await;
            }));
        }
    }

    pub(super) fn close_cloud_session(
        &mut self,
        key: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(session) = self.layout.members.get(&key).cloned().or_else(|| self.sessions.iter().find(|s| s.address == key).cloned()) else {
            return;
        };
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let result = cx
                .background_spawn(async move {
                    crate::control::call("sessions.stop", json!({"session_id":session.name}))
                })
                .await;
            let _ = this.update_in(cx, |this, window, cx| match result {
                Ok(_) => {
                    this.cloud_generation += 1;
                    this.tab_closed(&key, window, cx);
                    this.detach_cloud_view(&key, cx);
                    let mut listing = this.cloud_listing.clone();
                    if let Some(rows) = listing["terminals"].as_array_mut() {
                        rows.retain(|r| {
                            Some(r["address"].as_str().unwrap_or_default())
                                != key.strip_prefix("cloud:")
                        });
                    }
                    this.set_cloud_listing(listing, window, cx);
                    if this.route.is_none() {
                        if let Some(local) = this
                            .sessions
                            .iter()
                            .find(|s| !s.address.starts_with("cloud:"))
                        {
                            this.go(Route::Session(local.address.clone()), window, cx);
                        }
                    }
                }
                Err(error) => this.say(format!("{error:#}"), cx),
            });
        }));
    }

    /// Cloud sessions a local terminal already shows: one where a shim took
    /// over `claude` (or `fen attach` ran) labels itself `fennec.attached` with
    /// the session it streams. The label counts only while that terminal still
    /// runs an agent, so one left behind by a crash never hides a session.
    pub(super) fn shown_locally(&self) -> std::collections::HashSet<String> {
        self.events
            .as_ref()
            .and_then(|events| events.live()["terminals"].as_array().cloned())
            .into_iter()
            .flatten()
            .filter(|t| t["state"] == "live")
            .filter(|t| {
                t["address"]
                    .as_str()
                    .is_some_and(|a| self.activity[a]["program"]["agent"] == true)
            })
            .filter_map(|t| t["labels"]["fennec.attached"].as_str().map(str::to_owned))
            .filter(|name| !name.is_empty())
            .collect()
    }

    pub(super) fn terminal_records(&self) -> Vec<Value> {
        let mut local = self
            .events
            .as_ref()
            .map(|events| {
                events.live()["terminals"]
                    .as_array()
                    .cloned()
                    .unwrap_or_default()
            })
            .unwrap_or_default();
        let local_names: std::collections::HashSet<String> = local
            .iter()
            .filter_map(|t| t["labels"]["fennec.session"].as_str().map(str::to_owned))
            .collect();
        let mut cloud = self
            .cloud_events
            .as_ref()
            .map(|events| {
                events.live()["terminals"]
                    .as_array()
                    .cloned()
                    .unwrap_or_default()
            })
            .unwrap_or_default();
        let addresses: std::collections::HashSet<String> = cloud
            .iter()
            .filter_map(|r| r["address"].as_str().map(str::to_owned))
            .collect();
        let mut roots = HashMap::new();
        for terminal in &mut cloud {
            if let Some(address) = terminal["address"].as_str() {
                terminal["address"] = json!(format!("cloud:{address}"));
            }
            if let Some(parent) = terminal["parent"].as_str().filter(|p| !p.is_empty()) {
                let short = parent.rsplit('/').next().unwrap_or(parent);
                let parent = if addresses.contains(short) {
                    short
                } else {
                    parent
                };
                terminal["parent"] = json!(format!("cloud:{parent}"));
            }
            terminal["source"] = json!("cloud");
            if let (Some(name), Some(address)) = (
                terminal["labels"]["fennec.session"].as_str(),
                terminal["address"].as_str(),
            ) {
                roots.insert(name.to_owned(), address.to_owned());
            }
        }
        for terminal in &mut local {
            if let Some(parent) = terminal["labels"]["fennec.shell-of"]
                .as_str()
                .map(str::to_owned)
            {
                let explicit = parent.strip_prefix("cloud:");
                if explicit.is_some() || !local_names.contains(&parent) {
                    let name = explicit.unwrap_or(&parent);
                    if let Some(root) = roots.get(name) {
                        terminal["desktop_group"] = json!(root);
                        terminal["labels"]["fennec.shell-of"] = json!(format!("cloud:{name}"));
                    }
                }
            }
        }
        local.extend(cloud);
        for group in &self.layout.groups {
            if let Some(root) = group.tabs.iter().find(|key| local.iter().any(|r| r["address"].as_str() == Some(key.strip_prefix("local:").unwrap_or(key)))) {
                for record in &mut local {
                    if group.tabs.iter().any(|k| record["address"].as_str() == Some(k.strip_prefix("local:").unwrap_or(k))) {
                        record["desktop_group"] = json!(root.strip_prefix("local:").unwrap_or(root));
                    }
                }
            }
        }
        local
    }
}
