//! THI-169: thread message text can be selected with the mouse and copied with ⌘C.

use super::{FennecWindow, Route};
use crate::board::{Message, Project, Request};
use gpui::{
    Entity, Focusable, Modifiers, MouseButton, TestAppContext, VisualTestContext, point, px,
};

const BODY: &str = "Selectable thread message body";

fn window(cx: &mut TestAppContext) -> (Entity<FennecWindow>, &mut VisualTestContext) {
    cx.update(|cx| {
        gpui_base::init(cx);
        crate::keys::install(crate::keys::parse(crate::keys::DEFAULTS).unwrap(), cx);
    });
    let (app, cx) = cx.add_window_view(|window, cx| {
        let (_, receive) = async_channel::unbounded();
        let mut app = FennecWindow::build(receive, window, cx, false);
        let project = Project {
            id: "this".into(),
            key: "THI".into(),
            name: "Fennec".into(),
            ..Default::default()
        };
        let request = Request {
            number: 169,
            title: "Copy thread text".into(),
            status: "working".into(),
            messages: vec![Message {
                id: 1,
                author: "someone".into(),
                body: BODY.into(),
                at: "2026-11-25T08:47:12Z".into(),
                ..Default::default()
            }],
            ..Default::default()
        };
        app.projects = vec![project];
        app.requests.insert("this".into(), vec![request.clone()]);
        app.route = Some(Route::Project("this".into()));
        app.open = Some(169);
        app.thread = Some(request);
        app
    });
    cx.run_until_parked();
    cx.update(|window, _| window.refresh());
    cx.run_until_parked();
    (app, cx)
}

fn copied(cx: &mut VisualTestContext) -> String {
    cx.update(|_, cx| cx.read_from_clipboard())
        .and_then(|item| item.text())
        .unwrap_or_default()
}

#[gpui::test]
fn dragging_over_a_message_selects_it_and_cmd_c_copies_it(cx: &mut TestAppContext) {
    let (app, cx) = window(cx);
    // The reply box has focus, as after typing a reply: ⌘C must still copy the thread text.
    cx.update(|window, cx| {
        let reply = app.read(cx).reply.clone();
        reply.read(cx).focus_handle(cx).focus(window, cx);
    });
    let bounds = cx
        .debug_bounds("thread-message-1")
        .expect("thread message is rendered");
    let y = bounds.top() + px(8.);
    let start = point(bounds.left() + px(1.), y);
    let end = point(bounds.right() - px(1.), y);
    cx.simulate_mouse_down(start, MouseButton::Left, Modifiers::default());
    cx.simulate_mouse_move(end, Some(MouseButton::Left), Modifiers::default());
    cx.simulate_mouse_up(end, MouseButton::Left, Modifiers::default());
    cx.run_until_parked();

    cx.simulate_keystrokes("cmd-c");
    let text = copied(cx);
    assert!(
        text.starts_with("Selectable") && BODY.contains(text.as_str()),
        "copied {text:?}"
    );
}

#[gpui::test]
fn cmd_c_in_the_reply_box_still_copies_its_own_selection(cx: &mut TestAppContext) {
    let (app, cx) = window(cx);
    cx.update(|window, cx| {
        let reply = app.read(cx).reply.clone();
        reply.update(cx, |s, cx| s.set_value("draft reply", window, cx));
        reply.read(cx).focus_handle(cx).focus(window, cx);
    });
    cx.simulate_keystrokes("cmd-a cmd-c");
    assert_eq!(copied(cx), "draft reply");
}

#[gpui::test]
fn right_click_on_selected_text_offers_copy(cx: &mut TestAppContext) {
    let (app, cx) = window(cx);
    let bounds = cx
        .debug_bounds("thread-message-1")
        .expect("thread message is rendered");
    let y = bounds.top() + px(8.);
    let start = point(bounds.left() + px(1.), y);
    let end = point(bounds.right() - px(1.), y);

    // Nothing selected: a right-click opens no menu.
    cx.simulate_mouse_down(start, MouseButton::Right, Modifiers::default());
    cx.simulate_mouse_up(start, MouseButton::Right, Modifiers::default());
    assert!(app.read_with(cx, |app, _| app.context_menu.is_none()));

    cx.simulate_mouse_down(start, MouseButton::Left, Modifiers::default());
    cx.simulate_mouse_move(end, Some(MouseButton::Left), Modifiers::default());
    cx.simulate_mouse_up(end, MouseButton::Left, Modifiers::default());
    cx.run_until_parked();

    let at = point(bounds.left() + px(20.), y);
    cx.simulate_mouse_down(at, MouseButton::Right, Modifiers::default());
    cx.simulate_mouse_up(at, MouseButton::Right, Modifiers::default());
    cx.run_until_parked();
    assert!(app.read_with(cx, |app, _| app.context_menu.is_some()));

    // The menu opens at the click; its first item sits just below it.
    let item = point(at.x + px(30.), at.y + px(18.));
    cx.simulate_mouse_down(item, MouseButton::Left, Modifiers::default());
    cx.simulate_mouse_up(item, MouseButton::Left, Modifiers::default());
    cx.run_until_parked();
    assert!(app.read_with(cx, |app, _| app.context_menu.is_none()));
    let text = copied(cx);
    assert!(
        text.starts_with("Selectable") && BODY.contains(text.as_str()),
        "copied {text:?}"
    );
}
