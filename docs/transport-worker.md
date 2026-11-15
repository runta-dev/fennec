# Application restart and retained SSH transport

The status-menu **Restart** restarts the native application and its daemon. An
independent, one-shot launchd job waits for the old UI to exit, replaces the
observed daemon under the shared startup lock, verifies the installed backend,
and opens the installed application. Reconnecting shims share that lock. A daemon
already replaced by another caller is not restarted again. Repeated requests
from one UI process coalesce. Scheduling failure leaves the UI open; a UI that
does not exit within 15 seconds cancels the restart. Startup failure still reopens
the UI so its normal connection recovery can run. Internal failures are recorded
in `~/.config/fennec/ipc/app-restart.log`.

Only the transport worker is retained as daemon infrastructure. Cloud sessions
and CLI clients keep their own lifetimes. The worker keeps the authenticated
upstream SSH connection; daemon-owned channels, reverse-SSH service and resident
application state still reconnect. Retaining TCP does **not** promise uninterrupted
reverse-SSH commands or zero reconnect latency during a daemon restart.

## Transport-only boundary

The worker owns pinned SSH authentication, channel forwarding, transport health,
network observation, empty session-channel prewarming and lease/idle lifecycle.
It does not interpret or replay commands, own agent processes, migrate model
credentials, select models, call account APIs, or make approval decisions. It
reads the pinned SSH private key, so it is not credential-free; it never receives
the account API token or model-provider credentials. Fresh API ownership and key
validation remain the daemon's responsibility before commands are permitted.
An architecture test prevents imports of application services into the worker.

Both initial preparation and network recovery use the same retained dialer.
Previously, broker recovery used direct SSH, allowing the worker to expire and
making a later daemon restart pay for a new upstream connection.

## Compatibility contract

`workerCompatibilityEpoch` in `internal/sshcarrier/compatibility.go` versions the
worker independently of the application build. Epoch 5 preserves the existing
v5 socket/config/launchd identities. Local leases authenticate using the
`fennec-transport-v1` SSH username, a pinned key and a bounded handshake.

| Change | Required behavior |
| --- | --- |
| UI, model, credentials, daemon or resident application protocol | Keep the worker epoch; application protocols remain opaque channel bytes |
| Endpoint, account/token scope, runtime/user, host/client key, key path or TCP policy | Existing configuration digest selects a separate worker |
| Incompatible local wire format, configuration or forwarding semantics | Bump the epoch; bump the lease protocol name for a wire change |
| Mandatory worker security/liveness fix | Bump the epoch even if the wire format remains compatible, so clients do not retain the unfixed worker |

Never fall back to a previous epoch's socket when a new worker fails. New epochs
use separate launchd jobs without killing workers with active leases. An old
worker exits after its last lease has been absent for 30 seconds; logout or an
environment change explicitly revokes retained workers. Ordinary restart does
not revoke them. Epoch changes can require a fresh WAN SSH handshake.

This is an explicit upgrade strategy, not a promise that transport code can never
need a breaking change. Review every worker change against this table. Compatibility
tests preserve the existing v5 namespace, reject incompatible leases without
disrupting peers, and verify daemon replacement reuses one upstream connection.

## Validation

Cloud CLI/runta/sshcarrier tests and vet passed, as did the full Mac race suites
for those packages, source/localization checks, UI checks, control-contract
packaging and signed-bundle verification. One initial Linux credential-watcher
timing test failed, then passed five focused repeats and the subsequent full
suite; no watcher implementation changed here. The Mac cancellation regression
also exposed a real platform difference (signaling PID 1 is not permitted), fixed
by checking cancellation before polling process liveness.

The final development package is installed on the Mac. Two end-to-end coordinator
runs replaced the UI and daemon while retaining the exact worker and upstream TCP
connection. Original sessions and fresh input survived. Recovery took 4.627 and
5.439 seconds, exceeding the unchanged performance gate. Native menu click
automation was blocked by macOS assistive-access permissions; the installed
menu's Go coordinator was exercised directly. See the
[performance ledger](performance-baseline.md#app-and-daemon-restart-acceptance).
