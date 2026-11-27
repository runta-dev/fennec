//! A queue thread ends with its agent's latest output: a peek at its terminal
//! through herdr-core, drawn in the terminal's own colors. Local agents are
//! watched on this core (a watch-only attach); cloud agents with `sessions.peek`,
//! which watches them on the cloud computer the same way.
use super::*;
use crate::vt::{Cell, Rgb, Vt};
use serde_json::json;

/// Lines of the agent's output a thread shows.
const LINES: usize = 6;
/// The agent's input box and footer sit in these last lines of its screen.
const CHROME: usize = 8;
/// How often the open thread's agent screen is read: an agent's clock ticks each second.
const EVERY: Duration = Duration::from_millis(500);
/// How often the reader looks for a newly opened thread.
const TICK: Duration = Duration::from_millis(50);
/// A peek this recent needs no new read when its card is hovered or clicked.
const FRESH: Duration = Duration::from_millis(1500);
/// Background reads the queue's prefetch keeps in flight at most.
const PREFETCH: usize = 4;

/// Peek reads in flight, and when each agent's screen was last read.
#[derive(Default)]
pub(super) struct Reads {
    reading: std::collections::HashSet<String>,
    started: HashMap<String, std::time::Instant>,
}

/// A stretch of a line drawn in one style.
#[derive(Clone, Debug, Default, PartialEq)]
pub(super) struct Run {
    pub text: String,
    pub fg: Option<Rgb>,
    pub bg: Option<Rgb>,
    pub bold: bool,
    pub italic: bool,
    pub faint: bool,
    pub underline: bool,
    pub strike: bool,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub(super) struct Peek {
    /// The terminal it was read from: a peek is shown only for that one.
    pub address: String,
    pub lines: Vec<Vec<Run>>,
    /// The terminal's background, when the screen came with its colors.
    pub bg: Option<Rgb>,
}

/// What a read returns: the screen as the terminal draws it, or its text.
enum Screen {
    Paint {
        bytes: Vec<u8>,
        cols: u16,
        rows: u16,
    },
    Text(String),
}

impl FennecWindow {
    /// The open thread's agent session. Hidden once the request is in
    /// review: the agent's work is handed off and its reply is in the thread.
    pub(super) fn peek_target(&self) -> Option<&Session> {
        let r = self
            .thread
            .as_ref()
            .filter(|t| Some(t.number) == self.open && t.status != "review")?;
        r.active_session(&self.sessions)
    }

    /// The address of the open thread's agent session in any status, review
    /// included (that is when a finished agent is usually `done`).
    pub(super) fn thread_looking_session(&self) -> Option<String> {
        let r = self.thread.as_ref().filter(|t| Some(t.number) == self.open)?;
        Some(r.active_session(&self.sessions)?.address.clone())
    }

