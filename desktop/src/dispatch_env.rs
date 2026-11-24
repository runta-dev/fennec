//! Markers a Fennec agent launch leaves in its environment, kept out of the
//! desktop window's terminals.
//!
//! `fen` marks an agent it runs on this Mac with `FENNEC_DISPATCHED`, and a
//! command the cloud computer runs here through the relay with
//! `FENNEC_MAC_RELAY` (`internal/launcher/stable.go`). Either one makes every
//! `claude`/`codex`/... shim run the agent locally instead of handing it to the
//! cloud computer. They describe the process that launched the window, not the
//! person's shells: a window started from such a process would otherwise pass
//! them to its herdr-core, which records its launcher's environment for every
//! terminal it starts, and no shell tab would ever be taken over.

/// The names `launcher.DispatchedEnv` and `launcher.RelayEnv` use.
pub const MARKERS: [&str; 2] = ["FENNEC_DISPATCHED", "FENNEC_MAC_RELAY"];

/// Removes the markers from this process's environment. Call it first in
/// `main`, before any thread or child process exists.
pub fn clear() {
    for marker in MARKERS {
        // SAFETY: runs at the top of `main`, while the process is still
        // single-threaded, so nothing reads the environment concurrently.
        unsafe { std::env::remove_var(marker) };
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;

    #[test]
    fn markers_match_the_launcher() {
        let launcher = include_str!("../../internal/launcher/stable.go");
        assert!(launcher.contains(r#"const DispatchedEnv = "FENNEC_DISPATCHED""#));
        assert!(launcher.contains(r#"const RelayEnv = "FENNEC_MAC_RELAY""#));
    }

    /// The child re-runs this test binary with the markers set; it clears them
    /// and starts a shell the way a terminal would, which must see neither.
    #[test]
    fn cleared_markers_do_not_reach_children() {
        if std::env::var_os("FENNEC_DISPATCH_ENV_CHILD").is_some() {
            clear();
            let shell = Command::new("/bin/sh")
                .args(["-c", r#"printf '%s|%s|%s' "${FENNEC_DISPATCHED-unset}" "${FENNEC_MAC_RELAY-unset}" "${FENNEC_HC-unset}""#])
                .output()
                .unwrap();
            print!("shell-saw:{}:end", String::from_utf8_lossy(&shell.stdout));
            return;
        }
        let output = Command::new(std::env::current_exe().unwrap())
            .args([
                "dispatch_env::tests::cleared_markers_do_not_reach_children",
                "--exact",
                "--nocapture",
                "--test-threads=1",
            ])
            .env("FENNEC_DISPATCH_ENV_CHILD", "1")
            .env("FENNEC_DISPATCHED", "1")
            .env("FENNEC_MAC_RELAY", "1")
            .env("FENNEC_HC", "/kept")
            .output()
            .unwrap();
        let stdout = String::from_utf8_lossy(&output.stdout);
        assert!(
            output.status.success(),
            "{stdout}{}",
            String::from_utf8_lossy(&output.stderr)
        );
        assert!(
            stdout.contains("shell-saw:unset|unset|/kept:end"),
            "{stdout}"
        );
    }
}
