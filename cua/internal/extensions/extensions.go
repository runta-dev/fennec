// Package extensions runs fennec-cua's extensions, declared by JSON spec files
// in the plugin directory:
//
//   - Script plugins add a `*_run` tool with the run tool's input schema. A
//     call runs the plugin's own program under the run contract (desk turn,
//     stop, timeout, capped logs, report), with its desk calls coming back
//     through the daemon's MCP socket as the run's own caller.
//   - Service plugins are long-lived programs the daemon keeps running, each
//     listening on a private socket. Each MCP call of one of their tools is
//     forwarded as one request and answered with one reply; byte streaming
//     is not supported.
//
// # Program contract
//
// Every program gets the daemon's environment minus the model key variables,
// plus:
//
//	FENNEC_CUA_DIR             the daemon's directory, the variable fennec-cua
//	                           itself reads to find the daemon (cua.sock in it)
//	FENNEC_CUA_EMBEDDED=1      so `fennec-cua mcp` never starts another daemon
//
// A script plugin also gets FENNEC_CUA_PLUGIN (its tool name), FENNEC_CUA_AGENT
// (the caller), FENNEC_CUA_INPUT (the input mode) and FENNEC_CUA_RUN, a token it
// passes as `_meta["fennec-cua/run"]` in its MCP initialize so its desk calls
// pass as the run's own caller. Its stdin carries the run request as one JSON
// line and stays open while the run lasts: end of input means the run ended
// or the daemon died, and the program should exit. Its stdout and stderr are
// the run's logs (capped at 32 KiB). It writes its report, one JSON object
// {"result", "error": {"kind", "message"}, "usage"}, to file descriptor 3
// (FENNEC_CUA_REPORT_FD); exiting 0 without one reports no result. A timeout,
// stop or cancel sends SIGTERM and SIGKILL 2 s later.
//
// A service gets FENNEC_CUA_SERVICE_SOCKET, the unix socket it must listen
// on. Each call is one connection: the daemon writes one JSON line
// {"tool", "agent", "arguments"} and closes its sending side; the service
// writes one JSON value as its reply and closes. A reply that is an object
// with an "error" key is reported as a failed call.
package extensions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/models"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/schema"
)

// Environment variables handed to extension programs.
const (
	EnvDir           = "FENNEC_CUA_DIR"
	EnvEmbedded      = "FENNEC_CUA_EMBEDDED"
	EnvPlugin        = "FENNEC_CUA_PLUGIN"
	EnvAgent         = "FENNEC_CUA_AGENT"
	EnvInput         = "FENNEC_CUA_INPUT"
	EnvRun           = "FENNEC_CUA_RUN"
	EnvReportFD      = "FENNEC_CUA_REPORT_FD"
	EnvServiceSocket = "FENNEC_CUA_SERVICE_SOCKET"
)

// RunMeta is the initialize _meta key that carries a plugin's run token.
const RunMeta = "fennec-cua/run"

// MaxSocketPath is the longest socket path macOS binds (sun_path holds 104
// bytes; the daemon keeps room for a temporary name while binding).
const MaxSocketPath = 101

// Options configure Open.
type Options struct {
	// ConfigPath is the configuration file; the plugin directory is found
	// beside it.
	ConfigPath string
	// Plugins is [tools] plugins: the plugin directory, relative to the
	// configuration file's directory; empty means tools.d.
	Plugins string
	// ConfigBroken loads no plugins: a configuration that does not load
	// turns every extension off.
	ConfigBroken bool
	// DaemonDir is the daemon's directory (its endpoint); services' sockets
	// go in DaemonDir/services.
	DaemonDir string
	// KeyEnvs are model key variables to withhold besides
	// models.DefaultKeyEnv, such as the configured api_key_env.
	KeyEnvs []string
	// Env is the environment programs start from; nil means os.Environ().
	Env []string
	// Log receives one line per skipped file and service events.
	Log func(format string, args ...any)
}

