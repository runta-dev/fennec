//! Window screenshots for development and checks, as herdr-app's `shot`:
//! with FENNEC_DESKTOP_SHOT=<file.png>, writing `<file.png>.request` makes the
//! window show what it names (`project:<id>`, `request:<n>`,
//! `settings:<id>:<general|knowledge|queue|agents>`, `session:<address>`, or
//! nothing) and save its next frame to the PNG, then delete the request.

use std::io::Write as _;
use std::path::{Path, PathBuf};

pub fn path() -> Option<PathBuf> {
    std::env::var_os("FENNEC_DESKTOP_SHOT").map(PathBuf::from)
}

pub fn request(shot: &Path) -> Option<String> {
    let request = PathBuf::from(format!("{}.request", shot.display()));
    let text = std::fs::read_to_string(&request).ok()?;
    let _ = std::fs::remove_file(&request);
    Some(text.trim().to_owned())
}

/// herdr-app's control::save_shot.
pub fn save<C: gpui::AppContext>(window: gpui::AnyWindowHandle, cx: &mut C, path: &Path) -> anyhow::Result<()> {
    let image = window.render_current_to_image(cx)?;
    let mut bytes = Vec::new();
    {
        let mut encoder = png::Encoder::new(&mut bytes, image.width(), image.height());
        encoder.set_color(png::ColorType::Rgba);
        encoder.set_depth(png::BitDepth::Eight);
        encoder.set_source_srgb(png::SrgbRenderingIntent::Perceptual);
        encoder.write_header()?.write_image_data(image.as_raw())?;
    }
    let temporary = path.with_file_name(format!(".fennec-shot-{}.png", std::process::id()));
    let mut file = std::fs::File::create(&temporary)?;
    file.write_all(&bytes)?;
    file.sync_all()?;
    std::fs::rename(&temporary, path)?;
    Ok(())
}
