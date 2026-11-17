# Managed Magpie runtime

Fennec installs the Runta-maintained Magpie runtime only when Magpie integration
is enabled. Source lives in the private `runta-dev/magpie` repository; runtime
binaries and their notices are public dependency assets in `runta-dev/fennec`.
Cloud installation does not need a GitHub token or access to private source.

`internal/setup/toolchain.go` pins the runtime version, release URL and SHA-256
for Linux amd64 and arm64. Matching binaries are reused. New or mismatched
binaries are downloaded to a temporary directory and verified before an atomic
rename; a failed download or checksum leaves the installed binary untouched.

The setup script participates in preparation, installation, configuration and
gateway fingerprints. Changing the pin invalidates the old preparation cache.
After installation, `MagpieServiceScript` calls the managed gateway installer,
whose `Refresh` compares the systemd process's executable inode with the target
and restarts the owned service when necessary. It does not kill agent sessions
or alter credentials, models, reasoning effort or the saved Magpie toggle.

The initial runtime, `0.1.4177-runta.1`, is based on upstream commit
`e6e0585bd3d92a79dab08f0955789a5801382b71`. Codex omits the backend's output
`metadata.turn_id` from replayed history. The downstream ignores only this
top-level attribution when checking WebSocket continuation; other metadata and
content changes still force full requests. Runta versions do not self-update
from the upstream release feed, which would otherwise overwrite the fix.

To upgrade:

1. Validate and tag the private downstream on `main`. Run its `Runta runtime`
   workflow and verify the successful run's source SHA and artifact checksums.
2. Publish its Linux binaries, `LICENSE`, `THIRD_PARTY_NOTICES`, `SHA256SUMS`
   and `provenance.txt` in a Fennec dependency release named `magpie-v<version>`.
   Mark it non-latest so it does not replace the Fennec application update feed.
3. Update the version and both checksums in `internal/setup/toolchain.go`.
   Run `TestMagpieRuntimeInstall`, the setup/gateway/cli suites and UI/language
   checks. Build and install the development application according to the macOS
   development skill, or publish an application release when requested.
4. Verify the managed process actually runs the pinned binary, and inspect
   consecutive successful Codex requests for incremental upstream messages.
   Keep request sizes separate from compressed network bytes and distinguish
   first full requests from subsequent continuation requests.