    /// Read the agent's screen a few times a second while its thread is open.
    /// Every read runs on its own: a newly opened thread's agent is read on
    /// the next tick instead of after the previous thread's read comes back
    /// (each cloud read is a round trip to the cloud computer).
    pub(super) fn follow_thread_peek(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            loop {
                let alive = this.update(cx, |this, cx| {
                    // While a clicked thread loads, its agent is already the target.
                    let target = this
                        .peek_target()
                        .or_else(|| this.opening.and_then(|n| this.listed_peek_session(n)))
                        .cloned();
                    if let Some(session) = target {
                        this.read_peek(session, EVERY, cx);
                    }
                });
                if alive.is_err() {
                    break;
                }
                cx.background_executor().timer(TICK).await;
            }
        }));
    }

    /// The agent of request `n` in the open queue's list, unless it is in
    /// review (its peek is hidden then).
    fn listed_peek_session(&self, n: u64) -> Option<&Session> {
        if let Some(t) = self.thread.as_ref().filter(|t| t.number == n) {
            return (t.status != "review").then(|| t.active_session(&self.sessions)).flatten();
        }
        let project = self.project_id()?;
        self.requests
            .get(&project)?
            .iter()
            .find(|r| r.number == n && r.status != "review")?
            .active_session(&self.sessions)
    }

    /// Read request `n`'s agent now unless it was read a moment ago: a card
    /// about to be opened (hovered or clicked) then shows a fresh peek.
    pub(super) fn warm_peek(&mut self, n: u64, cx: &mut Context<Self>) {
        if let Some(session) = self.listed_peek_session(n).cloned() {
            self.read_peek(session, FRESH, cx);
        }
    }

    /// Read every live agent of the open queue once, so a thread opened later
    /// shows its peek at once and refreshes it, instead of growing a moment later.
    pub(super) fn prefetch_peeks(&mut self, cx: &mut Context<Self>) {
        let Some(project) = self.project_id() else {
            return;
        };
        let missing: Vec<Session> = self
            .requests
            .get(&project)
            .into_iter()
            .flatten()
            .filter(|r| r.status != "review")
            .filter_map(|r| r.active_session(&self.sessions))
            .filter(|s| !self.thread_peek.contains_key(&s.address))
            .cloned()
            .collect();
        for session in missing {
            if self.peek_reads.reading.len() >= PREFETCH {
                break;
            }
            self.read_peek(session, Duration::MAX, cx);
        }
    }

    /// Start reading `session`'s screen unless a read is in flight or one
    /// started less than `every` ago.
    fn read_peek(&mut self, session: Session, every: Duration, cx: &mut Context<Self>) {
        let address = session.address.clone();
        if self.peek_reads.reading.contains(&address) {
            return;
        }
        let now = std::time::Instant::now();
        if self
            .peek_reads
            .started
            .get(&address)
            .is_some_and(|at| now.duration_since(*at) < every)
        {
            return;
        }
        self.peek_reads.reading.insert(address.clone());
        self.peek_reads.started.insert(address.clone(), now);
        let core = self.core.clone();
        let theme = crate::vt::theme();
		let scope = self.board_scope.clone();
        cx.spawn(async move |this, cx| {
            let read = {
                let address = address.clone();
                cx.background_spawn(async move {
                    read_screen(&session, core).map(|screen| peek(address, screen, theme))
                })
                .await
            };
            let _ = this.update(cx, |this, cx| {
				if this.board_scope != scope { return; }
                this.peek_reads.reading.remove(&address);
                let Ok(peek) = read else { return };
                if this.thread_peek.get(&address) != Some(&peek) {
                    let shown = this.peek_target().is_some_and(|s| s.address == address);
                    this.thread_peek.insert(address, peek);
                    if shown {
                        cx.notify();
                    }
                }
            });
        })
        .detach();
    }

    /// The agent's row at the end of the thread: its state, what it says it
    /// is doing and the last lines it printed.
    pub(super) fn thread_peek_card(
        &self,
        session: &Session,
        status: &str,
        permission: Option<AnyElement>,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let asking = permission.is_some();
        let address = session.address.clone();
        let peek = self
            .thread_peek
            .get(&session.address);
        let lines = peek.map(|p| p.lines.clone()).unwrap_or_default();
        let bg = peek
            .and_then(|p| p.bg)
            .map(crate::terminal::hsla)
            .unwrap_or_else(|| c("f_card"));
        let font = crate::settings::get().font_family.clone();
        h_flex()
            .id("thread-peek")
            .items_start()
            // THI-115: the agent's whole screen, in a modal over the thread.
            .cursor_pointer()
            .on_click(cx.listener(move |this, _, window, cx| {
                this.open_session_modal(address.clone(), window, cx)
            }))
            .gap(px(12.0))
            .py(px(10.0))
            .child(
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
                    .child(ui::agent_icon(&session.agent, 19.0)),
            )
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .gap(px(6.0))
                    .child(
                        h_flex()
                            .gap(px(6.0))
                            .min_w_0()
                            .child(
                                // FEN-151: the agent's name (e.g. "Claude Code"),
                                // not the thread's title (FEN-141 tried that).
                                div()
                                    .flex_none()
                                    .text_size(px(13.0))
                                    .font_weight(FontWeight::MEDIUM)
                                    .text_color(c("f_text"))
                                    .child(sessions::agent_name(&session.agent).to_owned()),
                            )
                            .child(
                                div()
                                    .min_w_0()
                                    .truncate()
                                    .text_size(px(11.5))
                                    .text_color(c("f_muted"))
                                    .child(if asking {
                                        // Not Claude Code's "permission prompt": the bar says what.
                                        t("asking_permission_status")
                                    } else if session.activity.is_empty() {
                                        status_label(status)
                                    } else {
                                        session.activity.clone()
                                    }),
                            ),
                    )
                    .when(!lines.is_empty() && !asking, |d| {
                        d.child(
                            v_flex()
                                .rounded(px(8.0))
                                .bg(bg)
                                .border_1()
                                .border_color(c("f_line"))
                                .px(px(10.0))
                                .py(px(7.0))
                                .font_family(font.clone())
                                .text_size(px(11.5))
                                .line_height(relative(1.45))
                                .text_color(c("f_muted"))
                                .children(lines.clone().into_iter().map(|line| {
                                    h_flex()
                                        .min_w_0()
                                        .overflow_hidden()
                                        .whitespace_nowrap()
                                        .children(line.into_iter().map(run_element))
                                })),
                        )
                    })
                    // THI-205: the agent's screen and, under it, what it
                    // asks to run with Allow / Deny, in one orange frame.
                    .when_some(permission, |d, bar| {
                        d.child(
                            v_flex()
                                .rounded(px(8.0))
                                .overflow_hidden()
                                .border_1()
                                .border_color(c("orange").opacity(0.45))
                                .when(!lines.is_empty(), |d| {
                                    d.child(
                                        v_flex()
                                            .bg(bg)
                                            .border_b_1()
                                            .border_color(c("f_line"))
                                            .px(px(10.0))
                                            .py(px(7.0))
                                            .font_family(font.clone())
                                            .text_size(px(11.5))
                                            .line_height(relative(1.45))
                                            .text_color(c("f_muted"))
                                            .children(lines.iter().cloned().map(|line| {
                                                h_flex()
                                                    .min_w_0()
                                                    .overflow_hidden()
                                                    .whitespace_nowrap()
                                                    .children(line.into_iter().map(run_element))
                                            })),
                                    )
                                })
                                .child(bar),
                        )
                    }),
            )
            .into_any_element()
    }
}

