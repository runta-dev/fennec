//! Request composer clipboard UI. Persistence and validation live in the Go daemon.
use super::*;
use base64::Engine as _;
use std::collections::HashSet;
use std::io::{Cursor, Write};
use std::path::PathBuf;
use std::rc::Rc;

#[derive(Default)]
pub(super) struct Drafts {
    pub by_queue: HashMap<String, Vec<board::Attachment>>,
    pub busy: HashSet<String>,
    pub errors: HashMap<String, &'static str>,
}

/// The window's own full-size view of a request's images.
pub(super) struct Viewer {
    ids: Vec<String>,
    /// Pixel size of each image (0×0 when the attachment didn't record one).
    sizes: Vec<(u32, u32)>,
    index: usize,
    /// The shown image on disk, once the daemon has resolved it.
    path: Option<PathBuf>,
    failed: bool,
    /// Where the image stage was last laid out, to tell a click on the image
    /// from a click on the backdrop around it.
    stage: Rc<std::cell::Cell<Bounds<Pixels>>>,
}

/// Whether `click` lands on the picture itself, as `ObjectFit::Contain` draws an
/// image of `size` pixels inside `stage`. Clicks on the letterbox around it are
/// backdrop clicks and close the viewer. Without a recorded size the whole stage
/// counts as the picture.
fn hits_image(stage: Bounds<Pixels>, size: (u32, u32), click: Point<Pixels>) -> bool {
    if size.0 == 0 || size.1 == 0 {
        return stage.contains(&click);
    }
    let fitted = ObjectFit::Contain.get_bounds(
        stage,
        gpui::size(
            DevicePixels::from(size.0 as i32),
            DevicePixels::from(size.1 as i32),
        ),
    );
    fitted.contains(&click)
}

enum PasteSource {
    Image(Vec<u8>),
    Files(Vec<PathBuf>),
}

fn import_image(mut bytes: Vec<u8>) -> anyhow::Result<board::Attachment> {
    if bytes.len() > 64 << 20 {
        anyhow::bail!("clipboard image is too large");
    }
    let format = image::guess_format(&bytes)?;
    if matches!(format, image::ImageFormat::Tiff | image::ImageFormat::Bmp) {
        let (w, h) =
            image::ImageReader::with_format(Cursor::new(&bytes), format).into_dimensions()?;
        if u64::from(w) * u64::from(h) > 32_000_000 {
            anyhow::bail!("clipboard image dimensions are too large");
        }
        let decoded = image::load_from_memory_with_format(&bytes, format)?;
        let mut output = Cursor::new(Vec::new());
        decoded.write_to(&mut output, image::ImageFormat::Png)?;
        bytes = output.into_inner();
    }
    if bytes.len() > 8 << 20 {
        anyhow::bail!("image is too large");
    }
    let stamp = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)?
        .as_nanos();
    let path = std::env::temp_dir().join(format!(
        "fennec-request-{}-{stamp}.image",
        std::process::id()
    ));
    let mut options = std::fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt as _;
        options.mode(0o600);
    }
    let result = (|| {
        let mut file = options.open(&path)?;
        file.write_all(&bytes)?;
        drop(file);
        let value =
            crate::control::call("board.attachment.import", serde_json::json!({"path":path}))?;
        Ok(serde_json::from_value(value)?)
    })();
    let _ = std::fs::remove_file(&path);
    result
}

fn thumbnail(image: &board::Attachment, width: f32, height: f32) -> AnyElement {
    if let Some(data) = image
        .thumbnail
        .strip_prefix("data:image/jpeg;base64,")
        .and_then(|s| base64::engine::general_purpose::STANDARD.decode(s).ok())
    {
        return img(Arc::new(Image::from_bytes(ImageFormat::Jpeg, data)))
            .w(px(width))
            .h(px(height))
            .object_fit(ObjectFit::Cover)
            .rounded(px(10.0))
            .into_any_element();
    }
    div()
        .w(px(width))
        .h(px(height))
        .child(t("request_image"))
        .into_any_element()
}

