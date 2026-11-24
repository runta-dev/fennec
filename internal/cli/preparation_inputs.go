package cli

import (
	"context"
	"fennec/internal/launchprofile"
	"fennec/internal/setup"
	"path/filepath"
)

type preparationInputs struct {
	native                   setup.ModelAccountsRequest
	routes                   []setup.AgentRoute
	plugins                  []setup.ModelPlugin
	fingerprint              string
	configurationFingerprint string
	changedInputs            []string
	hasPreviousInputs        bool
	fingerprintErr           error
	err                      error
}

// This runs only after EnsureShared has authenticated the runtime's owner, but
// overlaps its remaining SSH/API work. No credentials are sent to the cloud here.
func gatherPreparationInputs(ctx context.Context, home, token, owner, runtimeID, artifacts string, prefs desktopPreferences, ids []string, versions []setup.Agent, localRoutes []setup.AgentRoute) (inputs preparationInputs) {
	inputs.routes = localRoutes
	magpie := prefs.Magpie
	nativeAgents := prefs.nativeAgents(ids)
	cloudAccounts := setup.NativeCloudPreferences(filepath.Join(home, ".config/fennec"), owner, runtimeID)
	var sourceIDs []string
	for _, id := range ids {
		if !cloudAccounts[id] || prefs.magpieFor(id) {
			sourceIDs = append(sourceIDs, id)
		}
	}
	finish := launchprofile.Begin("prepare.native_gather")
	inputs.native, inputs.err = setup.GatherAgentAccounts(ctx, home, sourceIDs, magpie, nativeAgents)
	finish()
	inputs.native.Agents = ids
	if inputs.err != nil {
		return
	}
	if magpie {
		inputs.routes, inputs.err = setup.ApplyModelPreferences(filepath.Join(home, ".config/fennec"), owner, localRoutes)
		if inputs.err != nil {
			return
		}
		inputs.plugins, inputs.err = setup.DesiredModelPlugins(home)
		if inputs.err == nil {
			inputs.routes, inputs.err = setup.WithAgentReasoning(inputs.routes, inputs.native)
		}
		if inputs.err != nil {
			return
		}
		parts := []any{versions, inputs.routes, inputs.plugins, inputs.native}
		if len(nativeAgents) > 0 {
			// Only a per-agent choice changes existing fingerprints.
			parts = append(parts, nativeAgents)
		}
		inputs.fingerprint, inputs.configurationFingerprint, inputs.fingerprintErr = preparationFingerprints(home, token, runtimeID, parts, artifacts, true)
	} else {
		inputs.fingerprint, inputs.configurationFingerprint, inputs.fingerprintErr = preparationFingerprints(home, token, runtimeID, []any{versions, inputs.native}, artifacts, false)
	}
	inputs.changedInputs, inputs.hasPreviousInputs = recordPreparationInputs(home, token, runtimeID, magpie, nativeAgents, versions, inputs.routes, inputs.plugins, inputs.native)
	return
}
