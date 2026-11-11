// Outputs credential shape and non-reversible identity comparisons, never tokens.
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
func main() {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".config/magpie/logins.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "login store unavailable")
		os.Exit(1)
	}
	var entries []map[string]any
	if json.Unmarshal(data, &entries) != nil {
		os.Exit(1)
	}
	for _, entry := range entries {
		auth := entry["auth"]
		raw, _ := json.Marshal(auth)
		fields := []string{}
		if object, ok := auth.(map[string]any); ok {
			for key := range object {
				fields = append(fields, key)
			}
		}
		sort.Strings(fields)
		tokens := []map[string]any{}
		var walk func(string, any)
		walk = func(path string, value any) {
			switch v := value.(type) {
			case map[string]any:
				for key, item := range v {
					walk(path+"."+key, item)
				}
			case string:
				parts := strings.Split(v, ".")
				if len(parts) != 3 {
					return
				}
				decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
				if err != nil {
					return
				}
				var claims map[string]any
				if json.Unmarshal(decoded, &claims) != nil {
					return
				}
				identity := []any{claims["sub"], claims["https://api.openai.com/auth"]}
				ids, _ := json.Marshal(identity)
				tokens = append(tokens, map[string]any{"field": path, "iat": claims["iat"], "exp": claims["exp"], "identity_hash": hash(string(ids))})
			}
		}
		walk("auth", auth)
		json.NewEncoder(os.Stdout).Encode(map[string]any{"agent": entry["agent"], "id_hash": hash(fmt.Sprint(entry["id"])), "user_hash": hash(fmt.Sprint(entry["user"])), "auth_hash": hash(string(raw)), "auth_fields": fields, "tokens": tokens, "hidden": entry["hidden"] != nil, "home": entry["home"] != nil, "own": entry["own"] != nil})
	}
}
