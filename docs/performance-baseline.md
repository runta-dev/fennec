# Fennec internal performance baseline

Updated: 2026-11-15 UTC. This is the current measurement ledger, not a claim that
Fennec has reached its latency target. Keep failures and superseded measurements
visible. Unless a row says otherwise, the development build tested here is `0.3.11151959-dev` (v33), build ID
`65beaa5bd8d5ccbd60e33aa94cdf9e1ba099f4f0e1d70838bd844bad366fa18c`.
The source is `a229eb0` plus this recovery-latency changeset. Measurements use the
installed signed Mac application and its real shared cloud machine; existing
user sessions remain running. Local temporary-file tests and UI fixtures do not
count as network or application recovery measurements.

## Recovery and application lifecycle

The original goal baseline is `0.3.11150353-dev`. A local daemon ping, attachment
receipt, cached session list, or first replayed frame is not input-ready recovery.
Recovery must preserve the original CLI and cloud terminal, accept fresh input,
and verify that input by reading the cloud terminal. Three seconds is the current
recovery screening threshold, not the final physical-latency target.

| Operation and endpoint | Original baseline ms | Latest measurement ms | Build / samples | Acceptance |
| --- | ---: | ---: | --- | --- |
| App + daemon Restart coordinator → original TUI input-ready | Previously restarted UI only; not comparable | 4,627 / 5,439 | 0.3.11152159-dev, 2 | FAIL, exceeds 3,000 ms; both processes replaced, worker/TCP/session/input preserved |
| Daemon restart → original TUI input-ready | 7,754 / 7,825 / 4,869 | 5,221 | v33, 1 | FAIL, exceeds 3,000 ms; identity, draft and input readback passed |
| Daemon restart → local daemon responds | Not independently comparable | 269 | v33, same run | Diagnostic only |
| Daemon shutdown | Not independently comparable | 104 | v33, same run | Diagnostic only |
| Cloud preparation start → verified ready | 4,263 / 3,069 / 2,802 | 1,716 | v33, same run | Improved; not a substitute for TUI recovery |
| Reverse connector setup | 4,978 (v29 comparison) | 1,015 | v33, same run | Shared resident RPC; original reverse SSH preserved |
| Wi-Fi routing restored → original TUI input-ready | 4,367 | 3,375 | v33, 1 | FAIL, exceeds 3,000 ms; original session and input readback passed |
| Downloaded Update click → new app and original TUI input-ready | 15,026 | 4,573 | v22, 1, historical | FAIL, exceeds 3,000 ms; NOT retested on v33 |
| Ctrl+C → detached | 1 | 2 | v33, same restart run | PASS |

The immediately preceding v29 restart took 5,265 ms and its cloud preparation
2,897 ms. v31 preparation reached 1,665 ms, but that restart timed out waiting for
fresh input and must not be counted as a successful recovery sample. v33 fixes a
reproduced PTY nonblocking-mode defect and passed the input/identity checks, while
still failing the recovery latency screen.

### App and daemon Restart acceptance

`0.3.11152159-dev` on `1258c04` plus the `fix/menu-restart-daemon` changeset
replaces both processes while retaining the transport worker. Two live runs of
`verify-reconnect -installed-shim -restart-app -max-recovery 3s` preserved worker
PID **94283** and its established upstream TCP endpoint pair. Local daemon ping
returned after **224 / 236 ms**; input-ready recovery took **4,627 / 5,439 ms**.
Both failed the unchanged 3-second performance screen and passed the functional
checks: new UI/daemon PIDs, original CLI/cloud-terminal identity, retained draft,
fresh input verified by cloud readback, and Ctrl+C detach in 1 ms.

This executes the production coordinator invoked by the menu, then terminates the
old UI process. It does not automate the native menu click: macOS denied assistive
access to the automation tool. The second run used the final signed package with
the original cached credential helper restored byte-for-byte; the first staging
build had inadvertently rebuilt that helper in the new worktree. Four preexisting
user terminals retained their identities and creation times. Reverse SSH returned
without recovery guards firing. These results do not demonstrate a latency
improvement over the daemon-only v33 test, and Wi-Fi recovery and downloaded
updates were not retested in this change.

