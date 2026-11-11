# Go control plane and performance acceptance

## Implemented and exercised locally

- The Go daemon independently owns account state, the Keychain helper, runtime preparation,
  broker, connector, approvals, takeover, and model preferences. Swift is a Unix-socket
  client and native platform adapter.
- `fen start` starts the background service and reuses an existing instance. The LaunchAgent
  is independent of the UI. Closing the UI was verified to preserve account restoration,
  connector, settings, takeover, and session send/read/stop with the same daemon PID.
- The app's UI executable is `Fennec`, its Go executable is `fen`, and the old
  `fennec-daemon` path is a compatibility symlink. Development signing and the cached
  `fennec-credentials` helper remain stable.
- UI, ordinary CLI, and compatibility read/send/list/attach/reconnect entries share one
  control plane. Normal operations, including non-TTY calls, reuse the resident protocol
  channel. Reconnect attaches only to confirmed sessions; it never creates a replacement agent.
- Go maintains the background session cache. Confirmed stop removes the row immediately;
  generations prevent late refreshes from resurrecting it. Explicit `sessions.refresh`
  waits for the remote result, never treating a cache hit as a remote refresh.
- Packaging checks the control API contract and remote terminal protocol. The running
  daemon is compared with `control-build-id`; `fen start` replaces stale services serially
  so a new UI cannot call an old interface.
- Background checks repair CLI entries only when a Fennec wrapper still matches its
  original backup. External changes are preserved. Old `/tmp/fennec-session-ui-validation`
  references were replaced with the fixed installation location.
- Credential reception is now Go. Account IDs take precedence; local and remote baselines
  are distinct. Concurrent refreshes of one account retain the newer token without
  aborting environment preparation. Empty auth or another account cannot replace existing
  credentials. In the actual incident, the cloud Codex token was 12 seconds newer than
  the local one; keeping it restored the environment.
- Run location represents user intent. Incomplete model preparation no longer disables
  Cloud selection. Selecting Cloud resumes preparation on demand without restarting an
  existing shared connection.

## Real acceptance gates

`make test-e2e-ops` exercises installed CLI → Go daemon → resident SSH/protocol channel
→ real Herdr terminal. Ordinary unit tests do not connect to the user's machine.

Each warm sample must be at most 500 ms, including remote refresh, send/read/stop,
launch receipt and input-ready TUI, and attach/resume's usable first screen. Cold SSH
is measured separately with a 5-second limit. Tests touch only their own sessions,
verify literal input delivery and confirmed stop, reject cache resurrection, and
check that daemon PID and resident channel generation remain unchanged. Failures and
over-budget samples exit nonzero.

## Recorded evidence and remaining issues (2026-11-11)

The subsequently deployed cold-pipeline build was
`3afd9c6fa47284151bb67f221c68e8dd904fba840c99ccd33c7b33ee4066cb07`.
For ordinary shared connections, only handshaking overlaps fresh authorization;
authorization failure closes that attempt's master. Changed keys/host identity require
another handshake after authorization. Remote `serve --protocol=N` rejects incompatible
versions before reading stdin, allowing the ordinary first channel to queue requests
without another ready round trip. `connection.status.ready` becomes true only after
handshake and protocol confirmation.

Installed cold TUI samples: 5379 / 3917 / 5640 ms in
`/tmp/fennec-cold-pipeline-installed.jsonl`. SSH measurements record fixed stage names,
not raw debug output: address preparation 6–29 ms, TCP 239–261 ms, KEX completion
952–981 ms, public-key acceptance 1906–1935 ms, final authentication 3212–4039 ms.
Slow samples primarily occur after public-key acceptance and during SSH channel/exec
establishment; remote workspace preparation and agent spawn still take milliseconds.
A consistent sub-5-second result has not been achieved.

The corresponding full warm gate, `/tmp/fennec-e2e-pipeline.log`, still FAILS:
TUI 321–350 ms; send about 790–910 ms; one resume at 508 ms; launch receipt at 568 ms.
Resident channel generation remained 1. No failed samples were discarded or budgets relaxed.

The profiler's animation detection was corrected: it explicitly uses a normal interactive
terminal, clears inherited `NO_COLOR=1`, and matches Fennec's full spinner sequence so
Codex's spinner is not mistaken for evidence. The real installed build reported
`spinner_visible=true`.

Earlier batching build:
`70ff0534783f1c3e6e0a0627594815e156a013a4cd9da5173d7af4a107a4e362`.

