//! FEN-106: a title bar across the window (the traffic lights at its left,
//! search in the middle) over a narrow rail on the left edge, as in Discord,
//! instead of the old sidebar. The rail holds the local terminal, the queues
//! (one icon each), New queue, and Settings at the bottom. The queue column
//! and the thread beside it fill the rest of the window.
use super::*;

/// The rail's width; it lines up with the traffic lights (x 16–68) above it.
pub(crate) const RAIL_WIDTH: f32 = 72.0;
/// The title bar's height; the traffic lights (y 17–29) sit in its middle.
pub(crate) const TITLEBAR_HEIGHT: f32 = 46.0;
/// A rail button's square.
const RAIL_ITEM: f32 = 44.0;

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
    /// traffic lights at its left and search (⌘K) in its middle.
    pub(super) fn titlebar(&self) -> impl IntoElement {
        h_flex()
            .id("titlebar")
            .debug_selector(|| "titlebar".into())
            .w_full()
            .h(px(TITLEBAR_HEIGHT))
            .flex_none()
            .px(px(RAIL_WIDTH + 12.0))
            .bg(c("f_bg"))
            .border_b_1()
            .border_color(c("f_line"))
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

    /// One rail button: a rounded square with a pill at the rail's left edge
    /// for the shown item, and its name on hover.
    fn rail_item(
        &self,
        id: SharedString,
        name: SharedString,
        selected: bool,
        content: AnyElement,
    ) -> Stateful<Div> {
        let group = SharedString::from(format!("{id}-group"));
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
            .child(
                div()
                    .absolute()
                    .left_0()
                    .top(px(if selected { 4.0 } else { 14.0 }))
                    .w(px(4.0))
                    .h(px(if selected { 36.0 } else { 16.0 }))
                    .rounded_r(px(3.0))
                    .bg(c("f_text"))
                    .when(!selected, |d| {
                        d.opacity(0.0).group_hover(group.clone(), |s| s.opacity(1.0))
                    }),
            )
            .child(
                div()
                    .size(px(RAIL_ITEM))
                    .flex()
                    .items_center()
                    .justify_center()
                    .child(content),
            )
            .child(rail_tooltip(name, group))
    }

    /// A glyph in a rounded square, for the rail's own buttons.
    fn rail_glyph(icon: &'static str, selected: bool) -> AnyElement {
        let color = if selected { c("f_text") } else { c("f_muted") };
        Self::rail_tile(ui::icon(icon, 19.0, color).into_any_element(), selected)
    }

    /// The terminal button wears the icon of the tab its terminal shows
    /// (Node, Claude, a shell…), so it matches the tab strip (FEN-127).
    /// Before any terminal opened it is the plain terminal glyph.
    fn rail_terminal_icon(&self, selected: bool) -> AnyElement {
        let alive = |a: &String| {
            self.layout.members.contains_key(a) || self.sessions.iter().any(|s| &s.address == a)
        };
        match self.last_terminal.as_ref().filter(|a| alive(a)) {
            Some(root) => {
                let tab = self.shown_tab(root);
                let icon = self.program_icon(tab.strip_prefix("local:").unwrap_or(&tab), "shell", 19.0);
                Self::rail_tile(icon, selected)
            }
            None => Self::rail_glyph("term", selected),
        }
    }

    fn rail_tile(icon: AnyElement, selected: bool) -> AnyElement {
        div()
            .size(px(RAIL_ITEM))
            .rounded(px(12.0))
            .flex()
            .items_center()
            .justify_center()
            .bg(if selected { c("f_card") } else { c("f_line") })
            .border_1()
            .border_color(if selected { c("f_field") } else { transparent_black() })
            .when(selected, |d| d.shadow(ui::selection_shadow()))
            .child(icon)
            .into_any_element()
    }

    pub(super) fn rail(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let route = self.route.clone();
        let queues: Vec<AnyElement> = self
            .visible_projects()
            .into_iter()
            .map(|p| {
                let selected = matches!(&route, Some(Route::Project(id) | Route::Settings(id, _)) if id == &p.id);
                let id = p.id.clone();
                let icon = div()
                    .relative()
                    .size(px(RAIL_ITEM))
                    .child(ui::project_icon(p, RAIL_ITEM))
                    .when(p.needs_you > 0, |d| {
                        d.child(
                            div()
                                .absolute()
                                .right(px(-4.0))
                                .bottom(px(-4.0))
                                .min_w(px(18.0))
                                .h(px(18.0))
                                .px(px(5.0))
                                .rounded(px(9.0))
                                .bg(c("orange"))
                                .border_2()
                                .border_color(c("f_bg"))
                                .text_color(white())
                                .text_size(px(10.5))
                                .font_weight(FontWeight::BOLD)
                                .flex()
                                .items_center()
                                .justify_center()
                                .child(p.needs_you.to_string()),
                        )
                    })
                    .into_any_element();
                self.context_row(
                    self.rail_item(
                        SharedString::from(format!("rail-queue-{}", p.id)),
                        p.display_name().into(),
                        selected,
                        icon,
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
        let separator = || div().w(px(32.0)).h(px(2.0)).my(px(2.0)).rounded(px(1.0)).bg(c("f_line")).flex_none();
        v_flex()
            .id("rail")
            .debug_selector(|| "rail".into())
            .w(px(RAIL_WIDTH))
            .h_full()
            .flex_none()
            .items_center()
            .bg(c("f_bg"))
            .border_r_1()
            .border_color(c("f_line"))
            .pt(px(10.0))
            .child(
                self.rail_item(
                    "rail-terminal".into(),
                    t("terminal").into(),
                    terminal_shown,
                    self.rail_terminal_icon(terminal_shown),
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
                            false,
                            Self::rail_glyph("plus", false),
                        )
                        .on_click(cx.listener(|this, _, window, cx| this.open_new_project(window, cx))),
                    ),
            )
            .child(
                div().w_full().pt(px(6.0)).pb(px(12.0)).flex_none().child(
                    self.rail_item(
                        "rail-settings".into(),
                        t("settings").into(),
                        settings_shown,
                        Self::rail_glyph("gear", settings_shown),
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
