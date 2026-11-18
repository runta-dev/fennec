# Private file previews

Agents register a file inside `~/.fen/<session>`:

```sh
fen preview --session dreamy-meadow --title "Sales chart" ~/.fen/dreamy-meadow/chart.png
```

The resident binary also accepts `fennec-terminal preview` with the same arguments.
The command snapshots the bytes and prints a Markdown image reference:

```md
![Sales chart](fennec-preview:pv_0123456789abcdef0123456789abcdef)
```

Include the actual returned reference on its own line, outside code blocks. Do not
invent IDs. Ordinary paths, XML tags, web URLs and incomplete references are not
preview instructions. A reference is scoped to its session. Register a changed
file again to obtain a new snapshot; duplicate registrations reuse the same ID.

## Desktop compatibility

Conversation text remains unchanged. iOS interprets the custom image destination;
terminal Markdown renderers can retain the image description as ordinary text.
Verified renderers: Codex 0.169.1 (source), Pi TUI 1.5.2 (actual rendered output),
and Kimi 2.6.3 (bundled source). Claude and OpenCode have not been verified; clients
without this behavior may display the full Markdown syntax. There is no promise
that every desktop client hides the destination. Raw XML is intentionally avoided.

## Transport and storage

HTTP runs inside the authenticated resident `preview` socket capability
(`preview_http_v1`), using in-memory connections. No TCP port, public ingress,
external URL, or separate bearer token is created. Resource IDs identify content;
they do not authorize network access. The authentication boundary is the user's
resident connection, not isolation from other programs running as that same user.

- `PUT /v1/sessions/{session}/uploads/{uploadID}?name=...`
- `GET /v1/sessions/{session}/previews/{ref}`
- `GET|HEAD /v1/sessions/{session}/previews/{ref}/content`

Uploads land at `~/.fen/<session>/uploads/<uploadID>/<name>`. A draft UUID makes
retries idempotent; conflicting bytes are rejected. Snapshots live separately at
`~/.local/fennec/previews/<session>/<ref>`. Workspace reads are confined using
`os.Root`; only regular files up to 64 MiB are accepted. Downloads verify length
and SHA-256. Snapshots currently persist until explicitly removed; automatic
retention/quota management is not implemented.

Raster images render inline. PDFs, supported plain text and audio/video can open
in native previews. HTML, SVG and unknown content are export-only. HTTP responses
forbid caching and sniffing, apply a sandbox CSP, and reject browser origins.

## iOS attachments and rollout

The photo picker, Files picker and explicit image paste create local drafts.
Images are converted to PNG and use native agent TUI image-paste frames, with
acknowledgements before submitting text. Files upload over the private HTTP
channel; the agent receives their workspace paths and registered references.
Selection is limited to eight attachments; PNGs are limited to 20 MiB each and
64 MiB total. Files are limited to 64 MiB each. An uncertain send is not replayed
automatically.

Both the iOS app and cloud resident binary must be updated. An older server
without the preview capability fails explicitly, with no public-network fallback.
Existing resident connections need to reconnect after installing the new binary.
New session instructions explain registration; existing session instruction files
are preserved and may need the preview instructions supplied explicitly.

Validation includes traversal and symlink rejection, immutable snapshots, upload
conflicts and retries, private HTTP Range/HEAD, parser boundaries, native paste
ordering, and registration-to-download through the real resident entry point.
Xcode compilation and simulator acceptance must be completed before releasing the
iOS build; Go tests alone do not validate the Swift UI.
