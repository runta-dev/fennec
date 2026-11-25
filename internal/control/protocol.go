package control

import (
	"context"
	"encoding/json"
)

const Version = 1

type Request struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Fault struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
	// Cause is the underlying error, kept for daemon logs; it is not sent.
	Cause error `json:"-"`
}

func (fault *Fault) Error() string { return fault.Message }
func (fault *Fault) Unwrap() error { return fault.Cause }

type Response struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Fault          `json:"error,omitempty"`
}

type Handler func(context.Context, json.RawMessage) (any, error)
