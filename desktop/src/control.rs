//! The Mac daemon's control socket (~/.config/fennec/ipc/control.sock):
//! one JSON line `{version, id, method, params}` per connection, one reply
//! line `{version, id, result | error}`. Same protocol as
//! native/macos/ControlClient.swift.

use anyhow::{Context as _, Result, anyhow, bail};
use serde_json::{Value, json};
use std::io::{BufRead, BufReader, Write};
use std::path::PathBuf;
use std::time::Duration;

pub fn socket() -> PathBuf {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .unwrap_or_default()
        .join(".config/fennec/ipc/control.sock")
}

pub fn available() -> bool {
    socket().exists()
}

/// Blocking: call from a background task.
#[cfg(unix)]
pub fn call(method: &str, params: Value) -> Result<Value> {
    let mut stream =
        std::os::unix::net::UnixStream::connect(socket()).context("Fennec isn't running")?;
    stream.set_read_timeout(Some(Duration::from_secs(30)))?;
    stream.set_write_timeout(Some(Duration::from_secs(30)))?;
    let id = format!(
        "desktop-{}",
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos())
            .unwrap_or(0)
    );
    let mut line =
        serde_json::to_vec(&json!({"version": 1, "id": id, "method": method, "params": params}))?;
    line.push(b'\n');
    stream.write_all(&line)?;
    let mut reply = String::new();
    BufReader::new(stream).take(8 << 20).read_line(&mut reply)?;
    let reply: Value = serde_json::from_str(&reply).context("invalid reply from Fennec")?;
    if reply["id"] != id {
        bail!("invalid reply from Fennec");
    }
    if let Some(error) = reply.get("error").filter(|e| !e.is_null()) {
        return Err(anyhow!(
            "{}",
            error["message"]
                .as_str()
                .unwrap_or("Fennec could not do that")
        ));
    }
    Ok(reply["result"].clone())
}

#[cfg(not(unix))]
pub fn call(_: &str, _: Value) -> Result<Value> {
    bail!("Fennec isn't running")
}

use std::io::Read as _;
