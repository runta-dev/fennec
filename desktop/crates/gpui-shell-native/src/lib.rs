#![allow(dead_code)] // the vendored files carry script-shell parts Fennec does not use
//! herdr-app's terminal core imports `gpui_shell` (gpui-kit's script shell) for
//! three native pieces: the key action type, file drags that keep their machine,
//! and the runtime metrics counters. Fennec runs no scripts, so this crate builds
//! just those, from the vendored gpui-kit sources, unchanged.

#[path = "../../../vendor/gpui-kit/crates/shell/src/action.rs"]
pub mod action;
#[path = "../../../vendor/gpui-kit/crates/shell/src/drag_paths.rs"]
mod drag_paths;
#[path = "../../../vendor/gpui-kit/crates/shell/src/metrics.rs"]
pub mod metrics;

pub use drag_paths::DraggedPaths;
