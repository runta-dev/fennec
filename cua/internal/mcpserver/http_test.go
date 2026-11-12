package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "s3cret-token"

type httpHarness struct {
	t    *testing.T
	url  string
	addr string
	srv  *HTTP
}

func startHTTP(t *testing.T, h Handler, cfg HTTPConfig) *httpHarness {
	t.Helper()
	if cfg.Token == "" {
		cfg.Token = testToken
	}
	srv, err := NewHTTP(h, Info{Name: "t"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return &httpHarness{t: t, url: "http://" + ln.Addr().String() + HTTPPath, addr: ln.Addr().String(), srv: srv}
}

type httpReply struct {
	status  int
	header  http.Header
	body    string
	decoded map[string]any
}

func (h *httpHarness) do(method, path, body string, headers map[string]string) httpReply {
	h.t.Helper()
	url := h.url
	if path != "" {
		url = "http://" + h.addr + path
	}
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	r := httpReply{status: resp.StatusCode, header: resp.Header, body: string(data)}
	_ = json.Unmarshal(data, &r.decoded)
	return r
}

func (h *httpHarness) initialize(client string) string {
	h.t.Helper()
	r := h.do("POST", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"`+client+`"}}}`, nil)
	sid := r.header.Get(SessionHeader)
	if r.status != 200 || sid == "" {
		h.t.Fatalf("initialize: %d %q %s", r.status, sid, r.body)
	}
	return sid
}

func (h *httpHarness) call(sid, body string) httpReply {
	h.t.Helper()
	return h.do("POST", "", body, map[string]string{SessionHeader: sid})
}

func TestHTTPRequests(t *testing.T) {
	h := startHTTP(t, whoHandler(), HTTPConfig{})
	r := h.do("POST", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, nil)
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") || r.decoded["result"] == nil {
		t.Fatalf("initialize %d %v %s", r.status, r.header, r.body)
	}
	sid := r.header.Get(SessionHeader)
	if len(sid) < 16 {
		t.Fatalf("session id %q", sid)
	}
	if other := h.initialize("x"); other == sid {
		t.Error("two initializes share a session")
	}
	r = h.call(sid, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if r.status != http.StatusAccepted || r.body != "" {
		t.Errorf("notification %d %q", r.status, r.body)
	}
	r = h.call(sid, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"who"}}`)
	if r.status != 200 {
		t.Fatalf("call %d %s", r.status, r.body)
	}
	text := r.decoded["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	if text != "mcp-http-"+sid[:min(len(sid), 32)] {
		t.Errorf("agent %v for session %s", text, sid)
	}
	r = h.call(sid, `{"jsonrpc":"2.0","id":3,"method":"no/such"}`)
	if r.status != 200 || errCode(r.decoded) != CodeMethodNotFound {
		t.Errorf("JSON-RPC errors travel in a 200 reply: %d %s", r.status, r.body)
	}
}

func TestHTTPChecks(t *testing.T) {
	h := startHTTP(t, whoHandler(), HTTPConfig{})
	sid := h.initialize("c")
	ping := `{"jsonrpc":"2.0","id":9,"method":"ping"}`
	cases := []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
		status  int
	}{
		{"other path", "POST", "/other", ping, map[string]string{SessionHeader: sid}, 404},
		{"root", "POST", "/", ping, map[string]string{SessionHeader: sid}, 404},
		{"get", "GET", "", "", map[string]string{SessionHeader: sid}, 405},
		{"delete", "DELETE", "", "", map[string]string{SessionHeader: sid}, 405},
		{"foreign origin", "POST", "", ping, map[string]string{SessionHeader: sid, "Origin": "https://evil.example"}, 403},
		{"lookalike origin", "POST", "", ping, map[string]string{SessionHeader: sid, "Origin": "http://localhost.evil.example"}, 403},
		{"null origin", "POST", "", ping, map[string]string{SessionHeader: sid, "Origin": "null"}, 403},
		{"not json", "POST", "", "{nope", map[string]string{SessionHeader: sid}, 400},
		{"batch", "POST", "", "[" + ping + "]", map[string]string{SessionHeader: sid}, 400},
		{"too big", "POST", "", `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"x":"` + strings.Repeat("a", MaxHTTPBody) + `"}}`, map[string]string{SessionHeader: sid}, 413},
		{"no session", "POST", "", ping, nil, 400},
		{"unknown session", "POST", "", ping, map[string]string{SessionHeader: "feedface"}, 404},
	}
	for _, c := range cases {
		r := h.do(c.method, c.path, c.body, c.headers)
		if r.status != c.status {
			t.Errorf("%s: %d, want %d (%s)", c.name, r.status, c.status, r.body)
		}
		if c.status == 405 && r.header.Get("Allow") != "POST" {
			t.Errorf("%s: Allow %q", c.name, r.header.Get("Allow"))
		}
	}
	for _, origin := range []string{"http://localhost:3000", "http://127.0.0.1", "https://[::1]:8443"} {
		if r := h.do("POST", "", ping, map[string]string{SessionHeader: sid, "Origin": origin}); r.status != 200 {
			t.Errorf("origin %s: %d", origin, r.status)
		}
	}
}

