import Foundation

/// App Review's sign-in. Entering this address on the email page sends no
/// code; any code then opens the demo: sample sessions on the phone alone,
/// with no Runta account, cloud computer or Mac. Sessions can be created,
/// messaged and stopped; agents answer with replies recorded from real runs
/// (the website's walkthrough, website/components/mobile-scenario.ts).
/// The demo lasts until Sign out.
enum ReviewDemo {
    static let email = "appreview@runta.com"

    static func matches(_ address: String) -> Bool {
        address.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() == email
    }

    static let profile = Profile(displayName: "App Review", email: email)

    static var enabled: Bool {
        get { UserDefaults.standard.bool(forKey: "ReviewDemo") }
        set { UserDefaults.standard.set(newValue, forKey: "ReviewDemo") }
    }
}

/// One recorded agent turn, replayed when a demo session gets a message.
struct DemoScript {
    enum Step {
        case activity(String)
        case agent(String)
        case tool(String)
    }
    /// Seconds the agent reports having worked ("Worked for …").
    var worked: Int
    /// Each step and the milliseconds after the prompt at which it appears.
    var steps: [(at: Int, step: Step)]

    /// Codex, Claude Code and Pi replay their own recordings; the other agents
    /// take turns with them. Later messages in a session move to the next one.
    static func reply(for agent: AgentKind, turn: Int) -> DemoScript {
        let first: Int = switch agent {
        case .codex, .cursor: 0
        case .claudeCode, .kimiCode: 1
        case .pi, .grokBuild, .openCode: 2
        }
        return all[(first + max(turn - 1, 0)) % all.count]
    }

    private static let mac = "ssh Alexs-Mac.local 'cd /tmp/relay-mac-demo && "

    static let all: [DemoScript] = [
        // Codex
        DemoScript(worked: 35, steps: [
            (1800, .agent("I’ll read the webhook code on your Mac, fix the duplicate deliveries, and run the tests there.")),
            (3300, .tool(mac + "for f in AGENTS.md go.mod *.go; do …'")),
            (7400, .agent("The delivery handler increments the count every time it receives an event, even when that event ID was already processed. I’ll add a duplicate check under the existing mutex and test both repeated and concurrent deliveries.")),
            (7800, .tool(mac + "/usr/bin/perl -0pi -e …'")),
            (8300, .activity("Adding map guard tests")),
            (12300, .tool("ssh Alexs-Mac.local 'uname -s && cd /tmp/relay-mac-demo && /opt/homebrew/bin/go test -race ./...'")),
            (13500, .agent("Fixed duplicate webhook deliveries in `/tmp/relay-mac-demo` on your Mac. Repeated event IDs are now ignored under the existing mutex. Added tests for repeated and concurrent deliveries.\n\nAll tests passed with `go test -race ./...`; `uname -s` confirmed `Darwin`.")),
        ]),
        // Claude Code
        DemoScript(worked: 45, steps: [
            (2300, .agent("I'll start by listing the fixture's files in `/tmp/relay-mac-demo` on your Mac over SSH.")),
            (2400, .tool(mac + "find . -type f -not -path \"./.git/*\" | head -50'")),
            (3766, .tool(mac + "cat go.mod relay.go relay_test.go AGENTS.md'")),
            (5300, .agent("`Authorize` in `relay.go` only checks revocation and never compares workspaces, so a key from workspace A passes in workspace B. I'm fixing that and adding tests for the cross-workspace and revoked cases.")),
            (6200, .activity("Fixing Authorize and adding tests")),
            (6300, .tool(mac + "/usr/bin/perl -0pi -e \"s/func Authorize…\" relay.go'")),
            (9200, .agent("The edit is in and gofmt reports no formatting problems. Now I'll run the tests on the Mac, with the race detector on:")),
            (9800, .tool(mac + "uname -s && /opt/homebrew/bin/go test -race -v ./... 2>&1 | tail -20'")),
            (13300, .agent("I fixed a cross-workspace access bug in your Mac project at `/tmp/relay-mac-demo`. All tests pass, and `uname -s` printed `Darwin`.\n\n**The bug:** `Authorize(key, workspace)` in `relay.go` only checked that the key wasn't revoked, so a valid key from `acme` was accepted in `globex`.\n\n**The fix:** a key must not be revoked and must belong to the workspace being requested.\n\nNothing was committed.")),
        ]),
        // Pi
        DemoScript(worked: 62, steps: [
            (1300, .agent("I’ll inspect the retry code and existing tests on your Mac, add coverage for edge cases, and fix any failures they reveal.")),
            (1540, .tool(mac + "pwd && rg --files'")),
            (2355, .activity("Retrying the SSH connection")),
            (2385, .tool("ssh -o ConnectTimeout=10 -o BatchMode=yes Alexs-Mac.local 'cd /tmp/relay-mac-demo && /usr/bin/find . -type f -maxdepth 3 -print'")),
            (3156, .agent("The SSH connection to your Mac has timed out twice. I’ll try once more; I haven’t been able to read or change any files yet.")),
            (3355, .tool("ssh -o ConnectTimeout=10 -o ConnectionAttempts=1 -o BatchMode=yes Alexs-Mac.local 'cd /tmp/relay-mac-demo && /usr/bin/sed -n \"1,160p\" go.mod'")),
            (3770, .tool(mac + "/usr/bin/sed -n \"1,180p\" AGENTS.md relay.go relay_test.go'")),
            (7200, .agent("`RetryDelay` caps large attempts at 32 seconds, but negative attempts cause a shift panic. I’ll add tests for the backoff sequence, cap boundaries, and extreme integers, then make negative attempts use the initial one-second delay.")),
            (7255, .activity("Implementing append-table extremes")),
            (7255, .tool(mac + "/bin/cat >> relay_test.go <<'EOF' …'")),
            (10000, .tool(mac + "/opt/homebrew/bin/gofmt -w relay.go && uname -s && /opt/homebrew/bin/go test -race ./... -count=1'")),
            (11240, .agent("Completed directly on your Mac in `/tmp/relay-mac-demo`.\n\n- Added 10 retry test cases covering negative attempts, the exponential sequence, cap boundaries, and minimum/maximum integers.\n- Confirmed negative attempts caused a panic, then fixed `RetryDelay` to return the initial one-second delay for them.\n- Ran the full suite with the race detector.\n\nVerification printed `Darwin`, and all tests passed: `ok relay`. No commit was created.")),
        ]),
    ]
}

