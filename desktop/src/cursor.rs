//! Fennec's desktop window never shows the pointing-hand cursor: buttons, links
//! and other clickable things keep the default arrow, like a native app (THI-63).
//!
//! Many places ask for `CursorStyle::PointingHand`: Fennec's own views, gpui-kit,
//! GPUI's text links and herdr-app's terminal links. The last three are kept
//! verbatim (see UPSTREAM.md), so the cursor is changed here instead. Every
//! cursor request reaches the OS through `Platform::set_cursor_style`. This
//! wrapper forwards every other call to the real platform unchanged. Other
//! cursors (I-beam, resize, drag) still work.

use std::{
    ffi::OsString,
    path::{Path, PathBuf},
    rc::Rc,
    sync::Arc,
};

use anyhow::Result;
use futures::channel::oneshot;
use gpui::{
    Action, ActivationPolicy, ActivityGuard, AnyWindowHandle, AppLifecyclePhase,
    BackgroundExecutor, ClipboardItem, ClipboardReadError, CursorStyle, ForegroundExecutor,
    GraphicalEnvironment, Keymap, Menu, MenuItem, OwnedMenu, PathPromptOptions, Platform,
    PlatformDisplay, PlatformGestures, PlatformKeyboardLayout, PlatformKeyboardMapper,
    PlatformTextSystem, PlatformWindow, ScreenCaptureSource, SystemNotification,
    SystemNotificationResponse, Task, ThermalState, WindowAppearance, WindowButtonLayout,
    WindowParams, WindowingRequest,
};
use smallvec::SmallVec;

/// The cursor Fennec shows for `style`: the arrow in place of the pointing hand.
pub fn fennec_style(style: CursorStyle) -> CursorStyle {
    match style {
        CursorStyle::PointingHand => CursorStyle::Arrow,
        other => other,
    }
}

/// Wraps the OS platform so it never shows the pointing hand.
pub fn platform(inner: Rc<dyn Platform>) -> Rc<dyn Platform> {
    Rc::new(DefaultCursor(inner))
}

struct DefaultCursor(Rc<dyn Platform>);

impl Platform for DefaultCursor {
    fn set_cursor_style(&self, style: CursorStyle) {
        self.0.set_cursor_style(fennec_style(style))
    }

    // Everything below forwards to the real platform.

