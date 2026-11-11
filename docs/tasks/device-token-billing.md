# Read-only account and billing API for standalone Runta device tokens

Implement the backend and create a PR; do not merge or deploy production. Use a
separate clean remote checkout after checking workspace ownership, repository rules,
and other sessions. Do not modify a Forge sync mirror or an occupied slot. Do not
start sub-agents.

## Goal

Fennec uses device-code authorization (currently Errand's `client_id=runta_crew`).
It needs account/organization status, balance, spend, and billable resource usage
through Runtime's public REST API without a Dashboard browser session. First verify
actual token types/scopes and existing endpoints. Treat “Dashboard only” as a hypothesis;
do not add duplicate endpoints based on an assumption.

## References and scope

- Dashboard `BillingPage.tsx`, `dashboard/src/lib/api.ts`, and `billing-usage.ts`.
- Dashboard `/v1/organizations/{org_id}/billing/account`, `/billing`,
  `/billing/usage?window=7d|30d`, and credit grants.
- Public `/v2/me` device-client identity, runta_crew scopes, billing domain/shared
  application services, `openapi/public.yaml`, and SDK generation.
- Expose account usability/restrictions, currency, available balance, distinguishable
  grant/paid credit sources using existing capabilities, total spend in the selected
  window, CPU/Memory/Disk billable usage and units, window boundaries, and
  fresh/stale/unavailable/as_of state.
- CPU vCPU-h and Memory/Disk GiB-h are cumulative billable usage, not live utilization.
  Define monetary precision and minor units explicitly.
- The user mentioned a default $50 grant. Do not hardcode balance or issue credits
  during reads; use actual billing-service records.
- Reuse Dashboard billing services and caches; do not duplicate Metronome logic or
  bypass authorization. Use minimal read-only scopes, explicit organization selection,
  and user/organization isolation. Reject unauthorized same-organization data and
  cross-organization access.
- Add no top-up, payment, or subscription write permissions. The $5 Flash subscription
  PR is unmerged; do not depend on it or expose a purchase entry.
- Preserve Dashboard behavior. Follow repository processes for OpenAPI, generated
  clients/SDKs, documentation, and meaningful contract tests.

## Acceptance

1. Explain how Fennec obtains organization scope. Provide recommended paths, request/
   response examples, and currency/unit/window semantics.
2. Real device-code tokens (runta_crew and other supported clients where applicable)
   can read permitted data. Reject insufficient scopes, other users/organizations,
   expired tokens, and revoked tokens correctly.
3. Distinguish zero balance from unavailable data and stale from fresh. Do not double
   count credits or lose monetary precision.
4. Match Dashboard balance and usage for the same organization/window, allowing only
   documented cache-age differences.
5. Submit focused test evidence and a feature PR. Do not directly push main/dev/integration
   or merge/deploy without authorization.

## Deliverable

Write `/home/shiqi/fennec-device-billing-result.md` with PR URL, commit, actual changes,
validation evidence, unverified boundaries, and a Fennec integration example. Do not
output tokens, cookies, complete keys, or sensitive billing details.
