//! Fennec's settings: what the terminal core (`terminal.rs`, `vt.rs`) reads.
//!
//! The palettes, the derived chrome colors and the font handling are
//! herdr-app's (src/settings.rs, Apache-2.0), kept as they are so terminals
//! draw exactly as in Herdr. Fennec has no settings.json or layouts: the
//! theme follows the system appearance (Herdr's Ink Dark / Paper) with
//! Runta orange as the accent.

use crate::vt::Rgb;
use gpui::{Hsla, Rgba};
use serde::{Deserialize, Serialize};
use std::cell::RefCell;
use std::collections::BTreeMap;
use std::rc::Rc;
use std::sync::Mutex;

#[derive(Clone)]
struct Palette {
    background: u32,
    foreground: u32,
    selection: u32,
    cursor: Option<u32>,
    ansi: [u32; 16],
    status: Option<[u32; 3]>,
}

const DARK_STATUS: Option<[u32; 3]> = Some([0xe5c07b, 0xf0705f, 0x76a8ff]);

const INK: Palette = Palette {
    background: 0x111720,
    foreground: 0xcdd6e4,
    selection: 0x344861,
    cursor: None,
    status: DARK_STATUS,
    ansi: [
        0x414b5a, 0xe58c9a, 0x89c5aa, 0xd9be82, 0x91b6ee, 0xb9a0e2, 0x85c4d0, 0xcdd6e4, 0x7f91a7,
        0xf5a5b2, 0xa5dec3, 0xedd39c, 0xaccbfa, 0xd0b7f4, 0xa0dce5, 0xf0f5fc,
    ],
};

const PAPER: Palette = Palette {
    background: 0xffffff,
    foreground: 0x25272c,
    selection: 0xcfdcf5,
    cursor: Some(0x25272c),
    status: Some([0x9a6700, 0xc93c2c, 0x2f6fe4]),
    ansi: [
        0x25272c, 0xc62a2a, 0x1a7f37, 0x8f5d00, 0x1f62d0, 0x7a45c8, 0x0f6f74, 0x5f6068, 0x7b7e87,
        0xd1242f, 0x1f883d, 0x9a6700, 0x0969da, 0x8250df, 0x1b7c83, 0x111214,
    ],
};

fn is_light(c: u32) -> bool {
    let ch = |s: u32| ((c >> s) & 255) as f32 / 255.0;
    0.2126 * ch(16) + 0.7152 * ch(8) + 0.0722 * ch(0) > 0.5
}

fn hex(c: u32) -> String {
    format!("#{c:06x}")
}

fn mix(a: u32, b: u32, t: f32) -> u32 {
    [16, 8, 0].iter().fold(0, |acc, s| {
        let (x, y) = (((a >> s) & 255) as f32, ((b >> s) & 255) as f32);
        acc | (((x * (1.0 - t) + y * t).round() as u32) << s)
    })
}

/// Every color the app uses, resolved. Values are `#rrggbb` or `#rrggbbaa`.
pub type Theme = BTreeMap<String, String>;

/// herdr-app's chrome derivation, unchanged.
fn resolve(p: &Palette, overrides: &BTreeMap<String, String>) -> Theme {
    let bg = p.background;
    let light = is_light(bg);
    let fg = if light { 0x1b1b1e } else { 0xeeeeef };
    let [working, blocked, done] = p.status.unwrap_or([p.ansi[3], p.ansi[1], p.ansi[4]]);
    let (popover, raised, hover, selected, border, input, canvas, chrome) = if light {
        let warm = 0xe8e8e2;
        (
            hex(bg),
            hex(mix(bg, 0, 0.035)),
            "#0000000c".into(),
            "#00000012".into(),
            "#00000017".into(),
            "#00000026".into(),
            mix(bg, warm, 0.2),
            mix(bg, warm, 0.6),
        )
    } else {
        (
            hex(mix(bg, 0xffffff, 0.06)),
            hex(mix(bg, 0xffffff, 0.09)),
            "#ffffff0e".into(),
            "#ffffff17".into(),
            "#ffffff18".into(),
            "#ffffff24".into(),
            mix(bg, 0, 0.16),
            mix(bg, 0, 0.32),
        )
    };
    let mut t: Theme = [
        ("background", hex(bg)),
        ("terminal_foreground", hex(p.foreground)),
        ("foreground", hex(fg)),
        ("surface", hex(bg)),
        ("canvas", hex(canvas)),
        ("chrome", hex(chrome)),
        ("popover", popover),
        ("raised", raised),
        ("hover", hover),
        ("selected", selected),
        ("border", border),
        ("input", input),
        ("muted", hex(mix(bg, fg, if light { 0.66 } else { 0.6 }))),
        ("dim", hex(mix(bg, fg, if light { 0.46 } else { 0.38 }))),
        ("accent", hex(p.ansi[4])),
        ("accent_foreground", hex(bg)),
        ("solid", hex(fg)),
        (
            "solid_foreground",
            hex(if light { 0xffffff } else { mix(bg, 0, 0.28) }),
        ),
        ("ring", hex(mix(bg, fg, 0.7))),
        ("selection", hex(p.selection)),
        ("cursor", hex(p.cursor.unwrap_or(p.foreground))),
        ("working", hex(working)),
        ("blocked", hex(blocked)),
        ("done", hex(done)),
        ("idle", hex(mix(bg, fg, 0.38))),
        ("unknown", hex(mix(bg, fg, 0.38))),
    ]
    .into_iter()
    .map(|(k, v)| (k.to_owned(), v))
    .collect();
    for (i, c) in p.ansi.iter().enumerate() {
        t.insert(format!("ansi{i}"), hex(*c));
    }
    for (k, v) in overrides {
        t.insert(k.clone(), v.clone());
    }
    t
}

