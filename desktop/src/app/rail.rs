//! FEN-106: a title bar across the window (the traffic lights at its left,
//! search in the middle) over a narrow rail on the left edge, as in Discord,
//! instead of the old sidebar. The rail holds the local terminal, the queues
//! (one icon each), New queue, and Settings at the bottom. The queue column
//! and the thread beside it fill the rest of the window.
//!
//! FEN-125: laid out like Slack. The title bar and the rail are one surface
//! (the chrome, `f_bg`, with no line between them), the content is a rounded
//! panel on it, and rail buttons grow under the pointer. A selected queue
//! gets a ring and a selected button a lighter square, with no pill at the
//! rail's edge.
use super::*;
use std::time::Instant;

/// The rail's width; it lines up with the traffic lights (x 16–68) above it.
pub(crate) const RAIL_WIDTH: f32 = 72.0;
/// The title bar's height; the traffic lights (y 17–29) sit in its middle.
pub(crate) const TITLEBAR_HEIGHT: f32 = 46.0;
/// A rail button's square.
const RAIL_ITEM: f32 = 44.0;
/// A queue icon, and a button's highlight, at rest; the ring of a selected
/// queue goes around it in the rest of `RAIL_ITEM`.
const RAIL_ICON: f32 = 36.0;
/// How much a hovered button grows (FEN-125).
const GROW: f32 = 0.1;
/// The opacity of a queue that isn't shown and isn't hovered (FEN-125).
const DIMMED: f32 = 0.45;
const GROW_IN: Duration = Duration::from_millis(140);
const GROW_OUT: Duration = Duration::from_millis(180);

/// The gap between the content panel and the window's right and bottom edges.
pub(crate) const PANEL_INSET: f32 = 6.0;
/// The content panel's corner radius.
pub(crate) const PANEL_RADIUS: f32 = 10.0;

/// The hovered rail button, and the one shrinking back after the pointer
/// left it: (id, start, the scale it started from).
#[derive(Default)]
pub(crate) struct RailHover {
    hovered: Option<(SharedString, Instant, f32)>,
    leaving: Option<(SharedString, Instant, f32)>,
}

fn ease_out_cubic(t: f32) -> f32 {
    1.0 - (1.0 - t.clamp(0.0, 1.0)).powi(3)
}

impl RailHover {
    /// The button's scale now, and whether it is still moving.
    fn scale(&self, id: &str, now: Instant) -> (f32, bool) {
        let step = |start: Instant, duration: Duration| {
            let t = now.saturating_duration_since(start).as_secs_f32() / duration.as_secs_f32();
            (ease_out_cubic(t), t < 1.0)
        };
        match (&self.hovered, &self.leaving) {
            (Some((h, start, from)), _) if h == id => {
                let (p, moving) = step(*start, GROW_IN);
                (from + (1.0 + GROW - from) * p, moving)
            }
            (_, Some((l, start, from))) if l == id => {
                let (p, moving) = step(*start, GROW_OUT);
                (from + (1.0 - from) * p, moving)
            }
            _ => (1.0, false),
        }
    }

    fn set(&mut self, id: SharedString, hovered: bool) {
        let now = Instant::now();
        let (from, _) = self.scale(&id, now);
        if hovered {
            if let Some((old, ..)) = self.hovered.take() {
                let (f, _) = self.scale(&old, now);
                self.leaving = Some((old, now, f));
            }
            if self.leaving.as_ref().is_some_and(|(l, ..)| l == &id) {
                self.leaving = None;
            }
            self.hovered = Some((id, now, from));
        } else if self.hovered.as_ref().is_some_and(|(h, ..)| h == &id) {
            self.hovered = None;
            self.leaving = Some((id, now, from));
        }
    }

    fn is_hovered(&self, id: &str) -> bool {
        self.hovered.as_ref().is_some_and(|(h, ..)| h == id)
    }
}

