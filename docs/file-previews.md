# Private file previews

Agents register a file inside `~/.fen/<session>`:

```sh
fen preview --session dreamy-meadow --title "Sales chart" ~/.fen/dreamy-meadow/chart.png
```

The resident binary also accepts `fennec-terminal preview` with the same arguments.
The command snapshots the bytes and prints a Markdown link:

```md
[Sales chart](http://localhost:47823/p/dreamy-meadow/pv_0123456789abcdef0123456789abcdef)
```

Every client can use it. In a terminal (Codex, Claude Code, Pi, Kimi, OpenCode,
the Fennec panel) it is a link that opens in the browser on the Mac. Fennec iOS
recognizes it and shows the file inline. The older image form
`![title](fennec-preview:pv_…)` is still recognized by the app.

Include the actual returned reference on its own line, outside code blocks. Do not
invent IDs. Ordinary paths, XML tags, web URLs and incomplete references are not
preview instructions. A reference is scoped to its session. Register a changed
file again to obtain a new snapshot; duplicate registrations reuse the same ID.

## On the Mac

The Fennec daemon serves previews on `127.0.0.1:47823` (`preview.Desktop`,
started with the daemon). A request for `/p/<session>/<ref>` goes to the cloud
computer's private preview handler over the daemon's authenticated resident
connection, the same channel the app uses: no port is opened on the cloud
computer and nothing is cached on the Mac. While the cloud computer is not
connected the page says so; reloading retries.

Only this Mac's browser is served. The Host must be the loopback origin (no DNS
rebinding); requests from other web pages (`Sec-Fetch-Site: cross-site`, a
foreign `Origin`) are refused. A reference (128 bits) acts as the capability;
another program running as the same user who knows it can read the file, as it
could read the workspace over the resident connection. Web pages are served
with a sandbox CSP: an opaque origin, inline scripts and styles, images from
`data:`/`https:`, and no requests back to this server or the Mac.

A snapshot is one file. A page that loads files beside it (`chart.png`,
`data.csv`) needs them inlined, or registered and linked separately.

When port 47823 is taken (another user's Fennec on the same Mac) the daemon
retries every 30 seconds; links then reach the other daemon, which has no such
reference and answers 404.

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
in native previews. HTML (kind `web`) shows a snapshot of its top and opens in a
sandboxed, non-persistent web view that can't navigate away. SVG and unknown
content are export-only. HTTP responses
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
