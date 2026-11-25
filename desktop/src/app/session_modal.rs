//! THI-115: "Open session" and a click on a thread's agent peek show the
//! agent's terminal in a modal over the thread, so the user keeps their place
//! instead of switching the whole window to the session. Behind it, a blurred
//! copy of the window: gpui has no backdrop filter, so the last frame is read
//! back once (`render_to_image`), shrunk and box-blurred off the main thread.
use super::*;
use gpui::{ImageSource, ObjectFit, RenderImage};

pub(super) struct SessionModal {
    /// The terminal's key (its address, `cloud:tXXX` for cloud agents).
    pub address: String,
    pub backdrop: Option<Arc<RenderImage>>,
    focused: bool,
}

/// Each side of the window is shrunk this many times before blurring; the
/// GPU's filtering when it is drawn at full size smooths it further.
const SHRINK: u32 = 8;
const BLUR_RADIUS: i32 = 3;
const BLUR_PASSES: usize = 3;

impl FennecWindow {
    pub(super) fn open_session_modal(
        &mut self,
        address: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if self
            .session_modal
            .as_ref()
            .is_some_and(|m| m.address == address)
        {
            return;
        }
        self.close_session_modal(window, cx);
        self.menu = None;
        self.context_menu = None;
        self.cloud_attach_failed.remove(&address);
        // Read before the modal is drawn, so the copy is of the thread alone.
        let frame = window.render_to_image().ok();
        self.session_modal = Some(SessionModal {
            address: address.clone(),
            backdrop: None,
            focused: false,
        });
        if let Some(frame) = frame {
            self._tasks.push(cx.spawn(async move |this, cx| {
                let image = cx.background_spawn(async move { blurred(frame) }).await;
                let _ = this.update(cx, |this, cx| {
                    if let Some(m) = this.session_modal.as_mut().filter(|m| m.address == address)
                    {
                        m.backdrop = image;
                        cx.notify();
                    }
                });
            }));
        }
        cx.notify();
    }

    /// Close the modal. Its terminal stops streaming unless the window is
    /// showing that session (as when leaving a session's page, see `go`).
    pub(super) fn close_session_modal(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(modal) = self.session_modal.take() else {
            return;
        };
        let on_screen =
            matches!(&self.route, Some(Route::Session(a)) if self.shown_tab(a) == modal.address);
        if !on_screen {
            if let Some(view) = self.terms.remove(&modal.address) {
                view.read(cx).detach();
            }
        }
        if matches!(self.route, Some(Route::Project(_))) && self.open.is_some() {
            self.reply.read(cx).focus_handle(cx).focus(window, cx);
        } else {
            self.focus.focus(window, cx);
        }
        cx.notify();
    }

    /// The full session page, for when the modal is not enough.
    fn expand_session_modal(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(modal) = self.session_modal.take() else {
            return;
        };
        // The terminal stays attached: the session page shows the same view.
        self.go(Route::Session(modal.address), window, cx);
    }

    pub(super) fn session_modal_view(
        &mut self,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        let address = self.session_modal.as_ref()?.address.clone();
        let backdrop = self.session_modal.as_ref()?.backdrop.clone();
        let view = self.terminal(&address, window, cx);
        // The terminal schedules output paints only while marked in this frame.
        if let Some(view) = &view {
            view.read(cx).mark_shown();
            if let Some(m) = self.session_modal.as_mut().filter(|m| !m.focused) {
                view.read(cx).focus_target(cx).focus(window, cx);
                m.focused = true;
            }
        }
        let session = self.sessions.iter().find(|s| s.address == address).cloned();
        let cloud_failed = self.cloud_attach_failed.contains(&address);
        let connecting = view.is_none() && !cloud_failed;
        let title = session
            .as_ref()
            .and_then(|s| self.session_request(s))
            .map(|(_, _, title)| title);
        let agent = session.as_ref().map(|s| s.agent.clone()).unwrap_or_default();
        let activity = session
            .as_ref()
            .map(|s| s.activity.clone())
            .unwrap_or_default();
        let body = div()
            .flex_1()
            .min_h_0()
            .bg(c("background"))
            .when(connecting, |d| {
                d.flex().items_center().justify_center().child(
                    svg()
                        .path("programs/activity-spinner.svg")
                        .size(px(18.0))
                        .text_color(c("muted"))
                        .with_animation(
                            "session-modal-connecting",
                            Animation::new(Duration::from_millis(900)).repeat(),
                            |icon, delta| {
                                icon.with_transformation(Transformation::rotate(percentage(
                                    delta,
                                )))
                            },
                        ),
                )
            })
            .when(cloud_failed, |d| {
                let key = address.clone();
                d.flex().items_center().justify_center().child(
                    secondary_button("session-modal-retry", t("try_again"), false).on_click(
                        cx.listener(move |this, _, _, cx| {
                            this.cloud_attach_failed.remove(&key);
                            cx.notify();
                        }),
                    ),
                )
            })
            .when_some(view, |d, v| d.child(v));
        let header = h_flex()
            .h(px(44.0))
            .flex_none()
            .pl(px(14.0))
            .pr(px(8.0))
            .gap(px(8.0))
            .bg(c("f_bg"))
            .border_b_1()
            .border_color(c("f_line"))
            .when(!agent.is_empty(), |d| d.child(ui::agent_icon(&agent, 16.0)))
            .child(
                div()
                    .flex_none()
                    .text_size(px(13.0))
                    .font_weight(FontWeight::MEDIUM)
                    .text_color(c("f_text"))
                    .child(if agent.is_empty() {
                        address.clone()
                    } else {
                        sessions::agent_name(&agent).to_owned()
                    }),
            )
            .child(
                div()
                    .min_w_0()
                    .flex_1()
                    .truncate()
                    .text_size(px(12.0))
                    .text_color(c("f_muted"))
                    .child(if activity.is_empty() {
                        title.unwrap_or_default()
                    } else {
                        activity
                    }),
            )
            .child(
                secondary_button("session-modal-expand", t("open_in_tab"), false).on_click(
                    cx.listener(|this, _, window, cx| this.expand_session_modal(window, cx)),
                ),
            )
            .child(
                ui::icon_button("session-modal-close", "close").on_click(
                    cx.listener(|this, _, window, cx| this.close_session_modal(window, cx)),
                ),
            );
        let panel = v_flex()
            .id("session-modal")
            .w(relative(0.88))
            .max_w(px(1360.0))
            .h(relative(0.86))
            .rounded(px(12.0))
            .overflow_hidden()
            .border_1()
            .border_color(c("f_line"))
            .bg(c("background"))
            .shadow(vec![BoxShadow {
                color: hsla(0.0, 0.0, 0.0, 0.45),
                offset: point(px(0.0), px(18.0)),
                blur_radius: px(48.0),
                spread_radius: px(0.0),
                inset: false,
            }])
            // Clicks inside stay inside; only the backdrop closes the modal.
            .on_click(|_, _, cx| cx.stop_propagation())
            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
            .child(header)
            .child(body);
        Some(
            div()
                .id("session-modal-backdrop")
                .absolute()
                .inset_0()
                .occlude()
                .flex()
                .items_center()
                .justify_center()
                .when_some(backdrop, |d, image| {
                    d.child(
                        img(ImageSource::Render(image))
                            .absolute()
                            .inset_0()
                            .size_full()
                            .object_fit(ObjectFit::Fill),
                    )
                })
                // The tint alone stands in for the blur until it is ready
                // (and on platforms that cannot read the frame back).
                .child(
                    div()
                        .absolute()
                        .inset_0()
                        .bg(hsla(0.0, 0.0, 0.0, 0.38)),
                )
                .on_click(cx.listener(|this, _, window, cx| this.close_session_modal(window, cx)))
                .child(panel)
                .into_any_element(),
        )
    }
}