/// FEN-125: the content panel's corners and edge. gpui clips children to the
/// panel's rectangle, not its rounded corners (see gpui-rounded-clipping in
/// the queue knowledge), so this paints the chrome over each corner outside
/// the curve, then the panel's border on top. It's the panel's last child.
pub(crate) fn panel_frame() -> impl IntoElement {
    let chrome = c("f_bg");
    let corners = canvas(
        |_, _, _| {},
        move |bounds, _, window, _| {
            let r = px(PANEL_RADIUS);
            let (o, w, h) = (bounds.origin, bounds.size.width, bounds.size.height);
            // Each corner, and the direction into the panel from it.
            for (corner, dx, dy) in [
                (o, 1.0, 1.0),
                (point(o.x + w, o.y), -1.0, 1.0),
                (point(o.x + w, o.y + h), -1.0, -1.0),
                (point(o.x, o.y + h), 1.0, -1.0),
            ] {
                let mut path = PathBuilder::fill();
                path.move_to(corner);
                path.line_to(point(corner.x + r * dx, corner.y));
                path.arc_to(point(r, r), px(0.0), false, dx * dy < 0.0, point(corner.x, corner.y + r * dy));
                path.close();
                if let Ok(path) = path.build() {
                    window.paint_path(path, chrome);
                }
            }
        },
    )
    .absolute()
    .inset_0();
    div()
        .absolute()
        .inset_0()
        .child(corners)
        .child(
            div()
                .absolute()
                .inset_0()
                .rounded(px(PANEL_RADIUS))
                .border_1()
                .border_color(c("f_line")),
        )
}

/// The arrow's length; it points at the rail button from the tooltip's left.
const TOOLTIP_ARROW: f32 = 6.0;

/// FEN-119: a rail button's name, as in Discord: a dark bubble to the right
/// of the button, centred on it, with an arrow pointing back at it. Shown at
/// once while the button is hovered (`group`), not after gpui's tooltip delay
/// at the mouse.
fn rail_tooltip(name: SharedString, group: SharedString) -> impl IntoElement {
    let dark = crate::settings::get().dark;
    let bg = if dark { rgb(0x111214).into() } else { c("f_card") };
    let border = c("f_field");
    let arrow = canvas(
        |_, _, _| {},
        move |bounds, _, window, _| {
            let o = bounds.origin;
            let (w, h) = (bounds.size.width, bounds.size.height);
            let tip = point(o.x, o.y + h / 2.0);
            let top = point(o.x + w, o.y);
            let bottom = point(o.x + w, o.y + h);
            let mut body = PathBuilder::fill();
            body.move_to(tip);
            body.line_to(top);
            body.line_to(bottom);
            body.close();
            if let Ok(path) = body.build() {
                window.paint_path(path, bg);
            }
            let mut edge = PathBuilder::stroke(px(1.0));
            edge.move_to(top);
            edge.line_to(tip);
            edge.line_to(bottom);
            if let Ok(path) = edge.build() {
                window.paint_path(path, border);
            }
        },
    )
    // One pixel more than the arrow, to cover the bubble's border under it.
    .w(px(TOOLTIP_ARROW + 1.0))
    .h(px(TOOLTIP_ARROW * 2.0));
    deferred(
        h_flex()
            .absolute()
            .top_0()
            .bottom_0()
            // The button's right edge, plus a gap, plus the arrow.
            .left(px((RAIL_WIDTH + RAIL_ITEM) / 2.0 + 6.0 + TOOLTIP_ARROW))
            .items_center()
            .invisible()
            .group_hover(group, |s| s.visible())
            .child(
                div()
                    .relative()
                    .flex_none()
                    .px(px(12.0))
                    .py(px(7.0))
                    .rounded(px(8.0))
                    .bg(bg)
                    .border_1()
                    .border_color(border)
                    .shadow_lg()
                    .whitespace_nowrap()
                    .text_size(px(14.0))
                    .font_weight(FontWeight::SEMIBOLD)
                    .text_color(c("f_text"))
                    .child(name)
                    .child(
                        h_flex()
                            .absolute()
                            .top_0()
                            .bottom_0()
                            .left(px(-TOOLTIP_ARROW))
                            .items_center()
                            .child(arrow),
                    ),
            ),
    )
    .with_priority(2)
}

