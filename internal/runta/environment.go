package runta

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	EnvProduction  = "production"
	EnvIntegration = "integration"
)

var safeEnvKey = regexp.MustCompile(`[^a-z0-9.-]+`)

// Environment describes the coordinated API, Dashboard, and SSH gateway endpoints
// for one Runta deployment zone.
type Environment struct {
	Name      string `json:"name"`
	API       string `json:"api"`
	Dashboard string `json:"dashboard"`
	SSHHost   string `json:"ssh_host"`
}

func ProductionEnvironment() Environment {
	return Environment{
		Name:      EnvProduction,
		API:       "https://api.runta.com",
		Dashboard: "https://dashboard.runta.com",
		SSHHost:   "ssh.runta.com",
	}
}

func IntegrationEnvironment() Environment {
	return Environment{
		Name:      EnvIntegration,
		API:       "https://api.runta.xyz",
		Dashboard: "https://dashboard.runta.xyz",
		SSHHost:   "ssh.runta.xyz",
	}
}

// ResolveEnvironment parses "production", "integration", or an explicit HTTPS
// API origin such as "https://api.runta.xyz".
func ResolveEnvironment(spec string) (Environment, error) {
	trimmed := strings.TrimSpace(spec)
	switch strings.ToLower(trimmed) {
	case "", EnvProduction, "https://api.runta.com":
		return ProductionEnvironment(), nil
	case EnvIntegration, "https://api.runta.xyz":
		return IntegrationEnvironment(), nil
	case "beta", "https://api.runta.me":
		return Environment{
			Name:      "beta",
			API:       "https://api.runta.me",
			Dashboard: "https://dashboard.runta.me",
			SSHHost:   "ssh.runta.me",
		}, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Environment{}, fmt.Errorf("invalid Runta API endpoint %q", spec)
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" || strings.ContainsAny(hostname, " \t\r\n/@") {
		return Environment{}, fmt.Errorf("invalid Runta API hostname %q", spec)
	}
	zone := strings.TrimPrefix(hostname, "api.")
	origin := "https://" + parsed.Host
	dashboard := "https://dashboard." + zone
	sshHost := "ssh." + zone
	if zone == hostname {
		dashboard = origin
		sshHost = hostname
	}
	return Environment{
		Name:      origin,
		API:       origin,
		Dashboard: dashboard,
		SSHHost:   sshHost,
	}, nil
}

// Key returns a filesystem-safe identifier, or "" for production.
func (e Environment) Key() string {
	if e.Name == "" || e.Name == EnvProduction || e.API == "https://api.runta.com" {
		return ""
	}
	if e.Name == EnvIntegration || e.Name == "beta" {
		return e.Name
	}
	u, err := url.Parse(e.API)
	host := e.Name
	if err == nil && u.Host != "" {
		host = strings.ToLower(u.Host)
	}
	cleaned := strings.Trim(safeEnvKey.ReplaceAllString(strings.ToLower(host), "-"), "-.")
	if cleaned == "" {
		return "custom"
	}
	return cleaned
}

type storedRuntimePrefs struct {
	Environment  string `json:"environment"`
	NoSSHNoDelay bool   `json:"no_ssh_nodelay"`
}

func readStoredRuntimePrefs(home string) storedRuntimePrefs {
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return storedRuntimePrefs{}
		}
	}
	raw, err := os.ReadFile(filepath.Join(home, ".config/fennec/settings.json"))
	if err != nil || len(raw) > 65536 {
		return storedRuntimePrefs{}
	}
	var prefs storedRuntimePrefs
	if json.Unmarshal(raw, &prefs) != nil {
		return storedRuntimePrefs{}
	}
	return prefs
}

// ActiveEnvironment returns the currently configured Runta environment for home.
func ActiveEnvironment(home string) Environment {
	if override := strings.TrimSpace(os.Getenv("FENNEC_RUNTA_ENV")); override != "" {
		if env, err := ResolveEnvironment(override); err == nil {
			return env
		}
	}
	prefs := readStoredRuntimePrefs(home)
	if env, err := ResolveEnvironment(prefs.Environment); err == nil {
		return env
	}
	return ProductionEnvironment()
}

// ActiveSSHHost returns the SSH gateway hostname for the active environment.
func ActiveSSHHost(home ...string) string {
	h := ""
	if len(home) > 0 {
		h = home[0]
	}
	return ActiveEnvironment(h).SSHHost
}

// SSHNoDelayEnabled reports whether TCP_NODELAY is enabled for client SSH sockets.
func SSHNoDelayEnabled(home string) bool {
	if override := strings.TrimSpace(os.Getenv("FENNEC_SSH_NODELAY")); override != "" {
		return override != "0" && !strings.EqualFold(override, "false") && !strings.EqualFold(override, "off")
	}
	return !readStoredRuntimePrefs(home).NoSSHNoDelay
}

// TakeoverPreferencesPath scopes saved runtime/takeover hints per environment so
// switching to integration never overwrites production's saved runtime selection.
func TakeoverPreferencesPath(home string) string {
	env := ActiveEnvironment(home)
	if key := env.Key(); key != "" {
		return filepath.Join(home, ".config/fennec/takeover-preferences."+key+".json")
	}
	return filepath.Join(home, ".config/fennec/takeover-preferences.json")
}

// NewClient constructs a Client configured with the active environment and SSH TCP_NODELAY policy.
func NewClient(home, token string) *Client {
	env := ActiveEnvironment(home)
	return &Client{
		Endpoint:     env.API,
		Token:        token,
		NoSSHNoDelay: !SSHNoDelayEnabled(home),
	}
}
