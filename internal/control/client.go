package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

type Client struct{ Socket string }

func (client Client) Call(ctx context.Context, method string, params any, result any) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	payload, err := json.Marshal(params)
	if err != nil {
		return err
	}
	request := Request{Version: Version, ID: hex.EncodeToString(nonce[:]), Method: method, Params: payload}
	connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", client.Socket)
	if err != nil {
		return &Fault{Code: "daemon_unavailable", Message: "Fennec daemon is not running", Retryable: true}
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return err
	}
	var response Response
	if err := json.NewDecoder(io.LimitReader(connection, 8<<20)).Decode(&response); err != nil {
		return err
	}
	if response.Version != Version || response.ID != request.ID {
		return fmt.Errorf("control response identity mismatch")
	}
	if response.Error != nil {
		return response.Error
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(response.Result, result)
}
