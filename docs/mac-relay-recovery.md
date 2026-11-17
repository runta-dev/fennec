# Mac reachability and recovery notifications

The resident transport heartbeat proves Mac-to-cloud connectivity, not that a
cloud agent can SSH back to the Mac. The connector therefore runs a separate,
serialized end-to-end check every two seconds after the previous check completes.
It invokes the registered SSH alias, authenticates through the ProxyCommand and
reverse bridge, opens a session on the Mac connector, and checks a random nonce.
The reserved `fennec-health-v1` request only echoes a 16-byte nonce; ordinary exec,
SFTP and forwarding retain their existing authorization and sandbox policies.

Initial recovery and every fifteenth check also verify a fresh connection. If a
normal multiplexed connection fails but a fresh connection works, `ssh -O stop`
retires only that alias's master from accepting new clients. Existing commands
are not replayed. The ProxyCommand re-reads the verified relay state when opening
an upstream connection, so an old master does not retain a dead relay port.

A failed check cancels only the device link, then the existing Link reconnect
loop reconciles its service, alias and reverse bridges over the shared resident
channel. A failed end-to-end check requests replacement even if the bridge port
still accepts TCP. The Go loopback relay replaces the former Python service;
repair locks are per device and process identity is checked before signaling.
Network-offline cancellation does not force replacement of a healthy relay.

## Event batching and delivery

Reachability is published only after a successful complete probe. An English
`<fennec-event type="mac-online">` notification additionally waits for at least
three seconds of verified stability. Recoveries less than thirty seconds apart
share one batch (a sliding window). The Mac persists its batch identity across
daemon restarts. This debounce affects notifications only, never repair.

The cloud outbox records one delivery outcome per live Fennec agent terminal,
including detached terminals. Ordinary shells and exited terminals are excluded.
A batch reuses its outbox, so delivered terminals do not receive repeated prompts.
Blocked, busy or unreported terminals remain pending; delivery uses Herdr's
conditional `terminal.send` with `force=false` and `if=idle`, including its input
quiet period. No stop keys or permission answers are injected. A new batch
replaces obsolete pending events. Each send rechecks the actual SSH path.

Attempts are recorded before sending. A confirmed `blocked` or `status_mismatch`
refusal can retry; an unknown outcome remains `uncertain` instead of risking a
duplicate prompt. `delivered` means Herdr confirmed input, not that the model has
processed it. Pending delivery stops during loss of the device link and resumes
only after verified recovery. The infrastructure cannot guarantee that a network
will remain available after a successful probe or retract input already accepted
by Herdr. Herdr 0.1.18 provides a typing quiet period, not full draft detection.

Private diagnostics are the connector stage/retry logs, the device relay's
`state.json` and log, the Mac's `ssh/online-batch.json`, and cloud
`.local/fennec/notifications/<device>.json`. They contain no credentials.

## Verification

`go test -race ./internal/localssh ./internal/runtime ./internal/cli` covers
health authorization, actual OpenSSH alias/ProxyCommand traversal, relay death
and recovery, batching, deferred delivery, unknown receipts and stale events.
The relay lifecycle test refuses an unrelated PID. Existing relay tests cover
capacity, pairing and host-key checks, cancellation and reconnect behavior.
Run `make check-ui`, the source-language check and installed-path acceptance
before deploying a development build. Live fault injection must use an isolated
fixture, never kill a user's relay or agent sessions merely to test recovery.
