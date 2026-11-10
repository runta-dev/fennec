# Fennec design

## 1. Product contract

**Keep your agent. Leave the laptop.** Fennec separates the agent loop from the device providing its initial environment. The local app is a connector and control surface, not a replacement harness or model router.

The first complete vertical slice is: launch a new cloud agent, access an approved local project, prepare task-specific cloud dependencies, verify without local access, disconnect, complete a real task, reattach, and retrieve its changes.

The macOS menu-bar client signs into Runta and discovers supported agents. The resident Go daemon owns provisioning, account state, shared SSH transport, and session operations. The Swift shell controls presentation through local IPC. The separate HTTP surface is a read-only development preview. Existing local agent processes are not migrated.

## 2. Architecture

```text
Local CLI / macOS menu-bar shell
              |
       Local Fennec daemon
       |                |
  session transport   device connector
       |                | outbound authenticated channel
       +------ Cloud session supervisor ------+
                        |                     |
                 harness adapter       scoped tool requests
                        |                     |
                   agent loop          approved local actions
                        |
                Runta cloud workspace
```

The cloud supervisor owns process lifetime independently of SSH/terminal clients. It must persist session metadata, bound output storage, and maintain sequence-aware terminal replay. Disconnecting a client must not kill the process. A process crash is different from a transport disconnect and needs harness-specific recovery.

Use SSH where appropriate for terminal transport; do not invent an alternate shell protocol unnecessarily. A connector initiates an authenticated outbound connection so devices need no public inbound SSH port. SSH alone does not provide device authorization, task supervision, or spending enforcement.

## 3. Component boundaries

| Component | Responsibility | Current implementation |
| --- | --- | --- |
| CLI | Launch entry, attach, status | Help, agents, local serve only |
| Local daemon | UI API, future pairing and approval | Loopback read-only HTTP |
| Harness adapter | Configure tool destinations and session behavior | PATH discovery only |
| Connector | Authorized file and command access | Go interface only |
| Runtime provider | Provision, stop, enforce resource budget | Go interface only |
| Handoff | Prepare and verify cloud independence | Checklist policy and skill |
| UI | Agents, environments, sessions, permissions | Live discovery and honest empty states |

Do not assume an SSH tool redirects a harness's built-in filesystem or shell tools. Every supported adapter must explicitly establish tool destinations, working directories, cancellation behavior, and interactive input. Codex Desktop integration is exploratory and depends on supported backend interfaces; CLI compatibility does not imply desktop compatibility.

## 4. Handoff state machine

```text
created -> preparing -> verifying -> ready -> running -> completed
                 |           |                 |
           needs_approval  blocked         waiting_input
                                               |
                                           running
active states -> failed / cancelled / budget_exhausted
```

Cloud execution can begin while the connector is online; offline readiness is a separate property, not a prerequisite for all execution. Only operations needing unavailable local resources must pause. Connection state is separate: attached, detached, reconnecting, offline. A disconnected client does not mean a failed task. No UI action may mark a session ready merely because files were copied.

Required readiness evidence:

1. A manifest of selected files and content hashes, including explicitly selected uncommitted/untracked changes and exclusions.
2. Required tools, services, and versions validated in the cloud.
3. Required authorization approved and checked for sufficient lifetime; device-bound credentials reported as blockers.
4. Representative task checks succeed while connector access is disabled.
5. The runtime confirms the requested spending limit can be enforced.

The current checklist function enforces completeness, not evidence authenticity. Production readiness must be computed from trusted verifier records bound to session ID, environment revision, manifest digest, and expiry. Agent statements alone are not evidence. Environment/configuration changes invalidate affected evidence.

Offline readiness is bounded by the checked task. Newly discovered local dependencies pause the task and request reconnection; they do not trigger indefinite retries or unauthorized access.

## 5. Files and environment preparation

The agent plans task-specific transfers through the connector rather than requiring eager full-repository upload. Default-deny private credential directories and sensitive files, even if an agent requests them. Enforce canonical path containment, symlink handling, byte limits, and explicit approvals in the connector, not in prompts alone.

Record an upload baseline so returned changes can be reviewed as a diff or PR. Never overwrite local modifications made after that baseline. Preserve provenance for generated artifacts. A prepared workspace/checkpoint can accelerate subsequent tasks, but retained files need a deletion policy and tenant isolation.

## 6. Authorization and secrets