Raw results: [2026-11-15-menu-restart.json](benchmarks/2026-11-15-menu-restart.json).
Lifecycle and upgrade contract: [transport-worker.md](transport-worker.md).

### v33 restart breakdown

These intervals overlap; do not add them to invent an end-to-end measurement.

| Stage | Duration ms | Interpretation |
| --- | ---: | --- |
| Restore account | 60.725 | Local credential helper |
| Retained SSH lease | 3.765 | Existing transport worker, not fresh WAN authentication |
| Fresh ownership/key checks and connection resolution | 1,065.491 | Includes API identity check at 1,055.353 ms |
| Native account gather | 139.571 | Overlaps resident startup |
| Resident startup including prepared-runtime health | 600.235 | One SSH exec, health proof reused only for matching inputs |
| Complete preparation | 1,716.095 | No repeated gateway installation or credential migration |
| Connector resident operation round trip | 373.593 | Cloud work 10.944 ms |
| Connector setup including resident availability | 1,015.210 | Replaces repeated pair / relay / alias execs |
| CLI reattach start → receipt and first output | 1,051 | Cloud lookup 6.643 ms, first visible PTY output 34.987 ms |
| Restart → confirmed fresh input | 5,221 | Authoritative user-visible endpoint |

The gap after preparation remains unresolved. During this startup, otherwise
small resident requests took 1,177–1,845 ms, while steady-state reads in the next
suite took 185–379 ms. Diagnose startup contention and transfer/dispatch waits;
do not call the slower startup requests the network's physical limit.

## Warm operation baseline

Measured on v33, 2026-11-15 UTC, using `TestSessionOperations500ms`. Overall suite:
**FAIL**. Every warm sample must finish within **500 ms**; cold SSH is separate
with its existing 5,000 ms gate. Medians describe these small samples only, not a
percentile SLA. Do not average away a failure. Raw numeric samples are checked in
at [2026-11-15-v33-ops.json](benchmarks/2026-11-15-v33-ops.json).

| Operation | Samples | Min ms | Median ms | Max ms | Over budget |
| --- | ---: | ---: | ---: | ---: | ---: |
| account.status | 3 | 9.019 | 10.493 | 12.103 | 0/3 |
| agents.list | 3 | 11.197 | 11.241 | 31.325 | 0/3 |
| approvals.list | 3 | 8.703 | 8.947 | 9.716 | 0/3 |
| attach (usable TUI) | 3 | 236.191 | 240.213 | 246.473 | 0/3 |
| cold SSH | 1 | 4589.079 | 4589.079 | 4589.079 | 0/1 |
| connection.status | 3 | 10.435 | 10.591 | 13.035 | 0/3 |
| daemon.ping | 3 | 12.329 | 14.416 | 16.825 | 0/3 |
| launch (usable TUI) | 3 | 301.995 | 303.025 | 356.945 | 0/3 |
| model.set | 2 | 514.362 | 628.765 | 743.168 | 2/2 |
| resume (usable TUI) | 3 | 230.012 | 242.066 | 270.540 | 0/3 |
| sessions.allocate | 3 | 11.969 | 16.043 | 26.002 | 0/3 |
| sessions.launch | 1 | 256.683 | 256.683 | 256.683 | 0/1 |
| sessions.list | 3 | 9.239 | 9.620 | 10.048 | 0/3 |
| sessions.read | 9 | 185.486 | 217.067 | 379.102 | 0/9 |
| sessions.refresh | 3 | 460.334 | 464.905 | 575.053 | 1/3 |
| sessions.send (existing) | 3 | 491.499 | 801.580 | 836.725 | 2/3 |
| sessions.send (fresh) | 3 | 967.568 | 1000.494 | 1037.103 | 3/3 |
| sessions.send (mature) | 3 | 457.250 | 458.099 | 546.654 | 1/3 |
| sessions.stop | 3 | 237.672 | 260.915 | 320.948 | 0/3 |
| settings.get | 4 | 17.669 | 19.446 | 20.365 | 0/4 |
| state.get | 4 | 17.421 | 17.805 | 21.204 | 0/4 |
| takeover.get | 3 | 8.769 | 9.012 | 9.579 | 0/3 |

