.PHONY: dev build test check
.PHONY: preview-ui
preview-ui:
	go run ./cmd/ui-preview

dev:
	go run . serve
build:
	go build -trimpath -o bin/fennec .
test:
	go test -race ./...
check:
	go vet ./...
	test -z "$$(gofmt -l .)"
	go run ./cmd/check-english

.PHONY: macos macos-run
macos:
	./scripts/build-macos.sh
macos-run: macos
	open build/Fennec.app

.PHONY: check-ui test-connector
check-ui:
	node --check internal/gui/assets/app.js
	node --check native/macos/UI/panel.js
	node --check native/macos/UI/agent-login.js
	node --check native/macos/UI/cloud-computer.js
	node --check native/macos/UI/locale.js
	node --check native/macos/UI/menus.js
	node --check native/macos/UI/terminal.js
	node --check native/macos/UI/terminal-links.js
	node scripts/tests/terminal-bridge.cjs
	node scripts/tests/terminal-localization.cjs
	node scripts/tests/menu-state.cjs
	node scripts/tests/session-status.cjs
	node scripts/tests/session-detail.cjs
	node scripts/tests/session-row.cjs
	node scripts/tests/preparation-state.cjs
	node scripts/tests/login-state.cjs
	node scripts/tests/takeover-state.cjs
	node scripts/tests/fox-motion.cjs

test-connector: build
	python3 scripts/tests/relay.py
	FENNEC_TEST_BINARY="$(CURDIR)/bin/fennec" python3 scripts/tests/relay-ssh.py
	FENNEC_TEST_BINARY="$(CURDIR)/bin/fennec" python3 scripts/tests/never-ask.py
	FENNEC_TEST_BINARY="$(CURDIR)/bin/fennec" python3 scripts/tests/connector-cancellation.py
	FENNEC_TEST_BINARY="$(CURDIR)/bin/fennec" python3 scripts/tests/forward.py
	FENNEC_TEST_BINARY="$(CURDIR)/bin/fennec" python3 scripts/tests/sftp.py

.PHONY: test-e2e-ops
test-e2e-ops:
	FENNEC_LIVE_E2E=1 FENNEC_E2E_BINARY="$(CURDIR)/build/Fennec.app/Contents/MacOS/fen" go test -count=1 -timeout=4m -v ./internal/ope2e

.PHONY: test-e2e-startup test-e2e-performance test-e2e-configuration
test-e2e-configuration:
	go run ./cmd/profile-launch --binary "$(CURDIR)/build/Fennec.app/Contents/MacOS/fen" --mode daemon-cold --invalidate-configuration --samples 3

test-e2e-startup:
	go run ./cmd/profile-launch --binary "$(CURDIR)/build/Fennec.app/Contents/MacOS/fen" --mode daemon-cold --samples 3
	go run ./cmd/profile-launch --binary "$(CURDIR)/build/Fennec.app/Contents/MacOS/fen" --mode cold --samples 3
	go run ./cmd/profile-launch --binary "$(CURDIR)/build/Fennec.app/Contents/MacOS/fen" --mode warm --samples 3

# Serialize live measurements even under make -j: parallel profilers contend
# for the same shared machine and distort the wall-time acceptance boundary.
test-e2e-performance:
	$(MAKE) test-e2e-startup
	$(MAKE) test-e2e-ops
	$(MAKE) test-e2e-configuration

.PHONY: test-cmux
test-cmux:
	test -n "$(CMUX_BINARY)" && test -x "$(CMUX_BINARY)"
	FENNEC_CMUX_BINARY="$(CMUX_BINARY)" go test -race -count=1 -timeout=90s ./internal/cmuxhooks ./internal/hookbridge ./internal/hookplugins ./internal/localssh ./internal/control ./internal/terminalstream ./internal/launcher ./internal/cli ./cmd/fennec-terminal
.PHONY: test-e2e-launchd-demand
test-e2e-launchd-demand:
	test "$$(uname -s)" = Darwin
	FENNEC_LAUNCHD_E2E=1 go test -count=1 -timeout=30s -v ./internal/cli -run '^TestLaunchAgentWaitsForExplicitDemand$$'

.PHONY: test-e2e-installation
FENNEC_ACCEPTANCE_APP ?= $(CURDIR)/build/Fennec.app
test-e2e-installation:
	test "$$(uname -s)" = Darwin
	go run ./cmd/check-installation --app "$(FENNEC_ACCEPTANCE_APP)"
