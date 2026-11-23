package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"fennec/internal/board"
	"fennec/internal/control"
)

// Agent results use the same authenticated daemon API as the desktop. Text
// stays on stdin and a session fence prevents a late run completing a new run.
func fenQueueCommand(args []string, output io.Writer) error {
	method := "board.local.call"
	if len(args) > 0 && args[0] == "--cloud" {
		method = "board.call"
		args = args[1:]
	}
	if len(args) != 3 && len(args) != 6 {
		return fmt.Errorf("usage: fen queue [--cloud] read <queue> <number> | reply <queue> <number> <agent> <session> <you|resolved|failed> (text on stdin)")
	}
	n, err := strconv.Atoi(args[2])
	if err != nil || n < 1 {
		return fmt.Errorf("invalid request number")
	}
	path := fmt.Sprintf("/v1/projects/%s/requests/%d", args[1], n)
	verb := "GET"
	var body any
	if args[0] == "reply" && len(args) == 6 {
		if !board.Agents[args[3]] {
			return fmt.Errorf("invalid agent")
		}
		text, err := io.ReadAll(io.LimitReader(os.Stdin, (64<<10)+1))
		if err != nil {
			return err
		}
		body = map[string]string{"agent": args[3], "author": args[3], "session": args[4], "outcome": args[5], "body": string(text)}
		verb = "POST"
		path += "/complete"
	} else if args[0] != "read" || len(args) != 3 {
		return fmt.Errorf("invalid queue command")
	}
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	client := control.Client{Socket: socket}
	var raw json.RawMessage
	err = client.Call(context.Background(), method, map[string]any{"method": verb, "path": path, "body": body}, &raw)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(raw)
}