- Initial receipts and first-screen fragments were batched and deployed. Full CLI
  end-to-end attach was 348 / 349 / 354 ms and resume 311 / 359 / 353 ms; the previous
  resume timeout did not recur in this run.
- Warm Codex input-ready first screen: 321 / 364 / 324 ms. Remote refresh 284–370 ms,
  read 296–303 ms, and stop 342–347 ms.
- Real installed cold Codex first screen: 4411 / **5251** / 4246 ms. One sample still
  exceeded 5 seconds, so the cold-start goal remained incomplete. That sample spent
  about 3.8 seconds from handshake to execution, with `ssh.receipt` at 969 ms.
  Further reduce channel/exec/PTY round trips after handshaking. Existing
  `serve-initial`/`StartInitial` are starting points, but agents must not start before
  ownership verification.
- Send remained 788–793 ms; one launch receipt took 556 ms. `make test-e2e-ops` correctly
  exited nonzero. Logs: `/tmp/fennec-e2e-ops-batched.log` and
  `/tmp/fennec-cold-installed-batched.jsonl`.
- An upgrade race where `launchctl bootout` returned before registration disappeared
  was fixed by waiting before re-registering. The first real upgrade's `fen start`
  improved from a 15-second timeout to success in 0.93 seconds; later starts reused
  the same daemon.

Earlier installed display-prefetch results are retained for comparison:

- Genuine cold startup to an input-ready Codex screen: 4828 / 4234 / 4181 ms.
  Handshaking overlapped ownership verification; no agent was prestarted.
- Warm input-ready first screen: 375 / 367 / 347 ms.
- Remote refresh: 268–340 ms; read about 294–304 ms; ordinary stop about 300 ms.
- Send about 790 ms, still over budget. Herdr 0.1.18 waits 800 ms for new-program output
  stability, with another 200 ms between paste and Enter. Do not use `--force` or a
  queued receipt as evidence of completed input.
- Attach/resume originally took about 560 ms. Isolated measurements showed about 5 ms
  of remote screen generation but an extra 230 ms from separate receipt and screen
  packets. Batching (25 ms maximum, 8 ms quiet window) measured 306 / 307 / 342 ms over
  real networking; full-app results are listed above.
- A resume timeout followed by a stop timeout still needs investigation. Passing normal
  samples does not erase these failures.

Overall performance acceptance remained FAIL and the goal active. Earlier complete
logs: `/tmp/fennec-e2e-ops-display-v6.log` and `/tmp/fennec-cold-v6.jsonl`.
These measurements do not automatically establish results for subsequent builds.

## Remaining work

### Cold path implementation and early staging evidence

After broker ownership verification, `internal/launcher/cold_stream.go` validates
runtime ID and the host-key snapshot, then executes `serve-initial` over that cold
attempt's existing SSH master. It uses a non-PTY SSH channel and one exec carrying
the launch frame plus local terminal information. `terminalstream.OfferLocal` shares
the streaming UI path; `StartInitial` preregisters the mailbox to prevent lost early
receipts or duplicate launches. Tests cover the initial frame, target mismatch, and
host-key changes.

Staging: `/tmp/fennec-cold-once.aBz2xM/Fennec.app`.
`/tmp/fennec-cold-stream.jsonl` recorded 5451 / 3783 / 3789 ms. Signed staging results
in `/tmp/fennec-cold-stream-signed.jsonl` were 10268 / 4230 / 4310 ms. The slowest
sample spent 8789 ms in `cold.ssh_handshake` and 5692 ms in its concurrent broker
request; remote preparation took only tens of milliseconds. Do not discard that
sample to claim success. Decompose DNS/TCP/KEX/auth and repeat on the real installed path.

Apply cold-path round-trip savings to ordinary daemon channel initialization too,
not only explicit `FENNEC_COLD_START` profiling. Investigate sending the first launch
with channel initialization only after ownership is verified and the remote helper
can reject incompatibility through its explicit protocol parameter. Never start an
agent before verification completes.

- Remove remaining real cold-TUI round trips and samples over 5 seconds; keep investigating
  intermittent reconnection failures.
- Fix actual send completion time while retaining input/dialog protections and the
  original acceptance boundary.
- Independently audit explicit project sync, bulk transfer, and private project-launch
  paths. Ordinary-session results do not prove those paths are consolidated.
- Verify bidirectional visibility of GUI/CLI settings changes and complete the untested
  first-login and approval interactions.
