use super::{FennecWindow, Route};
use crate::board::{Attachment, Project, Request, Scope};
use gpui::TestAppContext;

#[gpui::test]
fn computer_switch_isolates_queue_views_and_restores_drafts(cx: &mut TestAppContext) {
    cx.update(gpui_base::init);
    let (app, cx) = cx.add_window_view(|window, cx| {
        let (_, receive) = async_channel::unbounded();
        FennecWindow::build(receive, window, cx, false)
    });
    cx.update(|window, cx| {
        app.update(cx, |app, cx| {
            app.switch_board_scope(Scope("a".repeat(64)), window, cx);
            app.projects.push(Project {
                id: "starter".into(),
                ..Default::default()
            });
            app.route = Some(Route::Project("starter".into()));
            app.open = Some(1);
            app.thread = Some(Request {
                number: 1,
                title: "Private A".into(),
                ..Default::default()
            });
            app.thread_cache
                .insert(("starter".into(), 1), app.thread.clone().unwrap());
            app.deleted_projects.insert("old-a".into());
            app.composer
                .update(cx, |input, cx| input.set_value("A draft", window, cx));
            app.reply
                .update(cx, |input, cx| input.set_value("A reply", window, cx));
            app.request_images.by_queue.insert(
                "starter".into(),
                vec![Attachment {
                    id: "a-image".into(),
                    ..Default::default()
                }],
            );
            app.switch_board_scope(Scope("b".repeat(64)), window, cx);
            assert!(app.projects.is_empty());
            assert!(app.requests.is_empty());
            assert!(app.thread_cache.is_empty());
            assert!(app.deleted_projects.is_empty());
            assert!(app.thread.is_none() && app.open.is_none());
            assert!(app.composer.read(cx).value().is_empty());
            assert!(app.reply.read(cx).value().is_empty());
            assert!(app.request_images.by_queue.is_empty());
            app.composer
                .update(cx, |input, cx| input.set_value("B draft", window, cx));
            app.switch_board_scope(Scope("a".repeat(64)), window, cx);
            assert_eq!(app.composer.read(cx).value().as_ref(), "A draft");
            assert_eq!(app.reply_drafts["reply:starter:1"], "A reply");
            assert_eq!(app.request_images.by_queue["starter"][0].id, "a-image");
            assert!(app.route == Some(Route::Project("starter".into())));
            app.switch_board_scope(Scope("b".repeat(64)), window, cx);
            assert_eq!(app.composer.read(cx).value().as_ref(), "B draft");
        });
    });
}