func TestHTTPToken(t *testing.T) {
	for _, tok := range []string{"", "  "} {
		if _, err := NewHTTP(whoHandler(), Info{}, HTTPConfig{Token: tok}); !errors.Is(err, ErrNoToken) || !strings.Contains(err.Error(), TokenEnv) {
			t.Errorf("token %q: %v", tok, err)
		}
	}
	h := startHTTP(t, whoHandler(), HTTPConfig{})
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	for _, auth := range []string{"", "Bearer", "Bearer wrong", "Basic " + testToken, testToken, "Bearer " + testToken + "x"} {
		for _, method := range []string{"POST", "GET", "DELETE"} {
			r := h.do(method, "", init, map[string]string{"Authorization": auth})
			if r.status != 401 || r.header.Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("%s with %q: %d %v", method, auth, r.status, r.header)
			}
		}
		if r := h.do("POST", "/elsewhere", init, map[string]string{"Authorization": auth}); r.status != 401 {
			t.Errorf("other path with %q: %d", auth, r.status)
		}
	}
	for _, auth := range []string{"Bearer " + testToken, "bearer " + testToken, "BEARER  " + testToken} {
		if r := h.do("POST", "", init, map[string]string{"Authorization": auth}); r.status != 200 {
			t.Errorf("%q: %d", auth, r.status)
		}
	}
}

func TestHTTPAuth(t *testing.T) {
	// A loopback bind still needs the token.
	if _, err := NewHTTP(whoHandler(), Info{}, HTTPConfig{}); !errors.Is(err, ErrNoToken) {
		t.Errorf("no token: %v", err)
	}
	h := startHTTP(t, whoHandler(), HTTPConfig{})
	if r := h.do("POST", "", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, map[string]string{"Authorization": ""}); r.status != 401 {
		t.Errorf("loopback without token: %d", r.status)
	}
	if r := h.do("POST", "", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, map[string]string{"Authorization": "bEaReR " + testToken}); r.status != 200 {
		t.Errorf("scheme case: %d", r.status)
	}
	sid := h.initialize("c")
	if r := h.do("POST", "", `{"jsonrpc":"2.0","id":2,"method":"ping"}`, map[string]string{SessionHeader: sid, "Origin": "http://192.168.1.5"}); r.status != 403 {
		t.Errorf("non-local origin: %d", r.status)
	}
	if MaxHTTPConns != 64 {
		t.Errorf("connection cap %d", MaxHTTPConns)
	}
	// The cap holds: with two connections allowed, a third waits until one
	// closes.
	capped := startHTTP(t, whoHandler(), HTTPConfig{MaxConns: 2})
	var conns []net.Conn
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", capped.addr)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte("POST /mcp HTTP/1.1\r\nHost: x\r\n")) // hold it open
		conns = append(conns, c)
	}
	third, err := net.Dial("tcp", capped.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	_, _ = third.Write([]byte("POST /mcp HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n"))
	_ = third.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _ := third.Read(make([]byte, 1)); n != 0 {
		t.Error("a connection over the cap was served")
	}
	_ = conns[0].Close()
	_ = third.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(third).ReadString('\n')
	if err != nil || !strings.Contains(line, "401") {
		t.Errorf("after a slot freed: %q %v", line, err)
	}
	_ = conns[1].Close()
}

