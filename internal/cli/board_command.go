package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"fennec/internal/board"
	"fennec/internal/control"
)

// Agent results use the same authenticated daemon API as the desktop. Text
// stays on stdin and a session fence prevents a late run completing a new run.
func fenQueueCommand(args []string, output io.Writer) error {
	method := "board.local.call"
	scope := ""
	if len(args) > 0 && args[0] == "--cloud" {
		method = "board.call"
		args = args[1:]
	}
	if len(args) >= 2 && args[0] == "--scope" {
		scope, args = args[1], args[2:]
		if method != "board.local.call" || !boardScopeID.MatchString(scope) {
			return board.ErrInvalid
		}
		method = "board.agent.call"
	}
	// fen queue threads <queue>: the folder this computer mirrors the queue's
	// threads into (THI-103); the board refreshes it on every change.
	if len(args) == 2 && args[0] == "threads" {
		socket, err := controlSocket()
		if err != nil {
			return err
		}
		var result struct {
			Path string `json:"path"`
		}
		if err := (control.Client{Socket: socket}).Call(context.Background(), "board.threads", map[string]string{"project": args[1], "scope": scope}, &result); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, result.Path)
		return err
	}
	// fen queue list <queue>: every request of the queue with its status.
	if len(args) == 2 && args[0] == "list" {
		return fenQueueList(method, scope, args[1], output)
	}
	if len(args) != 3 && len(args) != 6 && len(args) != 7 {
		return fmt.Errorf("usage: fen queue [--cloud] read <queue> <number> | reply <queue> <number> <agent> <session> <you|resolved|failed> (text on stdin) | new <queue> <number> <agent> <session> <tag>[,<tag>...]|auto (title line, then body, on stdin) | link <queue> <number> <agent> <session> <KEY-n> [related|duplicate|none] | merge <queue> <number> <agent> <session> <KEY-n> | released <queue> <number> <agent> <session> <version> | threads <queue> | list <queue> | retry <queue> <number>")
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
	} else if args[0] == "new" && len(args) == 6 {
		if !board.Agents[args[3]] {
			return fmt.Errorf("invalid agent")
		}
		text, err := io.ReadAll(io.LimitReader(os.Stdin, (64<<10)+1))
		if err != nil {
			return err
		}
		body = map[string]string{"agent": args[3], "session": args[4], "tag": args[5], "text": string(text)}
		verb = "POST"
		path += "/file"
	} else if (args[0] == "link" && len(args) >= 6) || (args[0] == "merge" && len(args) == 6) {
		link, err := fenQueueLink(args)
		if err != nil {
			return err
		}
		body = link
		verb = "POST"
		path += "/links"
	} else if args[0] == "released" && len(args) == 6 {
		if !board.Agents[args[3]] {
			return fmt.Errorf("invalid agent")
		}
		body = map[string]string{"agent": args[3], "session": args[4], "version": args[5]}
		verb = "POST"
		path += "/released"
	} else if args[0] == "retry" && len(args) == 3 {
		verb = "POST"
		path += "/retry"
	} else if args[0] != "read" || len(args) != 3 {
		return fmt.Errorf("invalid queue command")
	}
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	client := control.Client{Socket: socket}
	var raw json.RawMessage
	err = client.Call(context.Background(), method, map[string]any{"method": verb, "path": path, "body": body, "scope": scope}, &raw)
	if err != nil {
		return err
	}
	var request board.Request
	if json.Unmarshal(raw, &request) != nil || request.Number < 1 {
		return fmt.Errorf("invalid request reply")
	}
	return json.NewEncoder(output).Encode(board.RequestForAgent(request))
}

// fenQueueLink is the body of `fen queue link|merge`: the run working on a
// request links it to another request of its queue, or merges it into one.
func fenQueueLink(args []string) (map[string]any, error) {
	if !board.Agents[args[3]] {
		return nil, fmt.Errorf("invalid agent")
	}
	ref := strings.TrimPrefix(args[5], "#")
	if dash := strings.LastIndexByte(ref, '-'); dash >= 0 {
		ref = ref[dash+1:]
	}
	to, err := strconv.Atoi(ref)
	if err != nil || to < 1 {
		return nil, fmt.Errorf("%q is not a request reference (use KEY-n)", args[5])
	}
	kind := board.LinkRelated
	if args[0] == "merge" {
		kind = board.LinkDuplicate
	} else if len(args) == 7 {
		switch args[6] {
		case board.LinkRelated, board.LinkDuplicate:
			kind = args[6]
		case "none", "remove":
			kind = ""
		default:
			return nil, fmt.Errorf("unknown link kind %q (related, duplicate or none)", args[6])
		}
	}
	return map[string]any{"number": to, "kind": kind, "author": args[3], "agent": args[3], "session": args[4]}, nil
}

// fenQueueList prints `fen queue list`: the queue's requests with their
// status, through the same daemon call as the other queue commands.
func fenQueueList(method, scope, project string, output io.Writer) error {
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	client := control.Client{Socket: socket}
	get := func(path string, into any) error {
		var raw json.RawMessage
		if err := client.Call(context.Background(), method, map[string]any{"method": "GET", "path": path, "scope": scope}, &raw); err != nil {
			return err
		}
		if json.Unmarshal(raw, into) != nil {
			return fmt.Errorf("invalid queue reply")
		}
		return nil
	}
	var p board.Project
	if err := get("/v1/projects/"+project, &p); err != nil {
		return err
	}
	var list []board.Summary
	if err := get("/v1/projects/"+project+"/requests", &list); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(board.QueueItems(p, list))
}
