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
}

func (fault *Fault) Error() string { return fault.Message }

type Response struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Fault          `json:"error,omitempty"`
}

type Handler func(context.Context, json.RawMessage) (any, error)
