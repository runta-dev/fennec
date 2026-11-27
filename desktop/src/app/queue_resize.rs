//! Resizing the queue's request list against the thread beside it, using the
//! same GPUI resize band as Herdr. The width persists through the daemon's
//! `desktop.settings` as `queue_width` (THI-153). FEN-106 removed the sidebar,
//! whose width used to be resized here too.
use super::*;
use gpui_base::{HandleEdge, resize_handle};
use serde_json::json;

/// The request list keeps at least this much, and the thread beside it
/// keeps at least `THREAD_MIN`.
pub(crate) const QUEUE_MIN: f32 = 240.0;
pub(crate) const QUEUE_MAX: f32 = 720.0;
const THREAD_MIN: f32 = 360.0;

#[derive(Default)]
pub(crate) struct State {
    pub drag: Option<(f32, u16)>,
    pub pending: Option<u16>,
    pub saving: bool,
    focus: Option<FocusHandle>,
}
#[derive(Clone)]
pub(super) struct QueueDrag;
impl Render for QueueDrag {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        div().size(px(0.0))
    }
}
impl FennecWindow {
    fn queue_max(&self, window: &Window) -> f32 {
        (f32::from(window.viewport_size().width) - THREAD_MIN).clamp(QUEUE_MIN, QUEUE_MAX)
    }
    /// 0 is the automatic width: 42% of the window, at most 460 px.
    pub(super) fn queue_width(&self, window: &Window) -> f32 {
        match self.app_preferences.value.queue_width {
            0 => (f32::from(window.viewport_size().width) * 0.42).min(460.0),
            w => (w as f32).clamp(QUEUE_MIN, self.queue_max(window)),
        }
    }
    /// True while the column is being dragged or its width is being saved,
    /// so a settings reply doesn't overwrite the width on screen.
    pub(crate) fn column_resize_busy(&self) -> bool {
        let s = &self.queue_resize;
        s.drag.is_some() || s.saving || s.pending.is_some()
    }
    pub(super) fn queue_resize_handle(&self, cx: &Context<Self>) -> impl IntoElement {
        resize_handle("queue-resize", Axis::Horizontal)
            .inside(HandleEdge::Trailing)
            // The column already paints its themed hairline. Reuse only the
            // handle's hit area and cursor, without a second default-theme line.
            .with_appearance(std::rc::Rc::new(|_, _, _| {
                Some(div().size_full().into_any_element())
            }))
            .debug_selector(|| "queue-resize".into())
            .on_drag(QueueDrag, |drag, _, _, cx| cx.new(|_| drag.clone()))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(move |this, event: &MouseDownEvent, window, cx| {
                    cx.stop_propagation();
                    this.queue_resize.focus = window.focused(cx);
                    this.focus.focus(window, cx);
                    if event.click_count == 2 {
                        this.queue_resize.drag = None;
                        this.app_preferences.value.queue_width = 0;
                        this.save_queue_width(window, cx);
                        this.restore_column_focus(window, cx);
                    } else {
                        let width = this.queue_width(window).round() as u16;
                        this.queue_resize_from_auto = this.app_preferences.value.queue_width == 0;
                        this.queue_resize.drag = Some((f32::from(event.position.x), width));
                    }
                    cx.notify();
                }),
            )
    }
    pub(super) fn drag_queue(
        &mut self,
        event: &DragMoveEvent<QueueDrag>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some((start, width)) = self.queue_resize.drag else {
            return;
        };
        let width = (width as f32 + f32::from(event.event.position.x) - start)
            .clamp(QUEUE_MIN, self.queue_max(window))
            .round() as u16;
        if width != self.app_preferences.value.queue_width {
            self.app_preferences.value.queue_width = width;
            cx.notify();
        }
    }
    pub(super) fn finish_queue_resize(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if let Some((_, before)) = self.queue_resize.drag.take() {
            // A drag from the automatic width counts as a change once it
            // moved, so the width stops following the window.
            let before = if self.queue_resize_from_auto { 0 } else { before };
            if before != self.app_preferences.value.queue_width {
                self.save_queue_width(window, cx);
            }
        }
        self.restore_column_focus(window, cx);
        self.queue_resize_from_auto = false;
    }
    /// Escape during a drag: put the width back and don't save.
    pub(super) fn cancel_queue_resize(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if let Some((_, width)) = self.queue_resize.drag.take() {
            self.app_preferences.value.queue_width =
                if self.queue_resize_from_auto { 0 } else { width };
            self.restore_column_focus(window, cx);
        }
        self.queue_resize_from_auto = false;
    }
    fn restore_column_focus(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if let Some(focus) = self.queue_resize.focus.take() {
            focus.focus(window, cx);
        }
    }
    fn save_queue_width(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let width = self.app_preferences.value.queue_width;
        self.queue_resize.pending = Some(width);
        // The isolated renderer never writes the user's preferences.
        if crate::preview::active() || cfg!(test) || self.queue_resize.saving {
            return;
        }
        self.queue_resize.saving = true;
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            loop {
                let Ok(width) = this.update(cx, |this, _| this.queue_resize.pending.take()) else {
                    return;
                };
                let Some(width) = width else {
                    let _ = this.update(cx, |this, _| this.queue_resize.saving = false);
                    return;
                };
                let saved = cx
                    .background_spawn(async move {
                        crate::control::call("desktop.settings.set", json!({ "queue_width": width }))
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
