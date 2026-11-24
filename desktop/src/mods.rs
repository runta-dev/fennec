//! What the terminal core asks of herdr-app's `mods` module, answered by
//! Fennec's window: toasts it wants shown, and the live terminals for
//! Cmd-click on an address. Fennec has no script mods.

use serde_json::Value;
use std::cell::{Cell, RefCell};
use std::sync::OnceLock;

pub enum Request {
    /// herdr-app's overlay request; the terminal core only opens toasts with it.
    #[allow(dead_code)]
    OpenOverlay { id: String, spec: Value },
}

static REQUESTS: OnceLock<async_channel::Sender<Request>> = OnceLock::new();

/// The window's end of the requests; call once at start.
pub fn requests() -> async_channel::Receiver<Request> {
    let (tx, rx) = async_channel::unbounded();
    let _ = REQUESTS.set(tx);
    rx
}

pub fn request(r: Request) {
    if let Some(tx) = REQUESTS.get() {
        let _ = tx.try_send(r);
    }
}

thread_local! {
    static OVERLAY_SEQ: Cell<u64> = const { Cell::new(0) };
    static TERMINALS: RefCell<Vec<String>> = const { RefCell::new(Vec::new()) };
}

pub fn new_overlay_id() -> String {
    OVERLAY_SEQ.with(|s| {
        s.set(s.get() + 1);
        format!("o{}", s.get())
    })
}

/// The live terminals' addresses, as the core reports them.
pub fn publish_terminals(v: Value) -> bool {
    let list: Vec<String> = v
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|t| t["address"].as_str().map(str::to_owned))
        .collect();
    TERMINALS.with(|t| {
        let changed = *t.borrow() != list;
        *t.borrow_mut() = list;
        changed
    })
}

pub fn terminal_address(word: &str, source: &str) -> Option<String> {
    TERMINALS.with(|t| {
        crate::links::terminal_address(word, source, t.borrow().iter().map(String::as_str))
    })
}
