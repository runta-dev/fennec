// verify-permissions is a live end-to-end check of Fennec permission modes:
// cloud agent -> SSH -> Mac connector -> Seatbelt. It switches modes with fen,
// answers prompts with fen approve/deny, prints sizes only (never secret
// contents), cleans up and restores Approve for me. Usage: make test-e2e-permissions
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const fen = "/Applications/Fennec.app/Contents/MacOS/fen"

var mac, sshHost, workspace string

// discover reads the paired device alias, cloud SSH host and workspace root
// from the running daemon so the check runs on any installation.
func discover() {
	var approvals struct {
		Connector struct{ Detail string } `json:"connector"`
	}
	out, _ := exec.Command(fen, "approvals").Output()
	json.Unmarshal(out, &approvals)
	mac = approvals.Connector.Detail
	var status struct {
		Settings struct {
			SSHHost string `json:"ssh_host"`
		} `json:"settings"`
	}
	out, _ = exec.Command(fen, "status").Output()
	json.Unmarshal(out, &status)
	sshHost = status.Settings.SSHHost
	var sandbox struct {
		Roots []string `json:"effective_sandbox_roots"`
	}
	out, _ = exec.Command(fen, "sandbox", "status", "--json").Output()
	json.Unmarshal(out, &sandbox)
	if len(sandbox.Roots) > 0 {
		workspace = sandbox.Roots[0]
	}
	if mac == "" || sshHost == "" || workspace == "" {
		fmt.Fprintln(os.Stderr, "Fennec is not connected: device alias, cloud SSH host or workspace root unavailable")
		os.Exit(2)
	}
}

var home, _ = os.UserHomeDir()

type expect struct {
	ok       bool   // command exit 0
	prompt   bool   // a user prompt is expected
	decision string // "approve" | "deny" when prompted
	stderr   string // required stderr substring
}

type testCase struct {
	name   string
	cloud  string // raw cloud script; overrides cmd
	cmd    string // command run on the Mac via ssh
	expect map[string]expect
}

func cloudSSH(ctx context.Context, script string) (string, string, error) {
	root := filepath.Join(home, ".config/fennec/ssh/cloud")
	data, _ := os.ReadFile(filepath.Join(home, ".config/fennec/preparation.json"))
	var state struct {
		ID string `json:"runtime_id"`
	}
	json.Unmarshal(data, &state)
	cmd := exec.CommandContext(ctx, "/usr/bin/ssh", "-T", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile="+filepath.Join(root, "known_hosts"), "-i", filepath.Join(root, "runtime_key"), "-o", "ConnectTimeout=15",
		state.ID+"@"+sshHost, script)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Stdin = nil
	err := cmd.Run()
	return out.String(), errb.String(), err
}

type approval struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Reason  string `json:"reason"`
	Kind    string `json:"kind"`
}

func pending() []approval {
	out, _ := exec.Command(fen, "approvals").Output()
	var v struct {
		Approvals []approval `json:"approvals"`
	}
	json.Unmarshal(out, &v)
	return v.Approvals
}

