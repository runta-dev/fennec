//! THI-195: queue rows in the sidebar keep the session row height and text column,
//! even when the sessions overflow the scroll column.

use super::{EventLines, FennecWindow, ROW_HEIGHT, sessions};
use serde_json::json;
use crate::board::Project;
use gpui::{TestAppContext, px};

#[gpui::test]
fn queue_rows_match_session_rows_when_sessions_overflow(cx: &mut TestAppContext) {
    cx.update(gpui_base::init);
    let (_app, cx) = cx.add_window_view(|window, cx| {
        let (_, receive) = async_channel::unbounded();
        let mut app = FennecWindow::build(receive, window, cx, false);
        app.projects = ["this", "test"]
            .map(|id| Project { id: id.into(), name: id.into(), needs_you: 1, ..Default::default() })
            .to_vec();
        let records: Vec<_> = (0..60)
            .map(|i| json!({"address":format!("s{i}"),"state":"live","title":format!("Agent {i}"),
                "labels":{"fennec.session":format!("s{i}")},"status":{"app":"codex","state":"idle"},"created_ms":i}))
            .collect();
        let records = json!(records);
        app.events = Some(EventLines::new(&json!({"terminals":records,"seq":1})));
        app.sessions = sessions::from_terminals(records.as_array().unwrap());
        app
    });
    cx.simulate_resize(gpui::size(px(1000.0), px(500.0)));
    cx.run_until_parked();
    let session = cx.debug_bounds("session-row-s0").expect("session row");
    for id in ["this", "test"] {
        let row = cx.debug_bounds(&format!("project-row-{id}")).expect("project row");
        assert_eq!(row.size.height, px(ROW_HEIGHT), "queue row {id} shrank");
        assert_eq!(row.size.height, session.size.height);
        assert_eq!(row.left(), session.left());
    }
}