impl FennecWindow {
    pub(super) fn request_image_status(&self, project: &str) -> Option<&'static str> {
        if self.request_images.busy.contains(project) {
            Some("request_image_adding")
        } else {
            self.request_images.errors.get(project).copied()
        }
    }

    pub(super) fn paste_request_image(
        &mut self,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> bool {
        if crate::preview::active() {
            return false;
        }
        let reply = self.reply.read(cx).focus_handle(cx).is_focused(window);
        if !reply && !self.composer.read(cx).focus_handle(cx).is_focused(window) {
            return false;
        }
        let Some(mut project) = self.project_id() else {
            return false;
        };
        if reply {
            let Some(number) = self.open else {
                return false;
            };
            project = format!("reply:{project}:{number}");
        }
        let Some(clipboard) = cx.read_from_clipboard() else {
            return false;
        };
        let mut source = None;
        for entry in clipboard.into_entries() {
            match entry {
                ClipboardEntry::Image(image) => {
                    source = Some(PasteSource::Image(image.bytes));
                    break;
                }
                ClipboardEntry::ExternalPaths(files)
                    if !files.paths().is_empty()
                        && files.paths().iter().all(|p| {
                            p.extension().and_then(|s| s.to_str()).is_some_and(|s| {
                                matches!(
                                    s.to_ascii_lowercase().as_str(),
                                    "png" | "jpg" | "jpeg" | "tif" | "tiff" | "bmp"
                                )
                            })
                        }) =>
                {
                    source = Some(PasteSource::Files(files.paths().to_vec()))
                }
                _ => {}
            }
        }
        let Some(source) = source else { return false };
        if self.request_images.busy.contains(&project) {
            return true;
        }
        let count = match &source {
            PasteSource::Image(_) => 1,
            PasteSource::Files(paths) => paths.len(),
        };
        if self
            .request_images
            .by_queue
            .get(&project)
            .map_or(0, Vec::len)
            + count
            > 8
        {
            self.request_images
                .errors
                .insert(project, "request_image_limit");
            cx.notify();
            return true;
        }
        self.request_images.errors.remove(&project);
        self.request_images.busy.insert(project.clone());
        cx.notify();
        self._tasks.push(cx.spawn(async move |this, cx| {
            let result: anyhow::Result<Vec<board::Attachment>> = cx
                .background_spawn(async move {
                    match source {
                        PasteSource::Image(bytes) => Ok(vec![import_image(bytes)?]),
                        PasteSource::Files(paths) => paths
                            .into_iter()
                            .map(|p| {
                                let file = std::fs::File::open(p)?;
                                let mut bytes = Vec::new();
                                use std::io::Read as _;
                                file.take((64 << 20) + 1).read_to_end(&mut bytes)?;
                                import_image(bytes)
                            })
                            .collect(),
                    }
                })
                .await;
            let _ = this.update(cx, |this, cx| {
                this.request_images.busy.remove(&project);
                match result {
                    Ok(images) => {
                        let draft = this.request_images.by_queue.entry(project).or_default();
                        for image in images {
                            if !draft.iter().any(|a| a.id == image.id) {
                                draft.push(image);
                            }
                        }
                    }
                    Err(_) => {
                        this.request_images
                            .errors
                            .insert(project, "request_image_error");
                    }
                }
                cx.notify();
            });
        }));
        true
    }

    pub(super) fn draft_images(&self, project: &str, cx: &Context<Self>) -> Option<AnyElement> {
        let images = self
            .request_images
            .by_queue
            .get(project)
            .cloned()
            .unwrap_or_default();
        if images.is_empty() {
            return None;
        }
        Some(
            v_flex()
                .w_full()
                .flex_none()
                .mt(px(8.0))
                .gap(px(6.0))
                .child(h_flex().gap(px(8.0)).flex_wrap().children(
                    images.iter().cloned().enumerate().map(|(index, image)| {
                        let project = project.to_owned();
                        let remove = image.id.clone();
                        let ids: Vec<(String, (u32, u32))> = images
                            .iter()
                            .map(|a| (a.id.clone(), (a.width, a.height)))
                            .collect();
                        let (width, height) = (80.0, 80.0);
                        div()
                            .id(SharedString::from(format!("draft-image-{}", image.id)))
                            .group("request-attachment")
                            .relative()
                            .w(px(width))
                            .h(px(height))
                            .flex_none()
                            .rounded(px(10.0))
                            .border_1()
                            .border_color(c("f_line"))
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.open_request_image(ids.clone(), index, window, cx)
                            }))
                            .child(
                                div()
                                    .size(px(width))
                                    .rounded(px(10.0))
                                    .overflow_hidden()
                                    .child(thumbnail(&image, width, height)),
                            )
                            .child(
                                div().absolute().top(px(-5.0)).right(px(-5.0)).child(
                                    div()
                                        .id(SharedString::from(format!(
                                            "remove-image-{}",
                                            image.id
                                        )))
                                        .flex()
                                        .items_center()
                                        .justify_center()
                                        .size(px(20.0))
                                        .invisible()
                                        .group_hover("request-attachment", |s| s.visible())
                                        .rounded_full()
                                        .bg(c("f_text"))
                                        .hover(|s| s.bg(c("f_muted")))
                                        .on_mouse_down(MouseButton::Left, |_, _, cx| {
                                            cx.stop_propagation()
                                        })
                                        .child(ui::icon("close", 10.0, c("f_card")))
                                        .on_click(cx.listener(move |this, _, _, cx| {
                                            cx.stop_propagation();
                                            if let Some(images) =
                                                this.request_images.by_queue.get_mut(&project)
                                            {
                                                images.retain(|a| a.id != remove);
                                            }
                                            this.request_images.errors.remove(&project);
                                            cx.notify();
                                        })),
                                ),
                            )
                    }),
                ))
                .into_any_element(),
        )
    }

    pub(super) fn stored_images(
        &self,
        images: &[board::Attachment],
        key: u64,
        cx: &Context<Self>,
    ) -> AnyElement {
        h_flex()
            .gap(px(8.0))
            .flex_wrap()
            .mt(px(6.0))
            .children(images.iter().enumerate().map(|(index, image)| {
                let id = image.id.clone();
                let ids: Vec<(String, (u32, u32))> = images
                    .iter()
                    .map(|a| (a.id.clone(), (a.width, a.height)))
                    .collect();
                let (width, height) = (96.0, 96.0);
                div()
                    .id(SharedString::from(format!("request-image-{key}-{id}")))
                    .w(px(width))
                    .h(px(height))
                    .flex_none()
                    .rounded(px(10.0))
                    .border_1()
                    .border_color(c("f_line"))
                    .overflow_hidden()
                    .child(thumbnail(image, width, height))
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.open_request_image(ids.clone(), index, window, cx)
                    }))
            }))
            .into_any_element()
    }

    /// Opens `ids[index]` in the window's own image viewer, never in another app.
    fn open_request_image(
        &mut self,
        images: Vec<(String, (u32, u32))>,
        index: usize,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if crate::preview::active() || index >= images.len() {
            return;
        }
        let (ids, sizes) = images.into_iter().unzip();
        self.image_viewer = Some(Viewer {
            ids,
            sizes,
            index,
            path: None,
            failed: false,
            stage: Rc::default(),
        });
        // Keys go to the window while the viewer is up, not to the reply field.
        self.focus.focus(window, cx);
        self.load_viewer_image(cx);
        cx.notify();
    }

    fn load_viewer_image(&mut self, cx: &mut Context<Self>) {
        let Some(viewer) = self.image_viewer.as_mut() else {
            return;
        };
        viewer.path = None;
        viewer.failed = false;
        let id = viewer.ids[viewer.index].clone();
        self._tasks.push(cx.spawn(async move |this, cx| {
            let request = id.clone();
            let result = cx
                .background_spawn(async move {
                    crate::control::call("board.attachment.url", serde_json::json!({"id":request}))
                })
                .await;
            let path = result
                .ok()
                .and_then(|value| value["url"].as_str().and_then(file_url_path));
            let _ = this.update(cx, |this, cx| {
                // A later image may have been chosen while this one resolved.
                let Some(viewer) = this.image_viewer.as_mut().filter(|v| v.ids[v.index] == id)
                else {
                    return;
                };
                viewer.failed = path.is_none();
                viewer.path = path;
                cx.notify();
            });
        }));
    }

    pub(super) fn close_image_viewer(&mut self, cx: &mut Context<Self>) {
        if self.image_viewer.take().is_some() {
            cx.notify();
        }
    }

    fn step_image_viewer(&mut self, delta: isize, cx: &mut Context<Self>) {
        let Some(viewer) = self.image_viewer.as_mut() else {
            return;
        };
        let count = viewer.ids.len() as isize;
        let next = viewer.index as isize + delta;
        if count < 2 || next < 0 || next >= count {
            return;
        }
        viewer.index = next as usize;
        self.load_viewer_image(cx);
        cx.notify();
    }

    /// Esc closes the viewer and ←/→ move between the request's images.
    /// Every other key is swallowed while it is open.
    pub(super) fn image_viewer_key(&mut self, key: &str, cx: &mut Context<Self>) -> bool {
        if self.image_viewer.is_none() {
            return false;
        }
        match key {
            "escape" | "space" => self.close_image_viewer(cx),
            "left" => self.step_image_viewer(-1, cx),
            "right" => self.step_image_viewer(1, cx),
            _ => {}
        }
        cx.stop_propagation();
        true
    }

    pub(super) fn image_viewer_view(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let viewer = self.image_viewer.as_ref()?;
        let count = viewer.ids.len();
        let (index, path, failed) = (viewer.index, viewer.path.clone(), viewer.failed);
        let has_image = path.is_some();
        let size = viewer.sizes.get(index).copied().unwrap_or_default();
        let stage = viewer.stage.clone();
        let measured = viewer.stage.clone();
        let on_dark = hsla(0.0, 0.0, 1.0, 0.92);
        let button = |id: &'static str, icon: &'static str| {
            div()
                .id(id)
                .size(px(36.0))
                .flex()
                .flex_none()
                .items_center()
                .justify_center()
                .rounded_full()
                .bg(hsla(0.0, 0.0, 1.0, 0.12))
                .hover(|s| s.bg(hsla(0.0, 0.0, 1.0, 0.22)))
                .child(ui::icon(icon, 16.0, on_dark))
        };
        let body = match path {
            Some(path) => img(path)
                .size_full()
                .object_fit(ObjectFit::Contain)
                .with_fallback(|| {
                    div()
                        .text_color(hsla(0.0, 0.0, 1.0, 0.7))
                        .child(t("request_image_not_ready"))
                        .into_any_element()
                })
                .into_any_element(),
            None => div()
                .text_size(px(13.0))
                .text_color(hsla(0.0, 0.0, 1.0, 0.7))
                .when(failed, |d| d.child(t("request_image_not_ready")))
                .into_any_element(),
        };
        Some(
            div()
                .id("image-viewer")
                .absolute()
                .inset_0()
                .bg(hsla(0.0, 0.0, 0.0, 0.86))
                .occlude()
                .on_click(cx.listener(|this, _, _, cx| this.close_image_viewer(cx)))
                .child(
                    div()
                        .id("image-viewer-stage")
                        .absolute()
                        .top(px(56.0))
                        .bottom(px(56.0))
                        .left(px(72.0))
                        .right(px(72.0))
                        .flex()
                        .items_center()
                        .justify_center()
                        // Only the picture itself keeps a click; the letterbox
                        // around it is backdrop and closes the viewer.
                        .on_click(move |event, _, cx| {
                            if has_image && hits_image(stage.get(), size, event.position()) {
                                cx.stop_propagation();
                            }
                        })
                        .child(
                            canvas(move |bounds, _, _| measured.set(bounds), |_, _, _, _| {})
                                .absolute()
                                .size_full(),
                        )
                        .child(body),
                )
                .child(
                    h_flex()
                        .absolute()
                        .top(px(12.0))
                        .left_0()
                        .right_0()
                        .px(px(16.0))
                        .items_center()
                        .justify_between()
                        .child(
                            div()
                                .text_size(px(12.0))
                                .font_weight(FontWeight::MEDIUM)
                                .text_color(hsla(0.0, 0.0, 1.0, 0.7))
                                .when(count > 1, |d| d.child(format!("{} / {count}", index + 1))),
                        )
                        .child(button("image-viewer-close", "close").on_click(cx.listener(
                            |this, _, _, cx| {
                                cx.stop_propagation();
                                this.close_image_viewer(cx)
                            },
                        ))),
                )
                .when(count > 1 && index > 0, |d| {
                    d.child(
                        div()
                            .absolute()
                            .left(px(18.0))
                            .top_0()
                            .bottom_0()
                            .flex()
                            .items_center()
                            .child(
                                button("image-viewer-previous", "back").on_click(cx.listener(
                                    |this, _, _, cx| {
                                        cx.stop_propagation();
                                        this.step_image_viewer(-1, cx)
                                    },
                                )),
                            ),
                    )
                })
                .when(count > 1 && index + 1 < count, |d| {
                    d.child(
                        div()
                            .absolute()
                            .right(px(18.0))
                            .top_0()
                            .bottom_0()
                            .flex()
                            .items_center()
                            .child(button("image-viewer-next", "forward").on_click(cx.listener(
                                |this, _, _, cx| {
                                    cx.stop_propagation();
                                    this.step_image_viewer(1, cx)
                                },
                            ))),
                    )
                })
                .into_any_element(),
        )
    }
}