`daemon.ping`, account/settings/state reads, `sessions.list`, and allocation are
local control-plane or cache operations. `sessions.refresh` is an authoritative
remote list. `sessions.launch` measures a receipt only; the separate usable-TUI
row launches real Codex and checks the screen. Attach/resume include usable
output. Sending is verified by subsequent literal input readback; stops require
a confirmed finished terminal. Model mutations include cloud configuration
readback and restore the user's original model afterward.

The fresh and existing send failures include **617–768 ms** and **255–611 ms** of
remote Herdr send work respectively; mature sends still spend **202–204 ms**
there. Network latency alone does not explain these waits. Preserve literal
input/paste semantics while investigating them. Model changes took
**497–689 ms** in their remote round trip. Refresh and these model operations
remain failures even though most local operations and warm TUI starts pass.

## Credential synchronization

Magpie credential files are watched locally, debounced for 150 ms, and sent over
the existing resident channel. Timestamp/format-only changes do not upload;
offline changes retry after readiness notifications and on a five-second
fallback. Newer cloud grants remain protected by the existing merge rules.
There is no manual Sync credentials control.

The isolated Linux filesystem-to-merge test measured 157 ms, and the Mac kqueue
and race tests plus resident-protocol write/readback test passed. These use
synthetic credentials in temporary directories and **are not** a measured
production Mac-to-cloud token refresh latency. No real credentials were changed
to manufacture a timing sample. Add a production timing when a genuine refresh
is observed; log aggregate duration only, never credential material.

## Reproduction and maintenance

Run each live workload serially, with the installed build and daemon identity
pinned for the entire run. Retain all raw samples, failed runs and identity
checks. The `cmd/run-macos-check` runner gives disruptive tests an independent
launchd lifetime; use an independent recovery guard and verify reverse SSH after
restarts/network changes. Do not weaken timeouts or the 500 ms gate to make a
report pass.

```sh
FENNEC_LIVE_E2E=1 \
FENNEC_E2E_BINARY=/Applications/Fennec.app/Contents/MacOS/fen \
go test -count=1 -timeout=4m -v ./internal/ope2e -run TestSessionOperations500ms

go run ./cmd/verify-reconnect -installed-shim -restart-daemon -max-recovery 3s
# Run separately through the independent runner and recovery guard:
go run ./cmd/verify-reconnect -installed-shim -cycle-wifi en0 -max-recovery 3s
```

Update-to-TUI acceptance uses the signed development update fixture and
`verify-reconnect -installed-shim -install-update`; a direct bundle installation
is not equivalent to clicking a downloaded Update. Never use a release update
as a test fixture or change the user's update preferences.

For preparation breakdowns, opt in with
`~/.config/fennec/launch-profile.enabled`, record the daemon/CLI PIDs, and filter
`launch-profile.jsonl` by those PIDs. Remove the task-created marker afterward.
Private raw logs for this run live on the test Mac under
`/tmp/fennec-v33-restart/`, `/tmp/fennec-v33-warm-ops2/`, and
`/tmp/fennec-v33-wifi/`. Historical evidence and implementation history are in
[recovery-performance.md](tasks/recovery-performance.md).

Next priorities: reduce post-preparation TUI recovery stalls, fresh API identity
and resident bootstrap round trips, Herdr send settling, and model/refresh tail
latency. Retest all three lifecycle paths on each candidate; passing the unit
suite or one fast restart does not complete the performance objective.
