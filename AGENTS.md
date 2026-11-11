# Fennec project conventions

## Source language and localization

- Write implementation code, comments, developer documentation, task records, and project skills in English.
- Keep translated UI copy in `native/macos/UI/translations.json`, addressed by stable English message IDs. Do not use Chinese sentences or mutable English copy as translation keys.
- WebView dynamic copy uses `[[i18n:ui.message_id]]` tokens. Static HTML uses `data-i18n` and English fallback text. Native UI uses `L10n.text` with the same catalog. The build generates the WebView payload and compiled English fallback.
- Preserve Unicode fixtures that specifically test multilingual text, paths, titles, punctuation, or byte boundaries. Keep their surrounding code and comments in English.
- Run the source-language and localization checks before merging. Do not hide translated UI copy in Unicode escapes to bypass the checks.

## Use Go for new implementation

- Use Go for new business logic, daemon and remote services, session and PTY management, transport protocols, diagnostics, performance measurement, prototypes, and tests.
- Do not introduce Python as a temporary script, quick probe, or single-file test. Do not embed Python in Go or shell to bypass this rule.
- One-off tools can use `go run path/to/probe.go`. Single-file tests can use `go test path/to/probe_test.go`; list same-directory implementation files explicitly when needed, for example `go test path/to/probe.go path/to/probe_test.go`.
- Migrate existing Python prototypes or embedded logic in the paths being changed to Go. Do not extend them into production implementations or alter unrelated features merely to migrate them.
- Existing Swift/AppKit native UI, WebView HTML/CSS/JavaScript, and necessary build/package shell retain their respective roles. They do not justify new Python or other scripting-language business logic. Explicit user instructions take precedence.

## Startup and connections

- Respect saved Magpie choices. Under the latest first-run policy, enable it only when actual local installation is verified; otherwise keep it off. Old configuration/cache remnants are not installation evidence, and a saved off choice must not be reenabled. With Magpie off, synchronize each agent's own files, environment, or Keychain credentials into isolated cloud configuration directories without installing, starting, or probing Magpie. UI and CLI share `settings.set`; cross-provider model switching requires Magpie to be enabled.
- With Magpie disabled, the Go control plane validates native model catalogs and choices: Codex/OpenAI, Claude Code/Claude, and Pi's configured providers. Do not reuse Magpie preferences. Remove incompatible inherited models during synchronization, preserve cloud TUI settings and valid native choices, and never use inference probes to determine readiness.
- Native credential synchronization must cover fresh users without Magpie, independent agent failures, cloud OAuth refresh preservation, Keychain denial, and path/credential isolation. Do not copy whole configuration directories, sessions, hooks, or local executable commands. Never put secrets in argv, logs, RPC responses, or the repository.
- Each user has one shared cloud machine for all cloud agents. One Fennec daemon owns a shared SSH connection with independent session channels. Do not introduce a multi-machine connection pool or scheduler.
- Coalesce connection initialization and reconnection into one shared pending result. Continue verifying identity, runtime ownership, and host keys; preserve session isolation and duplicate prevention after uncertain launch outcomes.
- All session operations (launch/resume/stop/read/send/list) use the same daemon control plane and resident protocol channel. SSH ControlMaster reuse followed by a fresh exec channel is insufficient. Normal UI/CLI operations must not repeat authentication and SSH establishment. Update cache/UI on confirmation and fence late refreshes. Follow “Session operations must reuse the resident channel” in `skills/fennec-macos-development/SKILL.md`.
- Do not prewarm agent process pools. SSH connections and transport channels may be prewarmed. Measure cold connections, warm connections, and agent startup separately. End-to-end timing ends at an input-ready TUI, not a launch receipt.
- Reuse an available cloud binary; install only when missing. Do not compare or synchronize exact local versions. Routine credential/model synchronization must not issue inference probes or gate TUI startup on model rate limits or quota.
- Normal terminal startup shows only a text-free loading animation, cleared before TUI handoff. Noninteractive output stays clean; failures retain necessary errors.
- Enforce a 500 ms per-sample limit for real end-to-end warm session operations. Measure cold SSH separately. Timeouts, failures, or over-budget samples must exit nonzero. Cache hits or receipts cannot substitute for completed operations and an input-ready first screen.
- Do not expose CLI integration diagnostics, daemon/RPC internal codes, or interface mismatches in product UI. Recover and repair in the background; an unknown check is not a failure. Follow the product UI rules in the project skill.

## macOS build acceptance

- Follow `skills/fennec-macos-development/SKILL.md` for builds, signing, and restarts. Reuse the fixed development certificate and stable credential helper.
- Build and verify signatures in staging, then stop the old UI, install the complete bundle, and restart. Never overwrite running signed binaries in place.
- Native UI and Go artifacts must come from the same source snapshot and pass control-contract checks. Use the installed `fen start` to verify that the matching backend is ready; restarting only the UI while leaving an old daemon is insufficient.
- Preserve uncommitted changes, existing credentials, and cloud sessions. Completion requires evidence from the actual startup path; compilation, tests, or configuration state alone do not prove usability.
