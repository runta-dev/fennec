# Candidate 2f11e5d acceptance

Version `0.2.11120351`, source
`2f11e5dbca34d342a7b6853d3df88e37976a4770`.
Release CI 43090963108 and source CI 43090961019 succeeded. SHA256SUMS, App
signature, App/DMG stapler validation, and both Accepted notarization results
were checked. Helper CDHash remains `ca5027a933e4998d33f9fcc71747f8f3a2cf2eb9`,
team `4J499G24H7`.
Installed build: `aa2a9832fb18762148ec5fa82c4a70e86cf6071adde24dc74c5ae079c5952384`.
Backup: `/Applications/.fennec-update-3625031212/Fennec.app`.

## Actual running-UI upgrade

Before installation, native UI PID was 20168. The candidate's product installer
returned `ui_restarted=true`; afterward UI PID was 45367 and daemon PID 45396
reported the exact new bundle build. No manual UI restart or separate `fen start`
was used to rescue this upgrade. Account restored authorized and the existing
runtime/broker subsequently reached ready. This proves that upgrade case on the
developer Mac, not clean-Mac installation or reboot acceptance.

## Actual switch profile

Evidence: `/tmp/fennec-2f11e5d-magpie-profile.jsonl` and `.err`.
Switch confirmed in 3609.950ms and failed the 1-second budget. Restoration
confirmed in 635.492ms and matched original routes. Original mode remains off.

Local preparation wall time was 762.743ms; its overlapping native-account read
was 308.024ms and local routes were 760.604ms. Previously these stages were serial
and totaled about 1534ms in the db0fd06 sample; this comparison is observational,
not a controlled claim of fixed savings.

Remote round trip was 2824.297ms: request write 0.670ms, response wait 2823.185ms,
and remote operation 326.593ms. Remote stages: tool preflight 1.287ms, catalog
preflight 6.486ms, accounts 16.342ms, routes 296.835ms, catalog readback 5.561ms.
Restoration RTT was 361.186ms, with remote execution 3.291ms. The remaining large
switch response wait needs attribution; do not claim all of it is network latency
or infer a throughput bottleneck without further evidence.

## Billing and installed-path findings

With private profiling enabled, a live `account.billing` failure recorded only
`account.http_status.403`. The account itself remained authorized. This is an API
access failure, not a rendering-only issue; its server-side permission/audience
cause remains to be identified.

Source inspection also found inconsistent local Magpie discovery: detection
supports user Applications/case variants, but route reads used only PATH and a
fixed lowercase system-app fallback. The working tree now reuses the established
LocalMagpieBinary resolver and excludes relative application roots. These changes
are not in this measured candidate and have not been credited as a speed gain.

Performance, real vendor OAuth, disposable-cloud deletion, and clean-Mac final
acceptance remain incomplete. No real cloud computer was deleted.

## Payload attribution and next transport change

An additional failed sample recorded a 29-entry model catalog of only 707 JSON
bytes, ruling out a large catalog response as the explanation for that sample's
4.39-second response wait. Switch wall time was 5764.508ms; restoration also
failed its budget at 1618.413ms. Evidence is retained in
`/tmp/fennec-2f11e5d-catalog-size.jsonl` and `.err`.

A read-only local-input diagnostic collected the same configuration shape without
sending credentials and emitted sizes only. The operation JSON was 87197 bytes;
Pi's model configuration accounted for 54252 base64 bytes. BestSpeed gzip yielded
23510 bytes, or 31348 bytes after wire base64 encoding. No credential contents
were printed. Evidence: `/tmp/fennec-config-payload-compressed-sizes.json`.

Source now negotiates `configuration_gzip` on the existing resident protocol and
compresses only larger reconfiguration operations when beneficial. Older peers
receive the unchanged uncompressed form. Expanded input is bounded by the existing
256KiB inbound budget; corrupt/truncated, ambiguous, and non-configuration payloads
are rejected. Tests verify capability gating, payload preservation, bounded
expansion, and multiple operations on the same resident transport. No performance
pass is claimed until the signed candidate is measured.

## Billing authorization evidence

The inspected Runta source's device authorization scopes include org/runtime/event/
secret access, but not `billing:read`; billing handlers require ReadBilling.
Fennec currently requests the `runta_crew` device client. This is consistent with
the observed 403 and is a fresh-login policy gap, not permission to bypass the
server check. A properly scoped Fennec client/consent path needs backend work and
verification. The unrelated dirty Runta checkout was only inspected, not modified.
