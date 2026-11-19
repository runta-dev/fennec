package board

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// Command runs `fen reply` and `fen request` for agents on the cloud computer.
//
//	fen request FEN-42                 print the request and its thread
//	fen reply FEN-42 "Fixed: …"       answer; the request then needs the person
//	fen reply FEN-42 -                 read the answer from standard input
func Command(home, name string, args []string, stdin io.Reader, out io.Writer) error {
	s := Store{Home: home}
	switch name {
	case "request":
		if len(args) != 1 {
			return fmt.Errorf("usage: fen request KEY-n")
		}
		p, n, err := s.Find(args[0])
		if err != nil {
			return fmt.Errorf("no request %s", args[0])
		}
		r, err := s.Request(p.ID, n)
		if err != nil {
			return err
		}
		fmt.Fprint(out, Thread(p, r))
		return nil
	case "reply":
		flags := flag.NewFlagSet("reply", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		agent := flags.String("agent", "", "agent answering (defaults to the request's agent)")
		if err := flags.Parse(args); err != nil || flags.NArg() != 2 {
			return fmt.Errorf("usage: fen reply [--agent name] KEY-n message|-")
		}
		p, n, err := s.Find(flags.Arg(0))
		if err != nil {
			return fmt.Errorf("no request %s", flags.Arg(0))
		}
		body := flags.Arg(1)
		if body == "-" {
			data, err := io.ReadAll(io.LimitReader(stdin, maxBody+1))
			if err != nil {
				return err
			}
			body = string(data)
		}
		r, err := s.Request(p.ID, n)
		if err != nil {
			return err
		}
		if *agent == "" {
			*agent = r.Agent
		}
		if *agent == "" {
			*agent = "claude"
		}
		if _, err = s.Reply(p.ID, n, *agent, *agent, strings.TrimSpace(body)); errors.Is(err, ErrInvalid) {
			return fmt.Errorf("the reply is empty, too long, or the agent is unknown")
		} else if err != nil {
			return err
		}
		fmt.Fprintf(out, "Replied to %s-%d.\n", p.Key, n)
		return nil
	}
	return fmt.Errorf("unknown command")
}

// Thread is a request as plain text: what an agent is given to work on and
// what `fen request` prints.
func Thread(p Project, r Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s-%d: %s\n", p.Key, r.Number, r.Title)
	fmt.Fprintf(&b, "Project: %s", p.Name)
	if p.Repository != "" {
		fmt.Fprintf(&b, " (github.com/%s)", p.Repository)
	}
	tag := r.Tag
	for _, t := range p.Tags {
		if t.ID == r.Tag {
			tag = t.Label
		}
	}
	fmt.Fprintf(&b, "\nTag: %s\nStatus: %s\n", tag, r.Status)
	for _, m := range r.Messages {
		who := m.Author
		if m.Agent != "" {
			who += " (agent)"
		}
		fmt.Fprintf(&b, "\n--- %s, %s\n%s\n", who, m.At.Format("2006-01-02 15:04 UTC"), m.Body)
	}
	return b.String()
}