/// The local path of a `file://` URL as the daemon writes it (percent-encoded).
fn file_url_path(url: &str) -> Option<PathBuf> {
    let encoded = url.strip_prefix("file://")?;
    if !encoded.starts_with('/') {
        return None;
    }
    let bytes = encoded.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' {
            let hex = std::str::from_utf8(bytes.get(i + 1..i + 3)?).ok()?;
            out.push(u8::from_str_radix(hex, 16).ok()?);
            i += 3;
        } else {
            out.push(bytes[i]);
            i += 1;
        }
    }
    Some(PathBuf::from(String::from_utf8(out).ok()?))
}

#[cfg(test)]
mod tests {
    use super::{file_url_path, hits_image};
    use gpui::{bounds, point, px, size};
    use std::path::PathBuf;

    #[test]
    fn letterbox_clicks_miss_the_image() {
        // A 2:1 screenshot in a 1000×1000 stage is drawn 1000×500, centered.
        let stage = bounds(point(px(0.0), px(0.0)), size(px(1000.0), px(1000.0)));
        assert!(hits_image(stage, (3024, 1512), point(px(500.0), px(500.0))));
        assert!(!hits_image(
            stage,
            (3024, 1512),
            point(px(500.0), px(100.0))
        ));
        assert!(!hits_image(
            stage,
            (3024, 1512),
            point(px(500.0), px(900.0))
        ));
        // A tall image leaves the sides as backdrop instead.
        assert!(!hits_image(stage, (500, 1000), point(px(100.0), px(500.0))));
        // No recorded size: the whole stage counts as the image.
        assert!(hits_image(stage, (0, 0), point(px(500.0), px(100.0))));
    }

    #[test]
    fn file_urls_decode_to_paths() {
        assert_eq!(
            file_url_path("file:///Users/me/Library/Application%20Support/a.png"),
            Some(PathBuf::from("/Users/me/Library/Application Support/a.png"))
        );
        assert_eq!(
            file_url_path("file:///tmp/%23%25.jpg"),
            Some(PathBuf::from("/tmp/#%.jpg"))
        );
        assert_eq!(file_url_path("https://example.com/a.png"), None);
        assert_eq!(file_url_path("file:///bad%2"), None);
    }
}
