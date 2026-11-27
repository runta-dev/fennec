//! FEN-106: no sidebar. The title bar switches queues; the queue column and
//! the thread beside it fill the window.

use super::{FennecWindow, Route};
use crate::board::{Project, Request};
use gpui::{Entity, Focusable as _, TestAppContext, VisualTestContext, px, size};

fn window(cx: &mut TestAppContext) -> (Entity<FennecWindow>, &mut VisualTestContext) {
    cx.update(gpui_base::init);
    let (app, cx) = cx.add_window_view(|window, cx| {
        let (_, receive) = async_channel::unbounded();
        let mut app = FennecWindow::build(receive, window, cx, false);
        app.projects = vec![Project { id: "fennec".into(), name: "Fennec".into(), key: "FEN".into(), ..Default::default() }];
        app.requests.insert("fennec".into(), vec![request(1), request(2)]);
        app.route = Some(Route::Project("fennec".into()));
        app
    });
    (app, cx)
}

fn request(number: u64) -> Request {
    Request { number, title: format!("Request {number}"), status: "queued".into(), ..Default::default() }
}

#[gpui::test]
fn queue_column_starts_at_the_window_edge_under_the_title_bar(cx: &mut TestAppContext) {
    let (_app, cx) = window(cx);
    cx.simulate_resize(size(px(1200.0), px(800.0)));
    cx.run_until_parked();
    assert!(cx.debug_bounds("sidebar-column").is_none());
    let title = cx.debug_bounds("queue-switcher").expect("queue switcher in the title bar");
    let column = cx.debug_bounds("queue-column").expect("queue column");
    assert_eq!(column.left(), px(0.0));
    assert!(column.top() >= title.bottom(), "the column sits under the title bar");
    assert!(cx.debug_bounds("new-request").is_some());
}

#[gpui::test]
fn cmd_n_focuses_the_composer_and_keeps_the_thread_open(cx: &mut TestAppContext) {
    let (app, cx) = window(cx);
    cx.update(|_, cx| {
        app.update(cx, |app, cx| {
            app.open = Some(1);
            app.thread = Some(request(1));
            cx.notify();
        })
    });
    cx.run_until_parked();
    cx.update(|window, cx| app.update(cx, |app, cx| app.focus_new_request(window, cx)));
    cx.run_until_parked();
    cx.update(|window, cx| {
        let app = app.read(cx);
        assert_eq!(app.open, Some(1), "the thread beside the list stays open");
        assert!(app.composer.read(cx).focus_handle(cx).is_focused(window));
    });
}
