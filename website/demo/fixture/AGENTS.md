# Relay demo repository

This tiny Go project is the source fixture for a recorded Fennec walkthrough.
The recording copy lives on the paired Mac at `/tmp/relay-mac-demo`. During
recording, the cloud agent reads and edits that copy through `ssh Alexs-Mac.local`, then
runs tests in the Mac shell. Do not make a cloud copy for these demonstration
tasks: their purpose is to show direct access to the user's local files and shell.

Work only within the fixture directory. Do not inspect other projects,
credentials, file ownership, account settings, or shell configuration. Use Go
for implementation and tests; do not use Python or start other agents. Keep
responses concise and in English. Run the relevant tests after changes and show
`uname -s` with the result so the execution location is explicit. The recording
Mac's Go and gofmt executables are `/opt/homebrew/bin/go` and
`/opt/homebrew/bin/gofmt`. Respect the saved permission mode.
