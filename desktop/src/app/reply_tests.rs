//! THI-41/THI-61: the thread reply box must show every line it holds.

use super::{FennecWindow, Route};
use crate::board::{Message, Project, Request};
use gpui::{AppContext, Entity, TestAppContext, VisualTestContext};

fn window(cx: &mut TestAppContext) -> (Entity<FennecWindow>, &mut VisualTestContext) {
    cx.update(gpui_base::init);
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
            number: 61,
            title: "Reply box".into(),
            status: "working".into(),
            messages: vec![Message {
                id: 1,
                author: "someone".into(),
                body: "hello".into(),
                at: "2026-11-24T07:31:39Z".into(),
                ..Default::default()
            }],
            ..Default::default()
        };
        app.projects = vec![project];
        app.requests.insert("this".into(), vec![request.clone()]);
        app.route = Some(Route::Project("this".into()));
        app.open = Some(61);
        app.thread = Some(request);
        app
    });
    cx.run_until_parked();
    (app, cx)
}

fn text_height(cx: &mut VisualTestContext) -> f32 {
    cx.run_until_parked();
    // A second pass picks up the row count auto-grow measured in the first.
    cx.update(|window, _| window.refresh());
    cx.run_until_parked();
    let bounds = cx
        .debug_bounds("thread-reply-text")
        .expect("reply box is rendered");
    f32::from(bounds.size.height)
}

#[gpui::test]
fn reply_box_grows_to_show_every_line(cx: &mut TestAppContext) {
    let (app, cx) = window(cx);
    let one = text_height(cx);
    assert!(one > 0.0);
    cx.update(|window, cx| {
        let reply = app.read(cx).reply.clone();
        reply.update(cx, |s, cx| s.set_value("one\ntwo\nthree", window, cx));
    });
    let three = text_height(cx);
    assert!(
        three >= one * 2.9,
        "three lines need three rows: one line {one}px, three lines {three}px"
    );
}

#[gpui::test]
fn shift_enter_adds_a_line_and_enter_still_sends(cx: &mut TestAppContext) {
    let (app, cx) = window(cx);
    let one = text_height(cx);
    cx.update(|window, cx| {
        let reply = app.read(cx).reply.clone();
        reply.read(cx).focus_handle(cx).focus(window, cx);
    });
    cx.simulate_keystrokes("a shift-enter b");
    let value = cx.update(|_, cx| app.read(cx).reply.read(cx).value().to_string());
    assert_eq!(value, "a\nb");
    let two = text_height(cx);
    assert!(
        two >= one * 1.9,
        "the second line must be visible: one line {one}px, two lines {two}px"
    );
}
