package cli

import (
	"context"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
)

func syncPreparedAccounts(ctx context.Context, request setup.ModelAccountsRequest, capable bool, operate func(context.Context, cloudruntime.LocalOperation) (json.RawMessage, error), fallback func(context.Context, setup.ModelAccountsRequest) (setup.ModelAccountsResult, error)) (setup.ModelAccountsResult, error) {
	if err := ctx.Err(); err != nil {
		return setup.ModelAccountsResult{}, err
	}
	if !capable {
		return fallback(ctx, request)
	}
	operation := cloudruntime.LocalOperation{Kind: "model_accounts", Accounts: &request}
	encoded, err := json.Marshal(operation)
	if err != nil {
		return setup.ModelAccountsResult{}, err
	}
	if capable && len(encoded) <= 200<<10 {
		raw, err := operate(ctx, operation)
		if err != nil {
			return setup.ModelAccountsResult{}, err
		}
		return setup.DecodeModelAccountsResult(raw, len(request.Files))
	}
	return fallback(ctx, request)
}