extension SessionStore {
    /// Plays a recorded reply into a demo session, step by step on its own
    /// clock. A new message or Stop replaces the reply in progress.
    func playDemoReply(_ id: String) {
        guard core == nil, let session = session(id) else { return }
        demoReplies[id]?.cancel()
        let script = DemoScript.reply(for: session.agent, turn: session.messages.filter(\.isUser).count)
        update(id) {
            $0.state = .working
            $0.turnStarted = .now
            $0.statusSince = .now
            $0.activity = nil
        }
        demoReplies[id] = Task { [weak self] in
            let start = ContinuousClock.now
            for (index, entry) in script.steps.enumerated() {
                do { try await Task.sleep(until: start + .milliseconds(entry.at), clock: .continuous) } catch { return }
                guard let self else { return }
                let last = index == script.steps.count - 1
                self.update(id) { session in
                    switch entry.step {
                    case .activity(let text):
                        session.activity = text
                    case .tool(let command):
                        let use = ToolUse(name: "ran", summary: command)
                        if case .tools(let uses)? = session.messages.last?.kind {
                            session.messages[session.messages.count - 1].kind = .tools(uses + [use])
                        } else {
                            session.messages.append(.init(kind: .tools([use]), time: .now))
                        }
                    case .agent(let text):
                        if last {
                            let duration = script.worked < 60 ? "\(script.worked)s" : "\(script.worked / 60)m \(script.worked % 60)s"
                            session.messages.append(.init(kind: .step(L10n.text("ios.worked_for", values: ["duration": duration]))))
                        }
                        session.messages.append(.init(kind: .agent(text), time: .now))
                    }
                    if last {
                        session.state = .idle
                        session.activity = nil
                    }
                }
            }
            self?.demoReplies[id] = nil
        }
    }

    /// The composer's Stop in a demo session: ends the reply in progress.
    func stopDemoTurn(_ id: String) {
        guard core == nil else { return }
        demoReplies[id]?.cancel()
        demoReplies[id] = nil
        update(id) {
            guard $0.state == .working else { return }
            $0.messages.append(.init(kind: .step(L10n.text("ios.stopped"))))
            $0.state = .idle
            $0.activity = nil
        }
    }
}
