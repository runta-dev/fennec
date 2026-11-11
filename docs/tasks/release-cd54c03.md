# Signed candidate cd54c03 acceptance

Candidate: `0.2.11120259`, arm64, source
`cd54c0389dbbfb68b524714a66ac2d25fabae4ab`.

- Release CI: https://github.com/runta-dev/errand/actions/runs/43082833532 (success)
- DMG SHA-256: `1e538cb0616758cf0c5994d90b2d8dfe5de403ba2d058cc47acac5cf8ffd591a`
- ZIP SHA-256: `8f128d273da3159e2aa2c9f42959b29a3ede60d15690c8a5075e06207a6c8953`
- Installed control build: `c4b95ef882b30921de5a0339f62d2d9cbcf74ca69461d7d1410a6bcd8895bc1a`
- Apple notarization: App `7b22f141-cafb-42d8-ac90-1b92a1320297` Accepted, DMG `67528b86-c03e-44e7-a4f4-341c9fca6448` Accepted
- Downloaded artifact files passed SHA256SUMS; local App signature and App/DMG stapler validation passed
- Credential helper CDHash remains `ca5027a933e4998d33f9fcc71747f8f3a2cf2eb9`, with the same designated requirement and Developer ID team `4J499G24H7` as the previous installed production helper

## Developer-machine upgrade

Installed with the candidate's `fen install --replace --destination /Applications/Fennec.app`.
Backup: `/Applications/.fennec-update-2547073432/Fennec.app`.
The installed `fen start` and app were started. Public account state immediately
returned `authorized`, without stalling for developer-certificate authorization.
The new daemon build matched the bundle. The existing cloud runtime progressed
through session-service installation to `ready`, with broker ready and Codex active.
No cloud computer or user session was deleted.

This is an upgrade on the existing developer machine, not a fresh-user login,
default clean-Mac Gatekeeper check, real reboot, or unaided-user acceptance.
The release workflow uploaded verified artifacts; it did not publish a public
GitHub Release or website download entry.

## First real Magpie switch: failed gate

Invoked `cmd/profile-magpie-switch` against the exact installed build and runtime,
with its 1-second per-operation budget. Evidence:

- `/tmp/fennec-cd54c03-magpie-switch.jsonl`
- `/tmp/fennec-cd54c03-magpie-switch.err`

Results:

- Switch to Magpie: configuration confirmed, 3526.820 ms, over budget
- Restore native mode: 607.978 ms, route validation failed
- Command exited nonzero, correctly leaving acceptance failed

Post-run state was verified: Magpie off, revision 2, runtime ready, no queued
configuration, broker ready, Codex configured and active, resident channel ready
with generation 1. Claude and Pi reported `local_provider_unavailable`.
The profiler did not record initial route statuses, so do not assert those agents'
post-run route states equal their pre-run states without further evidence.

Next work: decompose the 3.53-second switch, and make restoration validation
compare the original route baseline without weakening the target-mode usable-data
gate. Preserve this failed sample. Actual OAuth, complete performance matrix,
rendered UI, disposable-runtime deletion, and clean-Mac acceptance remain open.

## Baseline-aware repeat

The profiler now records initial public route states and compares restoration
against that exact baseline, while retaining the strict target-mode usability
and 1-second gates. A repeat against the same installed build recorded:

- Initial: Codex configured; Claude/Pi local_provider_unavailable
- Switch: 4593.770 ms, confirmed, failed budget
- Restore: 605.809 ms, confirmed and matched initial routes
- Overall exit nonzero due to the slow switch

Evidence: `/tmp/fennec-cd54c03-magpie-baseline.jsonl` and corresponding `.err`.
No phase timings existed in this installed candidate, reported explicitly as null.
Source instrumentation now separates local transport/native-account/route/plugin/
gateway-account preparation, remote round trip, and remote tool/catalog/account/
route phases. Only allowlisted numeric timing fields are included in profiler
reports; no credential payloads or normal UI debug text are added. It still needs
a matching signed daemon/remote-helper candidate for actual phase evidence.
