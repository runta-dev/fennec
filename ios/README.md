# Fennec for iPhone

SwiftUI app for viewing and managing Fennec cloud agent sessions. The design is
in [DESIGN.html](DESIGN.html); open it in a browser.

## How it reuses the Fennec client

The app does not reimplement Fennec. `mobile/` (Go package `fennecmobile`) is
bound with gomobile into `ios/Frameworks/FennecCore.xcframework` and wraps the
same packages the Mac client uses:

| Feature | Fennec package |
| --- | --- |
| Google, GitHub, email-code sign-in (device flow, token expiry) | `internal/account` |
| Find the shared cloud computer, provision the device SSH key, connect | `internal/runta` |
| List, read, send, stop sessions (`fennec-terminal` on the cloud computer) | `internal/runtime` |
| Session filtering, titles, status labels | `internal/cli.ParseSessions` (the Mac panel's parser) |

Swift only adds the Keychain token store and the UI. The phone never creates a
cloud computer; it connects to the one the Mac app prepared.

## Requirements

- Xcode 26 or later with an iOS simulator runtime (`xcodebuild -downloadPlatform iOS`)
- Go, gomobile (`go install golang.org/x/mobile/cmd/gomobile@latest && gomobile init`)
- [XcodeGen](https://github.com/yonaskolb/XcodeGen) (`brew install xcodegen`)

The framework and the Xcode project are generated and not checked in.

## Build and run

```sh
make ios                          # binds FennecCore and generates the project
open ios/Fennec.xcodeproj         # then Run on an iPhone simulator
```

From the command line:

```sh
cd ios
xcodebuild -project Fennec.xcodeproj -scheme Fennec \
  -destination 'platform=iOS Simulator,name=iPhone 18 Pro' \
  -derivedDataPath build test
```

Launch arguments (Debug builds, set in the scheme):

| Argument | Effect |
| --- | --- |
| `-PreviewSignedIn YES` | Skip sign-in and show example sessions |
| `-RuntaEnvironment integration` | Start on Integration (api.runta.xyz) |

## Switching environments

Tap the Fennec mark on the sign-in screen, or the avatar on the Account screen,
five times within 1.8 seconds (the Mac panel uses the same gesture on its
logo) to choose Production or Integration. Each environment keeps its own
Keychain token and device SSH key, and an INT badge shows when Integration is
active.

## Sign-in

The sign-in page offers Google, GitHub, Email, and Scan, each with an icon.
The camera opens only after selecting Scan. Google and GitHub start Supabase
OAuth directly in the system authentication window with client-owned PKCE and
return through `fennec://auth/callback`. Email uses native address and OTP
fields and Supabase's `/otp` and `/verify` endpoints; configure the Supabase
email template to include `{{ .Token }}` and production SMTP delivery.

After authentication, the Go client registers the session with the Dashboard
API, chooses the same first organization as Dashboard, and creates a scoped
30-day API key named Fennec iPhone. No Runta web page, device grant, or extra
consent screen is presented. Dashboard authentication uses the Supabase Bearer
session, not a browser cookie. Only the resulting API key is saved in Keychain;
Supabase tokens stay in memory. Failed storage or a canceled attempt revokes a
newly issued key when its ID is available. QR pairing keeps its existing Mac
approval flow and independent credential.

Add the exact `fennec://auth/callback` URL to each Supabase project's Redirect
URLs. Keep the provider-console callback set to Supabase's HTTPS callback and preserve the
Dashboard allowlist. `fennec://pair` remains the QR handoff route. See the
[shared deep-link contract](../docs/deep-links.md).
Verify Supabase email OTP expiry, rate limits, SMTP delivery, and the OTP email
template in each environment.

## Not built yet

- Live sessions show the agent's terminal screen (like the Mac panel's session
  view) and accept text. Approval and question sheets only appear for example
  sessions, because the cloud listing reports a state (Needs input), not the
  request's content.
- Lock Screen notifications and Live Activity need APNs from the Runta backend.