impl FennecWindow {
    /// The queue to come back to from settings or a terminal: the one shown,
    /// else the last one shown, else the first.
    pub(super) fn home_route(&self) -> Option<Route> {
        let id = self
            .project_id()
            .or_else(|| self.local_state.last_queue.clone())
            .filter(|id| self.projects.iter().any(|p| &p.id == id))
            .or_else(|| self.visible_projects().first().map(|p| p.id.clone()))?;
        Some(Route::Project(id))
    }

    pub(super) fn go_home(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        match self.home_route() {
            Some(route) => self.go(route, window, cx),
            None => {
                self.route = None;
                cx.notify();
            }
        }
    }

    /// ⌘N: the New request box, from anywhere (FEN-106). An open thread
    /// stays open beside it.
    pub(super) fn focus_new_request(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if !matches!(self.route, Some(Route::Project(_))) {
            self.go_home(window, cx);
        }
        self.menu = None;
        self.queue_scroll.set_offset(point(px(0.0), px(0.0)));
        self.composer.read(cx).focus_handle(cx).focus(window, cx);
        cx.notify();
    }

    /// ⌘K: search the queue's requests.
    pub(super) fn focus_search(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if !matches!(self.route, Some(Route::Project(_))) {
            self.go_home(window, cx);
        }
        self.menu = None;
        self.search.read(cx).focus_handle(cx).focus(window, cx);
        cx.notify();
    }

    /// The rail's terminal button: back to the terminal shown last, or a
    /// new local shell. More shells open as tabs there (⌘T or +).
    pub(super) fn open_local_terminal(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let alive = |this: &Self, a: &String| {
            this.layout.members.contains_key(a) || this.sessions.iter().any(|s| &s.address == a)
        };
        match self.last_terminal.clone().filter(|a| alive(self, a)) {
            Some(address) => self.go(Route::Session(address), window, cx),
            None => self.start_session(String::new(), window, cx),
        }
    }

    /// The title bar: it moves the window like a native one, holds the
    /// traffic lights at its left and search (⌘K) in its middle. It has no
    /// background or line of its own: it is the chrome the rail is on too.
    pub(super) fn titlebar(&self) -> impl IntoElement {
        h_flex()
            .id("titlebar")
            .debug_selector(|| "titlebar".into())
            .w_full()
            .h(px(TITLEBAR_HEIGHT))
            .flex_none()
            .px(px(RAIL_WIDTH + 12.0))
            .on_mouse_down(MouseButton::Left, ui::drag_titlebar)
            .child(div().flex_1())
            .child(self.search_field())
            .child(div().flex_1())
    }

