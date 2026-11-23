//! Sidebar boundary interaction, using the same GPUI resize band as Herdr.
use super::*;
use gpui_base::{HandleEdge, resize_handle};
use serde_json::json;

#[derive(Default)]
pub(crate) struct State {
    pub drag: Option<(f32, u16)>,
    pub pending: Option<u16>,
    pub saving: bool,
    focus: Option<FocusHandle>,
}
#[derive(Clone)]
pub(super) struct SidebarDrag;
impl Render for SidebarDrag {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        div().size(px(0.0))
    }
}
impl FennecWindow {
    pub(super) fn sidebar_width(&self, window: &Window) -> f32 {
        (self.app_preferences.value.sidebar_width as f32).clamp(
            180.0,
            (f32::from(window.viewport_size().width) - 400.0).clamp(180.0, 480.0),
        )
    }
    pub(super) fn sidebar_resize_handle(&self, cx: &Context<Self>) -> impl IntoElement {
        resize_handle("sidebar-resize", Axis::Horizontal)
            .inside(HandleEdge::Trailing)
            .debug_selector(|| "sidebar-resize".into())
            .on_drag(SidebarDrag, |drag, _, _, cx| cx.new(|_| drag.clone()))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, event: &MouseDownEvent, window, cx| {
                    cx.stop_propagation();
                    this.sidebar_resize.focus = window.focused(cx);
                    this.focus.focus(window, cx);
                    if event.click_count == 2 {
                        this.sidebar_resize.drag = None;
                        this.app_preferences.value.sidebar_width = 252;
                        this.save_sidebar_width(window, cx);
                        this.restore_sidebar_focus(window, cx);
                    } else {
                        this.sidebar_resize.drag = Some((
                            f32::from(event.position.x),
                            this.sidebar_width(window).round() as u16,
                        ));
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
        let Some((start, width)) = self.sidebar_resize.drag else {
            return;
        };
        let max = (f32::from(window.viewport_size().width) - 400.0).clamp(180.0, 480.0);
        let width = (width as f32 + f32::from(event.event.position.x) - start)
            .clamp(180.0, max)
            .round() as u16;
        if width != self.app_preferences.value.sidebar_width {
            self.app_preferences.value.sidebar_width = width;
            cx.notify();
        }
    }
    pub(super) fn finish_sidebar_resize(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if let Some((_, before)) = self.sidebar_resize.drag.take() {
            if before != self.app_preferences.value.sidebar_width {
                self.save_sidebar_width(window, cx);
            }
        }
        self.restore_sidebar_focus(window, cx);
    }
    pub(super) fn restore_sidebar_focus(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if let Some(focus) = self.sidebar_resize.focus.take() {
            focus.focus(window, cx);
        }
    }
    fn save_sidebar_width(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.sidebar_resize.pending = Some(self.app_preferences.value.sidebar_width);
        // The isolated renderer never writes the user's preferences.
        if crate::preview::active() || cfg!(test) || self.sidebar_resize.saving {
            return;
        }
        self.sidebar_resize.saving = true;
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            loop {
                let Ok(width) = this.update(cx, |this, _| this.sidebar_resize.pending.take())
                else {
                    return;
                };
                let Some(width) = width else {
                    let _ = this.update(cx, |this, _| this.sidebar_resize.saving = false);
                    return;
                };
                let saved = cx
                    .background_spawn(async move {
                        crate::control::call("desktop.settings.set", json!({"sidebar_width":width}))
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
