package daemon

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServer(t *testing.T) {
	handler := Handler("127.0.0.1:7420")
	for _, path := range []string{"/", "/app.css", "/app.js", "/api/status"} {
		request := httptest.NewRequest("GET", "http://127.0.0.1:7420"+path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("%s: %d", path, response.Code)
		}
		if response.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("missing CSP")
		}
		if path == "/api/status" {
			var status struct {
				Mode      string
				Sessions  []any
				Connector string
			}
			if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if status.Mode != "scaffold" || status.Connector != "not-connected" || len(status.Sessions) != 0 {
				t.Fatal("must not claim working integrations")
			}
		}
		if path == "/" && !strings.Contains(response.Body.String(), "Fennec") {
			t.Fatal("missing UI")
		}
	}
}

func TestBrowserGuards(t *testing.T) {
	for _, kind := range []string{"host", "origin", "fetch"} {
		request := httptest.NewRequest("GET", "http://127.0.0.1:7420/api/status", nil)
		switch kind {
		case "host":
			request.Host = "attacker.example"
		case "origin":
			request.Header.Set("Origin", "https://attacker.example")
		case "fetch":
			request.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		response := httptest.NewRecorder()
		Handler("127.0.0.1:7420").ServeHTTP(response, request)
		if response.Code != 403 {
			t.Fatalf("%s must be rejected", kind)
		}
	}
	request := httptest.NewRequest("POST", "http://127.0.0.1:7420/api/status", nil)
	response := httptest.NewRecorder()
	Handler("127.0.0.1:7420").ServeHTTP(response, request)
	if response.Code != 405 {
		t.Fatal("mutations must not be accepted")
	}
}

func TestLoopback(t *testing.T) {
	for _, address := range []string{"0.0.0.0:7420", "example.com:7420", "invalid"} {
		if Loopback(address) {
			t.Fatalf("unsafe address %s", address)
		}
	}
	if !Loopback("127.0.0.1:7420") || !Loopback("[::1]:7420") {
		t.Fatal("loopback should work")
	}
}