/// Fennec's own colors over Herdr's chrome: Runta orange, and the tint
/// behind "Needs you" (panel.css `--tint`, `--accent-fg`).
fn fennec_colors(dark: bool) -> BTreeMap<String, String> {
    let pairs: &[(&str, &str)] = if dark {
        &[
            ("orange", "#f38d25"),
            ("tint", "#392c21"),
            ("accent_text", "#f6aa5c"),
            ("bug", "#ee5b51"),
            ("feature", "#86a5ea"),
            ("discussion", "#b596e8"),
            ("ok", "#5fc081"),
            ("f_bg", "#1b1c20"),
            ("f_card", "#25262c"),
            ("f_expanded", "#212227"),
            ("f_text", "#eeeef0"),
            ("f_muted", "#9c9ca6"),
            ("f_line", "#ffffff12"),
            ("f_field", "#ffffff1f"),
        ]
    } else {
        &[
            ("orange", "#f38d25"),
            ("tint", "#fff2e5"),
            ("accent_text", "#b06014"),
            ("bug", "#d6413a"),
            ("feature", "#4a73c9"),
            ("discussion", "#8a62c9"),
            ("ok", "#2f9a58"),
            ("f_bg", "#f2f2f5"),
            ("f_card", "#ffffff"),
            ("f_expanded", "#f8f8fa"),
            ("f_text", "#1c1c21"),
            ("f_muted", "#85858d"),
            ("f_line", "#00000012"),
            ("f_field", "#0000001f"),
        ]
    };
    pairs
        .iter()
        .map(|(k, v)| ((*k).to_owned(), (*v).to_owned()))
        .collect()
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum OptionAsAlt {
    #[default]
    Auto,
    Off,
    On,
    Left,
    Right,
}

/// The fields herdr-app's `Settings` has that the terminal core reads,
/// with herdr-app's defaults.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Settings {
    pub font_family: String,
    pub font_size: f32,
    pub font_weight: u16,
    pub font_ligatures: bool,
    pub terminal_line_height: f32,
    pub terminal_cell_width: f32,
    pub terminal_scroll_speed: f32,
    pub ui_font_family: String,
    pub ui_font_size: f32,
    pub zoom: f32,
    pub dark: bool,
    pub pane_scrollbars: bool,
    pub terminal_padding: [f32; 2],
    pub terminal_opacity: f32,
    pub clipboard_write: bool,
    pub copy_on_select: bool,
    pub minimum_contrast: f32,
    pub option_as_alt: OptionAsAlt,
}

impl Default for Settings {
    fn default() -> Self {
        Settings {
            font_family: DEFAULT_FONT.into(),
            font_size: 14.0,
            font_weight: 500,
            font_ligatures: false,
            terminal_line_height: 1.0,
            terminal_cell_width: 1.0,
            terminal_scroll_speed: 1.0,
            ui_font_family: UI_FONT.into(),
            ui_font_size: 13.0,
            zoom: 1.0,
            dark: true,
            pane_scrollbars: true,
            terminal_padding: [14.0, 12.0],
            terminal_opacity: 1.0,
            clipboard_write: true,
            copy_on_select: true,
            minimum_contrast: 1.0,
            option_as_alt: OptionAsAlt::Auto,
        }
    }
}

pub fn parse_hex(s: &str) -> Option<Hsla> {
    let h = s.trim().trim_start_matches('#');
    let v = u32::from_str_radix(h, 16).ok()?;
    let (rgb, a) = match h.len() {
        6 => (v, 255),
        8 => (v >> 8, v & 255),
        _ => return None,
    };
    Some(
        Rgba {
            r: ((rgb >> 16) & 255) as f32 / 255.0,
            g: ((rgb >> 8) & 255) as f32 / 255.0,
            b: (rgb & 255) as f32 / 255.0,
            a: a as f32 / 255.0,
        }
        .into(),
    )
}

pub fn rgb(s: &str) -> Rgb {
    let h = s.trim().trim_start_matches('#');
    let v = u32::from_str_radix(&h[..h.len().min(6)], 16).unwrap_or(0xff00ff);
    Rgb((v >> 16) as u8, (v >> 8) as u8, v as u8)
}

