//! The herdr-core the window talks to. Fennec's agents run in the cloud
//! computer's core: `FENNEC_HC` names a program that behaves like `hc`
//! (`hc connect` on the cloud computer over Fennec's relay); without it,
//! this machine's `hc`.

use std::path::PathBuf;

pub fn path() -> PathBuf {
    std::env::var_os("FENNEC_HC")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("hc"))
}
