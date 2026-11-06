package daemon

import (
	"encoding/json"
	"io/fs"
	"net"
	"net/http"

	"fennec/internal/agent"
	"fennec/internal/gui"
	"fennec/internal/handoff"
	"fennec/internal/runtime"
)

func Handler(address string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(writer).Encode(struct {
			Mode      string            `json:"mode"`
			Connector string            `json:"connector"`
			Agents    []agent.Agent     `json:"agents"`
			Sessions  []runtime.Session `json:"sessions"`
			Checks    []handoff.Check   `json:"checks"`
		}{"scaffold", "not-connected", agent.Discover(), []runtime.Session{}, handoff.RequiredChecks()})
	})
	assets, err := fs.Sub(gui.Assets, "assets")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(assets)))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		if request.Host != address {
			http.Error(writer, "invalid host", http.StatusForbidden)
			return
		}
		if origin := request.Header.Get("Origin"); origin != "" && origin != "http://"+address {
			http.Error(writer, "invalid origin", http.StatusForbidden)
			return
		}
		if site := request.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
			http.Error(writer, "cross-site access denied", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(writer, request)
	})
}

func Loopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	return err == nil && net.ParseIP(host).IsLoopback()
}