- Pair devices explicitly; use short-lived session-scoped capabilities and revocation.
- Treat project files, tool output, and skill text as untrusted with respect to permissions.
- Prefer supported provider authorization or user-supplied scoped API credentials. Do not scrape keychains or copy entire subscription login directories.
- Transfer approved secrets through a dedicated encrypted channel into ephemeral runtime mounts or provider-supported secret storage. Give the model an opaque grant handle, not the secret value.
- Exclude secrets from logs, terminal captures, images and snapshots. Memory checkpoints may contain secrets; do not offer such snapshots until redaction/exclusion guarantees are established.
- Local credential-command execution requires a trusted broker, explicit approval and bounded output handling; never accept arbitrary model-supplied credential extraction commands.
- Model APIs may receive selected source content. Explain both runtime access and model-provider disclosure before use.

Before exposing any mutating local API, add per-install authentication, CSRF protection, device/session binding, audited approval records, request limits and token rotation. The current host/origin checks are defense in depth, not authorization.

## 7. Execution reliability

Every connector action needs request ID, session, capability, target, timeout, cancellation, and an auditable outcome. After disconnect, distinguish not-started, running, completed and unknown. Never automatically replay a side-effecting command with unknown outcome. Reconcile against execution records first.

Cloud sessions need bounded retry policies, keepalives, idle policies, terminal resize/input handling, and output replay. A stopped budget must stop further billable work, not merely display a warning. Explain whether retained storage remains billable. Model charges and environment charges are distinct.

## 8. UI direction

Product clarification: Fennec is a resident menu-bar/system-tray daemon application, not a website. Its primary entry is a compact tray panel showing connection state, active agents and pending approvals, with launch/reattach actions. Primary controls stay inside the popover. Closing a window must not stop the connector; explicit quit must explain which local-dependent tasks will be blocked and which cloud tasks can continue. Embedded HTML is an implementation detail, not a requirement to use a browser or remember a localhost port.

The existing browser preview is temporary and its marketing hero should be removed in the desktop iteration. Prioritize the native shell and lifecycle next, ahead of expanding dashboard features. Linux development cannot validate macOS menu-bar behavior, signing, or permissions; those require a local Mac session.

Follow Magpie's compact local utility model: restrained sidebar, discover existing agents, small settings surfaces, and clear statuses. Do not recreate a chat/task management platform.

Use Runta's public brand accent `hsl(30.291 89.565% 54.902%)` (approximately `#f38d25`), with warm neutral surfaces and light/dark themes. Use dark foreground on bright orange; use darker orange text on light tinted surfaces. Keyboard focus, semantic navigation, responsive layout, and visible error states are baseline requirements.

The application uses the Fennec fox mark. Selected Magpie agent SVGs and compact layout metrics are reused under MIT; see THIRD_PARTY_NOTICES.md.

## 9. Delivery stages and acceptance

1. **Foundation (this scaffold):** read-only UI, discovery, package contracts, readiness policy, docs, automated checks.
2. **Local macOS companion:** titleless menu-bar popover, Runta device-code login and automatic supported-agent discovery. Acceptance: dismiss/reopen without quitting, complete real browser authorization, restore credentials securely and manually request cloud takeover.
3. **One real cloud CLI:** explicit opt-in launch, persistent PTY supervisor, detach/reattach, budget enforcement. Acceptance: closing the client does not kill the cloud task.
4. **Connector and handoff:** pairing, scoped transfer, approved grants, offline verifier. Acceptance: a selected real task completes with the local connector disconnected; missing dependencies block visibly.
5. **Results and recovery:** baseline-aware diff retrieval, restart-safe metadata, output replay and unknown-outcome reconciliation.
6. **Distribution and adapters:** signed/notarized packaging, another CLI adapter, desktop integration only after protocol validation.

Only launch agents through the authenticated runtime and supported adapter paths. Do not claim universal harness support or existing-process migration. The acceptance criteria above do not imply every roadmap capability has shipped.

## 10. Validation

`make test` covers account/control IPC, runtime and terminal operations, transport, session identity, and readiness evidence. `make check` runs Go vet and formatting checks; `make check-ui` validates frontend behavior. Operational end-to-end tests live in `internal/ope2e` and are separate from unit checks.

Future integration tests must cover connector disconnect during side effects, credentials expiry, budget termination, denied path traversal, symlink escapes, project conflicts, and a complete cloud-only continuation after preparation.

## 11. Revised macOS interaction contract

The product entry is a transient, titleless popover anchored to the menu-bar icon, never a standalone main window. Clicking the icon toggles it; clicking outside dismisses it while the app stays resident. Match Magpie's compact utility hierarchy with Runta orange accents.

