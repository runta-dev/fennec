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

The runtime, `0.1.4177-runta.6`, is based on upstream commit
`e6e0585bd3d92a79dab08f0955789a5801382b71`. Codex omits the backend's output
`metadata.turn_id` from replayed history. The downstream excludes this
top-level attribution when checking WebSocket continuation. Version `runta.3`
extends that comparison as described below; substantive content changes still
force full requests. Runta versions do not self-update
from the upstream release feed, which would otherwise overwrite the fix.

Version `runta.2` also disables unused next-prompt suggestions in Magpie's hidden
Claude child. These requests resent full history even when the main Messages
request used thread continuation. User preferences are unchanged, and feature
fetching remains enabled. The pinned source is Magpie commit
`f33819cf9f45dba32dcf48b60ec79db309b1c3c5`.

Version `runta.3` also handles backend assistant messages whose top-level
`internal_chat_message_metadata_passthrough` is omitted by Codex 0.169.0 on
replay. Comparison ignores only its string `turn_id` and numeric `create_time`
and `content_token_count`. Unknown fields, unexpected types, other roles and
substantive history changes still require a full request; transmitted items
are unchanged.

Version `runta.5` lists the model catalog of an account signed in after the
gateway started. Fennec starts a new cloud computer's gateway before it copies
the Mac's sign-ins in; `magpie serve` fetched account lists only at start and
while one was missing, and the hourly relist leaves accounts without a list to
that fetch, so the Codex account was never listed and Codex was offered only
Claude models until the gateway restarted. The gateway now looks for such
accounts every minute. It also re-reads catalog, memory and session database
files written within two seconds of their last read, so a same-size rewrite is
not missed. The pinned source is Magpie commit
`9f7f7faa2d5b364951a8fdee2731550c910557aa`; `runta.4` was not released.

Version `runta.6` fixes cloud Claude sessions that returned 502 on every
request ("interactive mode didn't start … exited before it answered magpie's
initialize request"). Interactive mode resumes the conversation in a nested
Claude Code TUI, and the readiness check took the words "trust this folder" or
"Bypass Permissions mode" anywhere on screen for Claude Code's own dialog, so a
conversation whose last reply quoted them never became ready. A question now
counts only when one of its choice lines is drawn. Source: Magpie commit
`abab94c2e23d10feb45f8475140aec2855b1fb0b`.

Version `runta.7` does the same for the two remaining first-run screens, the
Claude in Chrome offer and the theme (text style) screen. On `runta.6`, a
resumed reply that quoted their words made the helper press keys at the prompt
until it gave up, so the session returned 502 on every request again (THI-215,
THI-217). The pinned source is Magpie commit
`331bebff03e722f4ea22c702b7ae1174f4eb64e4`.

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
