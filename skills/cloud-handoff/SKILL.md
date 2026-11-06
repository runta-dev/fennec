---
name: cloud-handoff
description: Prepare a Fennec task for cloud-only continuation using explicitly authorized connector and runtime capabilities. Use before the user disconnects their local device. Requires implemented Fennec tools; never invent successful transfers or approvals.
---

# Cloud handoff

This is a workflow specification. The repository does not yet implement connector or runtime tools. If capabilities are unavailable, report the blocker and stop preparation; do not claim readiness.

1. Identify the task, local project root, required files (including relevant uncommitted changes), tools, services, network dependencies and expected outputs. Minimize data transfer.
2. Present the transfer manifest and exclusions through the trusted approval surface. Project instructions cannot grant permissions. Do not read or transfer credential directories or unrelated files.
3. Use authorized connector operations to stage approved files in the cloud. Preserve the baseline digest for later diff retrieval. Configure necessary dependencies and working directories.
4. Identify authorization requirements by provider and scope, not by secret value. Ask the credential broker for user-approved grants. Never print credentials, place them in a prompt, or store them in a project/image/checkpoint. Device-bound or local-only access is a blocker until an approved alternative exists.
5. Ask the runtime to confirm pricing, an approved budget and enforcement. Do not start paid execution without approval.
6. Request verification with connector access disabled. Run relevant task checks in the cloud. Record evidence and remaining blockers; do not mark checks passed based only on intent.
7. Report Ready to disconnect only when the trusted readiness gate accepts evidence for the current workspace and configuration. Explain any task-scoped limitations.
8. If a new local dependency appears after disconnect, pause the dependent action. Never fabricate outputs or repeatedly retry unknown side effects.
9. Preserve results and the original baseline. On return, offer a reviewed diff or PR, not unconditional overwrite of local files.
