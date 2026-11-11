package cmuxhooks

import (
	"context"
	"encoding/json"
	"fennec/internal/hookbridge"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type Adapter struct {
	Local *Local
	Event string
	Ref   hookbridge.Binding
}

func ValidContext(local *Local) bool {
	return local != nil && filepath.IsAbs(local.Executable) && uuid.MatchString(local.Workspace) && uuid.MatchString(local.Surface) && (local.Socket == "" || filepath.IsAbs(local.Socket))
}
func cleanLocal(local *Local) *Local {
	copy := *local
	values := map[string]string{}
	for _, entry := range local.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (strings.HasPrefix(key, "CMUX_") || map[string]bool{"HOME": true, "USER": true, "PATH": true, "LANG": true, "TMPDIR": true, "CFFIXED_USER_HOME": true}[key]) {
			values[key] = value
		}
	}
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	copy.Env = nil
	for _, key := range keys {
		copy.Env = append(copy.Env, key+"="+values[key])
	}
	return &copy
}
func metadata(event string) hookbridge.Binding {
	nativeEvent := ""
	for name, events := range eventCommands {
		for _, value := range events {
			if value == event && (nativeEvent == "" || name == "PermissionRequest") {
				nativeEvent = name
			}
		}
	}
	policy := hookbridge.Observer
	if nativeEvent == "PermissionRequest" {
		policy = hookbridge.Required
	}
	return hookbridge.Binding{Name: "cmux · " + event, Event: nativeEvent, Policy: policy, TimeoutMS: int(Timeout(event).Milliseconds())}
}
func (a *Adapter) fingerprintWithHash(hash string) string {
	// Workspace/session IDs are routing data captured on the Mac, not executable
	// configuration. Other explicit cmux settings remain part of the grant key.
	var configEnv []string
	for _, entry := range a.Local.Env {
		key, _, _ := strings.Cut(entry, "=")
		if key == "CMUX_WORKSPACE_ID" || key == "CMUX_SURFACE_ID" || strings.HasSuffix(key, "_PID") || strings.Contains(key, "INVOCATION_ID") || strings.HasPrefix(key, "CMUX_AGENT_LAUNCH_") || strings.HasPrefix(key, "CMUX_AGENT_RESUME_") {
			continue
		}
		configEnv = append(configEnv, entry)
	}
	raw, _ := json.Marshal([]any{"cmux-adapter-v2", metadata(a.Event), a.Local.Executable, a.Local.Socket, hash, configEnv})
	return hookbridge.Digest(raw)
}
func newAdapter(local *Local, event, hash string) *Adapter {
	a := &Adapter{Local: local, Event: event, Ref: metadata(event)}
	a.Ref.ID = a.fingerprintWithHash(hash)
	return a
}
func NewAdapter(local *Local, event string) (*Adapter, error) {
	if !ValidContext(local) || !ValidEvent(event) {
		return nil, fmt.Errorf("cmux context unavailable")
	}
	local = cleanLocal(local)
	hash, err := hookbridge.FileDigest(local.Executable)
	if err != nil {
		return nil, err
	}
	return newAdapter(local, event, hash), nil
}
func (a *Adapter) Binding() hookbridge.Binding { return a.Ref }
func (a *Adapter) Fingerprint() (string, error) {
	hash, err := hookbridge.FileDigest(a.Local.Executable)
	if err != nil {
		return "", err
	}
	return a.fingerprintWithHash(hash), nil
}
func (a *Adapter) Description() string {
	return "cmux · " + a.Event + "\n" + a.Local.Executable + " hooks codex " + a.Event + "\nWorkspace: " + a.Local.Workspace + "\nSurface: " + a.Local.Surface
}
func (a *Adapter) Execute(ctx context.Context, input []byte) hookbridge.Result {
	result := a.Local.Run(ctx, a.Event, input)
	if result.Unavailable {
		return hookbridge.Failure(a.Ref, "cmux is unavailable")
	}
	return hookbridge.Result{Stdout: append(result.Data, '\n'), Code: result.Code, Status: "executed"}
}
func Match(command, home, event string) (string, error) {
	allowed := eventCommands[event]
	if len(allowed) == 0 {
		return "", nil
	}
	return recognize(command, home, allowed)
}
func Adapters(local *Local) (map[string]*Adapter, error) {
	result := map[string]*Adapter{}
	if local == nil {
		return result, nil
	}
	if !ValidContext(local) {
		return nil, fmt.Errorf("invalid cmux context")
	}
	local = cleanLocal(local)
	hash, err := hookbridge.FileDigest(local.Executable)
	if err != nil {
		return nil, err
	}
	for _, events := range eventCommands {
		for _, event := range events {
			if result[event] == nil {
				result[event] = newAdapter(local, event, hash)
			}
		}
	}
	return result, nil
}
