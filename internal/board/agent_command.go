package board

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

func AgentCommand(home string, args []string, in io.Reader, out io.Writer) error {
	if len(args) != 3 && len(args) != 6 {
		return fmt.Errorf("usage: queue read <queue> <number> | queue reply <queue> <number> <agent> <run> <you|resolved|failed> (text on stdin)")
	}
	n, err := strconv.Atoi(args[2])
	if err != nil || n < 1 {
		return ErrInvalid
	}
	s := Store{Home: home, Role: "cloud"}
	if args[0] == "read" && len(args) == 3 {
		r, err := s.Request(args[1], n)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(r)
	}
	if args[0] != "reply" || len(args) != 6 {
		return ErrInvalid
	}
	b, err := io.ReadAll(io.LimitReader(in, maxBody+1))
	if err != nil {
		return err
	}
	r, err := s.Complete(args[1], n, args[4], args[3], string(b), args[5])
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(r)
}