    /// The title bar's search field (⌘K).
    fn search_field(&self) -> Stateful<Div> {
        h_flex()
            .id("titlebar-search")
            .debug_selector(|| "titlebar-search".into())
            .w(px(360.0))
            .flex_shrink(1.0)
            .min_w(px(160.0))
            .h(px(28.0))
            .px(px(8.0))
            .gap(px(7.0))
            .rounded(px(7.0))
            .bg(c("f_line"))
            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
            .child(ui::icon("search", 13.0, c("f_muted")))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .text_size(px(12.5))
                    .child(Input::new(&self.search)),
            )
            .child(div().text_size(px(11.0)).text_color(c("f_muted")).child("⌘K"))
    }

    /// One rail button, with its name on hover. `content` draws it at a
    /// scale: it grows while hovered (FEN-125), in place, so nothing moves.
    fn rail_item(
        &self,
        id: SharedString,
        name: SharedString,
        content: impl FnOnce(f32, bool) -> AnyElement,
        now: Instant,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Stateful<Div> {
        let group = SharedString::from(format!("{id}-group"));
        let (scale, moving) = self.rail_hover.scale(&id, now);
        if moving {
            window.request_animation_frame();
        }
        let hovered = self.rail_hover.is_hovered(&id);
        h_flex()
            .id(id.clone())
            .debug_selector({
                let id = id.clone();
                move || id.to_string()
            })
            .group(group.clone())
            .relative()
            .w_full()
            .h(px(RAIL_ITEM))
            .flex_none()
            .justify_center()
            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
            .on_hover(cx.listener({
                let id = id.clone();
                move |this, hovered: &bool, _, cx| {
                    this.rail_hover.set(id.clone(), *hovered);
                    cx.notify();
                }
            }))
            .child(
                div()
                    .size(px(RAIL_ITEM))
                    .flex()
                    .items_center()
                    .justify_center()
                    .child(content(scale, hovered)),
            )
            .child(rail_tooltip(name, group))
    }

    /// A glyph for the rail's own buttons.
    fn rail_glyph(
        icon: &'static str,
        selected: bool,
        filled: bool,
    ) -> impl FnOnce(f32, bool) -> AnyElement {
        Self::rail_tile(
            move |size, color| ui::icon(icon, size, color).into_any_element(),
            selected,
            filled,
        )
    }

    /// The terminal button wears the icon of the tab its terminal shows
    /// (Node, Claude, a shell…), so it matches the tab strip (FEN-127).
    /// Before any terminal opened it is the plain terminal glyph.
    ///
    /// A shell (or a program not known yet) is drawn exactly like that plain
    /// glyph, and the terminal counts as alive while its view is open, so a
    /// core resync that briefly empties the session list or the activity
    /// result doesn't flash the button to a different icon (FEN-161).
    fn rail_terminal_icon(&self, selected: bool) -> Box<dyn FnOnce(f32, bool) -> AnyElement + '_> {
        let alive = |a: &String| {
            self.layout.members.contains_key(a)
                || self.sessions.iter().any(|s| &s.address == a)
                || self.terms.contains_key(&self.shown_tab(a))
        };
        let program = self.last_terminal.as_ref().filter(|a| alive(a)).map(|root| {
            let tab = self.shown_tab(root);
            tab.strip_prefix("local:").unwrap_or(&tab).to_string()
        });
        match program.filter(|p| !self.program(p)["shell"].as_bool().unwrap_or(true)) {
            Some(program) => Box::new(Self::rail_tile(
                move |size, _| self.program_icon(&program, "shell", size),
                selected,
                false,
            )),
            None => Box::new(Self::rail_glyph("term", selected, false)),
        }
    }

    /// A rail button's tile, as in Slack: no square at rest, a faint one on
    /// hover, a lighter one when selected. `filled` keeps the faint square at
    /// rest (New queue). `icon` draws the glyph at a size and color.
    fn rail_tile(
        icon: impl FnOnce(f32, Hsla) -> AnyElement,
        selected: bool,
        filled: bool,
    ) -> impl FnOnce(f32, bool) -> AnyElement {
        move |scale, hovered| {
            div()
                .size(px(RAIL_ICON))
                .rounded(px(10.0))
                .flex()
                .items_center()
                .justify_center()
                .when(selected, |d| d.bg(c("f_field")))
                .when(!selected && (hovered || filled), |d| d.bg(c("f_line")))
                .when(filled && !hovered, |d| d.opacity(DIMMED))
                .child(icon(
                    18.0 * scale,
                    if selected || hovered { c("f_text") } else { c("f_muted") },
                ))
                .into_any_element()
        }
    }

    pub(super) fn rail(&self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let route = self.route.clone();
        let now = Instant::now();
        let queues: Vec<AnyElement> = self
            .visible_projects()
            .into_iter()
            .map(|p| {
                let selected = matches!(&route, Some(Route::Project(id) | Route::Settings(id, _)) if id == &p.id);
                let id = p.id.clone();
                let needs_you = p.needs_you;
                let icon = move |scale: f32, hovered: bool| {
                    let size = RAIL_ICON * scale;
                    let radius = px(if size >= 40.0 { 10.0 } else { 8.0 });
                    div()
                        .relative()
                        .size(px(RAIL_ITEM))
                        .flex()
                        .items_center()
                        .justify_center()
                        .child(
                            div().relative().size(px(size)).child(
                            div()
                                .relative()
                                .size_full()
                                .rounded(radius)
                                // FEN-125: the other queues are dimmed, the
                                // shown one stays in full color, lifted by a
                                // soft shadow and a faint edge. No ring.
                                .when(!selected && !hovered, |d| d.opacity(DIMMED))
                                .when(selected, |d| d.shadow_md())
                                .child(ui::project_icon(p, size))
                                .when(selected, |d| {
                                    d.child(
                                        div()
                                            .absolute()
                                            .inset_0()
                                            .rounded(radius)
                                            .border_1()
                                            .border_color(c("f_field")),
                                    )
                                }),
                            )
                            // Needs you: a dot at the top right, as in Slack,
                            // outside the dimmed icon so it stays bright.
                            .when(needs_you > 0, |d| {
                                    d.child(
                                        div()
                                            .absolute()
                                            .top(px(-4.0))
                                            .right(px(-4.0))
                                            .size(px(12.0))
                                            .rounded_full()
                                            .bg(c("orange"))
                                            .border_2()
                                            .border_color(c("f_bg")),
                                    )
                                }),
                        )
                        .into_any_element()
                };
                self.context_row(
                    self.rail_item(
                        SharedString::from(format!("rail-queue-{}", p.id)),
                        p.display_name().into(),
                        icon,
                        now,
                        window,
                        cx,
                    ),
                    context_menu::Target::Project(p.id.clone()),
                    cx,
                )
                .on_click(cx.listener(move |this, event: &ClickEvent, window, cx| {
                    if !event.is_right_click() && !event.modifiers().control {
                        this.go(Route::Project(id.clone()), window, cx);
                    }
                }))
                .into_any_element()
            })
            .collect();
        let terminal_shown = matches!(route, Some(Route::Session(_)));
        let settings_shown = route == Some(Route::AppSettings);
        // FEN-159: a hairline, one device pixel tall (0.5 pt on Retina). The
        // margin grows by what the line lost, so the rail keeps its spacing.
        let hairline = px(1.0 / window.scale_factor());
        let separator = move || div().w(px(32.0)).h(hairline).my((px(6.0) - hairline) / 2.0).bg(c("f_line")).flex_none();
        v_flex()
            .id("rail")
            .debug_selector(|| "rail".into())
            .w(px(RAIL_WIDTH))
            .h_full()
            .flex_none()
            .items_center()
            .pt(px(4.0))
            .child(
                self.rail_item(
                    "rail-terminal".into(),
                    t("terminal").into(),
                    self.rail_terminal_icon(terminal_shown),
                    now,
                    window,
                    cx,
                )
                .on_click(cx.listener(|this, _, window, cx| this.open_local_terminal(window, cx))),
            )
            .child(separator())
            .child(
                v_flex()
                    .id("rail-queues")
                    .w_full()
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .items_center()
                    .gap(px(8.0))
                    .py(px(4.0))
                    .children(queues)
                    .child(
                        self.rail_item(
                            "rail-new-queue".into(),
                            t("new_project").into(),
                            Self::rail_glyph("plus", false, true),
                            now,
                            window,
                            cx,
                        )
                        .on_click(cx.listener(|this, _, window, cx| this.open_new_project(window, cx))),
                    ),
            )
            .child(
                div().w_full().pt(px(6.0)).pb(px(12.0)).flex_none().child(
                    self.rail_item(
                        "rail-settings".into(),
                        t("settings").into(),
                        Self::rail_glyph("gear", settings_shown, false),
                        now,
                        window,
                        cx,
                    )
                    .on_click(cx.listener(|this, _, window, cx| this.open_app_settings(window, cx))),
                ),
            )
    }

    /// No queue to show yet: a way to make the first one.
    pub(super) fn no_queue(&self, _window: &Window, cx: &mut Context<Self>) -> AnyElement {
        let loading = self.board_scope.is_none() || !self.projects.is_empty();
        v_flex()
            .id("no-queue")
            .flex_1()
            .min_w_0()
            .h_full()
            .bg(c("background"))
            .items_center()
            .justify_center()
            .gap(px(12.0))
            .on_mouse_down(MouseButton::Left, ui::drag_titlebar)
            .when(loading, |d| {
                d.child(div().text_size(px(12.5)).text_color(c("f_muted")).child(t("loading")))
            })
            .when(!loading, |d| {
                d.child(primary_button("first-queue", t("new_project")).on_click(
                    cx.listener(|this, _, window, cx| this.open_new_project(window, cx)),
                ))
            })
            .into_any_element()
    }
}
