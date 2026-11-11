package hookbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func FileDigest(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute hook path required")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<20 {
		return "", fmt.Errorf("invalid hook executable or dependency")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, io.LimitReader(file, (128<<20)+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Definition is an explicit local configuration, never learned from a remote
// hook payload. Command is the exact Codex command string to match.
type Definition struct {
	Name       string   `json:"name"`
	Agent      string   `json:"agent"`
	Event      string   `json:"event"`
	Command    string   `json:"command"`
	Location   string   `json:"location"`
	Policy     Policy   `json:"policy"`
	Executable string   `json:"executable,omitempty"`
	Arguments  []string `json:"arguments,omitempty"`
	Directory  string   `json:"directory,omitempty"`
	Files      []string `json:"files,omitempty"`
	TimeoutMS  int      `json:"timeout_ms,omitempty"`
}

func (d Definition) Normalized() Definition {
	if d.Policy == "" {
		d.Policy = Required
	}
	if d.TimeoutMS == 0 {
		d.TimeoutMS = 10000
	}
	return d
}
func (d Definition) Validate() error {
	d = d.Normalized()
	if d.Name == "" || len(d.Name) > 128 || d.Agent != "codex" || d.Command == "" || len(d.Command) > 16384 || (d.Location != "local" && d.Location != "sandbox") {
		return fmt.Errorf("invalid hook definition")
	}
	if err := (Binding{ID: strings.Repeat("a", 64), Name: d.Name, Event: d.Event, Policy: d.Policy, TimeoutMS: d.TimeoutMS}).Validate(); err != nil {
		return err
	}
	if d.Location == "local" {
		if !filepath.IsAbs(d.Executable) || !filepath.IsAbs(d.Directory) {
			return fmt.Errorf("local hook requires an absolute executable and working directory")
		}
		if d.Policy == Required && !map[string]bool{"PreToolUse": true, "UserPromptSubmit": true, "Stop": true, "PermissionRequest": true}[d.Event] {
			return fmt.Errorf("this Codex event cannot enforce a required local hook")
		}
		if len(d.Arguments) > 64 || len(d.Files) > 16 {
			return fmt.Errorf("hook definition exceeds limit")
		}
		for _, value := range append(append([]string{d.Executable, d.Directory, d.Command}, d.Arguments...), d.Files...) {
			if strings.ContainsRune(value, 0) || len(value) > 16384 {
				return fmt.Errorf("invalid hook argument")
			}
		}
		for _, file := range d.Files {
			if !filepath.IsAbs(file) {
				return fmt.Errorf("absolute watched file required")
			}
		}
	}
	return nil
}
func DefinitionID(d Definition) (string, error) {
	d = d.Normalized()
	if err := d.Validate(); err != nil {
		return "", err
	}
	raw, _ := json.Marshal(d)
	parts := [][]byte{raw}
	paths := append([]string{d.Executable}, d.Files...)
	// Interpreter-backed hooks bind the script too, rather than trusting only
	// /bin/sh or node while allowing the hook file to change unnoticed.
	for _, argument := range d.Arguments {
		path := argument
		if !filepath.IsAbs(path) {
			path = filepath.Join(d.Directory, path)
		}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			paths = append(paths, path)
		}
	}
	for _, path := range paths {
		hash, err := FileDigest(path)
		if err != nil {
			return "", err
		}
		parts = append(parts, []byte(path+"\x00"+hash))
	}
	return Digest(bytes.Join(parts, []byte{0})), nil
}

type CommandExecutor struct {
	Definition Definition
	Ref        Binding
	Check      func(Definition) error
}

func NewCommand(d Definition, check func(Definition) error) (*CommandExecutor, error) {
	d = d.Normalized()
	hash, err := DefinitionID(d)
	if err != nil {
		return nil, err
	}
	return &CommandExecutor{Definition: d, Ref: Binding{ID: hash, Name: d.Name, Event: d.Event, Policy: d.Policy, TimeoutMS: d.TimeoutMS}, Check: check}, nil
}
func (e *CommandExecutor) Binding() Binding { return e.Ref }
func (e *CommandExecutor) Fingerprint() (string, error) {
	if e.Check != nil {
		if err := e.Check(e.Definition); err != nil {
			return "", err
		}
	}
	return DefinitionID(e.Definition)
}
func (e *CommandExecutor) Description() string {
	args, _ := json.Marshal(append([]string{e.Definition.Executable}, e.Definition.Arguments...))
	return e.Definition.Name + " · " + e.Definition.Event + "\n" + e.Definition.Directory + "\n" + string(args)
}
func (e *CommandExecutor) Execute(ctx context.Context, input []byte) Result {
	command := exec.CommandContext(ctx, e.Definition.Executable, e.Definition.Arguments...)
	command.Dir = e.Definition.Directory
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME"), "USER=" + os.Getenv("USER"), "LANG=en_US.UTF-8"}
	command.Stdin = bytes.NewReader(input)
	stdout, stderr := &Bounded{}, &Bounded{}
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = 250 * time.Millisecond
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	err := command.Run()
	code := 0
	if err != nil {
		if status, ok := err.(*exec.ExitError); ok && status.ExitCode() >= 0 {
			code = status.ExitCode()
		} else {
			return Failure(e.Ref, "could not execute")
		}
	}
	if stdout.Len()+stderr.Len() > PayloadLimit {
		return Failure(e.Ref, "output exceeded the limit")
	}
	return Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Code: code, Status: "executed"}
}

type Bounded struct{ bytes.Buffer }

func (b *Bounded) Write(data []byte) (int, error) {
	if b.Len()+len(data) > PayloadLimit {
		return 0, fmt.Errorf("hook output exceeds limit")
	}
	return b.Buffer.Write(data)
}
