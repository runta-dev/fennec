# Recorded terminal walkthrough

The four JSON recordings in `public/demo-recordings` contain actual terminal
bytes, parsed with the same xterm build shipped in Fennec. The website uses
`@xterm/addon-canvas` 0.7.0 with custom glyphs and single cell line height so
block artwork and box borders do not depend on system-font glyph metrics. `recordings.json`
records versions, source hashes, dimensions, edits and recording boundaries.
This edited presentation is not a latency benchmark.

## Capture

Build the recorder from the repository root:

```sh
go build -tags website_record -o /tmp/record-website-pty ./cmd/record-website-pty
```

Use a dedicated tmux socket and a 100-column, 32-row pane on the Mac. Set a
neutral prompt before attaching the recorder, then capture **output** with:

```sh
tmux -L fennec-landing pipe-pane -O -t codex "/tmp/record-website-pty pipe /tmp/codex.ndjson"
```

Type the installed Fennec-managed `codex`, `claude` or `pi` command and a prompt
against the isolated Go project in `fixture`. The committed recordings use
Mac → Fennec → cloud agent → `ssh machine` → Mac. Copy the fixture to
`/tmp/relay-mac-demo` on the Mac first. Codex fixes duplicate deliveries, Claude
fixes cross-workspace authorization, and Pi adds retry edge cases. Run them in
that order against the same fixture. Each reads and edits Mac files, then runs
the Mac's Go installation; `uname -s` identifies Darwin during verification.
These fixture paths are not product requirements.

`prompts.json` contains the visible requests. `recording-brief.txt` supplies
session-specific instructions through Codex's `developer_instructions`, Claude's
`--append-system-prompt`, and Pi's `--append-system-prompt`. It confines edits to
the fixture and excludes private device identities from public output. No
credential, permission preset, or global account setting was changed.

Run agents only in task-owned sessions. Detach, end the pipe, and stop only the
session IDs created for the recording. Never copy the user's real session list,
configuration, or private project into public assets.

Record `fen` with its production renderer and keyboard loop against isolated
sample session data:

```sh
/tmp/record-website-pty picker /tmp/fen.ndjson
```

`internal/cli/website_record.go` is excluded from normal builds. It injects only
session data and callbacks. The normal `fenPick` code handles actual PTY sizing,
ANSI output, selection, refresh and input. This avoids exposing unrelated user
sessions and avoids duplicating the terminal UI in HTML.

## Review and package

Raw files are private NDJSON with timestamps and byte-safe base64 chunks. Review
all selected terminal content before publishing. The packer rejects known private
markers, including markers split across chunks; this is an extra check, not a
substitute for reviewing output. Keep originals outside the repository.

```sh
# input output omitted-chunk-start omitted-chunk-end source-end-ms duration-ms
/tmp/record-website-pty pack /tmp/codex.ndjson public/demo-recordings/codex.json 0 0 0 18000 reviewed
/tmp/record-website-pty pack /tmp/claude.ndjson public/demo-recordings/claude.json 22 30 160000 18000 reviewed
/tmp/record-website-pty pack /tmp/pi.ndjson public/demo-recordings/pi.json 0 0 0 16000 reviewed
/tmp/record-website-pty pack /tmp/fen.ndjson public/demo-recordings/fen.json 0 0 6500 5000 reviewed
```

The Claude range is specific to this capture, not a generic trust-dialog filter.
It resumes at a complete alternate-screen repaint. Retained bytes are unchanged,
including split UTF-8 characters, cursor motion, colors and terminal mode changes.
Only shell setup preceding its initial clear, an explicit chunk range, source
end boundary and timing are edited. Update provenance when replacing a capture.

Each agent excerpt further compresses opening waits and holds the final result
for 2500ms. `recordings.json` lists exact packed chunk index / display millisecond
landmarks. Interpolate linearly between their original packed timestamps, keeping
every byte payload unchanged. Pi's initial SSH timeouts and successful retry are
retained with shorter waits. The picker was captured on Linux using the same
production renderer with sample session titles matching the Mac tasks.

The website lets each agent recording finish before switching to the next tab.
The requests, SSH commands, and final Mac test results are the content of the
walkthrough; the original dual-window layout is preserved.

## Native panel

`npm run prepare:demo` exports the actual WebView source, original custom menus,
icons, licenses and English catalog into the ignored `public/demo-native` folder.
The website builds run it automatically. Only the viewport CSS and fixture bridge
are website-specific. The original CSP remains in place; the bridge has no daemon
transport. Native panel controls act on browser-only fixture data.
