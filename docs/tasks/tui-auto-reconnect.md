# Persistent TUI reattachment

Interactive terminal clients retain their terminal ownership when the local
daemon or cloud stream disconnects. Recovery attaches to the verified original
runtime, session, agent and terminal address; it never launches a replacement.
Noninteractive commands retain finite failure behavior.

The first recovery attempt is immediate. Subsequent delays double from one
second to a maximum of 5 seconds, with jitter between 80% and 100% of the delay.
There is no retry count limit and no count is displayed. A connection must provide
terminal output and remain stable for a minute before its backoff resets.

For the first three seconds of an outage, the last screen remains untouched.
Longer outages show a spinner and `Reconnecting`, plus `Retrying in Ns` while waiting, with
`Ctrl+C to detach`, on one terminal row. The overlay and its readiness watcher
stop before restored terminal output is written. Shared-connection readiness
and local network recovery wake a pending delay. The CLI owns recovery so a
daemon restart does not end its lifetime, and the launch deadline does not
apply to an already attached session.

After the three-second grace, the `Reconnecting` label uses a two-second
left-to-right shimmer with a cosine falloff, following Codex's TUI animation
timing. It blends the terminal's reported foreground/background colors when
true color is available, with dim/normal/bright text as a fallback. `NO_COLOR`
and dumb/nonterminal output disable the shimmer. Countdowns and
the Ctrl+C hint stay static. Width clipping happens before ANSI styling, and
the animation is cancelled and cleared before recovered terminal output.

One input reader owns the terminal throughout recovery. Disconnected input is
discarded, including buffered input from an earlier connection generation.
Ctrl+C cancels waits, local RPCs and attachment handshakes. Terminal modes are
restored on final exit, before the manual `fen attach` notice is printed.
Confirmed remote exit, sign-out, changed runtime and identity failures stop
recovery. A fresh daemon's initial account snapshot and a stale session cache
are not evidence that a session is gone.

The resident protocol gains optional structured attachment errors. A missing
session is only reported after a successful authoritative remote listing;
transport failures remain retryable. Older peers' generic attachment failures
remain retryable until the matching remote helper is deployed.

## Validation

- PTY subprocess tests exercise transient broker failure, stale Unix sockets,
  failed attachment handshakes, repeated recovery to the same session, offline
  input isolation, brief outages without screen restoration, readiness wakeups,
  and Ctrl+C during RPC, backoff and handshake. Cancellation is measured to the
  restored-terminal attach notice, excluding the race detector's exit delay.
- Control-socket replacement tests exercise the actual local RPC path,
  cancellation, readiness transitions and account/runtime isolation.
- Authoritative session-loss tests distinguish deletion and identity mismatch
  from unavailable or malformed listings.
- The four affected Go packages pass their full Linux suites. Focused macOS
  race tests and source/UI checks pass. Linux race checks require a C compiler,
  which is absent from the cloud development environment.

## Live daemon restart acceptance

The candidate CLI passed an actual stop/start of the installed macOS daemon on
2026-11-14. The installed app remained release `0.3.11150021`; the candidate ran
separately from `/tmp/fennec-reconnect-live`. This verifies recovery against the
existing release daemon, without installing the candidate daemon or remote helper.

`cmd/verify-reconnect` creates an owned Codex session through the real control
plane, attaches the candidate CLI in a PTY, types an unsubmitted draft, restarts
the installed daemon, and verifies another unsubmitted input through both the
same CLI and an authoritative cloud readback. It checks the daemon PID changed
while its build stayed identical, the CLI PID survived, the receipt and cloud
terminal address/creation time stayed identical, and the original draft survived.
Only its own session is stopped during cleanup. No model prompt is submitted.

The final run changed daemon PID 29582 to 32170 while CLI PID 32048 survived.
The owned terminal `t3k9` retained its identity. Recovery through verified input
took 25,537 ms, including daemon startup and cloud connection restoration. The
then-current 15-second cap applied to each backoff delay, not total recovery time. Loading and
countdown were observed, without restoring the local terminal during the outage.
Ctrl+C after recovery restored terminal modes before the attach notice in 1 ms.
Cleanup was confirmed, and the installed daemon's shared connection was ready.

Two earlier runs exposed verifier issues: creation time belongs to the session
listing, not `sessions.read`, and the final PTY output must be checked when EOF
arrives before the next poll. Both were fixed before the complete passing run.

For a deliberate live restart, build this command and run it with
`-binary <absolute-candidate-cli-path> -restart-daemon`. Run the verifier detached
from SSH and save its output, because restarting the daemon also interrupts the
Mac connector. The default private terminal transcript is
`/tmp/fennec-reconnect-live/terminal.log`; create its directory first or pass
`-terminal-log <path>`. Inspect an uncertain launch's printed ID before retrying.

## Shim and update acceptance

Recovery lives in the local CLI process reached by both `dispatch-agent` (PATH
shims) and `dispatch-entry` (managed native entries). A lost daemon socket does
not end that process or return the user to their shell. The CLI retains its PTY
and attaches the same confirmed cloud session through the restored daemon.

