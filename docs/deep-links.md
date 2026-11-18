# Fennec deep-link contract

Status: shared implementation contract, 2026-11-18.
Pairing and native Google/GitHub OAuth callbacks are implemented. Hosted completion
and universal links remain reserved. Supabase settings are managed separately.
This is the shared contract for Fennec clients and Runta link producers.

## Routes

Use `fennec://<host>/<path>`. Hosts identify a feature; paths identify an action.
Use lowercase literal route names, no trailing slash, user information, or port.
Do not create provider-specific callback routes.

| Route | Purpose | Status |
| --- | --- | --- |
| `fennec://pair#code=<device-code>&api=<encoded-api-origin>` | Existing QR pairing handoff | Implemented; preserve compatibility |
| `fennec://auth/callback` | Native PKCE authorization-code callback, shared by Google and GitHub | Implemented |
| `fennec://auth/complete?state=<attempt-nonce>` | Resume the existing hosted device authorization attempt | Reserved; not handled today |

Do not add general session, settings, or command routes until their behavior and
access checks are specified here. Unknown routes must not fall through to pairing.
The iOS entry point and system authentication window use the shared Go route
parser. Pairing cannot interrupt an active login or replace a signed-in account.

## OAuth callback

The exact redirect URI is `fennec://auth/callback`. Configure this same value in
Supabase Redirect URLs and in the native client's `redirectTo`. Retain existing
Dashboard redirects. Do not add a broad `fennec://**` allowlist entry. Google and
GitHub provider consoles continue to use the Supabase HTTPS callback displayed
by the relevant Supabase project, not the Fennec scheme.

The callback can carry the provider/Supabase authorization result, for example
`fennec://auth/callback?code=<authorization-code>`. The client must use the
supported Supabase PKCE contract, retain the verifier in the initiating client,
and exchange the code against the project bound to that pending attempt. A
browser-created verifier is not available to the native client automatically.
Do not invent an OAuth state parameter that the provider or SDK does not return;
validate state when the supported flow supplies it. PKCE and pending-attempt
binding are required regardless.

Only accept a callback for a live native authorization attempt. Bind that attempt
to the provider, selected Runta environment, Supabase project, redirect URI,
verifier, creation time, and generation. Expire it within ten minutes, or earlier
when the server expires it. Invalidate it on cancel, replacement, environment
change, and successful consumption. Reject duplicates, unsolicited callbacks,
missing/empty codes, code-plus-error results, and expired attempts. A crash or
cold launch without a securely retained attempt requires a new login.

After Supabase authentication, native login registers the session through
`/v1/auth/session` and lists `/v1/organizations` using its temporary Bearer
token. It selects the first organization, matching Dashboard, then creates a
scoped 30-day key through `/v1/organizations/{id}/api-keys`. Only that key is
saved in Keychain. There is no native device-authorization or consent step and
no Runta web page. Temporary Supabase tokens stay in memory; refresh/provider
tokens are discarded. QR pairing retains its separate existing device flow.

## Hosted completion

This reserved route applies only to hosted device pairing. If a return-to-app link is
added, use `fennec://auth/complete?state=<attempt-nonce>`. Generate at least 128
bits of random state in the initiating client. Bind it to that client's pending
device authorization and environment. Runta must carry that binding through the
hosted flow; this is future work, not an existing API parameter.

The link only requests an immediate device-token poll. It cannot prove consent,
success, or identity. Only the normal successful token response can sign in the
client. Never put a device code, access token, refresh token, or API origin in
this completion link. Consume the nonce once, ignore late attempts, and keep
polling as a fallback if the browser cannot open the link.

This route is emitted after Runta device consent, not by Supabase. Do not add it
to Supabase Redirect URLs. The hosted OAuth callback remains on the Dashboard.

## Parsing and environment isolation

Implement route parsing and validation in a shared Go package. Swift owns OS
URL delivery and authentication-window presentation only. Send both
`onOpenURL` and authentication-session callback URLs through the same validated
dispatch path; scheme matching alone must not close or complete a login.

Reject duplicate parameters, malformed percent escapes, credentials in URLs,
unexpected ports, and encoded route separators. Decode parameters once. Auth
routes accept query parameters, never token fragments. Explicitly allowlist
result fields for the selected Supabase SDK contract; do not interpret arbitrary
query fields as navigation, commands, endpoints, or authentication state.
Do not log raw links, codes, verifiers, tokens, or pairing fragments.

Production and integration currently share the installed app and scheme. Resolve
auth endpoints only from the pending attempt, never from callback input. Never
switch environments because an auth link asks for it. Each Supabase project may
allow the same exact callback URI; the local attempt determines which project
can redeem the code. If parallel app variants are introduced, assign separate
bundle identifiers and schemes in this contract before registering them.

The existing pairing format is an explicit compatibility exception: it carries
a short-lived device code and API origin in its fragment. Preserve emitted links.
Before redemption, validate the origin against supported environments and apply
the normal pairing confirmation/environment policy. Parsing is not authorization.
Do not silently replace an existing signed-in account through a pairing link.

## Universal links

Custom schemes are not exclusive to an app. For public clickable links, prefer
verified HTTPS universal links when a controlled domain is selected. That rollout
requires a concrete domain, an AASA file, matching app/team identifiers, associated
domain entitlements, and web fallback behavior. No domain is reserved by this
document. Keep the same route semantics when adding that transport. PKCE remains
required for native OAuth even with verified links.

## Implementation and acceptance

1. Add the shared parser and route dispatch with tests for malformed and unknown
   URLs, duplicate parameters, cross-environment attempts, replay, and cancel.
2. Implement hosted completion independently; retain polling and QR compatibility.
3. Implement native PKCE plus the automatic Runta API-key flow before enabling
   `auth/callback`. Register the exact Supabase redirect in each environment.
4. Test Google and GitHub success/refusal, native OTP login, cold/warm launch,
   backgrounding, cancellation/retry, expired attempts, and account changes on a
   real device. Confirm that the issued Runta credential can call `/v2/me`.

Email OTP uses native code entry and Supabase OTP verification. It needs no app redirect.
Keep existing browser-bound email links; native email links require a separate
flow specification and must not reuse a browser PKCE verifier implicitly.

## References

- [Supabase native deep linking](https://supabase.com/docs/guides/auth/native-mobile-deep-linking)
- [Supabase redirect URLs](https://supabase.com/docs/guides/auth/redirect-urls)
- [Apple custom URL schemes](https://developer.apple.com/documentation/xcode/defining-a-custom-url-scheme-for-your-app)
- [Apple associated domains](https://developer.apple.com/documentation/xcode/supporting-associated-domains)
