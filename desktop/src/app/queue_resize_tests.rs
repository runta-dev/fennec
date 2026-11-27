//! THI-153: the queue's request list resizes against the thread beside it and keeps its width.

use super::{FennecWindow, Route};
use crate::board::{Project, Request};
use gpui::{
    Entity, Modifiers, MouseButton, TestAppContext, VisualTestContext, point, px,
};

fn window(cx: &mut TestAppContext) -> (Entity<FennecWindow>, &mut VisualTestContext) {
    cx.update(gpui_base::init);
    let (app, cx) = cx.add_window_view(|window, cx| {
        let (_, receive) = async_channel::unbounded();
        let mut app = FennecWindow::build(receive, window, cx, false);
        app.projects = vec![Project {
            id: "this".into(),
            key: "THI".into(),
            name: "Fennec".into(),
            ..Default::default()
        }];
        app.requests.insert(
            "this".into(),
            vec![Request {
                number: 153,
                title: "Resize".into(),
                status: "working".into(),
                ..Default::default()
            }],
        );
        app.route = Some(Route::Project("this".into()));
        app
    });
    cx.simulate_resize(gpui::size(px(1200.0), px(800.0)));
    cx.run_until_parked();
    (app, cx)
}

fn width(cx: &mut VisualTestContext) -> f32 {
    cx.run_until_parked();
    f32::from(cx.debug_bounds("queue-column").expect("queue column").size.width)
}

fn resize_queue(cx: &mut VisualTestContext, delta: f32) {
    cx.run_until_parked();
    let bounds = cx.debug_bounds("queue-column").unwrap();
    let start = point(bounds.right() - px(2.0), bounds.top() + px(300.0));
    let end = start + point(px(delta), px(0.0));
    let step = if delta > 0.0 { 10.0 } else { -10.0 };
    cx.simulate_mouse_down(start, MouseButton::Left, Modifiers::default());
    cx.simulate_mouse_move(start + point(px(step), px(0.0)), Some(MouseButton::Left), Modifiers::default());
    cx.simulate_mouse_move(end, Some(MouseButton::Left), Modifiers::default());
    cx.simulate_mouse_up(end, MouseButton::Left, Modifiers::default());
    cx.run_until_parked();
}

#[gpui::test]
fn queue_edge_drag_resizes_clamps_and_queues_one_save(cx: &mut TestAppContext) {
    let (app, cx) = window(cx);
    // Automatic: a quarter of the 1122 px beside the 72 px rail and the
    // panel's inset, so list and thread split 1:3 (FEN-209).
    assert_eq!(width(cx), 281.0);
    resize_queue(cx, 100.0);
    assert_eq!(width(cx), 381.0);
    app.read_with(cx, |app, _| {
        assert_eq!(app.app_preferences.value.queue_width, 381);
        assert_eq!(app.queue_resize.pending, Some(381));
    });
    resize_queue(cx, -1000.0);
    assert_eq!(width(cx), 240.0);
    // At most 720 px, and the thread keeps 360 px.
    resize_queue(cx, 2000.0);
    assert_eq!(width(cx), 720.0);
    // A narrower window shrinks a saved width without forgetting it: 900
    // minus the rail, the panel's inset (FEN-125) and the thread's 360.
    cx.simulate_resize(gpui::size(px(900.0), px(800.0)));
    assert_eq!(width(cx), 462.0);
    app.read_with(cx, |app, _| assert_eq!(app.app_preferences.value.queue_width, 720));
}

#[gpui::test]
fn queue_resize_escape_restores_automatic_width_without_saving(cx: &mut TestAppContext) {
    let (app, cx) = window(cx);
    let auto = width(cx);
    let bounds = cx.debug_bounds("queue-column").unwrap();
    let start = point(bounds.right() - px(2.0), bounds.top() + px(300.0));
    cx.simulate_mouse_down(start, MouseButton::Left, Modifiers::default());
    cx.simulate_mouse_move(start + point(px(10.0), px(0.0)), Some(MouseButton::Left), Modifiers::default());
    cx.simulate_mouse_move(start + point(px(90.0), px(0.0)), Some(MouseButton::Left), Modifiers::default());
    cx.simulate_keystrokes("escape");
    cx.simulate_mouse_up(start + point(px(90.0), px(0.0)), MouseButton::Left, Modifiers::default());
    assert_eq!(width(cx), auto);
    app.read_with(cx, |app, _| {
        assert_eq!(app.app_preferences.value.queue_width, 0);
        assert_eq!(app.queue_resize.pending, None);
    });
}

#[gpui::test]
fn queue_resize_double_click_returns_to_automatic_width(cx: &mut TestAppContext) {
    let (app, cx) = window(cx);
    let auto = width(cx);
    resize_queue(cx, 80.0);
    let bounds = cx.debug_bounds("queue-column").unwrap();
    let at = point(bounds.right() - px(2.0), bounds.top() + px(300.0));
    cx.simulate_event(gpui::MouseDownEvent {
        position: at,
        button: MouseButton::Left,
        modifiers: Modifiers::default(),
        click_count: 2,
        first_mouse: false,
    });
    cx.simulate_mouse_up(at, MouseButton::Left, Modifiers::default());
    assert_eq!(width(cx), auto);
    app.read_with(cx, |app, _| assert_eq!(app.queue_resize.pending, Some(0)));
}
