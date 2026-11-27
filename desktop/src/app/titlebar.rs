//! FEN-106: the window has no sidebar. The title bar switches queues (the
//! queue name opens a menu) and holds search (⌘K); below it are the queue
//! column and the thread beside it, as before. ⌘N focuses the New request box
//! from anywhere, and a terminal is reached only from a thread (or the hidden ⌘T).
use super::*;

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

    /// ⌘K: search the queue's requests from the title bar.
    pub(super) fn focus_search(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if !matches!(self.route, Some(Route::Project(_))) {
            self.go_home(window, cx);
        }
        self.menu = None;
        self.search.read(cx).focus_handle(cx).focus(window, cx);
        cx.notify();
    }

    /// Where the title bar's content starts: clear of the traffic lights.
    fn titlebar_left(&self, _window: &Window) -> f32 {
        self.traffic_lights_inset()
    }

    /// The queue menu opens under the queue name.
    pub(super) fn queue_menu_anchor(&self, window: &Window) -> Point<Pixels> {
        point(px(self.titlebar_left(window) - 6.0), px(TITLEBAR_HEIGHT - 4.0))
    }

    /// The title bar: the queue switcher, the offline badge, search and the
    /// queue's settings. It moves the window like a native title bar.
    pub(super) fn titlebar(
        &self,
        p: Option<&Project>,
        window: &Window,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let menu_open = self.menu == Some(Menu::Queues);
        let switcher = h_flex()
            .id("queue-switcher")
            .debug_selector(|| "queue-switcher".into())
            .min_w_0()
            .h(px(30.0))
            .ml(px(-8.0))
            .px(px(8.0))
            .gap(px(8.0))
            .rounded(px(7.0))
            .when(menu_open, |d| d.bg(c("f_line")))
            .when(!menu_open, |d| d.hover(|s| s.bg(c("f_line").opacity(0.5))))
            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
            .on_click(cx.listener(|this, _, window, cx| {
                this.menu_anchor = this.queue_menu_anchor(window);
                this.menu = if this.menu == Some(Menu::Queues) {
                    None
                } else {
                    Some(Menu::Queues)
                };
                cx.notify();
            }))
            .when_some(p, |d, p| d.child(ui::project_icon(p, 20.0)))
            .child(
                div()
                    .min_w_0()
                    .truncate()
                    .text_size(px(14.0))
                    .font_weight(FontWeight::MEDIUM)
                    .text_color(c("f_text"))
                    .child(p.map(|p| p.display_name()).unwrap_or_else(|| t("projects"))),
            )
            .child(ui::icon("chevron", 12.0, c("f_muted")));
        let search = h_flex()
            .id("titlebar-search")
            .w(px(220.0))
            .flex_shrink(1.0)
            .min_w(px(120.0))
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
            .child(div().text_size(px(11.0)).text_color(c("f_muted")).child("⌘K"));
        let now = sessions::now_ms();
        h_flex()
            .id("titlebar")
            .h(px(TITLEBAR_HEIGHT))
            .flex_none()
            .w_full()
            .pl(px(self.titlebar_left(window)))
            .pr(px(10.0))
            .gap(px(10.0))
            .bg(c("background"))
            .border_b_1()
            .border_color(c("f_line"))
            .on_mouse_down(MouseButton::Left, ui::drag_titlebar)
            .child(switcher)
            .child(div().flex_1())
            .when_some(p.filter(|p| p.sync_status == "offline"), |d, p| {
                d.child(offline_badge(&p.synced_at, now))
            })
            .when_some(p, |d, p| {
                let id = p.id.clone();
                d.child(search).child(
                    ui::icon_button("project-settings", "gear").on_click(cx.listener(
                        move |this, _, window, cx| {
                            this.go(
                                Route::Settings(id.clone(), crate::project_settings::Tab::General),
                                window,
                                cx,
                            )
                        },
                    )),
                )
            })
            .into_any_element()
    }

    /// No queue to show yet: the title bar (its menu has New queue and
    /// Settings) over a way to make the first queue.
    pub(super) fn no_queue(&self, window: &Window, cx: &mut Context<Self>) -> AnyElement {
        let loading = self.board_scope.is_none() || !self.projects.is_empty();
        v_flex()
            .flex_1()
            .min_w_0()
            .h_full()
            .bg(c("background"))
            .child(self.titlebar(None, window, cx))
            .child(
                v_flex()
                    .flex_1()
                    .items_center()
                    .justify_center()
                    .gap(px(12.0))
                    .pb(px(TITLEBAR_HEIGHT))
                    .when(loading, |d| {
                        d.child(
                            div()
                                .text_size(px(12.5))
                                .text_color(c("f_muted"))
                                .child(t("loading")),
                        )
                    })
                    .when(!loading, |d| {
                        d.child(primary_button("first-queue", t("new_project")).on_click(
                            cx.listener(|this, _, window, cx| this.open_new_project(window, cx)),
                        ))
                    }),
            )
            .into_any_element()
    }
}