fn run_element(run: Run) -> Div {
    let mut d = div().flex_none().whitespace_nowrap().child(run.text);
    if let Some(fg) = run.fg {
        let mut color = crate::terminal::hsla(fg);
        if run.faint {
            color = color.opacity(0.6);
        }
        d = d.text_color(color);
    }
    if let Some(bg) = run.bg {
        d = d.bg(crate::terminal::hsla(bg));
    }
    if run.bold {
        d = d.font_weight(FontWeight::BOLD);
    }
    if run.italic {
        d = d.italic();
    }
    if run.underline {
        d = d.underline();
    }
    if run.strike {
        d = d.line_through();
    }
    d
}

fn status_label(status: &str) -> String {
    match status {
        "failed" => t("failed"),
        "needs_you" | "you" | "stalled" => t("needs_you"),
        "review" => t("review"),
        "resolved" => t("resolved"),
        "working" => t("working"),
        _ => t("queued"),
    }
}

fn read_screen(session: &Session, core: Option<Arc<Core>>) -> anyhow::Result<Screen> {
    if let Some(address) = session.address.strip_prefix("cloud:") {
        let params = json!({"session_id": session.name, "address": address});
        // Older Fennec services only read the text.
        let read = crate::control::call("sessions.peek", params.clone())
            .or_else(|_| crate::control::call("sessions.read", params))?;
        if let Some(bytes) = read["vt"].as_str().and_then(|b| {
            use base64::Engine;
            base64::engine::general_purpose::STANDARD.decode(b).ok()
        }) {
            let size = |k: &str| read[k].as_u64().unwrap_or(0) as u16;
            return Ok(Screen::Paint {
                bytes,
                cols: size("cols"),
                rows: size("rows"),
            });
        }
        return Ok(Screen::Text(
            read["screen"]["text"]
                .as_str()
                .unwrap_or_default()
                .to_owned(),
        ));
    }
    let core = core.ok_or_else(|| anyhow::anyhow!("not connected"))?;
    // A finished terminal has no watchers: its text is still there to read.
    if let Ok(paint) = observe(&core, &session.address) {
        return Ok(paint);
    }
    let screen = core.call(
        "terminal.read",
        json!({"terminal": session.address, "lines": 0}),
    )?;
    Ok(Screen::Text(
        screen["text"].as_str().unwrap_or_default().to_owned(),
    ))
}

