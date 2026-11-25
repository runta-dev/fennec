package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

const agentUsage = "usage: queue read <queue> <number> | queue reply <queue> <number> <agent> <run> <you|resolved|failed> (text on stdin) | queue tag <queue> <number> <agent> <run> <tag>[,<tag>...] | queue new <queue> <number> <agent> <run> <tag>[,<tag>...]|auto (title line, then body, on stdin) | queue link <queue> <number> <agent> <run> <KEY-n> [related|duplicate|none] | queue merge <queue> <number> <agent> <run> <KEY-n> | queue threads <queue>"

func AgentCommand(home string, args []string, in io.Reader, out io.Writer) error {
	s := Store{Home: home, Role: "cloud"}
	// queue threads <queue>: refresh the thread mirror and print its folder.
	if len(args) == 2 && args[0] == "threads" {
		if err := s.MirrorThreads(args[1]); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, s.ThreadsPath(args[1]))
		return err
	}
	if len(args) != 3 && len(args) != 6 && len(args) != 7 {
		return errors.New(agentUsage)
	}
	n, err := strconv.Atoi(args[2])
	if err != nil || n < 1 {
		return ErrInvalid
	}
	if args[0] == "read" && len(args) == 3 {
		r, err := s.Request(args[1], n)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(RequestForAgent(r))
	}
	if args[0] == "tag" && len(args) == 6 {
		r, err := s.AutoTag(args[1], n, args[4], args[3], args[5])
		if errors.Is(err, ErrInvalid) {
			return fmt.Errorf("unknown tag in %q", args[5])
		} else if errors.Is(err, ErrConflict) {
			return fmt.Errorf("the tags of %s %d are not yours to set: a person chose it, or this run no longer owns the request", args[1], n)
		} else if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(RequestForAgent(r))
	}
	if args[0] == "new" && len(args) == 6 {
		b, err := io.ReadAll(io.LimitReader(in, maxBody+1))
		if err != nil {
			return err
		}
		r, err := s.FileRequest(args[1], n, args[4], args[3], args[5], string(b))
		if errors.Is(err, ErrConflict) {
			return fmt.Errorf("this run no longer owns %s %d", args[1], n)
		} else if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(RequestForAgent(r))
	}
	if (args[0] == "link" && (len(args) == 6 || len(args) == 7)) || (args[0] == "merge" && len(args) == 6) {
		return agentLink(s, args, n, out)
	}
	if args[0] != "reply" || len(args) != 6 {
		return errors.New(agentUsage)
	}
	b, err := io.ReadAll(io.LimitReader(in, maxBody+1))
	if err != nil {
		return err
	}
	r, err := s.Complete(args[1], n, args[4], args[3], string(b), args[5])
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(RequestForAgent(r))
}

// agentLink runs `queue link` and `queue merge` for the run working on a
// request: link it to another request of its queue, or mark it a duplicate
// of one (merge). Both threads then show the link.
func agentLink(s Store, args []string, n int, out io.Writer) error {
	p, err := s.Project(args[1])
	if err != nil {
		return err
	}
	to, ok := RefNumber(p, args[5])
	if !ok {
		return fmt.Errorf("%q is not a request of %s (use %s-<n>)", args[5], p.Name, p.Key)
	}
	kind := LinkRelated
	if args[0] == "merge" {
		kind = LinkDuplicate
	} else if len(args) == 7 {
		switch args[6] {
		case LinkRelated, LinkDuplicate:
			kind = args[6]
		case "none", "remove":
			kind = ""
		default:
			return fmt.Errorf("unknown link kind %q (related, duplicate or none)", args[6])
		}
	}
	_, err = s.SetLink(p.ID, n, to, kind, args[3], args[4], args[3])
	switch {
	case errors.Is(err, ErrNotFound):
		return fmt.Errorf("no request %s-%d", p.Key, to)
	case errors.Is(err, ErrConflict):
		return fmt.Errorf("this run no longer owns %s-%d", p.Key, n)
	case errors.Is(err, ErrInvalid):
		return fmt.Errorf("%s-%d can't link to itself", p.Key, n)
	case err != nil:
		return err
	}
	related, err := s.Related(p.ID, n)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(related)
}
