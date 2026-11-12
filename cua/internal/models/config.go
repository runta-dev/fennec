// Package models is fennec-cua's model client: one OpenAI-compatible endpoint
// with the user's own key, called by the daemon for decide, verify, ground and
// ask. Every call is one non-streaming request with no retry and no failover.
package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"strings"
)

// Defaults of the model configuration.
const (
	DefaultBaseURL      = "https://openrouter.ai/api/v1"
	DefaultDecisionsURL = "https://openrouter.ai/api/alpha/decisions"
	DefaultKeyEnv       = "OPENROUTER_API_KEY"
	DefaultTimeoutMS    = 60_000

	DefaultDecideModel = "typesafe/jev-1.14"
	DefaultGroundModel = "qwen/qwen3-vl-235b-a22b-instruct"
	DefaultAskModel    = "anthropic/claude-sonnet-5"
)

// DecideAPI selects how decide and verify reach their model.
type DecideAPI string

// Decide APIs.
const (
	// DecideDecisions posts {model, state, questions} to the decisions URL.
	DecideDecisions DecideAPI = "decisions"
	// DecideChat asks a chat model for the same answers as one JSON object.
	DecideChat DecideAPI = "chat"
)

func (a DecideAPI) valid() bool { return a == DecideDecisions || a == DecideChat }

// UnmarshalJSON refuses values other than decisions and chat.
func (a *DecideAPI) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("decide_api must be a string")
	}
	if !DecideAPI(s).valid() {
		return fmt.Errorf("decide_api %q is not one of decisions, chat", s)
	}
	*a = DecideAPI(s)
	return nil
}

// Secret is a model key. It prints, logs and marshals as *** so that it never
// reaches an error, a log line, a status answer or debug output.
type Secret string

const redacted = "***"

// String hides the key.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return redacted
}

// GoString hides the key from %#v.
func (s Secret) GoString() string { return s.String() }

// LogValue hides the key from slog.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalJSON hides the key from JSON output.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// Reveal returns the key itself, for the Authorization header only.
func (s Secret) Reveal() string { return string(s) }

// Roles names the model of each role. Verify falls back to Decide when unset.
type Roles struct {
	Decide string  `json:"decide"`
	Verify *string `json:"verify,omitempty"`
	Ground string  `json:"ground"`
	Ask    string  `json:"ask"`
}

// Config is the model part of the configuration file.
type Config struct {
	// BaseURL is the API root that /chat/completions is appended to.
	BaseURL string `json:"base_url"`
	// DecisionsURL is the whole URL of the decisions endpoint.
	DecisionsURL string `json:"decisions_url"`
	// APIKeyEnv names the environment variable that holds the key when
	// APIKey is unset. Empty means no variable.
	APIKeyEnv string `json:"api_key_env"`
	// APIKey is the key written in the file; such a file must be mode 0600.
	APIKey    Secret    `json:"api_key,omitempty"`
	TimeoutMS int       `json:"timeout_ms"`
	DecideAPI DecideAPI `json:"decide_api"`
	// DecideBuild pins the builds that may answer decide and verify: '#'
	// matches one ASCII digit and every other character itself. Nil takes
	// any build; an empty pattern is refused.
	DecideBuild *string `json:"decide_build,omitempty"`
	Models      Roles   `json:"models"`
}

// Defaults is the configuration of a missing file: OpenRouter with its key
// variable, the decisions API, a 60 s timeout, no build pin and verify unset.
func Defaults() Config {
	return Config{
		BaseURL:      DefaultBaseURL,
		DecisionsURL: DefaultDecisionsURL,
		APIKeyEnv:    DefaultKeyEnv,
		TimeoutMS:    DefaultTimeoutMS,
		DecideAPI:    DecideDecisions,
		Models: Roles{
			Decide: DefaultDecideModel,
			Ground: DefaultGroundModel,
			Ask:    DefaultAskModel,
		},
	}
}

