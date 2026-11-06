package connector

import "context"

type Request struct {
	ID         string
	SessionID  string
	ApprovalID string
	Directory  string
	Arguments  []string
}

type Result struct {
	ExitCode int
	Output   string
}

type Executor interface {
	Execute(context.Context, Request) (Result, error)
}
