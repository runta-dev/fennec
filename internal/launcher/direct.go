package launcher

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/launchprofile"
	"fennec/internal/runta"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type receiptOutput struct {
	buffer    bytes.Buffer
	confirmed bool
	accept    func(LaunchReceipt) error
	plan      LaunchPlan
	writer    io.Writer
	ready     func()
}

func (output *receiptOutput) Write(data []byte) (int, error) {
	length := len(data)
	if !output.confirmed {
		output.buffer.Write(data)
		lineEnd := bytes.IndexByte(output.buffer.Bytes(), '\n')
		if lineEnd < 0 {
			if output.buffer.Len() > 65536 {
				return 0, fmt.Errorf("oversized cloud launch receipt")
			}
			return length, nil
		}
		line := strings.TrimSuffix(string(output.buffer.Bytes()[:lineEnd]), "\r")
		const prefix = "\x1eFENNEC_RECEIPT "
		if !strings.HasPrefix(line, prefix) {
			return 0, fmt.Errorf("cloud launch did not return a receipt")
		}
		var result struct {
			ID      string `json:"id"`
			Agent   string `json:"agent"`
			Address string `json:"address"`
			State   string `json:"state"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, prefix)), &result) != nil || result.ID != output.plan.SessionID || result.Agent != output.plan.Agent || (result.State != "live" && result.State != "finished") {
			return 0, fmt.Errorf("cloud launch receipt identity mismatch")
		}
		receipt := LaunchReceipt{RuntimeID: output.plan.RuntimeID, SessionID: result.ID, Agent: result.Agent, Address: result.Address}
		if err := output.accept(receipt); err != nil {
			return 0, err
		}
		output.confirmed = true
		launchprofile.Session("direct.receipt", receipt.SessionID)
		data = append([]byte(nil), output.buffer.Bytes()[lineEnd+1:]...)
		output.buffer.Reset()
	}
	if len(data) > 0 {
		output.ready()
		if _, err := output.writer.Write(data); err != nil {
			return 0, err
		}
	}
	return length, nil
}

func ExecutePlan(plan LaunchPlan, intent LaunchIntent, home string, ready func(), connection *ColdConnection) error {
	directory := filepath.Join(home, ".config/fennec/cloud")
	if _, err := AttachArgs(plan.RuntimeID, "t1", plan.SessionID, directory); err != nil {
		return err
	}
	if plan.SessionID != intent.RequestID || plan.Agent != intent.Agent {
		return fmt.Errorf("launch plan identity mismatch")
	}
	var once sync.Once
	clear := func() { once.Do(ready) }
	defer clear()
	scope := sha256.Sum256([]byte(plan.RuntimeID + "\x00" + plan.Device))
	journal := LaunchJournal{Directory: filepath.Join(home, ".config/fennec/launch-journal", hex.EncodeToString(scope[:16]))}
	receiptDone := launchprofile.Begin("ssh.receipt")
	titles := &SessionTitleWriter{Writer: os.Stdout, SessionID: plan.SessionID}
	defer titles.Flush()
	output := &receiptOutput{plan: plan, writer: titles, ready: clear, accept: func(receipt LaunchReceipt) error {
		receiptDone()
		if _, err := AttachArgs(receipt.RuntimeID, receipt.Address, receipt.SessionID, directory); err != nil {
			return err
		}
		if err := SaveReceipt(filepath.Join(home, ".config/fennec/launches"), receipt); err != nil {
			return err
		}
		return journal.Complete(intent, receipt)
	}}
	remote := "sh -c \"$(printf '%s' '" + base64.StdEncoding.EncodeToString([]byte(plan.Script)) + "' | base64 -d)\""
	if plan.Launch != nil {
		payload, err := json.Marshal(plan.Launch)
		if err != nil {
			return err
		}
		remote = `"$HOME/.local/fennec/bin/fennec-terminal" start '` + base64.StdEncoding.EncodeToString(payload) + `'`
	}
	args := []string{"-tt", "-S", "none", "-o", "LogLevel=ERROR", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "ConnectTimeout=20", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=8"}
	if connection != nil {
		args[2] = connection.control
	}
	if plan.ControlPath != "" {
		if _, err := hex.DecodeString(filepath.Base(plan.ControlPath)); err != nil || len(filepath.Base(plan.ControlPath)) != 24 {
			return fmt.Errorf("invalid shared SSH control name")
		}
		if filepath.Dir(plan.ControlPath) != filepath.Join(directory, "mux") || filepath.Clean(plan.ControlPath) != plan.ControlPath {
			return fmt.Errorf("invalid shared SSH control path")
		}
		info, err := os.Lstat(filepath.Dir(plan.ControlPath))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return fmt.Errorf("shared SSH control directory must be private")
		}
		args[2] = plan.ControlPath
	}
	if args[2] == "none" {
		args = append(args, SSHTransportOptions(home)...)
	} else {
		args = append(args, "-o", "ProxyCommand=false")
	}
	args = append(args, "-o", "UserKnownHostsFile="+filepath.Join(directory, "known_hosts"), "-i", filepath.Join(directory, "runtime_key"), plan.RuntimeID+"@"+runta.ActiveSSHHost(home), remote)
	command := exec.Command("/usr/bin/ssh", args...)
	command.Stdin = os.Stdin
	command.Stdout = output
	diagnostics := &sshDiagnostics{sessionID: intent.RequestID, writer: terminalOutput{os.Stderr, clear}}
	defer diagnostics.Flush()
	command.Stderr = diagnostics
	err := command.Run()
	diagnostics.Finish(err)
	if !output.confirmed {
		return fmt.Errorf("launch outcome unknown; inspect request %s before retrying: %v", intent.RequestID, err)
	}
	if exit, ok := err.(*exec.ExitError); ok {
		connection.Close()
		code := exit.ExitCode()
		if code < 0 {
			code = 1
		}
		os.Exit(code)
	}
	return err
}

func savePlanReceipt(home string, plan LaunchPlan, intent LaunchIntent, receipt LaunchReceipt) error {
	if receipt.RuntimeID != plan.RuntimeID || receipt.SessionID != intent.RequestID || receipt.Agent != intent.Agent {
		return fmt.Errorf("launch receipt identity mismatch")
	}
	if err := SaveReceipt(filepath.Join(home, ".config/fennec/launches"), receipt); err != nil {
		return err
	}
	scope := sha256.Sum256([]byte(plan.RuntimeID + "\x00" + plan.Device))
	journal := LaunchJournal{Directory: filepath.Join(home, ".config/fennec/launch-journal", hex.EncodeToString(scope[:16]))}
	return journal.Complete(intent, receipt)
}