/// The first paint a watcher of this terminal gets.
fn observe(core: &Core, address: &str) -> anyhow::Result<Screen> {
    let watch = core.observe(address)?;
    let size = |k: &str| watch.terminal[k].as_u64().unwrap_or(0) as u16;
    let (cols, rows) = (size("cols"), size("rows"));
    let mut bytes = Vec::new();
    let mut last = std::time::Instant::now();
    let deadline = last + Duration::from_secs(2);
    // The paint comes at once; what follows is the program drawing.
    loop {
        match watch.stream.try_recv() {
            Ok(Stream::Data(data)) => {
                core.credit(watch.channel, data.len());
                bytes.extend(data);
                last = std::time::Instant::now();
            }
            Ok(_) | Err(async_channel::TryRecvError::Closed) => break,
            Err(async_channel::TryRecvError::Empty) => {
                let now = std::time::Instant::now();
                if now > deadline || (!bytes.is_empty() && now - last > Duration::from_millis(30)) {
                    break;
                }
                std::thread::sleep(Duration::from_millis(5));
            }
        }
    }
    core.detach(watch.channel);
    if bytes.is_empty() {
        anyhow::bail!("no paint");
    }
    Ok(Screen::Paint { bytes, cols, rows })
}

fn peek(address: String, screen: Screen, theme: crate::vt::Theme) -> Peek {
    match screen {
        Screen::Text(text) => {
            let lines: Vec<String> = text.lines().map(|l| l.replace('\u{a0}', " ")).collect();
            Peek {
                address,
                lines: snippet(&lines, LINES)
                    .into_iter()
                    .map(|i| {
                        vec![Run {
                            text: lines[i].trim_end().to_owned(),
                            ..Run::default()
                        }]
                    })
                    .collect(),
                bg: None,
            }
        }
        Screen::Paint { bytes, cols, rows } => {
            // Unknown sizes come from older services: wide enough not to wrap.
            let (cols, rows) = if cols == 0 || rows == 0 {
                (400, 200)
            } else {
                (cols, rows)
            };
            let mut vt = Vt::new(cols, rows);
            vt.set_theme(theme);
            vt.write(&bytes);
            let frame = vt.frame();
            let cells: Vec<&[Cell]> = frame.rows.iter().map(|r| r.as_slice()).collect();
            let lines: Vec<String> = cells.iter().map(|r| row_text(r)).collect();
            Peek {
                address,
                lines: snippet(&lines, LINES)
                    .into_iter()
                    .map(|i| runs(cells[i], frame.bg))
                    .collect(),
                bg: Some(frame.bg),
            }
        }
    }
}

/// A row's text, one character per cell (a wide character's second cell adds none).
fn row_text(cells: &[Cell]) -> String {
    let mut text = String::new();
    let mut tail = false;
    for cell in cells {
        if std::mem::take(&mut tail) {
            continue;
        }
        text.push_str(if cell.text.is_empty() {
            " "
        } else {
            &cell.text
        });
        tail = cell.wide;
    }
    text.replace('\u{a0}', " ")
}

