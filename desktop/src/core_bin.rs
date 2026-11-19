//! The herdr-core the terminal core runs (`hc connect`, `hc put`).
//!
//! Sessions are the cloud computer's core: `FENNEC_HC` names a program that
//! behaves like `hc` (on a Mac, `fen hc` over Fennec's resident connection);
//! without it, this machine's `hc`. Local shell tabs are this Mac's own core,
//! Fennec's isolated instance (`FENNEC_LOCAL_HC`, herdr-core bundled in the
//! app with HC_SESSION=fennec); `local` connects to it.

use std::cell::RefCell;
use std::path::PathBuf;

thread_local! {
    static OVERRIDE: RefCell<Option<PathBuf>> = const { RefCell::new(None) };
}

pub fn path() -> PathBuf {
    if let Some(p) = OVERRIDE.with(|o| o.borrow().clone()) {
        return p;
    }
    if !cloud() {
        if let Some(local) = local() {
            return local;
        }
    }
    std::env::var_os("FENNEC_HC")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("hc"))
}

/// This Mac's core for local shells, when Fennec bundles one.
pub fn local() -> Option<PathBuf> {
    std::env::var_os("FENNEC_LOCAL_HC").map(PathBuf::from)
}

/// Runs `f` (a `Core::connect`) against another core program.
pub fn using<R>(program: PathBuf, f: impl FnOnce() -> R) -> R {
    let previous = OVERRIDE.with(|o| o.replace(Some(program)));
    let result = f();
    OVERRIDE.with(|o| *o.borrow_mut() = previous);
    result
}

/// Cloud work is an explicit, separate window; the default needs no network.
pub fn cloud() -> bool {
    std::env::args().any(|arg| arg == "--cloud")
}
