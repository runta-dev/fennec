//! Settings › Cloud computer: export the cloud computer's queues and sessions
//! to a file and import such a file (THI-188). The daemon runs the job
//! (`cloud.archive.*`, THI-165); this dialog shows what it holds, polls the
//! job, and moves a finished export where the user chooses (like the log
//! export in app_preferences.rs).
use crate::app::{FennecWindow, primary_button, secondary_button};
use crate::i18n::{t, tf};
use crate::ui::{self, c};
use gpui::prelude::FluentBuilder as _;
use gpui::*;
use gpui_base::{h_flex, v_flex};
use serde_json::{Value, json};
use std::path::PathBuf;
use std::time::Duration;

#[derive(Clone, Copy, PartialEq)]
pub enum Mode {
    Export,
    Import,
}

#[derive(Clone, PartialEq)]
pub enum Phase {
    /// Asking the daemon whether a job is already running.
    Opening,
    Planning,
    /// Export: counts and sizes shown, waiting for the user.
    Ready,
    /// Import: waiting for a file.
    Choose,
    Reading,
    Preview,
    Running,
    Saving,
    Saved,
    Done,
    Cancelled,
    Failed,
}

pub struct Dialog {
    pub mode: Mode,
    pub phase: Phase,
    /// `cloud.archive.plan`: {all, queues} manifests, None when unavailable.
    pub plan: Option<Value>,
    pub sessions: bool,
    pub job: Value,
    pub manifest: Value,
    pub path: Option<PathBuf>,
    pub error: String,
    /// Bumped on every open so a stale poll loop stops.
    pub generation: u64,
}

/// Decimal sizes, as Finder shows them.
pub fn size(bytes: i64) -> String {
    let units = ["B", "KB", "MB", "GB", "TB"];
    let mut value = bytes.max(0) as f64;
    let mut unit = 0;
    while value >= 1000.0 && unit < units.len() - 1 {
        value /= 1000.0;
        unit += 1;
    }
    if unit == 0 || value >= 100.0 {
        format!("{} {}", value.round() as i64, units[unit])
    } else {
        format!("{value:.1} {}", units[unit])
    }
}

fn count(value: &Value, key: &str) -> String {
    value[key].as_i64().unwrap_or(0).to_string()
}

fn contents(counts: &Value) -> String {
    tf(
        "archive_queues_detail",
        &[
            ("queues", &count(counts, "projects")),
            ("requests", &count(counts, "requests")),
            ("knowledge", &count(counts, "knowledge")),
        ],
    )
}

fn sessions_detail(counts: &Value) -> String {
    tf(
        "archive_sessions_detail",
        &[("sessions", &count(counts, "sessions"))],
    )
}

/// The daemon's start errors are English; the one users can act on is localized.
fn start_error(error: &anyhow::Error) -> String {
    let text = error.to_string();
    if text.contains("already running") {
        t("archive_busy")
    } else {
        text
    }
}

