package runtime

import "context"

type LaunchRequest struct {
	AgentID     string
	Environment string
	BudgetCents int64
}

type Session struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	State string `json:"state"`
}

type Provider interface {
	Launch(context.Context, LaunchRequest) (Session, error)
	Stop(context.Context, string) error
}
