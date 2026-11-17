# Cloud model gateway lifetime

Fennec owns one `fennec-magpie.service` per cloud computer. systemd starts
`magpie serve` directly, restarts failed processes, bounds crash loops, and
contains all gateway/plugin children in its own cgroup. Neither SSH nor a Mac
process owns its lifetime. An explicit stop is not an automatic restart request.

`fennec-magpie-health.timer` invokes the Go `magpie-service check` command every
five seconds after the previous check completes. The helper is an atomically
installed copy of the terminal executable, independent of later terminal
symlink replacement. Non-root installation requires an already persistent
systemd user manager; unsupported machines fail preparation rather than silently
falling back to an unsupervised process.

## Health and recovery

Magpie's pinned build has no health endpoint. The checker requests the deliberately
unknown local `/_fennec/liveness` route and requires its structured `404` with
`error.type=not_found_error`. This exercises the HTTP server without invoking a
model or refreshing a provider catalog. `/v1/models` validation remains part of
configuration preparation. Upstream authentication, rate limits and inference
failures do not drive local restarts.

Three consecutive failed checks trigger a service restart. Health recovery is
limited to three restarts in two minutes; systemd independently limits process
starts to five in two minutes. The checker retains the recovery budget across
successful checks. A tripped circuit remains failed until explicit preparation
recovery. An unresponsive process receives the same bounded graceful stop as a
configuration restart, followed by systemd's service-scoped cleanup.

State lives in private `~/.config/fennec/gateway-state.json`. Only its status is
included in the existing resident-channel session listing; timestamps and failure
counters never invalidate the desktop's high-frequency state snapshot. A stale
healthy observation becomes unknown. UI feedback occupies a fixed slot, preserves
session rows, and offers the existing runtime retry action after failure.

## Mode and session ownership

Gateway desire is stored separately from configuration readiness. Mode changes
and timer recovery share a cloud-local lifecycle lock. Turning Magpie off never
installs or probes it when no process depends on it. Existing gateway agents
carry `FENNEC_GATEWAY_DEPENDENCY=1`; the checker derives dependencies from live
processes rather than a persisted reference count. Legacy cloud workspace agents
without isolated native configuration are retained conservatively. While a live
old session depends on the gateway, it remains supervised; after the last
session exits, the timer disables itself and the gateway service.

Native credential copying inside a Magpie synchronization does not change the
routing mode. Only an explicit mode application changes it. New native sessions
use isolated native configuration. Existing requests are never replayed by the
recovery layer, and no agent process pool is prewarmed.

The CPU scheduler excludes the systemd gateway cgroup, including plugin agent
children, from per-session classification. Session shutdown cannot kill the
shared gateway; gateway restart cannot kill user terminal sessions. In-flight
model requests may nevertheless fail when their gateway crashes.

## Configuration and upgrade

Model-account changes hold the shared Magpie configuration lock. Configuration is
written atomically before requesting a service restart. Restart failure is
returned to the caller; there is no process-name `pkill` and no successful result
on a readiness timeout. Unchanged plugin configuration does not restart anything.
The lifecycle lock serializes installation, mode changes, and health recovery.

Preparation retires only the verified legacy supervisor and waits for its own
Magpie child to drain. Unmanaged listeners are neither killed nor adopted.
Existing installed gateway executables are reused; executable replacement is
checked against the systemd main process's inode before a draining restart.
Credentials, model choices and terminal sessions are not reset.

## Verification

Run the gateway tests on Linux as well as the setup/runtime tests on the Mac.
Tests cover local-response validation, stale observations, recovery coalescing,
consecutive failure reset, the persistent restart circuit, off-mode dependency
draining, lock cancellation, configuration-before-restart ordering, and native
credential synchronization without a transient mode switch.

Live acceptance must check service/timer ownership, SSH-independent recovery,
unchanged user session processes, and real Codex and Claude requests. Fault
injection intentionally affects the gateway and can interrupt a model request;
never use session termination as a substitute. Warm session/model operation
latency remains governed by `make test-e2e-ops`; recovery duration is a separate
boundary and is not a successful warm-operation sample.
