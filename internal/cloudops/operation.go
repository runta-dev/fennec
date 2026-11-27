// Package cloudops routes the operations the Mac sends to the cloud computer
// over the resident channel to the package that owns each one: sessions to
// runtime, preparation and accounts to setup, the Mac link to localssh, and
// queue threads to the scheduler. runtime itself only runs sessions (FEN-81).
package cloudops

import (
	"fennec/internal/localssh"
	"fennec/internal/runtime"
	"fennec/internal/setup"
	"fmt"
	"regexp"
	"strings"
)

// Operation is the wire format of one request. The Mac and the cloud computer
// run different builds, so field names and order are fixed
// (TestOperationWireFormat).
type Operation struct {
	Health            *localssh.LinkHealth         `json:"health,omitempty"`
	MagpieCredentials map[string]string            `json:"magpie_credentials,omitempty"`
	Connector         *localssh.LinkBootstrap      `json:"connector,omitempty"`
	Preparation       *setup.PreparationHealth     `json:"preparation,omitempty"`
	Configuration     *setup.RuntimeConfiguration  `json:"configuration,omitempty"`
	Login             *setup.NativeLogin           `json:"login,omitempty"`
	NativeModel       *setup.NativeModelSelection  `json:"native_model,omitempty"`
	Accounts          *setup.ModelAccountsRequest  `json:"accounts,omitempty"`
	Credential        *setup.NativeCredentialPush  `json:"credential,omitempty"`
	CredentialWatch   *setup.NativeCredentialWatch `json:"credential_watch,omitempty"`
	Routes            []setup.AgentRoute           `json:"routes,omitempty"`
	Launch            *runtime.LocalLaunch         `json:"launch,omitempty"`
	Kind              string                       `json:"kind"`
	Address           string                       `json:"address,omitempty"`
	SessionID         string                       `json:"session_id,omitempty"`
	Text              string                       `json:"text,omitempty"`
	Project           string                       `json:"project,omitempty"`
	RequestNumber     int                          `json:"request_number,omitempty"`
}

var (
	threadProject   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	terminalAddress = regexp.MustCompile(`^t[0-9a-z]+$`)
)

func (o Operation) Validate() error {
	if o.Kind == "thread_session" {
		if !threadProject.MatchString(o.Project) || o.RequestNumber < 1 {
			return fmt.Errorf("invalid thread")
		}
		return nil
	}
	if o.Kind == "connector_health" || o.Kind == "mac_online" {
		if o.Health == nil {
			return fmt.Errorf("health identity required")
		}
		if o.Kind == "mac_online" && (o.Health.Generation == "" || o.Health.Since <= 0) {
			return fmt.Errorf("generation required")
		}
		return o.Health.Validate()
	}
	if o.Kind == "magpie_credentials_sync" {
		return setup.ValidateMagpieCredentials(o.MagpieCredentials)
	}
	if o.Kind == "connector_prepare" {
		if o.Connector == nil {
			return fmt.Errorf("connector identity required")
		}
		return o.Connector.Validate()
	}
	if o.Kind == "preparation_health" {
		if o.Preparation == nil {
			return fmt.Errorf("preparation check required")
		}
		return o.Preparation.Validate()
	}
	if o.Kind == "reconfigure_runtime" {
		if o.Configuration == nil {
			return fmt.Errorf("configuration required")
		}
		return o.Configuration.Validate()
	}
	if o.Kind == "native_login_status" || o.Kind == "native_login_cancel" {
		if o.Login == nil || !terminalAddress.MatchString(o.Address) {
			return fmt.Errorf("login required")
		}
		return o.Login.Validate()
	}
	if o.Kind == "native_models" || o.Kind == "native_model_set" || o.Kind == "native_account" {
		agents := map[string]bool{"codex": true, "claude": true, "pi": true, "kimi": true, "cursor": true, "grok": true}
		// Antigravity CLI shows its migrated sign-in; Fennec lists no models for it.
		if o.Kind == "native_account" {
			agents["agy"] = true
		}
		if o.NativeModel == nil || !agents[o.NativeModel.Agent] {
			return fmt.Errorf("unsupported native model agent")
		}
		if o.Kind == "native_model_set" && !setup.NativeModelFamily(o.NativeModel.Agent, o.NativeModel.Model) {
			return fmt.Errorf("unsupported native model")
		}
		return nil
	}

	if o.Kind == "model_catalog" {
		return nil
	}
	if o.Kind == "native_credential_set" {
		if o.Credential == nil {
			return fmt.Errorf("credential required")
		}
		return o.Credential.Validate()
	}
	if o.Kind == "native_credential_watch" {
		if o.CredentialWatch == nil {
			return fmt.Errorf("credential watch required")
		}
		return o.CredentialWatch.Validate()
	}
	if o.Kind == "system_metrics" || o.Kind == "cloud_agents" {
		return nil
	}
	if o.Kind == "model_accounts" && o.Accounts != nil {
		return setup.ValidateModelAccountsRequest(*o.Accounts)
	}
	if o.Kind == "reconcile_routes" {
		return setup.ValidateDesiredRoutes(o.Routes)
	}
	if o.Kind == "launch" && o.Launch != nil {
		return o.Launch.Validate()
	}
	if o.Kind == "list" {
		return nil
	}
	if o.Kind != "read" && o.Kind != "peek" && o.Kind != "send" && o.Kind != "stop" {
		return fmt.Errorf("unsupported operation")
	}
	if !terminalAddress.MatchString(o.Address) || !runtime.ValidSessionID(o.SessionID) {
		return fmt.Errorf("invalid session identity")
	}
	if o.Kind == "send" && (strings.TrimSpace(o.Text) == "" || len(o.Text) > 16384 || strings.ContainsRune(o.Text, 0)) {
		return fmt.Errorf("input must contain 1–16384 bytes without NUL")
	}
	return nil
}
