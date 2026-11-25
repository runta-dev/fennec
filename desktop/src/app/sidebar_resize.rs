//! Column boundary interaction for the sidebar and the queue's request list,
//! using the same GPUI resize band as Herdr. Both widths persist through the
//! daemon's `desktop.settings` (THI-153).
use super::*;
use gpui_base::{HandleEdge, resize_handle};
use serde_json::json;

pub(crate) const SIDEBAR_DEFAULT: u16 = 252;
pub(crate) const SIDEBAR_MIN: f32 = 180.0;
pub(crate) const SIDEBAR_MAX: f32 = 480.0;
/// The request list keeps at least this much, and the thread beside it
/// keeps at least `THREAD_MIN`.
pub(crate) const QUEUE_MIN: f32 = 240.0;
pub(crate) const QUEUE_MAX: f32 = 720.0;
const THREAD_MIN: f32 = 360.0;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(crate) enum Pane {
    Sidebar,
    Queue,
}

#[derive(Default)]
pub(crate) struct State {
    pub drag: Option<(f32, u16)>,
    pub pending: Option<u16>,
    pub saving: bool,
    focus: Option<FocusHandle>,
}
#[derive(Clone)]
pub(super) struct SidebarDrag(Pane);
impl Render for SidebarDrag {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        div().size(px(0.0))
    }
}
impl FennecWindow {
    pub(super) fn sidebar_width(&self, window: &Window) -> f32 {
        (self.app_preferences.value.sidebar_width as f32).clamp(
            SIDEBAR_MIN,
            (f32::from(window.viewport_size().width) - 400.0).clamp(SIDEBAR_MIN, SIDEBAR_MAX),
        )
    }
    /// Width left for the request list and the thread.
    fn main_width(&self, window: &Window) -> f32 {
        f32::from(window.viewport_size().width) - self.sidebar_width(window) * self.sidebar_shown
    }
    fn queue_max(&self, window: &Window) -> f32 {
        (self.main_width(window) - THREAD_MIN).clamp(QUEUE_MIN, QUEUE_MAX)
    }
    /// 0 is the automatic width: 42% of the main area, at most 460 px.
    pub(super) fn queue_width(&self, window: &Window) -> f32 {
        match self.app_preferences.value.queue_width {
            0 => (self.main_width(window) * 0.42).min(460.0),
            w => (w as f32).clamp(QUEUE_MIN, self.queue_max(window)),
        }
    }
    fn pane_state(&mut self, pane: Pane) -> &mut State {
        match pane {
            Pane::Sidebar => &mut self.sidebar_resize,
            Pane::Queue => &mut self.queue_resize,
        }
    }
    fn pane_value(&mut self, pane: Pane) -> &mut u16 {
        match pane {
            Pane::Sidebar => &mut self.app_preferences.value.sidebar_width,
            Pane::Queue => &mut self.app_preferences.value.queue_width,
        }
    }
    fn pane_width(&self, pane: Pane, window: &Window) -> f32 {
        match pane {
            Pane::Sidebar => self.sidebar_width(window),
            Pane::Queue => self.queue_width(window),
        }
    }
    /// True while either column is being dragged or its width is being saved,
    /// so a settings reply doesn't overwrite the width on screen.
    pub(crate) fn column_resize_busy(&self) -> bool {
        [&self.sidebar_resize, &self.queue_resize]
            .iter()
            .any(|s| s.drag.is_some() || s.saving || s.pending.is_some())
    }
    pub(super) fn sidebar_resize_handle(&self, cx: &Context<Self>) -> impl IntoElement {
        self.column_resize_handle(Pane::Sidebar, cx)
    }
    pub(super) fn column_resize_handle(&self, pane: Pane, cx: &Context<Self>) -> impl IntoElement {
        let id = match pane {
            Pane::Sidebar => "sidebar-resize",
            Pane::Queue => "queue-resize",
        };
        resize_handle(id, Axis::Horizontal)
            .inside(HandleEdge::Trailing)
            // The column already paints its themed hairline. Reuse only the
            // handle's hit area and cursor, without a second default-theme line.
            .with_appearance(std::rc::Rc::new(|_, _, _| {
                Some(div().size_full().into_any_element())
            }))
            .debug_selector(move || id.into())
            .on_drag(SidebarDrag(pane), |drag, _, _, cx| cx.new(|_| drag.clone()))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(move |this, event: &MouseDownEvent, window, cx| {
                    cx.stop_propagation();
                    this.pane_state(pane).focus = window.focused(cx);
                    this.focus.focus(window, cx);
                    if event.click_count == 2 {
                        this.pane_state(pane).drag = None;
                        *this.pane_value(pane) = match pane {
                            Pane::Sidebar => SIDEBAR_DEFAULT,
                            Pane::Queue => 0,
                        };
                        this.save_column_width(pane, window, cx);
                        this.restore_column_focus(pane, window, cx);
                    } else {
                        let width = this.pane_width(pane, window).round() as u16;
                        if pane == Pane::Queue {
                            this.queue_resize_from_auto = this.app_preferences.value.queue_width == 0;
                        }
                        this.pane_state(pane).drag = Some((f32::from(event.position.x), width));
                    }
                    cx.notify();
                }),
            )
    }
    pub(super) fn drag_sidebar(
        &mut self,
        event: &DragMoveEvent<SidebarDrag>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let pane = event.drag(cx).0;
        let Some((start, width)) = self.pane_state(pane).drag else {
            return;
        };
        let (min, max) = match pane {
            Pane::Sidebar => (
                SIDEBAR_MIN,
                (f32::from(window.viewport_size().width) - 400.0).clamp(SIDEBAR_MIN, SIDEBAR_MAX),
            ),
            Pane::Queue => (QUEUE_MIN, self.queue_max(window)),
        };
        let width = (width as f32 + f32::from(event.event.position.x) - start)
            .clamp(min, max)
            .round() as u16;
        if width != *self.pane_value(pane) {
            *self.pane_value(pane) = width;
            cx.notify();
        }
    }
    pub(super) fn finish_sidebar_resize(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        for pane in [Pane::Sidebar, Pane::Queue] {
            if let Some((_, before)) = self.pane_state(pane).drag.take() {
                // A drag from the automatic queue width counts as a change
                // once it moved, so the width stops following the window.
                let before = if pane == Pane::Queue && self.queue_resize_from_auto { 0 } else { before };
                if before != *self.pane_value(pane) {
                    self.save_column_width(pane, window, cx);
                }
            }
            self.restore_column_focus(pane, window, cx);
        }
        self.queue_resize_from_auto = false;
    }
    /// Escape during a drag: put the width back and don't save.
    pub(super) fn cancel_column_resize(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        for pane in [Pane::Sidebar, Pane::Queue] {
            if let Some((_, width)) = self.pane_state(pane).drag.take() {
                let restored = match pane {
                    Pane::Queue if self.queue_resize_from_auto => 0,
                    _ => width,
                };
                *self.pane_value(pane) = restored;
                self.restore_column_focus(pane, window, cx);
            }
        }
        self.queue_resize_from_auto = false;
    }
    fn restore_column_focus(&mut self, pane: Pane, window: &mut Window, cx: &mut Context<Self>) {
        if let Some(focus) = self.pane_state(pane).focus.take() {
            focus.focus(window, cx);
        }
    }
    fn save_column_width(&mut self, pane: Pane, window: &mut Window, cx: &mut Context<Self>) {
        let width = *self.pane_value(pane);
        self.pane_state(pane).pending = Some(width);
        // The isolated renderer never writes the user's preferences.
        if crate::preview::active() || cfg!(test) || self.pane_state(pane).saving {
            return;
        }
        self.pane_state(pane).saving = true;
        let key = match pane {
            Pane::Sidebar => "sidebar_width",
            Pane::Queue => "queue_width",
        };
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            loop {
                let Ok(width) = this.update(cx, |this, _| this.pane_state(pane).pending.take())
                else {
                    return;
                };
                let Some(width) = width else {
                    let _ = this.update(cx, |this, _| this.pane_state(pane).saving = false);
                    return;
                };
                let saved = cx
                    .background_spawn(async move {
                        crate::control::call("desktop.settings.set", json!({ (key): width }))
                    })
                    .await;
                if this
                    .update(cx, |this, cx| match saved {
                        Ok(value) => {
                            if let Some(revision) = value["revision"].as_u64() {
                                this.app_preferences.value.revision =
                                    this.app_preferences.value.revision.max(revision);
                            }
                        }
                        Err(_) => this.say(t("try_again"), cx),
                    })
                    .is_err()
                {
                    return;
                }
            }
        }));
    }
}
