# Fennec design

## 1. Product contract

**Keep your agent. Leave the laptop.** Fennec separates the agent loop from the device providing its initial environment. The local app is a connector and control surface, not a replacement harness or model router.

The first complete vertical slice is: launch a new cloud agent, access an approved local project, prepare task-specific cloud dependencies, verify without local access, disconnect, complete a real task, reattach, and retrieve its changes.

Today this repository implements only the local read-only foundation. Interfaces and skill instructions are not working cloud integrations.

## 2. Architecture

```text
Local CLI / future desktop shell
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

Connection state is separate: attached, detached, reconnecting, offline. A disconnected client does not mean a failed task. No UI action may mark a session ready merely because files were copied.

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

Product clarification: Fennec is a resident menu-bar/system-tray daemon application, not a website. Its primary entry is a compact tray panel showing connection state, active agents and pending approvals, with launch/reattach actions. Settings open in a separate native window. Closing a window must not stop the connector; explicit quit must explain which local-dependent tasks will be blocked and which cloud tasks can continue. Embedded HTML is an implementation detail, not a requirement to use a browser or remember a localhost port.

The existing browser preview is temporary and its marketing hero should be removed in the desktop iteration. Prioritize the native shell and lifecycle next, ahead of expanding dashboard features. Linux development cannot validate macOS menu-bar behavior, signing, or permissions; those require a local Mac session.

Follow Magpie's compact local utility model: restrained sidebar, discover existing agents, small settings surfaces, and clear statuses. Do not recreate a chat/task management platform.

Use Runta's public brand accent `hsl(30.291 89.565% 54.902%)` (approximately `#f38d25`), with warm neutral surfaces and light/dark themes. Use dark foreground on bright orange; use darker orange text on light tinted surfaces. Keyboard focus, semantic navigation, responsive layout, and visible error states are baseline requirements.

The current lettermark is provisional, not an official fox logo. No Magpie assets or source are copied.

## 9. Delivery stages and acceptance

1. **Foundation (this scaffold):** read-only UI, discovery, package contracts, readiness policy, docs, automated checks.
2. **One real cloud CLI:** explicit opt-in launch, persistent PTY supervisor, detach/reattach, budget enforcement. Acceptance: closing the client does not kill the cloud task.
3. **Connector and handoff:** pairing, scoped transfer, approved grants, offline verifier. Acceptance: a selected real task completes with the local connector disconnected; missing dependencies block visibly.
4. **Results and recovery:** baseline-aware diff retrieval, restart-safe metadata, output replay and unknown-outcome reconciliation.
5. **Packaging:** tray/native shell, another CLI adapter, desktop integration only after protocol validation.

Do not launch paid sessions from this scaffold. Do not claim universal harness support or existing-process migration.

## 10. Validation

`make test` covers API restrictions, static asset delivery, CLI failures, and incomplete/duplicate readiness evidence. `make check` runs Go vet and formatting checks; CI also parses frontend JavaScript. No cloud end-to-end coverage exists until a real adapter is implemented.

Future integration tests must cover connector disconnect during side effects, credentials expiry, budget termination, denied path traversal, symlink escapes, project conflicts, and a complete cloud-only continuation after preparation.
