# Recorded terminal walkthrough

The four JSON recordings in `public/demo-recordings` contain actual terminal
bytes, rendered with the same xterm build shipped in Fennec. `recordings.json`
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
against the isolated Go project in `fixture`. The committed recordings used
Mac → Fennec → remote agent sessions. Their prompts explicitly limited work to
`/tmp/relay` on the cloud machine; Go was available at
`/root/.local/toolchains/go/bin/go`. These paths are recording fixtures, not
product requirements. No credential or account setting was changed.

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
/tmp/record-website-pty pack /tmp/claude.ndjson public/demo-recordings/claude.json 9 16 0 18000 reviewed
/tmp/record-website-pty pack /tmp/pi.ndjson public/demo-recordings/pi.json 0 0 0 13000 reviewed
/tmp/record-website-pty pack /tmp/fen.ndjson public/demo-recordings/fen.json 0 0 6500 5000 reviewed
```

The Claude range is specific to this capture, not a generic trust-dialog filter.
It resumes at a complete alternate-screen repaint. Retained bytes are unchanged,
including split UTF-8 characters, cursor motion, colors and terminal mode changes.
Only shell setup preceding its initial clear, an explicit chunk range, source
end boundary and timing are edited. Update provenance when replacing a capture.

## Native panel

`npm run prepare:demo` exports the actual WebView source, original custom menus,
icons, licenses and English catalog into the ignored `public/demo-native` folder.
The website builds run it automatically. Only the viewport CSS and fixture bridge
are website-specific. The original CSP remains in place; the bridge has no daemon
transport. Native panel controls act on browser-only fixture data.