/// A row as runs of one style, without its trailing blanks.
fn runs(cells: &[Cell], screen_bg: Rgb) -> Vec<Run> {
    let mut out: Vec<Run> = Vec::new();
    let mut tail = false;
    let blank = |c: &Cell| c.text.trim().is_empty() && c.bg.is_none_or(|bg| bg == screen_bg);
    let end = cells.iter().rposition(|c| !blank(c)).map_or(0, |i| i + 1);
    for cell in &cells[..end] {
        if std::mem::take(&mut tail) {
            continue;
        }
        tail = cell.wide;
        let text = if cell.text.is_empty() {
            " "
        } else {
            cell.text.as_str()
        };
        let text = if text == "\u{a0}" { " " } else { text };
        let style = Run {
            text: String::new(),
            fg: Some(cell.fg),
            bg: cell.bg.filter(|bg| *bg != screen_bg),
            bold: cell.bold,
            italic: cell.italic,
            faint: cell.faint,
            underline: cell.underline.is_some(),
            strike: cell.strike,
        };
        match out.last_mut() {
            Some(last)
                if Run {
                    text: String::new(),
                    ..last.clone()
                } == style =>
            {
                last.text.push_str(text)
            }
            _ => out.push(Run {
                text: text.to_owned(),
                ..style
            }),
        }
    }
    out
}

/// Which of a screen's lines to show: the last `max` printed lines above its input box.
pub(super) fn snippet<S: AsRef<str>>(lines: &[S], max: usize) -> Vec<usize> {
    let lines: Vec<&str> = lines.iter().map(|l| l.as_ref().trim_end()).collect();
    let mut end = lines.len();
    while end > 0 && lines[end - 1].trim().is_empty() {
        end -= 1;
    }
    // Agents draw their input box between rules at the bottom: cut at the topmost.
    if let Some(i) = (end.saturating_sub(CHROME)..end).find(|&i| is_rule(lines[i])) {
        end = i;
    }
    let mut out: Vec<usize> = (0..end)
        .rev()
        .filter(|&i| !lines[i].trim().is_empty() && !is_rule(lines[i]))
        .take(max)
        .collect();
    out.reverse();
    out
}

fn is_rule(line: &str) -> bool {
    let line = line.trim();
    line.chars().count() >= 12 && line.chars().all(|c| "─━═╌┄-▔▁╭╮╰╯┌┐└┘".contains(c))
}

#[cfg(test)]
mod tests {
    use super::snippet;

    fn shown(screen: &str, max: usize) -> Vec<String> {
        let lines: Vec<&str> = screen.lines().collect();
        snippet(&lines, max)
            .into_iter()
            .map(|i| lines[i].trim_end().replace('\u{a0}', " "))
            .collect()
    }

    #[test]
    fn keeps_the_output_above_claude_codes_input_box() {
        let rule = "─".repeat(80);
        let screen = format!(
            "\n\n● Reading the queue thread code\n  ⎿ Read 40 lines\n\n✽ Undulating… (1m 40s)\n\n{rule}\n❯\u{a0}\n{rule}\n  ⏵⏵ bypass permissions on\n\n"
        );
        assert_eq!(
            shown(&screen, 6),
            [
                "● Reading the queue thread code",
                "  ⎿ Read 40 lines",
                "✽ Undulating… (1m 40s)"
            ]
        );
    }

    #[test]
    fn keeps_the_last_lines_of_a_screen_without_an_input_box() {
        let screen = (1..=10)
            .map(|n| format!("line {n}"))
            .collect::<Vec<_>>()
            .join("\n");
        assert_eq!(shown(&screen, 3), ["line 8", "line 9", "line 10"]);
    }

    #[test]
    fn a_rule_far_above_the_bottom_is_output() {
        let rule = "─".repeat(40);
        let mut lines = vec!["before".to_owned(), rule, "after".to_owned()];
        lines.extend((1..=10).map(|n| format!("out {n}")));
        assert_eq!(shown(&lines.join("\n"), 2), ["out 9", "out 10"]);
        assert!(shown("", 6).is_empty());
    }
}
