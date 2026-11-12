# Independent cloud-computer deletion

Status: implemented in source, not accepted in production. Do not validate by
deleting the user's current runtime. The running production app is unchanged.

## Internal CLI control plane (hidden from production UI)

Cloud-computer deletion is an internal clean-macOS test-preparation tool only and
must not appear in the production UI (Account, Settings, or Sessions) or public
`fen --help`. The internal CLI uses `cloud.status`, `cloud.delete.preview`,
`cloud.delete`, and `cloud.create`, requiring exact runtime-ID confirmation.

CLI commands:

- `fen cloud status`
- `fen cloud delete <runtime-id>` with interactive exact-ID confirmation
- `fen cloud delete <runtime-id> --confirm-runtime <runtime-id>` for explicit automation
- `fen cloud create` after confirmed deletion

Deletion returns an accepted receipt, not success. Status and the CLI wait for
verified completion of the matching runtime/request. Unknown outcomes fail CLI
acceptance and never permit creation. Status reads remain independent of the
network-bound action mutex. New UI copy has English, Simplified/Traditional
Chinese, Japanese, and Korean translations, with the existing British-English
fallback.

## Identity, shutdown, and recovery

The Go service binds expiring previews to build, authenticated owner, runtime,
and revision. It rechecks remote ownership/purpose/revision and local service
owner/runtime/epoch before writing intent or disrupting current work. The
controller repeats the expected runtime check under its lifecycle lock.

Intent is persisted before quiescing preparation and resident services. Queued
preparation is dropped; new session/browser/native-login operations are rejected.
Shutdown waits for old workers, retaining initialization exclusion after a caller
timeout until those workers exit. Local login/settings remain usable. Only then
is `DeleteOwnedShared` called; it verifies absence of the original runtime rather
than treating DELETE acceptance as completion.

A lost response retains the exact target, request, and revision across restart.
HTTP 409/412 and a definitive pre-DELETE revision mismatch are distinguished from
unknown outcomes. Only definitive rejection permits a new preview/confirmation
for a newer revision of the same runtime. Old previews cannot authorize later
cloud work. Explicit create-again authorization clears the matching confirmed
record and invalidates old previews; it does not silently follow a failed delete.

Records live in `~/.local/state/fennec-cloud-lifecycle`, outside ordinary local
uninstall cleanup. They contain identity/revision/status metadata, not tokens.
This intentional recovery retention prevents uninstall/reinstall from forgetting
an unresolved outcome or silently replacing an intentionally deleted computer.
Corrupt records fail closed, and another account is unaffected.

After restart, an intentional creation block is restored as a disabled cloud
state, not an environment-preparation error. Sessions hides stale rows and offers
Manage cloud computer, with no ongoing preparation spinner. Recreating remains an
explicit user action through Account/CLI.

## Verified source evidence

Go/race tests cover durable intent, account isolation, malformed records, exact
confirmation, expiration, wrong builds/targets, changed remote/local runtime,
quiesce timeout, worker drain, unknown outcomes, rejection/re-review, restart,
explicit recreation, and old-preview invalidation. REST fixtures assert blocked
EnsureShared performs no mutation. RPC and CLI tests distinguish receipt from
completion and verify destructive confirmation and duplicate prevention.

Go-driven frontend tests cover modal target mismatch, cancellation, failed
confirmation, unknown outcome, and creation gating. The UI state suite covers
intentional deletion without stale session rows or failure/loading UI. Swift
typechecking, `make check`, and `make check-ui` passed. These are source/fixture
results, not real OAuth, clean-Mac, or cloud-deletion acceptance.

An isolated source preview runs at `http://127.0.0.1:56075` and uses fake cloud
transitions only. Its served cloud script was byte-compared with source. The older
preview server remains untouched.

## Still required

- Rendered layout, keyboard focus, modal, and translated copy verification
- Final native/Go artifact consistency and CI package verification
- Dedicated disposable-runtime deletion/recreation through UI and CLI
- Real interrupted request, daemon restart, account switch, and uninstall/reinstall acceptance
- Final signed DMG and clean-Mac/independent-user evidence in the onboarding matrix

No actual cloud DELETE or production app update was performed for this work.

## Local package validation

A complete staged development app was built with the fixed local identity at
`build/performance-dev/Fennec.app`. Control-contract validation and
`codesign --verify --deep --strict` passed. Its control build ID is
`8497fb190e77f1d1d96b68a5a7ab1bc902072060f2c9424ebdddda3690e81eb7`.
The cached credential helper remains byte-identical at SHA-256
`f69a5f6c9bd8a928771bf151f564ff4c25c065df8ee390bad8291a0dbad539fe`.
This staged app was not launched or installed; no runtime/Keychain acceptance is
inferred from its successful build. The production app remains unchanged.
