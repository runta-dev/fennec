package cli

import (
	"context"
	"fennec/internal/agentcmd"
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
func gatherPreparationInputs(ctx context.Context, home, token, owner, runtimeID, artifacts string, magpie bool, ids []string, versions []setup.Agent, localRoutes []setup.AgentRoute) (inputs preparationInputs) {
	inputs.routes = localRoutes
	cloudAccounts := setup.NativeCloudPreferences(filepath.Join(home, ".config/fennec"), owner, runtimeID)
	var sourceIDs []string
	for _, id := range ids {
		if !cloudAccounts[id] || (magpie && !agentcmd.NativeOnly(id)) {
			sourceIDs = append(sourceIDs, id)
		}
	}
	finish := launchprofile.Begin("prepare.native_gather")
	inputs.native, inputs.err = setup.GatherNativeAccounts(ctx, home, sourceIDs, magpie)
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
		inputs.fingerprint, inputs.configurationFingerprint, inputs.fingerprintErr = preparationFingerprints(home, token, runtimeID, []any{versions, inputs.routes, inputs.plugins, inputs.native}, artifacts, true)
	} else {
		inputs.fingerprint, inputs.configurationFingerprint, inputs.fingerprintErr = preparationFingerprints(home, token, runtimeID, []any{versions, inputs.native}, artifacts, false)
	}
	inputs.changedInputs, inputs.hasPreviousInputs = recordPreparationInputs(home, token, runtimeID, magpie, versions, inputs.routes, inputs.plugins, inputs.native)
	return
}