The daemon now refreshes managed shims and existing managed entries as soon as
it owns its control socket, before credential restoration or cloud preparation.
Old paths are migrated to the installed bundle's `Contents/MacOS/fen`. A custom
wrapper is preserved and reported in private diagnostics; it does not prevent
other owned wrappers from being refreshed. Standalone development daemons do
not redirect installed shims. Normal broker startup continues to refresh shell
integration and routing preferences as before.

Upgrade tests run the automatic updater's bundle installation path, then execute
every supported harness's preexisting shim to verify it reaches the replacement
CLI without restarting the shell. Separate offline refresh tests cover old
application paths, managed native entry migration, original executable retention,
and preservation of user-edited wrappers without blocking other shim updates.

An additional real macOS daemon restart passed through the generated production
PATH shim: `zsh → codex → dispatch-agent → run-agent`, rather than starting with
`fen attach`. Run the verifier with `-shim` to exercise this path. The shim is
generated in a private PATH directory and points at the candidate binary; it
does not redirect the user's installed release shims. The owned session was
`bloom-body` at `t3m1`; CLI PID 47337 survived daemon PID 32170 → 47359, with the
same release build. Loading and countdown were observed; input
was restored and read back remotely in 28,380 ms. Ctrl+C after recovery detached
in 1 ms, and the owned session was cleaned up.

Newly launched or reattached clients use the updated bundle after installation.
An already running old CLI cannot acquire new reconnect code through a bundle
replacement; users may need to attach it once after their first upgrade to this
feature. The release must include these CLI changes, not just a new daemon.

## Network event wakeups

Both the recovering CLI and the shared daemon observe local network changes,
including transitions that leave the online boolean true. On macOS a read-only
AF_ROUTE subscription receives route, interface and IP address changes. This
covers VPN route/interface changes and Wi-Fi/Ethernet path changes. ARP/neighbor
cache entries and traffic-created cloned routes are excluded. No independent SSH
connection or remote probe is opened, and healthy connections are not closed
merely because an interface changed.

A one-second local fingerprint check supplements kernel events and works if the
subscription is unavailable. It compares interface flags/addresses and the
system resolver file. A delayed timer after sleep also wakes recovery even if
addresses are unchanged. Scoped VPN DNS changes not reflected in that file need
a corresponding route/interface event; this is not a complete DNS reachability
monitor. Topology events do not prove internet access or captive-portal login.

Notifications are coalesced for 300 ms and emitted at most once per second,
with one queued wake per subscriber. They interrupt the existing backoff without
starting concurrent attachment attempts; failed attempts retain bounded backoff.
Cancellation closes the CLI watcher after recovery or Ctrl+C. Tests cover an
online-to-online event, bursts, polling fallback, shared daemon notification,
CLI wakeups while the daemon is still unavailable, and Darwin routing filters.

Remaining validation boundaries: physical network/VPN switching, Ctrl+C during a live
outage, and interactive harnesses other than Codex have not been tested live.
Cancellation during recovery and the other failure cases above are covered by
PTY/control-socket tests. Release publication remains pending.

## Installed five-second build acceptance

The retry cap is now five seconds: delays grow through 1, 2, 4, and 5 seconds,
with 80–100% jitter. Individual connection/handshake timeouts remain separate
from this delay cap. Network events can interrupt a pending delay early.

Built and installed the complete `0.3.11150238-dev` bundle at
`/Applications/Fennec.app`, signed with the existing `Fennec Local Development`
identity. The cached credential helper was reused byte-for-byte (SHA-256
`f69a5f6c9bd8a928771bf151f564ff4c25c065df8ee390bad8291a0dbad539fe`).
The previous release is backed up at
`/Applications/.fennec-update-4248093834/Fennec.app`.
The installed daemon build ID is
`9a5a2e4dfa5027596564a661d8dd9041d74c7cb91617027ec94652d22eaee13e`.
Credential restoration and cloud readiness were confirmed after installation.

`cmd/verify-reconnect -installed-shim -restart-daemon` now uses the existing
`~/.local/share/fennec/shims/codex`, verifies its installed binary target, and
executes the actual installed CLI through `zsh → codex → dispatch-agent`.
The installed-build run passed: CLI PID 13740 survived daemon PID 8824 → 13762;
owned session `cord-great` retained terminal `t3q5`, its creation time, receipt,
and unsubmitted draft. Restored input was verified in authoritative cloud output.
Recovery took 24,542 ms including daemon startup and cloud restoration. Loading
and countdown appeared without a retry counter or terminal restoration during
the outage. Ctrl+C after recovery restored the terminal before the attach hint
in 1 ms. The owned test session was stopped successfully.

The three preexisting sessions (`grain-jolly`, `antenna-large`, and
`football-carpet`) remained live with identical addresses and creation times.
Mac race suites for launcher, connectivity, and CLI passed, as did `make check`,
deep/strict signature verification, and the full package verifier including
Chromium renderer and credits smoke checks. Linux launcher tests also passed.
Mac evidence is under `/tmp/fennec-five-build/` (`build.log`, `tests.log`,
`check.log`, `deploy.log`, `e2e.log`, and `installed-terminal.log`).