    fn background_executor(&self) -> BackgroundExecutor {
        self.0.background_executor()
    }
    fn foreground_executor(&self) -> ForegroundExecutor {
        self.0.foreground_executor()
    }
    fn text_system(&self) -> Arc<dyn PlatformTextSystem> {
        self.0.text_system()
    }
    fn run(&self, on_finish_launching: Box<dyn 'static + FnOnce()>) {
        self.0.run(on_finish_launching)
    }
    fn quit(&self) {
        self.0.quit()
    }
    fn set_initial_windowing(&self, request: WindowingRequest) {
        self.0.set_initial_windowing(request)
    }
    fn request_windowing(&self, request: WindowingRequest) -> Task<anyhow::Result<()>> {
        self.0.request_windowing(request)
    }
    fn restart(&self, binary_path: Option<PathBuf>, arguments: Vec<OsString>) {
        self.0.restart(binary_path, arguments)
    }
    fn activate(&self, ignoring_other_apps: bool) {
        self.0.activate(ignoring_other_apps)
    }
    fn set_activation_policy(&self, policy: ActivationPolicy) {
        self.0.set_activation_policy(policy)
    }
    fn hide(&self) {
        self.0.hide()
    }
    fn hide_other_apps(&self) {
        self.0.hide_other_apps()
    }
    fn unhide_other_apps(&self) {
        self.0.unhide_other_apps()
    }
    fn displays(&self) -> Vec<Rc<dyn PlatformDisplay>> {
        self.0.displays()
    }
    fn primary_display(&self) -> Option<Rc<dyn PlatformDisplay>> {
        self.0.primary_display()
    }
    fn active_window(&self) -> Option<AnyWindowHandle> {
        self.0.active_window()
    }
    fn window_stack(&self) -> Option<Vec<AnyWindowHandle>> {
        self.0.window_stack()
    }
    fn is_screen_capture_supported(&self) -> bool {
        self.0.is_screen_capture_supported()
    }
    fn screen_capture_sources(
        &self,
    ) -> oneshot::Receiver<anyhow::Result<Vec<Rc<dyn ScreenCaptureSource>>>> {
        self.0.screen_capture_sources()
    }
    fn open_window(
        &self,
        handle: AnyWindowHandle,
        options: WindowParams,
    ) -> anyhow::Result<Box<dyn PlatformWindow>> {
        self.0.open_window(handle, options)
    }
    fn window_appearance(&self) -> WindowAppearance {
        self.0.window_appearance()
    }
    fn set_window_appearance(&self, appearance: Option<WindowAppearance>) {
        self.0.set_window_appearance(appearance)
    }
    fn button_layout(&self) -> Option<WindowButtonLayout> {
        self.0.button_layout()
    }
    fn open_url(&self, url: &str) {
        self.0.open_url(url)
    }
    fn on_open_urls(&self, callback: Box<dyn FnMut(Vec<String>)>) {
        self.0.on_open_urls(callback)
    }
    fn register_url_scheme(&self, url: &str) -> Task<Result<()>> {
        self.0.register_url_scheme(url)
    }
    fn prompt_for_paths(
        &self,
        options: PathPromptOptions,
    ) -> oneshot::Receiver<Result<Option<Vec<PathBuf>>>> {
        self.0.prompt_for_paths(options)
    }
    fn prompt_for_new_path(
        &self,
        directory: &Path,
        suggested_name: Option<&str>,
    ) -> oneshot::Receiver<Result<Option<PathBuf>>> {
        self.0.prompt_for_new_path(directory, suggested_name)
    }
    fn can_select_mixed_files_and_dirs(&self) -> bool {
        self.0.can_select_mixed_files_and_dirs()
    }
    fn reveal_path(&self, path: &Path) {
        self.0.reveal_path(path)
    }
    fn open_with_system(&self, path: &Path) {
        self.0.open_with_system(path)
    }
    fn on_quit(&self, callback: Box<dyn FnMut() -> bool>) {
        self.0.on_quit(callback)
    }
    fn on_reopen(&self, callback: Box<dyn FnMut()>) {
        self.0.on_reopen(callback)
    }
    fn on_system_sleep(&self, callback: Box<dyn FnMut()>) {
        self.0.on_system_sleep(callback)
    }
    fn on_system_wake(&self, callback: Box<dyn FnMut()>) {
        self.0.on_system_wake(callback)
    }
    fn on_app_lifecycle(&self, callback: Box<dyn FnMut(AppLifecyclePhase)>) {
        self.0.on_app_lifecycle(callback)
    }
    fn on_memory_warning(&self, callback: Box<dyn FnMut()>) {
        self.0.on_memory_warning(callback)
    }
    fn gestures(&self) -> Option<Rc<dyn PlatformGestures>> {
        self.0.gestures()
    }
    fn set_menus(&self, menus: Vec<Menu>, keymap: &Keymap) {
        self.0.set_menus(menus, keymap)
    }
    fn get_menus(&self) -> Option<Vec<OwnedMenu>> {
        self.0.get_menus()
    }
    fn set_dock_menu(&self, menu: Vec<MenuItem>, keymap: &Keymap) {
        self.0.set_dock_menu(menu, keymap)
    }
    fn perform_dock_menu_action(&self, action: usize) {
        self.0.perform_dock_menu_action(action)
    }
    fn add_recent_document(&self, path: &Path) {
        self.0.add_recent_document(path)
    }
    fn update_jump_list(
        &self,
        menus: Vec<MenuItem>,
        entries: Vec<SmallVec<[PathBuf; 2]>>,
    ) -> Task<Vec<SmallVec<[PathBuf; 2]>>> {
        self.0.update_jump_list(menus, entries)
    }
    fn on_app_menu_action(&self, callback: Box<dyn FnMut(&dyn Action)>) {
        self.0.on_app_menu_action(callback)
    }
    fn on_will_open_app_menu(&self, callback: Box<dyn FnMut()>) {
        self.0.on_will_open_app_menu(callback)
    }
    fn on_validate_app_menu_command(&self, callback: Box<dyn FnMut(&dyn Action) -> bool>) {
        self.0.on_validate_app_menu_command(callback)
    }
    fn thermal_state(&self) -> ThermalState {
        self.0.thermal_state()
    }
    fn on_thermal_state_change(&self, callback: Box<dyn FnMut()>) {
        self.0.on_thermal_state_change(callback)
    }
    fn prevent_idle_sleep(&self, reason: &str) -> Task<Result<ActivityGuard>> {
        self.0.prevent_idle_sleep(reason)
    }
    fn set_app_identity(&self, identifier: &str, name: &str) {
        self.0.set_app_identity(identifier, name)
    }
    fn show_system_notification(&self, notification: SystemNotification) {
        self.0.show_system_notification(notification)
    }
    fn dismiss_system_notification(&self, tag: &str) {
        self.0.dismiss_system_notification(tag)
    }
    fn on_system_notification_response(
        &self,
        callback: Box<dyn FnMut(SystemNotificationResponse)>,
    ) {
        self.0.on_system_notification_response(callback)
    }
    fn compositor_name(&self) -> &'static str {
        self.0.compositor_name()
    }
    fn graphical_environment(&self) -> Option<GraphicalEnvironment> {
        self.0.graphical_environment()
    }
    fn app_path(&self) -> Result<PathBuf> {
        self.0.app_path()
    }
    fn path_for_auxiliary_executable(&self, name: &str) -> Result<PathBuf> {
        self.0.path_for_auxiliary_executable(name)
    }
    fn hide_cursor_until_mouse_moves(&self) {
        self.0.hide_cursor_until_mouse_moves()
    }
    fn is_cursor_visible(&self) -> bool {
        self.0.is_cursor_visible()
    }
    fn should_auto_hide_scrollbars(&self) -> bool {
        self.0.should_auto_hide_scrollbars()
    }
    fn read_from_clipboard(&self) -> Option<ClipboardItem> {
        self.0.read_from_clipboard()
    }
    fn write_to_clipboard(&self, item: ClipboardItem) {
        self.0.write_to_clipboard(item)
    }
    fn read_from_clipboard_async(
        &self,
    ) -> Task<Result<Option<ClipboardItem>, ClipboardReadError>> {
        self.0.read_from_clipboard_async()
    }
    #[cfg(any(target_os = "linux", target_os = "freebsd"))]
    fn read_from_primary(&self) -> Option<ClipboardItem> {
        self.0.read_from_primary()
    }
    #[cfg(any(target_os = "linux", target_os = "freebsd"))]
    fn write_to_primary(&self, item: ClipboardItem) {
        self.0.write_to_primary(item)
    }
    #[cfg(target_os = "macos")]
    fn read_from_find_pasteboard(&self) -> Option<ClipboardItem> {
        self.0.read_from_find_pasteboard()
    }
    #[cfg(target_os = "macos")]
    fn write_to_find_pasteboard(&self, item: ClipboardItem) {
        self.0.write_to_find_pasteboard(item)
    }
    fn write_credentials(&self, url: &str, username: &str, password: &[u8]) -> Task<Result<()>> {
        self.0.write_credentials(url, username, password)
    }
    fn read_credentials(&self, url: &str) -> Task<Result<Option<(String, Vec<u8>)>>> {
        self.0.read_credentials(url)
    }
    fn delete_credentials(&self, url: &str) -> Task<Result<()>> {
        self.0.delete_credentials(url)
    }
    fn keyboard_layout(&self) -> Box<dyn PlatformKeyboardLayout> {
        self.0.keyboard_layout()
    }
    fn keyboard_mapper(&self) -> Rc<dyn PlatformKeyboardMapper> {
        self.0.keyboard_mapper()
    }
    fn on_keyboard_layout_change(&self, callback: Box<dyn FnMut()>) {
        self.0.on_keyboard_layout_change(callback)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pointing_hand_becomes_arrow() {
        assert_eq!(fennec_style(CursorStyle::PointingHand), CursorStyle::Arrow);
        assert_eq!(fennec_style(CursorStyle::IBeam), CursorStyle::IBeam);
        assert_eq!(fennec_style(CursorStyle::ResizeLeftRight), CursorStyle::ResizeLeftRight);
    }
}
