//! Isolated visual fixtures for comparing the native renderer with the design.
//! No account, daemon, terminal process or production board is used in this mode.
use serde_json::Value;
use std::sync::LazyLock;

static FIXTURE: LazyLock<Option<Value>> = LazyLock::new(|| {
    let path = std::env::var_os("FENNEC_DESKTOP_PREVIEW")?;
    let bytes = std::fs::read(path).ok()?;
    serde_json::from_slice(&bytes).ok()
});
pub fn active() -> bool {
    FIXTURE.is_some()
}
pub fn terminals() -> Vec<Value> {
    FIXTURE
        .as_ref()
        .and_then(|f| f["terminals"].as_array())
        .cloned()
        .unwrap_or_default()
}
pub fn call(method: &str, path: &str) -> Option<anyhow::Result<Value>> {
    let fixture = FIXTURE.as_ref()?;
    Some(if method != "GET" {
        Err(anyhow::anyhow!("Visual preview is read-only"))
    } else {
        fixture["responses"]
            .get(path)
            .cloned()
            .ok_or_else(|| anyhow::anyhow!("No preview fixture for {path}"))
    })
}
