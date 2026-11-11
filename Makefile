.PHONY: dev build test check
dev:
	go run . serve
build:
	go build -trimpath -o bin/fennec .
test:
	go test -race ./...
check:
	go vet ./...
	test -z "$$(gofmt -l .)"

.PHONY: macos macos-run
macos:
	./scripts/build-macos.sh
macos-run: macos
	open build/Fennec.app

.PHONY: check-ui test-connector
check-ui:
	node --check internal/gui/assets/app.js
	node --check native/macos/UI/panel.js
	node --check native/macos/UI/locale.js
	node --check native/macos/UI/menus.js
	node --check native/macos/UI/terminal.js
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
