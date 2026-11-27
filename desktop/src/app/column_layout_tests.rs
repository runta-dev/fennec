//! FEN-106: a title bar (traffic lights, search) over a narrow rail
//! (terminal, queues, settings) that replaces the sidebar; the queue column
//! and the thread beside it fill the rest.

use super::{FennecWindow, Route, rail::RAIL_WIDTH};
use crate::board::{Project, Request};
use gpui::{Entity, Focusable as _, Modifiers, TestAppContext, VisualTestContext, px, size};

fn window(cx: &mut TestAppContext) -> (Entity<FennecWindow>, &mut VisualTestContext) {
    cx.update(gpui_base::init);
    let (app, cx) = cx.add_window_view(|window, cx| {
        let (_, receive) = async_channel::unbounded();
        let mut app = FennecWindow::build(receive, window, cx, false);
        app.projects = ["fennec", "website"]
            .map(|id| Project { id: id.into(), name: id.into(), key: "FEN".into(), needs_you: 2, ..Default::default() })
            .to_vec();
        app.requests.insert("fennec".into(), vec![request(1), request(2)]);
        app.requests.insert("website".into(), Vec::new());
        app.route = Some(Route::Project("fennec".into()));
        app
    });
    cx.simulate_resize(size(px(1200.0), px(800.0)));
    cx.run_until_parked();
    (app, cx)
}

fn request(number: u64) -> Request {
    Request { number, title: format!("Request {number}"), status: "queued".into(), ..Default::default() }
}

#[gpui::test]
fn rail_holds_terminal_queues_and_settings(cx: &mut TestAppContext) {
    let (_app, cx) = window(cx);
    let rail = cx.debug_bounds("rail").expect("rail");
    assert_eq!(rail.left(), px(0.0));
    assert_eq!(rail.size.width, px(RAIL_WIDTH));
    let terminal = cx.debug_bounds("rail-terminal").expect("terminal button");
    let fennec = cx.debug_bounds("rail-queue-fennec").expect("queue icon");
    let website = cx.debug_bounds("rail-queue-website").expect("queue icon");
    let settings = cx.debug_bounds("rail-settings").expect("settings button");
    let title = cx.debug_bounds("titlebar").expect("title bar");
    assert_eq!(title.left(), px(0.0));
    assert_eq!(title.size.width, px(1200.0));
    assert!(rail.top() >= title.bottom(), "the rail starts under the title bar");
    let search = cx.debug_bounds("titlebar-search").expect("search in the title bar");
    assert!((search.center().x - px(600.0)).abs() < px(1.0), "search is centered");
    assert!(terminal.bottom() <= fennec.top() && fennec.bottom() <= website.top());
    assert!(settings.bottom() > px(700.0), "settings sits at the bottom");
    let column = cx.debug_bounds("queue-column").expect("queue column");
    assert_eq!(column.left(), px(RAIL_WIDTH));
    assert!(column.top() >= title.bottom());
    assert!(cx.debug_bounds("new-request").is_some());
}

#[gpui::test]
fn clicking_a_rail_queue_switches_to_it(cx: &mut TestAppContext) {
    let (app, cx) = window(cx);
    let website = cx.debug_bounds("rail-queue-website").unwrap();
    cx.simulate_click(website.center(), Modifiers::default());
    cx.run_until_parked();
    app.read_with(cx, |app, _| assert!(app.route == Some(Route::Project("website".into()))));
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