// Extensions are the loaded plugins and the running services.
type Extensions struct {
	set     Set
	opts    Options
	plugins map[string]*Plugin
	tools   map[string]*Service // service tool name to its service
	uris    map[string]*Resource

	// ListenWait bounds how long a call waits for its service to accept.
	ListenWait time.Duration
	// Grace is a plugin's time between SIGTERM and SIGKILL.
	Grace time.Duration
	// StopWait is a service's time between SIGTERM and SIGKILL.
	StopWait time.Duration

	mu       sync.Mutex
	runs     map[string]string // run token to the caller that owns the run
	sups     map[string]*supervisor
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// New builds extensions from a loaded set; Start runs the services.
func New(set Set, opts Options) *Extensions {
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	x := &Extensions{
		set: set, opts: opts,
		plugins:    map[string]*Plugin{},
		tools:      map[string]*Service{},
		uris:       map[string]*Resource{},
		runs:       map[string]string{},
		sups:       map[string]*supervisor{},
		ListenWait: 10 * time.Second,
		Grace:      2 * time.Second,
		StopWait:   3 * time.Second,
	}
	for i := range x.set.Plugins {
		p := &x.set.Plugins[i]
		x.plugins[p.Name] = p
		for j := range p.Resources {
			x.uris[p.Resources[j].URI] = &p.Resources[j]
		}
	}
	for i := range x.set.Services {
		s := &x.set.Services[i]
		for _, t := range s.Tools {
			x.tools[t.Name] = s
		}
	}
	return x
}

// Open loads the plugin directory named by opts and starts its services. A
// service whose socket path is too long fails it before anything is created.
func Open(ctx context.Context, opts Options) (*Extensions, error) {
	var set Set
	if !opts.ConfigBroken {
		set = Load(Dir(opts.ConfigPath, opts.Plugins), opts.Log)
	}
	x := New(set, opts)
	if err := x.Start(ctx); err != nil {
		return nil, err
	}
	return x, nil
}

// Set is what was loaded.
func (x *Extensions) Set() Set { return x.set }

// ServicesDir holds the services' sockets.
func (x *Extensions) ServicesDir() string { return filepath.Join(x.opts.DaemonDir, "services") }

// Socket is a service's socket path.
func (x *Extensions) Socket(service string) string {
	return filepath.Join(x.ServicesDir(), service+".sock")
}

// Check validates the services' socket paths.
func (x *Extensions) Check() error {
	for _, s := range x.set.Services {
		if p := x.Socket(s.Name); len(p) > MaxSocketPath {
			return fmt.Errorf("service %s: its socket path %s is %d bytes, more than the %d a unix socket takes; use a shorter daemon directory (FENNEC_CUA_DIR) or service name", s.Name, p, len(p), MaxSocketPath)
		}
	}
	return nil
}

// Tools lists the extension tools after the six built-in ones: script
// plugins (only when scripts are on) with the run tool's input schema, then
// the service tools that are for MCP.
func (x *Extensions) Tools(scriptsOn bool) []mcpserver.Tool {
	if x == nil {
		return nil
	}
	var out []mcpserver.Tool
	if scriptsOn {
		runSchema := schema.For(protocol.RunRequest{})
		for _, p := range x.set.Plugins {
			out = append(out, mcpserver.Tool{Name: p.Name, Description: p.Description, InputSchema: runSchema})
		}
	}
	for _, s := range x.set.Services {
		for _, t := range s.Tools {
			if t.MCP {
				out = append(out, mcpserver.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
			}
		}
	}
	return out
}

// IsPlugin reports whether name is a script plugin's tool.
func (x *Extensions) IsPlugin(name string) bool {
	return x != nil && x.plugins[name] != nil
}

// IsService reports whether name is a service tool callable over MCP.
func (x *Extensions) IsService(name string) bool {
	if x == nil {
		return false
	}
	s := x.tools[name]
	if s == nil {
		return false
	}
	for _, t := range s.Tools {
		if t.Name == name {
			return t.MCP
		}
	}
	return false
}

// Resources lists the plugins' resources: uri, the file name as name, mime
// and description.
func (x *Extensions) Resources() []mcpserver.Resource {
	if x == nil {
		return nil
	}
	var out []mcpserver.Resource
	for _, p := range x.set.Plugins {
		for _, r := range p.Resources {
			out = append(out, mcpserver.Resource{URI: r.URI, Name: filepath.Base(r.Path), Description: r.Description, MIMEType: r.MIME})
		}
	}
	return out
}

// ReadResource reads a plugin resource as text.
func (x *Extensions) ReadResource(uri string) (mcpserver.ResourceContents, error) {
	var r *Resource
	if x != nil {
		r = x.uris[uri]
	}
	if r == nil {
		return mcpserver.ResourceContents{}, mcpserver.ErrUnknownResource
	}
	data, err := os.ReadFile(r.Path)
	if err != nil {
		return mcpserver.ResourceContents{}, err
	}
	return mcpserver.ResourceContents{URI: uri, MIMEType: r.MIME, Text: string(data)}, nil
}

// RunOwner names the caller that owns the plugin run of token, so the run's
// own desk calls pass as that caller.
func (x *Extensions) RunOwner(token string) (string, bool) {
	if x == nil || token == "" {
		return "", false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	owner, ok := x.runs[token]
	return owner, ok
}

func (x *Extensions) addRun(owner string) (token string, done func()) {
	var b [16]byte
	_, _ = rand.Read(b[:])
	token = hex.EncodeToString(b[:])
	x.mu.Lock()
	x.runs[token] = owner
	x.mu.Unlock()
	return token, func() {
		x.mu.Lock()
		delete(x.runs, token)
		x.mu.Unlock()
	}
}

// keyVars are the model key variables programs never see.
func (x *Extensions) keyVars() map[string]bool {
	out := map[string]bool{models.DefaultKeyEnv: true}
	for _, k := range x.opts.KeyEnvs {
		if k != "" {
			out[k] = true
		}
	}
	return out
}

// environ builds a program's environment: the base without model keys and
// without the variables fennec-cua sets, then extra.
func (x *Extensions) environ(extra map[string]string) []string {
	base := x.opts.Env
	if base == nil {
		base = os.Environ()
	}
	drop := x.keyVars()
	for k := range extra {
		drop[k] = true
	}
	drop[EnvDir], drop[EnvEmbedded] = true, true
	out := make([]string, 0, len(base)+len(extra)+2)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	out = append(out, EnvDir+"="+x.opts.DaemonDir, EnvEmbedded+"=1")
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// Result turns a service's reply into a tool result: the reply verbatim as
// text, and an object with an "error" key as a failed call.
func Result(reply []byte) mcpserver.Result {
	res := mcpserver.Result{Text: string(reply)}
	var obj map[string]json.RawMessage
	if json.Unmarshal(reply, &obj) == nil {
		_, res.IsError = obj["error"]
	}
	return res
}

// ErrNoService means no service accepted the call in time.
var ErrNoService = errors.New("the service did not accept the call")