/// A small, blurred BGRA copy of an RGBA frame.
fn blurred(frame: image::RgbaImage) -> Option<Arc<RenderImage>> {
    let (w, h) = (frame.width() / SHRINK, frame.height() / SHRINK);
    if w == 0 || h == 0 {
        return None;
    }
    let (w, h) = (w as usize, h as usize);
    let src = frame.as_raw();
    let stride = frame.width() as usize * 4;
    let n = (SHRINK * SHRINK) as u32;
    // Average each SHRINK×SHRINK block, as BGRA.
    let mut px = vec![[0f32; 4]; w * h];
    for y in 0..h {
        for x in 0..w {
            let mut sum = [0u32; 4];
            for dy in 0..SHRINK as usize {
                let row = (y * SHRINK as usize + dy) * stride;
                for dx in 0..SHRINK as usize {
                    let i = row + (x * SHRINK as usize + dx) * 4;
                    sum[0] += src[i + 2] as u32;
                    sum[1] += src[i + 1] as u32;
                    sum[2] += src[i] as u32;
                    sum[3] += src[i + 3] as u32;
                }
            }
            px[y * w + x] = sum.map(|v| (v / n) as f32);
        }
    }
    for _ in 0..BLUR_PASSES {
        px = box_pass(&px, w, h, true);
        px = box_pass(&px, w, h, false);
    }
    let bytes: Vec<u8> = px
        .iter()
        .flat_map(|p| p.map(|v| v.round().clamp(0.0, 255.0) as u8))
        .collect();
    let buf = image::RgbaImage::from_raw(w as u32, h as u32, bytes)?;
    Some(Arc::new(RenderImage::new(smallvec::smallvec![
        image::Frame::new(buf)
    ])))
}

/// One box blur along rows (or columns), clamping at the edges.
fn box_pass(src: &[[f32; 4]], w: usize, h: usize, horizontal: bool) -> Vec<[f32; 4]> {
    let mut out = vec![[0f32; 4]; w * h];
    let (len, lines) = if horizontal { (w, h) } else { (h, w) };
    let at = |line: usize, i: usize| if horizontal { line * w + i } else { i * w + line };
    let span = (2 * BLUR_RADIUS + 1) as f32;
    for line in 0..lines {
        for i in 0..len {
            let mut sum = [0f32; 4];
            for k in -BLUR_RADIUS..=BLUR_RADIUS {
                let j = (i as i32 + k).clamp(0, len as i32 - 1) as usize;
                let p = src[at(line, j)];
                for c in 0..4 {
                    sum[c] += p[c];
                }
            }
            out[at(line, i)] = sum.map(|v| v / span);
        }
    }
    out
}

#[cfg(test)]
mod tests {
    // Not `super::*`: that brings gpui's `test` macro over the built-in one.
    use super::blurred;

    #[test]
    fn blur_shrinks_and_swaps_to_bgra() {
        let mut frame = image::RgbaImage::new(64, 32);
        for p in frame.pixels_mut() {
            *p = image::Rgba([200, 100, 50, 255]);
        }
        let image = blurred(frame).unwrap();
        let size = image.size(0);
        assert_eq!((size.width.0, size.height.0), (8, 4));
        let bytes = image.as_bytes(0).unwrap();
        assert_eq!(&bytes[..4], &[50, 100, 200, 255]);
    }

    #[test]
    fn blur_skips_tiny_frames() {
        assert!(blurred(image::RgbaImage::new(4, 4)).is_none());
    }
}
