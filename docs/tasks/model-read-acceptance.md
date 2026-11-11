# Model and account read acceptance

Recorded at 2026-11-11T17:53Z on the developer Mac, against installed daemon PID
58307, build `a8c76277299e3fdffbf422a3bff5e410f11d296e4d6567eb58ff91e550647196`.
This is read-only IPC-to-response evidence, not UI or Magpie-switch acceptance.

`go run ./cmd/profile-model-ops -method model.get -samples 3`

- Samples: 357.359, 491.609, 298.001 ms
- Evidence: `/tmp/fennec-model-read-validated.jsonl`

`go run ./cmd/profile-model-ops -method agent.account.get -samples 3`

- Samples: 410.443, 264.781, 266.869 ms
- Evidence: `/tmp/fennec-account-read-validated.jsonl`

Both runs passed the 1-second individual-sample gate with unchanged daemon identity
and resident-channel generation checked after every sample. Response validation
checks agent identity, account location/status, native model objects (including the
valid empty-ID Default choice), gateway string catalogs, and duplicate entries.
Null, malformed, empty catalogs and unavailable account states fail nonzero.

Validation: `go test ./cmd/profile-model-ops`, `go run ./cmd/check-english`,
and `git diff --check` passed. These tests verify the acceptance tool, not vendor
authentication or clean-Mac onboarding.

Remaining: actual Magpie mode-switch timing through applied routes and usable
catalog, with account/runtime binding, restoration of the original preference,
and supersession detection. The installed daemon predates the resident
reconfiguration implementation, so the above timings do not validate that path.

## Configuration completion identity

The source control plane now exposes `runtime.generation` and `runtime.pending`
in both `state.get` and `runtime.status`. Generation increases when an actual
preparation/reconfiguration execution starts; repeated readiness reads do not
advance it. Pending indicates a coalesced newer configuration waiting for the
current execution to drain. Account changes hide the previous execution state.

A switch profiler must require a newer generation, `running=false`,
`pending=false`, `stage=ready`, the intended account epoch/runtime, verified
route results, and a usable post-configuration catalog. The generation is an
execution identity, not a settings revision: a profiler must additionally fence
concurrent settings changes and cannot infer exact request ownership from the
counter alone. Failed executions retain their generation and fail acceptance.

Race tests cover coalesced choices, stale execution events, stable readiness
reads, failed executions, retries, and logout isolation. This is source-level
completion-observability coverage, not a live Magpie-switch timing result.

## Live switch gate

`cmd/profile-magpie-switch` now implements an explicitly invoked live switch and
revision-guarded restoration:

```sh
go run ./cmd/profile-magpie-switch -live -runtime <expected-runtime-id> -build <expected-daemon-build-id>
```

It refuses old daemons without configuration generations before mutation. It
checks daemon identity, resident-channel generation, account epoch, expected
runtime, and exact settings revision while waiting for completed configuration.
Both switch and restoration must finish within the default 1-second budget.
The 30-second observation deadline allows an over-budget operation to settle;
it does not relax the performance budget. Unknown outcomes fail nonzero.
A newer user preference prevents automatic restoration, including an off/on ABA
change. Missing native accounts remain valid signed-out routes; gateway mode
requires configured routes and a nonempty post-configuration model catalog.

Settings now persist a monotonic revision and accept optional
`expected_revision` for conditional writes. Existing clients retain normal
unconditional behavior. No-op writes preserve revision; canceled and stale
writes do not modify preferences. This is a daemon-local conditional write,
not protection against direct edits to the settings file or concurrent daemon
replacement; the profiler separately fences daemon identity.

This gate is not UI click-to-paint measurement and has not yet passed against
the newly built backend. Clean-Mac, real vendor authentication, and signed-DMG
acceptance remain outstanding.

## Development-build attempt

A complete locally signed app was built at `build/performance-dev/Fennec.app`,
including the serialized settings-update fix. Its control build ID is
`44f3478678f8c31e4130ddaa222d7649e5afbcfad02d747c7f9dea90803f146b`.
`codesign --verify --deep --strict` passed. The credential helper SHA-256
`f69a5f6c9bd8a928771bf151f564ff4c25c065df8ee390bad8291a0dbad539fe`
matched the existing development helper cache. No signing authorization prompt
was observed during the build.

Actual startup did not restore credentials silently: the account stayed in
`restoring` while the helper and macOS SecurityAgent were active. The installed
production app uses Developer ID team `4J499G24H7`; the developer app uses the
fixed local development certificate. Reusing the development helper does not
prove it inherits the production helper's Keychain authorization. No ACL changes,
credential resets, or credential extraction were attempted.

The development app was stopped and the untouched `/Applications/Fennec.app`
backend/UI were restored. Public state then confirmed `authorized`, runtime
`ready`, original runtime ID, Magpie off, and original production daemon build
`a8c76277299e3fdffbf422a3bff5e410f11d296e4d6567eb58ff91e550647196`.
The switch profiler was not run against an unauthorized developer session.
Live switch timing therefore remains unverified. It needs an authorized
stable developer identity or a candidate signed with the existing production
identity; neither compilation nor restoration is switch-performance acceptance.

The settings handler also now serializes previous-mode reads, persistence, and
reconciliation notification across UI/CLI writes. Otherwise concurrent opposite
choices could both observe the original mode and skip the second reconciliation.
The full CLI/controlplane/profiler race tests and `make check` passed.

The switch profiler's lifecycle tests exercise successful restoration, an
individual sample over budget, unusable post-switch catalog, an intervening user
revision, and restoration RPC failure. They assert mutation counts and preserve
the original preference when restoration is appropriate. Report-write failure
also fails the command rather than losing acceptance evidence silently. These
are isolated tool-correctness fixtures, not live latency or onboarding evidence.

## Native sign-in control audit

Source fix: background agent-card rerenders now retain the pending browser-login
state. Previously the replacement Sign in button looked enabled while the
request deduplicator silently rejected clicks. Completion now also respects
current route availability rather than unconditionally enabling the button.
Account reset clears pending feedback. The existing UI state regression suite
covers rerender, duplicate prevention, failure recovery, route loss, and reset;
`make check-ui` and `make check` passed. Real vendor login remains unverified.
The source-served development preview picks up these UI changes on reload; the
currently running production app is unchanged.

## Retrying ended native logins

The native Sign in client now checks an existing login before reopening it. A
confirmed finished, unsigned login gets a new request identity. A completed
sign-in refreshes the informational account view. Live or unknown outcomes keep
the existing identity, preserving launch deduplication. The remote status
operation now rejects missing/unknown terminal state instead of converting it
into `running=false`. This prevents an ambiguous read from authorizing a new
login. Calls made before control readiness now invoke their failure callback so
pending controls can recover rather than remaining disabled indefinitely.

Focused Go/race runtime tests validate live, finished, missing, unknown, and
reconnecting terminal states. Swift typechecking, `make check`, and `make
check-ui` passed. These source checks do not establish real OAuth success,
cancellation, expiry, or a deployed native UI acceptance result.
