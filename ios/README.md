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

The scan screen also offers **Continue with Google**, **Continue with GitHub**,
and **Continue with email**. These open the Runta Dashboard in the system
authentication window. Google and GitHub select the requested provider; email
uses a code entered in that window, with a resend countdown and the existing
Turnstile challenge. A browser already signed in proceeds to device consent.

The user selects an organization and authorizes this iPhone. The existing device
flow issues an independent Runta credential, stores it in Keychain, and closes
the window. Supabase sessions and provider tokens never cross into the app.
Cancel stops polling; the scan flow and the Mac credential remain separate.

Deploy the matching Runta Dashboard and email-code delivery change before this
app release. The provider callbacks and Supabase Dashboard callback allowlist
remain the existing HTTPS URLs; this hosted flow does not require a new
`fennec://` OAuth callback. `fennec://pair` remains the QR handoff route.
Verify Supabase email OTP expiry/rate limits and Resend delivery in each
environment. The code is rendered by Runta's email template, not Supabase SMTP.

## Not built yet

- Live sessions show the agent's terminal screen (like the Mac panel's session
  view) and accept text. Approval and question sheets only appear for example
  sessions, because the cloud listing reports a state (Needs input), not the
  request's content.
- Lock Screen notifications and Live Activity need APNs from the Runta backend.
- Native email-code entry outside the system authentication window.
