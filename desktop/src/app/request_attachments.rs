//! Request composer clipboard UI. Persistence and validation live in the Go daemon.
use super::*;
use base64::Engine as _;
use std::collections::HashSet;
use std::io::{Cursor, Write};
use std::path::PathBuf;

#[derive(Default)]
pub(super) struct Drafts {
    pub by_queue: HashMap<String, Vec<board::Attachment>>,
    pub busy: HashSet<String>,
    pub errors: HashMap<String, &'static str>,
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
        if !self.composer.read(cx).focus_handle(cx).is_focused(window) {
            return false;
        }
        let Some(project) = self.project_id() else {
            return false;
        };
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
                .child(
                    h_flex()
                        .gap(px(8.0))
                        .flex_wrap()
                        .children(images.into_iter().map(|image| {
                            let project = project.to_owned();
                            let remove = image.id.clone();
                            let open = image.id.clone();
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
                                .cursor_pointer()
                                .on_click(cx.listener(move |this, _, window, cx| {
                                    this.open_request_image(open.clone(), window, cx)
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
                                            .cursor_pointer()
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
                        })),
                )
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
            .children(images.iter().map(|image| {
                let id = image.id.clone();
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
                        this.open_request_image(id.clone(), window, cx)
                    }))
            }))
            .into_any_element()
    }

    fn open_request_image(&mut self, id: String, window: &mut Window, cx: &mut Context<Self>) {
        if crate::preview::active() {
            return;
        }
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            let result = cx
                .background_spawn(async move {
                    crate::control::call("board.attachment.url", serde_json::json!({"id":id}))
                })
                .await;
            let _ = this.update(cx, |this, cx| {
                match result {
                    Ok(value) => {
                        if let Some(url) = value["url"].as_str() {
                            cx.open_url(url);
                        }
                    }
                    Err(_) => {
                        let message = t("request_image_not_ready");
                        this.notice = Some(message.clone());
                        this._tasks.push(cx.spawn(async move |this, cx| {
                            cx.background_executor().timer(Duration::from_secs(5)).await;
                            let _ = this.update(cx, |this, cx| {
                                if this.notice.as_deref() == Some(message.as_str()) {
                                    this.notice = None;
                                    cx.notify();
                                }
                            });
                        }));
                    }
                }
                cx.notify();
            });
        }));
    }
}