fn theme_for(dark: bool) -> Theme {
    let mut theme = resolve(if dark { &INK } else { &PAPER }, &fennec_colors(dark));
    if dark {
        for (alias, role) in [
            ("f_bg", "chrome"),
            ("f_card", "raised"),
            ("f_expanded", "popover"),
            ("f_text", "foreground"),
            ("f_muted", "muted"),
            ("f_line", "border"),
            ("f_field", "input"),
        ] {
            theme.insert(alias.into(), theme[role].clone());
        }
    }
    theme
}

thread_local! {
    static CURRENT: RefCell<(Rc<Settings>, Rc<Theme>)> = RefCell::new((Rc::new(Settings::default()), Rc::new(theme_for(true))));
}

pub fn get() -> Rc<Settings> {
    CURRENT.with(|c| c.borrow().0.clone())
}

pub fn theme() -> Rc<Theme> {
    CURRENT.with(|c| c.borrow().1.clone())
}

/// A theme color as GPUI paint (magenta if missing, so mistakes show).
pub fn color(name: &str) -> Hsla {
    theme()
        .get(name)
        .and_then(|v| parse_hex(v))
        .unwrap_or(gpui::hsla(0.83, 1.0, 0.5, 1.0))
}

/// Installs new settings; returns whether anything changed.
pub fn set(s: Settings) -> bool {
    #[cfg(target_os = "macos")]
    gpui_macos::set_option_as_alt(match s.option_as_alt {
        OptionAsAlt::Auto => None,
        OptionAsAlt::Off => Some(gpui_macos::OptionAsAlt::None),
        OptionAsAlt::On => Some(gpui_macos::OptionAsAlt::Both),
        OptionAsAlt::Left => Some(gpui_macos::OptionAsAlt::Left),
        OptionAsAlt::Right => Some(gpui_macos::OptionAsAlt::Right),
    });
    CURRENT.with(|c| {
        let theme = theme_for(s.dark);
        if *c.borrow().0 == s && *c.borrow().1 == theme {
            return false;
        }
        *c.borrow_mut() = (Rc::new(s), Rc::new(theme));
        true
    })
}

/// Follows the system's light or dark appearance; true when it changed.
pub fn set_dark(dark: bool) -> bool {
    set(Settings {
        dark,
        ..(*get()).clone()
    })
}

pub fn zoom() -> f32 {
    get().zoom
}

pub fn font_size() -> f32 {
    get().font_size * zoom()
}

pub fn ui_font_size() -> f32 {
    get().ui_font_size * zoom()
}

static FONTS: Mutex<Option<std::collections::HashSet<String>>> = Mutex::new(None);

pub fn note_fonts(names: Vec<String>) {
    *FONTS.lock().unwrap() = Some(names.into_iter().map(|n| n.to_lowercase()).collect());
}

static FONTS_ADDED: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

pub fn fonts_added() -> u64 {
    FONTS_ADDED.load(std::sync::atomic::Ordering::Relaxed)
}

/// The terminal font, or the bundled default when this machine lacks it:
/// the platform's own fallback is proportional and breaks the grid.
pub fn terminal_font() -> String {
    let family = get().font_family.clone();
    match FONTS.lock().unwrap().as_ref() {
        Some(have) if !have.contains(&family.to_lowercase()) => DEFAULT_FONT.into(),
        _ => family,
    }
}

/// Loads the bundled fonts once at start, as herdr-app does.
pub fn load_bundled_fonts(cx: &mut gpui::App) {
    let fonts = BUNDLED_FONTS
        .iter()
        .map(|b| std::borrow::Cow::Borrowed(*b))
        .collect();
    if let Err(e) = cx.text_system().add_fonts(fonts) {
        tracing::warn!("bundled fonts not loaded: {e:#}");
    }
    FONTS_ADDED.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
    note_fonts(cx.text_system().all_font_names());
}

pub const BUNDLED_FONTS: &[&[u8]] = &[
    include_bytes!("../assets/fonts/GeistMono-Variable.ttf"),
    include_bytes!("../assets/fonts/GeistMono-Italic-Variable.ttf"),
    include_bytes!("../assets/fonts/Geist.ttf"),
    include_bytes!("../assets/fonts/JetBrainsMono-Variable.ttf"),
    include_bytes!("../assets/fonts/JetBrainsMono-Italic-Variable.ttf"),
    SYMBOLS_FONT_FILE,
];

pub const SYMBOLS_FONT_FILE: &[u8] = include_bytes!("../assets/fonts/SymbolsNerdFont-Regular.ttf");
pub const SYMBOLS_FONT: &str = "Symbols Nerd Font";
pub const DEFAULT_FONT: &str = "JetBrains Mono";
/// The window's own text: the system UI font, as Fennec's panel uses.
#[cfg(target_os = "macos")]
pub const UI_FONT: &str = ".SystemUIFont";
#[cfg(not(target_os = "macos"))]
pub const UI_FONT: &str = "Geist";
