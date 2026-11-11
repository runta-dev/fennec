# Diagnostic candidate db0fd06

Candidate `0.2.11120314`, source
`db0fd0663d24b18a5e8c79b8ed55b20f48500875`.
Release CI 43085266917 and source CI 43085266199 succeeded. Downloaded SHA256SUMS,
App signature, and App/DMG stapler checks passed. Both notarization results were
Accepted. Helper CDHash stayed `ca5027a933e4998d33f9fcc71747f8f3a2cf2eb9`,
team `4J499G24H7`.

Installed control build:
`5ce6e36fd3e02a35739d82e45fdc3a6ff24f9db2d7c13e63d6b6ee490efd88fb`.
Backup: `/Applications/.fennec-update-2322281869/Fennec.app`.
Account restored authorized and the original cloud runtime reached ready.

## Actual phase measurement

Evidence: `/tmp/fennec-db0fd06-magpie-profile.jsonl` and `.err`.
Switch: 3585.709 ms, confirmed, failed 1-second gate.
Restoration: 828.346 ms, confirmed, matched starting route baseline.

| Local phase | ms |
| --- | ---: |
| Transport ready | 0.027 |
| Native-account preparation | 802.772 |
| Local route queries | 731.689 |
| Plugin metadata | 0.766 |
| Gateway account gathering | 0.360 |
| Remote round trip | 2039.022 |
| Publish | 2.210 |

Remote phase durations: tool preflight 1.134ms, catalog preflight 7.220ms,
accounts 8.045ms, routes 308.975ms, catalog readback 5.008ms. Remote configuration
work totals about 330ms; the difference from round-trip duration is not yet
attributed. Do not label all of it physical network latency without evidence.
Restore remote execution was about 3.2ms versus 587.486ms round trip.

## Native UI and upgrade defect

The installed daemon was current but UI PID 54029 was still the older process.
`fen install --replace` atomically replaced the bundle but did not stop/restart
its native UI. Opening the app reused that process, hiding the new Account cloud
controls. This violates UI/backend artifact consistency and needs a product fix.
The exact old UI process was terminated and the current installed UI reopened;
cloud controls then appeared. The backend/cloud sessions were not stopped.

Observed on the real current native UI: English/light-mode deletion modal fits
the popup, explains permanent cloud data loss and local retention, initially
focuses the confirmation field, keeps deletion disabled for a mismatched ID,
and Cancel returns focus to the Account deletion button. No valid target ID was
submitted and no DELETE was executed. Other languages/themes and actual deletion
remain unverified.

Account billing separately displayed Unable to load account usage. The live
`account.billing` call failed while `cloud.status` succeeded. Do not classify this
as a layout issue or conceal it by removing the failure state; its backend cause
still needs diagnosis.

## Upgrade UI lifecycle source fix

The transactional installer now stops only a running UI at the exact destination
bundle path, after copying and verifying the incoming bundle. It reopens that UI
after publication, or reopens the rolled-back original after a failed/canceled
replacement. An app that was not running stays closed. The independent daemon
and remote sessions are not terminated by this adapter.

Go owns staging/publication/rollback and reopening policy; the thin AppKit adapter
only locates and gracefully terminates the matching native application. The GUI
installer honors the resulting `ui_restarted` receipt rather than launching a
second copy. Other Fennec bundle paths are not broadly terminated.

Race tests cover verified-stage ordering, running versus closed UI, publication
rollback, rejected signatures, and cancellation after UI shutdown. Swift
typechecking and `make check` passed. This source fix still needs a signed-candidate
upgrade with a running UI to prove the new process is actually loaded.

## Next source changes for measured bottlenecks

Native credential gathering and gateway route/account preparation now run
concurrently. Neither sends remote configuration until both complete; an error
cancels and drains the sibling. The original Magpie CLI configuration semantics
are retained after reviewing the reference source, including aliases and custom
configuration roots. No simplified replacement parser or stale credential cache
was introduced. Source/race tests verify overlap, completion barriers, error
propagation, and cancellation.

Timing now includes `local_prepare_wall`; its concurrently measured child phases
must not be added together. Resident transport diagnostics separately measure
request-write time, response wait, and the server's existing operation duration,
without another SSH channel or protocol change. These are nested within RTT.

For the separately observed billing failure, opt-in private profiling now records
only HTTP status codes and unexpected organization counts. No credential or
billing payload is logged and no generic UI error was removed. Actual cause and
post-change latency remain unverified until the next candidate runs.