impl FennecWindow {
    pub(crate) fn open_archive(&mut self, mode: Mode, window: &mut Window, cx: &mut Context<Self>) {
        if crate::preview::active() {
            return;
        }
        let generation = self.archive.as_ref().map_or(0, |d| d.generation) + 1;
        self.archive = Some(Dialog {
            mode,
            phase: Phase::Opening,
            plan: None,
            sessions: true,
            job: Value::Null,
            manifest: Value::Null,
            path: None,
            error: String::new(),
            generation,
        });
        cx.notify();
        // A job started earlier (here, in the menu bar or with `fen cloud`)
        // shows its progress instead of a second one being refused.
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let status = cx
                .background_spawn(async { crate::control::call("cloud.archive.status", json!({})) })
                .await
                .unwrap_or(Value::Null);
            let plan = this
                .update_in(cx, |this, window, cx| {
                    let Some(d) = this.archive.as_mut().filter(|d| d.generation == generation)
                    else {
                        return false;
                    };
                    if status["state"] == "running" {
                        d.mode = if status["kind"] == "export" {
                            Mode::Export
                        } else {
                            Mode::Import
                        };
                        d.phase = Phase::Running;
                        d.job = status;
                        this.poll_archive(window, cx);
                        false
                    } else if d.mode == Mode::Export {
                        d.phase = Phase::Planning;
                        cx.notify();
                        true
                    } else {
                        d.phase = Phase::Choose;
                        cx.notify();
                        false
                    }
                })
                .unwrap_or(false);
            if !plan {
                return;
            }
            let plan = cx
                .background_spawn(async { crate::control::call("cloud.archive.plan", json!({})) })
                .await
                .ok()
                .filter(|p| p["all"].is_object() && p["queues"].is_object());
            let _ = this.update(cx, |this, cx| {
                if let Some(d) = this
                    .archive
                    .as_mut()
                    .filter(|d| d.generation == generation && d.phase == Phase::Planning)
                {
                    d.plan = plan;
                    d.phase = Phase::Ready;
                    cx.notify();
                }
            });
        }));
    }

    fn close_archive(&mut self, cx: &mut Context<Self>) {
        // A running job keeps going in the background; reopening shows it.
        self.archive = None;
        cx.notify();
    }

    fn start_archive_job(
        &mut self,
        method: &'static str,
        params: Value,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(d) = self.archive.as_mut() else {
            return;
        };
        d.phase = Phase::Running;
        d.job = Value::Null;
        let generation = d.generation;
        cx.notify();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let started = cx
                .background_spawn(async move { crate::control::call(method, params) })
                .await;
            let _ = this.update_in(cx, |this, window, cx| {
                let Some(d) = this.archive.as_mut().filter(|d| d.generation == generation) else {
                    return;
                };
                match started {
                    Ok(job) => {
                        d.job = job;
                        this.poll_archive(window, cx);
                    }
                    Err(error) => {
                        d.phase = Phase::Failed;
                        d.error = start_error(&error);
                        cx.notify();
                    }
                }
            });
        }));
    }

    /// Polls `cloud.archive.status` until the job ends, then saves an export
    /// or shows the import's result.
    fn poll_archive(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(generation) = self.archive.as_ref().map(|d| d.generation) else {
            return;
        };
        cx.notify();
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            loop {
                cx.background_executor()
                    .timer(Duration::from_millis(700))
                    .await;
                let status = cx
                    .background_spawn(async {
                        crate::control::call("cloud.archive.status", json!({}))
                    })
                    .await;
                let save = this.update(cx, |this, cx| {
                    let Some(d) = this
                        .archive
                        .as_mut()
                        .filter(|d| d.generation == generation && d.phase == Phase::Running)
                    else {
                        return None;
                    };
                    let Ok(job) = status else {
                        return Some(false); // keep polling through a daemon restart
                    };
                    let state = job["state"].as_str().unwrap_or("").to_owned();
                    d.job = job;
                    cx.notify();
                    match state.as_str() {
                        "running" => Some(false),
                        "done" if d.mode == Mode::Export => {
                            d.phase = Phase::Saving;
                            Some(true)
                        }
                        "done" => {
                            // The import rebinds this Mac's queues to this
                            // computer; the next sync brings the merged ones in.
                            d.phase = Phase::Done;
                            None
                        }
                        "cancelled" => {
                            d.phase = Phase::Cancelled;
                            None
                        }
                        _ => {
                            d.phase = Phase::Failed;
                            d.error = d.job["error"].as_str().unwrap_or("").to_owned();
                            None
                        }
                    }
                });
                match save {
                    Ok(Some(false)) => continue,
                    Ok(Some(true)) => {
                        let _ =
                            this.update_in(cx, |this, window, cx| this.save_archive(window, cx));
                        break;
                    }
                    _ => break,
                }
            }
        }));
    }

    fn save_archive(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(d) = self.archive.as_ref() else {
            return;
        };
        let generation = d.generation;
        let exports = std::env::var_os("HOME")
            .map(|home| PathBuf::from(home).join(".config/fennec/exports"))
            .unwrap_or_default();
        let source = d.job["path"].as_str().map(PathBuf::from);
        let Some(source) = source.filter(|p| p.starts_with(&exports) && p.is_file()) else {
            self.finish_archive_save(generation, Phase::Failed, cx);
            return;
        };
        let name = d.job["name"]
            .as_str()
            .map(str::to_owned)
            .or_else(|| source.file_name().map(|n| n.to_string_lossy().into_owned()))
            .unwrap_or_default();
        let downloads = std::env::var_os("HOME")
            .map(|home| PathBuf::from(home).join("Downloads"))
            .unwrap_or_default();
        let chosen = cx.prompt_for_new_path(&downloads, Some(&name));
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let Ok(Ok(Some(destination))) = chosen.await else {
                // The export stays in ~/.config/fennec/exports (newest two kept).
                let _ = this.update(cx, |this, cx| {
                    this.finish_archive_save(generation, Phase::Cancelled, cx)
                });
                return;
            };
            let target = destination.clone();
            let moved = cx
                .background_spawn(async move {
                    let _ = std::fs::remove_file(&target);
                    std::fs::rename(&source, &target).or_else(|_| {
                        std::fs::copy(&source, &target)?;
                        std::fs::remove_file(&source)
                    })
                })
                .await;
            let _ = this.update(cx, |this, cx| match moved {
                Ok(()) => {
                    cx.reveal_path(&destination);
                    this.finish_archive_save(generation, Phase::Saved, cx);
                }
                Err(error) => {
                    tracing::warn!("archive export: {error}");
                    this.finish_archive_save(generation, Phase::Failed, cx);
                }
            });
        }));
    }

    fn finish_archive_save(&mut self, generation: u64, phase: Phase, cx: &mut Context<Self>) {
        if let Some(d) = self.archive.as_mut().filter(|d| d.generation == generation) {
            d.phase = phase;
            d.error.clear();
            cx.notify();
        }
    }

    fn choose_archive(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(generation) = self.archive.as_ref().map(|d| d.generation) else {
            return;
        };
        let selected = cx.prompt_for_paths(PathPromptOptions {
            files: true,
            directories: false,
            multiple: false,
            prompt: Some(t("archive_choose_file").into()),
        });
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let Ok(Ok(Some(paths))) = selected.await else {
                return;
            };
            let Some(path) = paths.into_iter().next() else {
                return;
            };
            let _ = this.update(cx, |this, cx| {
                if let Some(d) = this.archive.as_mut().filter(|d| d.generation == generation) {
                    d.phase = Phase::Reading;
                    d.error.clear();
                    cx.notify();
                }
            });
            let text = path.to_string_lossy().into_owned();
            let manifest = cx
                .background_spawn(async move {
                    crate::control::call("cloud.archive.inspect", json!({"path": text}))
                })
                .await;
            let _ = this.update(cx, |this, cx| {
                let Some(d) = this.archive.as_mut().filter(|d| d.generation == generation) else {
                    return;
                };
                match manifest {
                    Ok(manifest) if manifest["counts"].is_object() => {
                        d.manifest = manifest;
                        d.path = Some(path);
                        d.phase = Phase::Preview;
                    }
                    _ => {
                        d.phase = Phase::Choose;
                        d.error = t("archive_not_a_backup");
                    }
                }
                cx.notify();
            });
        }));
    }

    fn cancel_archive_job(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self._tasks.push(cx.spawn_in(window, async move |_, cx| {
            let _ = cx
                .background_spawn(async { crate::control::call("cloud.archive.cancel", json!({})) })
                .await;
        }));
    }

    pub(crate) fn archive_dialog(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let d = self.archive.as_ref()?;
        let note = |text: String| {
            div()
                .text_size(px(12.0))
                .line_height(px(18.0))
                .text_color(c("f_muted"))
                .child(text)
        };
        let strong = |text: String| {
            div()
                .text_size(px(13.0))
                .font_weight(FontWeight::MEDIUM)
                .text_color(c("f_text"))
                .child(text)
        };
        let progress = |done: i64, total: i64| {
            let fraction = if total > 0 {
                (done as f32 / total as f32).clamp(0.0, 1.0)
            } else {
                0.0
            };
            div()
                .h(px(5.0))
                .w_full()
                .rounded(px(3.0))
                .bg(c("f_line"))
                .overflow_hidden()
                .child(if total > 0 {
                    div()
                        .h_full()
                        .w(relative(fraction))
                        .bg(c("f_text"))
                        .into_any_element()
                } else {
                    div()
                        .h_full()
                        .w(relative(0.35))
                        .bg(c("f_text"))
                        .opacity(0.5)
                        .with_animation(
                            "archive-indeterminate",
                            Animation::new(Duration::from_millis(1300)).repeat(),
                            |bar, delta| bar.ml(relative(delta * 1.35 - 0.35)),
                        )
                        .into_any_element()
                })
        };
        let item = |title: String, detail: Option<String>, size_text: Option<String>| {
            h_flex()
                .gap(px(10.0))
                .py(px(9.0))
                .child(
                    v_flex()
                        .flex_1()
                        .min_w_0()
                        .gap(px(2.0))
                        .child(div().text_size(px(13.0)).child(title))
                        .when_some(detail, |d, text| {
                            d.child(
                                div()
                                    .text_size(px(11.5))
                                    .text_color(c("f_muted"))
                                    .child(text),
                            )
                        }),
                )
                .when_some(size_text, |d, text| {
                    d.child(
                        div()
                            .text_size(px(11.5))
                            .text_color(c("f_muted"))
                            .child(text),
                    )
                })
        };
        let list = |rows: Vec<AnyElement>| {
            v_flex()
                .px(px(12.0))
                .rounded(px(9.0))
                .border_1()
                .border_color(c("f_line"))
                .children(rows.into_iter().enumerate().map(|(i, r)| {
                    div()
                        .when(i > 0, |d| d.border_t_1().border_color(c("f_line")))
                        .child(r)
                }))
        };
        let cancel = || {
            secondary_button("archive-cancel", t("archive_cancel"), false)
                .on_click(cx.listener(|this, _, _, cx| this.close_archive(cx)))
        };
        let stop = || {
            secondary_button("archive-stop", t("archive_cancel"), false)
                .on_click(cx.listener(|this, _, window, cx| this.cancel_archive_job(window, cx)))
        };
        let done = || {
            primary_button("archive-done", t("archive_done"))
                .on_click(cx.listener(|this, _, _, cx| this.close_archive(cx)))
        };
        let mut body: Vec<AnyElement> = Vec::new();
        let mut actions: Vec<AnyElement> = Vec::new();
        let job_bytes = d.job["bytes"].as_i64().unwrap_or(0);
        let job_total = d.job["total"].as_i64().unwrap_or(0);
        match (d.mode, &d.phase) {
            (_, Phase::Opening) | (Mode::Export, Phase::Planning) => {
                body.push(note(t("archive_counting")).into_any_element());
                body.push(progress(0, 0).into_any_element());
                actions.push(cancel().into_any_element());
            }
            (Mode::Export, Phase::Ready) => {
                let plan = d.plan.as_ref();
                let bytes =
                    |key: &str| plan.map_or(0, |p| p[key]["counts"]["bytes"].as_i64().unwrap_or(0));
                let queues = item(
                    t("archive_queues"),
                    plan.map(|p| contents(&p["queues"]["counts"])),
                    plan.map(|_| size(bytes("queues"))),
                );
                let sessions = h_flex()
                    .id("archive-sessions")
                    .gap(px(9.0))
                    .on_click(cx.listener(|this, _, _, cx| {
                        if let Some(d) = this.archive.as_mut() {
                            d.sessions = !d.sessions;
                        }
                        cx.notify();
                    }))
                    .child(
                        div()
                            .size(px(15.0))
                            .flex_none()
                            .rounded(px(4.0))
                            .border_1()
                            .border_color(if d.sessions {
                                c("f_text")
                            } else {
                                c("f_field")
                            })
                            .when(d.sessions, |b| b.bg(c("f_text")))
                            .flex()
                            .items_center()
                            .justify_center()
                            .when(d.sessions, |b| {
                                b.child(ui::icon("check", 11.0, c("f_card")))
                            }),
                    )
                    .child(div().flex_1().min_w_0().child(item(
                        t("archive_include_sessions"),
                        plan.map(|p| sessions_detail(&p["all"]["counts"])),
                        plan.map(|_| size(bytes("all") - bytes("queues"))),
                    )));
                body.push(
                    list(vec![queues.into_any_element(), sessions.into_any_element()])
                        .into_any_element(),
                );
                body.push(
                    div()
                        .text_size(px(12.5))
                        .child(if plan.is_some() {
                            tf(
                                "archive_size",
                                &[(
                                    "size",
                                    &size(bytes(if d.sessions { "all" } else { "queues" })),
                                )],
                            )
                        } else {
                            t("archive_plan_unavailable")
                        })
                        .into_any_element(),
                );
                body.push(note(t("archive_not_included")).into_any_element());
                body.push(
                    div()
                        .text_size(px(12.0))
                        .line_height(px(18.0))
                        .child(t("archive_keep_safe"))
                        .into_any_element(),
                );
                actions.push(cancel().into_any_element());
                let sessions = d.sessions;
                actions.push(
                    primary_button("archive-export", t("archive_export_button"))
                        .on_click(cx.listener(move |this, _, window, cx| {
                            this.start_archive_job(
                                "cloud.archive.export",
                                json!({"sessions": sessions}),
                                window,
                                cx,
                            )
                        }))
                        .into_any_element(),
                );
            }
            (Mode::Export, Phase::Running) => {
                body.push(
                    note(if job_total > 0 {
                        tf(
                            "archive_exporting",
                            &[
                                ("done", &size(job_bytes)),
                                ("total", &format!("~{}", size(job_total))),
                            ],
                        )
                    } else {
                        t("archive_preparing")
                    })
                    .into_any_element(),
                );
                body.push(progress(job_bytes, job_total).into_any_element());
                if d.job["manifest"].is_object() {
                    let m = &d.job["manifest"];
                    let mut text = contents(&m["counts"]);
                    if m["sessions"] == true {
                        text = format!("{text} · {}", sessions_detail(&m["counts"]));
                    }
                    body.push(note(text).into_any_element());
                }
                actions.push(stop().into_any_element());
            }
            (Mode::Export, Phase::Saving) => {
                body.push(note(t("archive_choose_location")).into_any_element());
            }
            (Mode::Import, Phase::Choose) | (Mode::Import, Phase::Reading) => {
                let reading = d.phase == Phase::Reading;
                body.push(
                    note(if reading {
                        t("archive_reading")
                    } else {
                        t("archive_choose_hint")
                    })
                    .into_any_element(),
                );
                if !d.error.is_empty() {
                    body.push(strong(d.error.clone()).into_any_element());
                }
                actions.push(cancel().into_any_element());
                if !reading {
                    actions.push(
                        primary_button("archive-choose", t("archive_choose_file"))
                            .on_click(
                                cx.listener(|this, _, window, cx| this.choose_archive(window, cx)),
                            )
                            .into_any_element(),
                    );
                }
            }
            (Mode::Import, Phase::Preview) => {
                let m = &d.manifest;
                let source = if m["source_home"] == "/root" {
                    t("archive_source_runta")
                } else {
                    t("archive_source_self_hosted")
                };
                let time = m["created"]
                    .as_str()
                    .map(crate::local_time::stamp)
                    .unwrap_or_default();
                if let Some(name) = d.path.as_ref().and_then(|p| p.file_name()) {
                    body.push(strong(name.to_string_lossy().into_owned()).into_any_element());
                }
                body.push(
                    note(tf("archive_from", &[("source", &source), ("time", &time)]))
                        .into_any_element(),
                );
                let sessions = if m["sessions"] == true {
                    sessions_detail(&m["counts"])
                } else {
                    t("archive_no_sessions")
                };
                body.push(
                    list(vec![
                        item(t("archive_queues"), Some(contents(&m["counts"])), None)
                            .into_any_element(),
                        item(sessions, None, None).into_any_element(),
                    ])
                    .into_any_element(),
                );
                body.push(note(t("archive_merge_note")).into_any_element());
                actions.push(
                    secondary_button("archive-rechoose", t("archive_choose_file"), false)
                        .on_click(
                            cx.listener(|this, _, window, cx| this.choose_archive(window, cx)),
                        )
                        .into_any_element(),
                );
                let path = d
                    .path
                    .clone()
                    .unwrap_or_default()
                    .to_string_lossy()
                    .into_owned();
                actions.push(
                    primary_button("archive-import", t("archive_import_button"))
                        .on_click(cx.listener(move |this, _, window, cx| {
                            this.start_archive_job(
                                "cloud.archive.import",
                                json!({"path": path}),
                                window,
                                cx,
                            )
                        }))
                        .into_any_element(),
                );
            }
            (Mode::Import, Phase::Running) => {
                let uploaded = job_total > 0 && job_bytes >= job_total;
                body.push(
                    note(if uploaded {
                        t("archive_merging")
                    } else if job_total > 0 {
                        tf(
                            "archive_uploading",
                            &[("done", &size(job_bytes)), ("total", &size(job_total))],
                        )
                    } else {
                        t("archive_preparing")
                    })
                    .into_any_element(),
                );
                body.push(
                    if uploaded {
                        progress(0, 0)
                    } else {
                        progress(job_bytes, job_total)
                    }
                    .into_any_element(),
                );
                actions.push(stop().into_any_element());
            }
            (Mode::Import, Phase::Done) => {
                let r = &d.job["result"];
                let n = |key: &str| r[key].as_i64().unwrap_or(0);
                body.push(strong(t("archive_import_done")).into_any_element());
                let mut lines = vec![tf(
                    "archive_result_requests",
                    &[("n", &n("requests_added").to_string())],
                )];
                if n("requests_existing") > 0 {
                    lines.push(tf(
                        "archive_result_existing",
                        &[("n", &n("requests_existing").to_string())],
                    ));
                }
                lines.push(tf(
                    "archive_result_sessions",
                    &[("n", &n("sessions_restored").to_string())],
                ));
                let conflicts = r["knowledge_conflicts"].as_array().map_or(0, |a| a.len());
                if conflicts > 0 {
                    lines.push(tf(
                        "archive_result_conflicts",
                        &[("n", &conflicts.to_string())],
                    ));
                }
                body.push(
                    v_flex()
                        .gap(px(4.0))
                        .children(lines.into_iter().map(|line| {
                            h_flex()
                                .gap(px(7.0))
                                .items_start()
                                .text_size(px(12.5))
                                .child(div().text_color(c("f_muted")).child("•"))
                                .child(div().flex_1().min_w_0().child(line))
                        }))
                        .into_any_element(),
                );
                actions.push(done().into_any_element());
            }
            (mode, phase) => {
                match phase {
                    Phase::Saved => body.push(strong(t("archive_saved")).into_any_element()),
                    Phase::Cancelled => body.push(note(t("archive_cancelled")).into_any_element()),
                    _ => {
                        body.push(
                            strong(if mode == Mode::Export {
                                t("archive_export_failed")
                            } else {
                                t("archive_import_failed")
                            })
                            .into_any_element(),
                        );
                        if !d.error.is_empty() {
                            body.push(note(d.error.clone()).into_any_element());
                        }
                    }
                }
                if matches!(phase, Phase::Failed | Phase::Cancelled) {
                    actions.push(
                        secondary_button("archive-retry", t("archive_try_again"), false)
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.open_archive(mode, window, cx)
                            }))
                            .into_any_element(),
                    );
                }
                actions.push(done().into_any_element());
            }
        }
        let title = if d.mode == Mode::Export {
            t("archive_export_title")
        } else {
            t("archive_import_title")
        };
        Some(
            div()
                .id("archive-scrim")
                .absolute()
                .inset_0()
                .bg(hsla(0.0, 0.0, 0.0, 0.35))
                .flex()
                .items_center()
                .justify_center()
                .occlude()
                .child(
                    v_flex()
                        .id("archive-dialog")
                        .w(px(420.0))
                        .p(px(18.0))
                        .gap(px(12.0))
                        .rounded(px(12.0))
                        .bg(c("f_card"))
                        .shadow_lg()
                        .on_click(|_, _, cx| cx.stop_propagation())
                        .child(
                            div()
                                .text_size(px(14.0))
                                .font_weight(FontWeight::MEDIUM)
                                .child(title),
                        )
                        .children(body)
                        .child(
                            h_flex()
                                .justify_end()
                                .gap(px(8.0))
                                .pt(px(4.0))
                                .children(actions),
                        ),
                )
                .into_any_element(),
        )
    }
}