// Validate refuses a configuration the client cannot use. Errors name the
// offending key and never echo the key's value.
func (c Config) Validate() error {
	var errs []error
	if err := checkURL("base_url", c.BaseURL); err != nil {
		errs = append(errs, err)
	}
	if c.DecisionsURL != "" || c.DecideAPI == DecideDecisions {
		if err := checkURL("decisions_url", c.DecisionsURL); err != nil {
			errs = append(errs, err)
		}
	}
	if strings.ContainsAny(c.APIKeyEnv, "= \t\n\x00") {
		errs = append(errs, errors.New("api_key_env is not an environment variable name"))
	}
	if c.TimeoutMS <= 0 {
		errs = append(errs, errors.New("timeout_ms must be above 0"))
	}
	if !c.DecideAPI.valid() {
		errs = append(errs, fmt.Errorf("decide_api %q is not one of decisions, chat", string(c.DecideAPI)))
	}
	if c.DecideBuild != nil && *c.DecideBuild == "" {
		errs = append(errs, errors.New("decide_build is empty; remove it to take any build"))
	}
	for _, r := range []struct{ key, model string }{
		{"models.decide", c.Models.Decide},
		{"models.ground", c.Models.Ground},
		{"models.ask", c.Models.Ask},
	} {
		if strings.TrimSpace(r.model) == "" {
			errs = append(errs, fmt.Errorf("%s must name a model", r.key))
		}
	}
	if c.Models.Verify != nil && strings.TrimSpace(*c.Models.Verify) == "" {
		errs = append(errs, errors.New("models.verify is empty; remove it to use the decide model"))
	}
	return errors.Join(errs...)
}

func checkURL(key, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s is empty", key)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		// The value is not echoed: it may hold credentials.
		return fmt.Errorf("%s must be an absolute http or https URL", key)
	}
	return nil
}

// ParseConfigJSON reads a model configuration from JSON over the defaults:
// fields the input names override, the rest keep their defaults. Unknown keys,
// wrong types and bad values are refused.
func ParseConfigJSON(data []byte) (Config, error) {
	c := Defaults()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("models config: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Config{}, errors.New("models config: trailing data after the object")
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("models config: %w", err)
	}
	return c, nil
}

// ConfigFromMap reads a model configuration from a decoded table, such as the
// top level of the configuration file, with the rules of ParseConfigJSON.
func ConfigFromMap(m map[string]any) (Config, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return Config{}, errors.New("models config: the table holds a value that is not plain data")
	}
	return ParseConfigJSON(data)
}

// Key is the model key: APIKey when set, else the APIKeyEnv variable when it
// is non-empty, else none. getenv nil means os.Getenv.
func (c Config) Key(getenv func(string) string) Secret {
	if c.APIKey != "" {
		return c.APIKey
	}
	if c.APIKeyEnv == "" {
		return ""
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	return Secret(getenv(c.APIKeyEnv))
}

// CheckKeyFileMode refuses a configuration file that holds api_key and is
// readable or writable by group or others.
func (c Config) CheckKeyFileMode(path string, mode fs.FileMode) error {
	if c.APIKey == "" || mode.Perm()&0o077 == 0 {
		return nil
	}
	return fmt.Errorf("%s holds api_key and is open to group or others (mode %04o); run chmod 600 %s", path, mode.Perm(), path)
}

// CheckKeyFile stats path and applies CheckKeyFileMode.
func (c Config) CheckKeyFile(path string) error {
	if c.APIKey == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	return c.CheckKeyFileMode(path, info.Mode())
}

// BuildMatches reports whether build fits pattern: '#' matches exactly one
// ASCII digit, every other byte matches itself, and the lengths are equal.
func BuildMatches(build, pattern string) bool {
	if len(build) != len(pattern) {
		return false
	}
	for i := 0; i < len(pattern); i++ {
		b, p := build[i], pattern[i]
		if p == '#' {
			if b < '0' || b > '9' {
				return false
			}
		} else if b != p {
			return false
		}
	}
	return true
}
