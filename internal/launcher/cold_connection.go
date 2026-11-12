package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/launchprofile"
	"fennec/internal/runta"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type ColdConnection struct {
	started      time.Time
	readyOnce    sync.Once
	closeOnce    sync.Once
	closeProfile func()
	runtimeID    string
	control      string
	knownPath    string
	known        []byte
	cancel       context.CancelFunc
	done         chan struct{}
	sshHost      string
}

func StartColdConnection(ctx context.Context, home string, intent LaunchIntent) *ColdConnection {
	var preference struct {
		RuntimeID string `json:"runtime_id"`
	}
	data, err := os.ReadFile(runta.TakeoverPreferencesPath(home))
	if err != nil || len(data) > 8192 || json.Unmarshal(data, &preference) != nil {
		return nil
	}
	directory := filepath.Join(home, ".config/fennec/cloud")
	control, err := SessionControlPath(directory, preference.RuntimeID, intent.RequestID)
	if err != nil {
		return nil
	}
	knownPath := filepath.Join(directory, "known_hosts")
	known, err := os.ReadFile(knownPath)
	if err != nil || len(known) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(directory, "runtime_key")); err != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(control), 0700); err != nil {
		return nil
	}
	info, err := os.Lstat(filepath.Dir(control))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil
	}
	if _, err := os.Lstat(control); !os.IsNotExist(err) {
		return nil
	}
	sshHost := runta.ActiveSSHHost(home)
	connectionCtx, cancel := context.WithCancel(ctx)
	args := []string{"-N", "-f", "-M", "-S", control, "-o", "ControlPersist=5", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "ConnectTimeout=20", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=8"}
	args = append(args, SSHTransportOptions(home)...)
	args = append(args, "-o", "UserKnownHostsFile="+knownPath, "-i", filepath.Join(directory, "runtime_key"), preference.RuntimeID+"@"+sshHost)
	command := exec.CommandContext(connectionCtx, "/usr/bin/ssh", args...)
	started := time.Now()
	closeProfile, err := attachSSHProfile(command, home, started)
	if err != nil {
		cancel()
		return nil
	}
	if err := command.Start(); err != nil {
		closeProfile()
		cancel()
		return nil
	}
	connection := &ColdConnection{started: started, closeProfile: closeProfile, runtimeID: preference.RuntimeID, control: control, knownPath: knownPath, known: known, cancel: cancel, done: make(chan struct{}), sshHost: sshHost}
	go func() { command.Wait(); close(connection.done) }()
	return connection
}

func (connection *ColdConnection) Ready(ctx context.Context, plan LaunchPlan) bool {
	if connection == nil || plan.RuntimeID != connection.runtimeID {
		return false
	}
	current, err := os.ReadFile(connection.knownPath)
	if err != nil || !bytes.Equal(current, connection.known) {
		return false
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	verified := func() bool {
		current, err := os.ReadFile(connection.knownPath)
		if err != nil || !bytes.Equal(current, connection.known) {
			return false
		}
		connection.readyOnce.Do(func() { launchprofile.Record("cold.ssh_handshake", connection.started, time.Since(connection.started)) })
		return true
	}
	for {
		if info, err := os.Lstat(connection.control); err == nil && info.Mode()&os.ModeSocket != 0 {
			return verified()
		}
		select {
		case <-ctx.Done():
			return false
		case <-connection.done:
			info, err := os.Lstat(connection.control)
			return err == nil && info.Mode()&os.ModeSocket != 0 && verified()
		case <-ticker.C:
		}
	}
}

func (connection *ColdConnection) Close() {
	if connection == nil {
		return
	}
	connection.closeOnce.Do(func() {
		connection.cancel()
		<-connection.done
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		host := connection.sshHost
		if host == "" {
			host = "ssh.runta.com"
		}
		exec.CommandContext(ctx, "/usr/bin/ssh", "-S", connection.control, "-O", "exit", connection.runtimeID+"@"+host).Run()
		if connection.closeProfile != nil {
			connection.closeProfile()
		}
	})
}
