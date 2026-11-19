package board

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) Store {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	return Store{Home: t.TempDir(), Now: func() time.Time { now = now.Add(time.Second); return now }}
}

func TestRequestLifecycle(t *testing.T) {
	s := testStore(t)
	p, err := s.CreateProject(Project{Name: "Fennec", Org: "runta"})
	if err != nil || p.ID != "fennec" || p.Key != "FEN" || p.Queue != 3 || len(p.Tags) != 3 || p.Next != 1 {
		t.Fatal(p, err)
	}
	if again, _ := s.CreateProject(Project{Name: "Fennec"}); again.ID != "fennec-2" {
		t.Fatal(again.ID)
	}
	r, err := s.CreateRequest("fennec", "shiqi", "Queues page overflows", "", "On iPhone it scrolls sideways.")
	if err != nil || r.Number != 1 || r.Status != Queued || r.Tag != "bug" {
		t.Fatal(r, err)
	}
	if r2, _ := s.CreateRequest("fennec", "shiqi", "Second", "feature", ""); r2.Number != 2 || r2.Messages[0].Body != "Second" {
		t.Fatal(r2)
	}
	if _, err = s.CreateRequest("fennec", "shiqi", "Bad tag", "nope", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if r, err = s.Claim("fennec", 1, "cone-leaf", "claude"); err != nil || r.Status != Working || r.Delivered != 1 {
		t.Fatal(r, err)
	}
	if _, err = s.Claim("fennec", 1, "dreamy-meadow", "codex"); !errors.Is(err, ErrConflict) {
		t.Fatal("claimed twice", err)
	}
	if r, err = s.Reply("fennec", 1, "claude", "claude", "Fixed."); err != nil || r.Status != NeedsYou || r.Delivered != 2 {
		t.Fatal(r, err)
	}
	if r, err = s.Reply("fennec", 1, "shiqi", "", "Also Chrome?"); err != nil || r.Status != Queued || r.Session != "cone-leaf" {
		t.Fatal(r, err)
	}
	if r, err = s.Release("fennec", 1, "cone-leaf"); err != nil || r.Session != "" || r.Status != Queued {
		t.Fatal(r, err)
	}
	if r, err = s.Change("fennec", 1, "", Resolved); err != nil || r.Status != Resolved {
		t.Fatal(r, err)
	}
	if r, err = s.Change("fennec", 1, "", NeedsYou); err != nil || r.Status != NeedsYou {
		t.Fatal(r, err)
	}
	list, err := s.Requests("fennec")
	if err != nil || len(list) != 2 || list[0].Number != 1 || list[0].Replies != 2 || list[0].Latest.Body != "Also Chrome?" || list[0].Messages != nil {
		t.Fatal(list, err)
	}
	q, err := s.Queue()
	if err != nil || len(q) != 2 || q[0].Project != "fennec" || len(q[0].Queued) != 1 || q[0].Queued[0].Number != 2 || len(q[0].Queued[0].Messages) != 1 {
		t.Fatal(q, err)
	}
	found, n, err := s.Find("fen-2")
	if err != nil || found.ID != "fennec" || n != 2 {
		t.Fatal(found, n, err)
	}
}

func TestProjectSettingsValidation(t *testing.T) {
	s := testStore(t)
	s.CreateProject(Project{Name: "Stone Age"})
	cases := map[string]string{
		"no worker on":    `{"workers":[{"agent":"claude","on":false}]}`,
		"unknown agent":   `{"workers":[{"agent":"vim","on":true}]}`,
		"key":             `{"key":"fen-1"}`,
		"queue":           `{"queue":11}`,
		"no tags":         `{"tags":[]}`,
		"icon":            `{"icon":"data:text/html;base64,PGI+"}`,
		"repository":      `{"repository":"https://github.com/a/b"}`,
		"duplicate tag":   `{"tags":[{"id":"bug","label":"Bug"},{"id":"bug","label":"Bug"}]}`,
		"color":           `{"color":"red"}`,
		"empty name":      `{"name":"  "}`,
		"unknown setting": `{"next": "x"}`,
	}
	for name, change := range cases {
		if _, err := s.UpdateProject("stone-age", json.RawMessage(change)); err == nil {
			t.Error(name, "accepted")
		}
	}
	p, err := s.UpdateProject("stone-age", json.RawMessage(`{"key":"STA","queue":5,"next":99,"id":"x","icon":"data:image/png;base64,iVBORw0KGgo=","workers":[{"agent":"codex","model":"GPT-5","on":true}]}`))
	if err != nil || p.Key != "STA" || p.Queue != 5 || p.Next != 1 || p.ID != "stone-age" || p.Workers[0].Agent != "codex" {
		t.Fatal(p, err)
	}
	if KeyFrom("runta.com") != "RUN" || KeyFrom("--") != "P" {
		t.Fatal(KeyFrom("runta.com"), KeyFrom("--"))
	}
}

const concept = `---
type: Playbook
title: "fen attach stuck at handshake: EOF"
description: Steps learned from FEN-40.
tags: [ssh, attach]
sources:
  - FEN-40
generated: {by: claude-code, at: 2026-11-18T09:12:00Z}
status: draft
custom: kept
---
1. Check the relay.
`

func TestKnowledgeReview(t *testing.T) {
	s := testStore(t)
	s.CreateProject(Project{Name: "Fennec"})
	dir := filepath.Join(s.KnowledgePath("fennec"), "runbooks")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "attach-eof.md"), []byte(concept), 0600)
	os.WriteFile(filepath.Join(dir, "no-type.md"), []byte("---\ntitle: x\n---\n"), 0600)
	list, err := s.Knowledge("fennec")
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	c := list[0]
	if c.ID != "runbooks/attach-eof" || c.Title != "fen attach stuck at handshake: EOF" || c.Status != "draft" || c.GeneratedBy != "claude-code" ||
		strings.Join(c.Tags, ",") != "ssh,attach" || strings.Join(c.Sources, ",") != "FEN-40" || c.Body != "1. Check the relay.\n" {
		t.Fatalf("%+v", c)
	}
	if c, err = s.Review("fennec", "runbooks/attach-eof", "review", "shiqi"); err != nil || c.Status != "stable" || len(c.Verified) != 1 {
		t.Fatal(c, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "attach-eof.md"))
	if !strings.Contains(string(data), "custom: kept\n") || !strings.Contains(string(data), "  - by: \"human:shiqi\"\n") || !strings.HasSuffix(string(data), "---\n1. Check the relay.\n") {
		t.Fatal(string(data))
	}
	list, _ = s.Knowledge("fennec")
	if list[0].Verified[0].By != "human:shiqi" || list[0].Status != "stable" {
		t.Fatalf("%+v", list[0])
	}
	if _, err = s.Review("fennec", "../../project", "deprecate", "shiqi"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestHandlerOverSocket(t *testing.T) {
	s := testStore(t)
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return s.Dial(ctx) }, DisableKeepAlives: true}}
	call := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, "http://board.fennec"+path, bytes.NewBufferString(body))
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out bytes.Buffer
		out.ReadFrom(res.Body)
		return res.StatusCode, out.String()
	}
	if code, body := call("POST", "/v1/projects", `{"name":"Fennec"}`); code != 200 || !strings.Contains(body, `"key":"FEN"`) {
		t.Fatal(code, body)
	}
	if code, _ := call("POST", "/v1/projects/fennec/requests", `{"author":"shiqi","title":"Hi"}`); code != 200 {
		t.Fatal(code)
	}
	if code, body := call("GET", "/v1/projects", ""); code != 200 || !strings.Contains(body, `"open":1`) {
		t.Fatal(code, body)
	}
	if code, _ := call("GET", "/v1/projects/fennec/requests/9", ""); code != 404 {
		t.Fatal(code)
	}
	if code, _ := call("POST", "/v1/projects/fennec/requests/1/claim", `{"session":"Bad Id","agent":"claude"}`); code != 400 {
		t.Fatal(code)
	}
	req, _ := http.NewRequest("GET", "http://board.fennec/v1/projects", nil)
	req.Header.Set("Origin", "https://example.com")
	if res, err := client.Do(req); err != nil || res.StatusCode != 403 {
		t.Fatal(res, err)
	}
}

func TestCommandReplies(t *testing.T) {
	s := testStore(t)
	s.CreateProject(Project{Name: "Fennec"})
	s.CreateRequest("fennec", "shiqi", "Hi", "", "")
	s.Claim("fennec", 1, "cone-leaf", "codex")
	var out bytes.Buffer
	if err := Command(s.Home, "reply", []string{"FEN-1", "-"}, strings.NewReader("Done.\n"), &out); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Request("fennec", 1)
	if r.Status != NeedsYou || r.Messages[1].Agent != "codex" || r.Messages[1].Body != "Done." {
		t.Fatal(r)
	}
	out.Reset()
	if err := Command(s.Home, "request", []string{"FEN-1"}, nil, &out); err != nil || !strings.Contains(out.String(), "FEN-1: Hi") || !strings.Contains(out.String(), "codex (agent)") {
		t.Fatal(out.String(), err)
	}
	if err := Command(s.Home, "reply", []string{"FEN-9", "x"}, nil, &out); err == nil {
		t.Fatal("replied to a missing request")
	}
}
