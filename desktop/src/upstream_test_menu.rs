//! Edit-menu fixture for the unchanged Herdr terminal tests.
//! Extracted from herdr-app src/macos.rs at 317e673f (Apache-2.0).
//! Fennec's production menus belong to its native shell; this fixture does not
//! install Herdr's application menus or claim to test that native shell.
use gpui::*;
use gpui_shell::action::ShellAction;

pub fn dispatch_edit(action: &ShellAction, terminal_focused: bool, window: &mut Window, cx: &mut App) -> bool {
    if terminal_focused {
        return false;
    }
    let action: Box<dyn Action> = match action.id() {
        "terminal.copy" => Box::new(gpui_base::input::Copy),
        "terminal.paste" => Box::new(gpui_base::input::Paste),
        _ => return false,
    };
    if !window.is_action_available(action.as_ref(), cx) {
        return false;
    }
    window.dispatch_action(action, cx);
    true
}

pub fn menus(_layouts: &[String], _shown: Option<&str>) -> Vec<Menu> {
    vec![Menu::new("Edit").items([
        MenuItem::os_action("Copy", ShellAction::new("terminal.copy"), OsAction::Copy),
        MenuItem::os_action("Paste", ShellAction::new("terminal.paste"), OsAction::Paste),
    ])]
}