The user signs in through Runta device-code authorization. Supported agents are discovered automatically on this Mac. Cloud takeover is explicitly user-initiated, after authentication and project selection; signing in or detecting an agent never enables it automatically. No SSH aliases, ports or transport diagnostics belong in onboarding.

Current native implementation: an arrowless menu-bar NSPanel with a bundled WKWebView UI, segmented navigation and execution-location controls, agent discovery, session management, account usage, and settings. The Go control daemon owns device authorization, Keychain access through a stable helper, account state, and cloud takeover. Swift presents state and sends typed control requests. Local animations respect reduced-motion settings.

Device authorization update: explicitly reuse Errand client_id `runta_crew` with device name `Fennec for Mac`. Implemented browser verification, bounded polling with slow_down handling, and Fennec-owned Keychain storage. Authorization endpoint acceptance verified live; user-approved token exchange and restart restoration still require end-to-end acceptance. Earlier statements about an unregistered client refer to the replaced `fennec` client_id.

## Shared runtime (revised product contract)

On first authenticated login, reconcile one durable runtime per user, shared by all that user's supported agents. Runtime ownership must use authenticated user ID, not email/display name; the backend must enforce atomic uniqueness across devices. Cloud Agents v2's one-agent-per-runtime model is not the provisioning primitive for this feature.

Preparation stages: allocate/reuse, wait for runtime readiness, reuse each existing cloud agent binary, install the publisher's latest stable release only when that binary is missing, install pinned Magpie, configure global agent routing, verify. Local CLI versions are not synchronized, compared or used to gate new sessions. New cloud installations are staged and verified before atomically replacing the executable alias; existing sessions retain their original executable. UI progress is completed stages, never a timer-based percentage. Failed stages remain retryable; ready requires the binary, gateway health and every selected agent's routing. Supported local provider credential stores and model preferences are synchronized through authenticated SSH, with conflict handling for remotely refreshed credentials. Missing provider authorization blocks the affected agent route. Agent execution remains explicitly user-triggered even though runtime preparation begins automatically.

`internal/setup` implements reconciliation and is invoked by the control daemon during runtime preparation. Existing cloud agent binaries are reused; missing binaries are installed without requiring local/cloud version parity. Preparation checkpoints skip unchanged work after health checks. Pin Magpie artifacts and verify checksums before installation. Magpie uses MIT; preserve transitive notices when packaging its binary.

Connector authorization: paired SSH devices request native Popup approval by
default for local exec, SFTP and forwarding. Allow once and Deny apply to one
request; Never Ask persists approval for that paired key and can be reset.
Pairing and revocation checks remain mandatory. Cloud agent full-permission
mode does not bypass local connector approval or macOS privilege/TCC boundaries.
Connector readiness is independent of agent installation and model readiness:
it starts after runtime ownership and SSH execution verification.

## Model credential ownership (supersedes the secret-stub proposal)

Magpie owns model providers, API keys and subscription credentials. Fennec
migrates authorized local Magpie model credential stores to the user's shared
runtime over verified SSH. Runtime provisioning remains Runta REST; model setup
must not query Runta model providers or bind Runta secret stubs. The prepare path
now migrates providers.json and logins.json only, with 0600 atomic writes and
hash baselines to refuse overwriting remote-refreshed credentials. Plugin-backed
subscriptions need explicit adapter/dependency handling before claiming readiness.

Local interception boundary: a future opt-in PATH shim must preserve original
CLI semantics while takeover is off. `local-agent` now uses process replacement
(exec), retaining cwd, stdio, signals and exit status; resolver excludes its own
shim directory and symlink/hardlink aliases. No user PATH or original agent binary
has been changed. Cloud dispatch/project handoff is still required before enabling
a production interception switch.

## Default cloud computer resources

New Fennec runtimes request 2 vCPUs and 4096 MiB baseline memory, with a 4096 MiB memory limit. Do not rely on the API default machine size. Existing runtimes are reused without deletion; creation defaults alone do not resize them.

## Agent enablement and session workspace scope

The Agents switch selects cloud execution for all subsequent launches of that
agent. It must not open a directory picker or bind the agent to one repository.
Project discovery, synchronization scope and permission requests belong to each
session and may change during its work. Enabling an agent does not authorize
uploading Home or every local directory. Existing sessions are not relocated by
changing this switch. The current broker's preauthorized-root requirement remains
an implementation gap: session-scoped synchronization authorization must replace
that requirement before claiming transparent arbitrary-directory launch support.