func TestHTTPHeaderTimeout(t *testing.T) {
	if HTTPHeaderTimeout != 10*time.Second {
		t.Fatalf("header timeout %s", HTTPHeaderTimeout)
	}
	h := startHTTP(t, whoHandler(), HTTPConfig{HeaderTimeout: 200 * time.Millisecond})
	c, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("POST /mcp HTTP/1.1\r\nHost: x\r\n")) // never finishes
	start := time.Now()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = io.ReadAll(c)
	if err != nil {
		t.Fatalf("not disconnected: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("disconnected after %s", d)
	}
	// The default applies when none is configured.
	srv, _ := NewHTTP(whoHandler(), Info{}, HTTPConfig{Token: "x"})
	if srv.cfg.HeaderTimeout != 10*time.Second || srv.cfg.MaxBody != 8<<20 || srv.cfg.MaxConns != 64 {
		t.Errorf("defaults %+v", srv.cfg)
	}
}

func TestHTTPRefusalCloses(t *testing.T) {
	h := startHTTP(t, whoHandler(), HTTPConfig{})
	sid := h.initialize("c")
	ping := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}"
	good := "POST /mcp HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + testToken + "\r\n" + SessionHeader + ": " + sid + "\r\nContent-Type: application/json\r\nContent-Length: " + itoa(len(ping)) + "\r\n\r\n" + ping
	refusals := map[string]string{
		"auth":   "POST /mcp HTTP/1.1\r\nHost: x\r\nContent-Length: " + itoa(len(ping)) + "\r\n\r\n" + ping,
		"origin": strings.Replace(good, "Host: x\r\n", "Host: x\r\nOrigin: https://evil.example\r\n", 1),
		"size":   "POST /mcp HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + testToken + "\r\nContent-Length: 99999999\r\n\r\n",
		"method": "GET /mcp HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + testToken + "\r\n\r\n",
	}
	for name, first := range refusals {
		c, err := net.Dial("tcp", h.addr)
		if err != nil {
			t.Fatal(err)
		}
		// The refused request and a good one pipelined behind it.
		_, _ = c.Write([]byte(first + good))
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		all, err := io.ReadAll(c)
		_ = c.Close()
		if err != nil {
			t.Errorf("%s: connection kept open: %v", name, err)
			continue
		}
		if n := strings.Count(string(all), "HTTP/1.1 "); n != 1 {
			t.Errorf("%s: %d responses on the connection, want only the refusal:\n%s", name, n, all)
		}
	}
	// A good request keeps the connection alive.
	c, _ := net.Dial("tcp", h.addr)
	defer c.Close()
	_, _ = c.Write([]byte(good + good))
	br := bufio.NewReader(c)
	for i := 0; i < 2; i++ {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		resp, err := http.ReadResponse(br, nil)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("good request %d: %v %v", i, resp, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestHTTPCancelPerSession(t *testing.T) {
	var mu sync.Mutex
	started := map[string]chan struct{}{}
	h := &scriptedHandler{tools: []string{"block"}, fn: func(ctx context.Context, s *Session, _ string, _ json.RawMessage) Result {
		mu.Lock()
		ch := started[s.AgentID]
		mu.Unlock()
		close(ch)
		select {
		case <-ctx.Done():
			return Result{Text: "cancelled"}
		case <-time.After(700 * time.Millisecond):
			return Result{Text: "finished " + s.AgentID}
		}
	}}
	srv := startHTTP(t, h, HTTPConfig{})
	a, b := srv.initialize("a"), srv.initialize("b")
	for _, sid := range []string{a, b} {
		mu.Lock()
		started["mcp-http-"+sid] = make(chan struct{})
		mu.Unlock()
	}
	results := make(chan [2]any, 2)
	for _, sid := range []string{a, b} {
		go func() {
			r := srv.call(sid, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"block"}}`)
			results <- [2]any{sid, r}
		}()
	}
	mu.Lock()
	sa, sb := started["mcp-http-"+a], started["mcp-http-"+b]
	mu.Unlock()
	<-sa
	<-sb
	if r := srv.call(a, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`); r.status != 202 {
		t.Errorf("cancel answered %d", r.status)
	}
	for i := 0; i < 2; i++ {
		got := <-results
		sid, r := got[0].(string), got[1].(httpReply)
		switch sid {
		case a:
			if r.status != 202 || r.body != "" {
				t.Errorf("cancelled request answered %d %q", r.status, r.body)
			}
		case b:
			if r.status != 200 || !strings.Contains(r.body, "finished mcp-http-"+b) {
				t.Errorf("the same id in another session was disturbed: %d %s", r.status, r.body)
			}
		}
	}
}