func run(tc testCase, mode string) (code int, stdout, stderr string, prompted []approval) {
	ex := tc.expect[mode]
	script := tc.cloud
	if script == "" {
		script = fmt.Sprintf(`c=$(echo %s | base64 -d); timeout 150 ssh %s "$c" </dev/null; echo "__EXIT=$?"`, base64.StdEncoding.EncodeToString([]byte(tc.cmd)), mac)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()
	done := make(chan struct{})
	var mu sync.Mutex
	go func() {
		seen := map[string]bool{}
		for {
			select {
			case <-done:
				return
			case <-time.After(300 * time.Millisecond):
			}
			for _, a := range pending() {
				if seen[a.ID] {
					continue
				}
				seen[a.ID] = true
				mu.Lock()
				prompted = append(prompted, a)
				mu.Unlock()
				verb := "deny"
				if ex.decision == "approve" {
					verb = "approve"
				}
				exec.Command(fen, verb, a.ID).Run()
			}
		}
	}()
	stdout, stderr, _ = cloudSSH(ctx, script)
	close(done)
	code = -1
	if m := regexp.MustCompile(`__EXIT=(\d+)`).FindStringSubmatch(stdout); m != nil {
		fmt.Sscanf(m[1], "%d", &code)
		stdout = strings.TrimSpace(strings.Replace(stdout, m[0], "", 1))
	}
	mu.Lock()
	defer mu.Unlock()
	return code, stdout, stderr, prompted
}

func daemonPID() string {
	out, _ := exec.Command("pgrep", "-f", "Fennec.app/Contents/MacOS/fen control-daemon").Output()
	return strings.TrimSpace(string(out))
}

// waitReady waits for the restarted daemon's connector before rerunning.
func waitReady() {
	for i := 0; i < 60; i++ {
		out, _ := exec.Command(fen, "status").Output()
		if strings.Contains(string(out), `"broker_ready":true`) {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func both(ask, auto, full expect) map[string]expect {
	return map[string]expect{"ask": ask, "auto": auto, "full": full}
}

func main() {
	discover()
	hint := "fennec-escalate --reason"
	blocked := expect{ok: false, stderr: hint}
	blockedNoHint := expect{ok: false}
	allowed := expect{ok: true}
	approveOK := expect{ok: true, prompt: true, decision: "approve"}
	cases := []testCase{
		{name: "workspace write", cmd: "touch .fennec-e2e-ws && rm .fennec-e2e-ws", expect: both(allowed, allowed, allowed)},
		{name: "write outside workspace", cmd: "touch ~/fennec-e2e-outside", expect: both(blocked, blocked, allowed)},
		{name: "write .git metadata", cmd: "touch .git/fennec-e2e && rm .git/fennec-e2e", expect: both(blocked, blocked, allowed)},
		{name: "read SSH private key", cmd: "wc -c < ~/.ssh/id_ed25519", expect: both(blockedNoHint, blockedNoHint, allowed)},
		{name: "list ~/.ssh", cmd: "ls ~/.ssh >/dev/null", expect: both(blockedNoHint, blockedNoHint, allowed)},
		{name: "read login keychain", cmd: "wc -c < ~/Library/Keychains/login.keychain-db", expect: both(blockedNoHint, blockedNoHint, allowed)},
		{name: "read AWS credentials", cmd: "wc -c < ~/.aws/credentials", expect: both(blockedNoHint, blockedNoHint, allowed)},
		{name: "network", cmd: "curl -sS -m 8 -o /dev/null -w '%{http_code}' https://example.com", expect: both(blockedNoHint, blockedNoHint, allowed)},
		{name: "escalate write outside, approved", cmd: `fennec-escalate --reason "e2e: create a scratch file outside the project" -- touch ~/fennec-e2e-esc`, expect: both(approveOK, approveOK, allowed)},
		{name: "escalate write outside, denied", cmd: `fennec-escalate --reason "e2e: denied scratch file" -- touch ~/fennec-e2e-esc-denied`,
			expect: both(expect{ok: false, prompt: true, decision: "deny"}, expect{ok: false, prompt: true, decision: "deny"}, allowed)},
		{name: "escalate read SSH key, approved", cmd: `fennec-escalate --reason "e2e: check key size" -- wc -c < ~/.ssh/id_ed25519`,
			expect: both(expect{ok: false, prompt: true, decision: "approve"}, expect{ok: false, prompt: true, decision: "approve"}, allowed)},
		{name: "escalate read keychain, approved", cmd: `fennec-escalate --reason "e2e: check keychain size" -- wc -c < ~/Library/Keychains/login.keychain-db`,
			expect: both(expect{ok: false, prompt: true, decision: "approve"}, expect{ok: false, prompt: true, decision: "approve"}, allowed)},
		{name: "escalate network, approved", cmd: `fennec-escalate --reason "e2e: reach example.com" -- curl -sS -m 8 -o /dev/null -w '%{http_code}' https://example.com`, expect: both(approveOK, approveOK, allowed)},
		{name: "forced rm in workspace", cmd: "mkdir -p .fennec-e2e-dir && rm -rf .fennec-e2e-dir",
			expect: both(approveOK, approveOK, expect{ok: false, stderr: "rejected by policy"})},
		// Agents run fennec-escalate on the cloud computer; it forwards to the Mac.
		{name: "cloud fennec-escalate write, approved", cloud: `export PATH="$HOME/.local/bin:$PATH"; fennec-escalate --reason "e2e: cloud command" -- 'touch ~/fennec-e2e-cloud' </dev/null; echo "__EXIT=$?"`, expect: both(approveOK, approveOK, allowed)},
		{name: "cloud fennec-escalate ls ~/.ssh, approved", cloud: `export PATH="$HOME/.local/bin:$PATH"; fennec-escalate --reason "e2e: list ssh keys" -- 'ls ~/.ssh >/dev/null' </dev/null; echo "__EXIT=$?"`, expect: both(approveOK, approveOK, allowed)},
		// Approved escalation reads public SSH material; private keys stay denied.
		{name: "escalate read SSH public key, approved", cmd: `fennec-escalate --reason "e2e: show public key" -- 'wc -c < ~/.ssh/id_ed25519.pub'`, expect: both(approveOK, approveOK, allowed)},
		{name: "scp from ~/.ssh (SFTP)", cloud: fmt.Sprintf(`timeout 60 scp -q %s:.ssh/known_hosts /tmp/fe2e_kh </dev/null; c=$?; rm -f /tmp/fe2e_kh; echo "__EXIT=$c"`, mac),
			expect: both(blockedNoHint, blockedNoHint, allowed)},
		{name: "scp into workspace (SFTP)", cloud: fmt.Sprintf(`echo hi > /tmp/fe2e_up; timeout 60 scp -q /tmp/fe2e_up %s:%s/.fennec-e2e-up </dev/null; echo "__EXIT=$?"`, mac, strings.TrimPrefix(strings.TrimPrefix(workspace, home), "/")),
			expect: both(allowed, allowed, allowed)},
		{name: "TCP forward to example.com:80", cloud: fmt.Sprintf(`printf 'HEAD / HTTP/1.0\r\nHost: example.com\r\n\r\n' | timeout 60 ssh -W example.com:80 %s | head -1; echo "__EXIT=${PIPESTATUS[1]}"`, mac),
			expect: both(approveOK, approveOK, allowed)},
	}
	modes := []string{"ask", "auto", "full"}
	if len(os.Args) > 1 {
		modes = strings.Split(os.Args[1], ",")
	}
	failures := 0
	for _, mode := range modes {
		exec.Command(fen, "sandbox", mode).Run()
		time.Sleep(1500 * time.Millisecond)
		status, _ := exec.Command(fen, "sandbox", "status", "--json").Output()
		fmt.Printf("\n===== mode %s  %s\n", mode, strings.TrimSpace(string(status)))
		for _, tc := range cases {
			ex := tc.expect[mode]
			start := time.Now()
			// A Fennec restart during a case (another session reinstalling the
			// app) drops in-flight connections; rerun that case once instead of
			// reporting a permission failure.
			before := daemonPID()
			code, stdout, stderr, prompted := run(tc, mode)
			note := ""
			if after := daemonPID(); after != before {
				waitReady()
				note = " (daemon restarted, rerun)"
				code, stdout, stderr, prompted = run(tc, mode)
			}
			ok := (code == 0) == ex.ok && (len(prompted) > 0) == ex.prompt && (ex.stderr == "" || strings.Contains(stderr+stdout, ex.stderr))
			verdict := "PASS"
			if !ok {
				verdict = "FAIL"
				failures++
			}
			reason := ""
			if len(prompted) > 0 {
				reason = fmt.Sprintf(" prompt[%s reason=%q]", prompted[0].Kind, prompted[0].Reason)
			}
			firstErr := strings.TrimSpace(stderr)
			if i := strings.IndexByte(firstErr, '\n'); i > 0 {
				firstErr = firstErr[:i]
			}
			if len(firstErr) > 110 {
				firstErr = firstErr[:110]
			}
			if len(stdout) > 40 {
				stdout = stdout[:40]
			}
			fmt.Printf("%s %-36s exit=%-3d %4.1fs%s%s out=%q err=%q\n", verdict, tc.name, code, time.Since(start).Seconds(), note, reason, stdout, firstErr)
		}
	}
	for _, p := range []string{"fennec-e2e-outside", "fennec-e2e-esc", "fennec-e2e-esc-denied", "fennec-e2e-cloud"} {
		os.Remove(filepath.Join(home, p))
	}
	os.Remove(filepath.Join(workspace, ".fennec-e2e-up"))
	exec.Command(fen, "sandbox", "auto").Run()
	fmt.Printf("\nfailures=%d (mode restored to auto)\n", failures)
	if failures > 0 {
		os.Exit(1)
	}
}
