package guardian

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Codex parity: auto-review calls the Responses API directly with the user's
// local Codex sign-in, the same request Codex's guardian sub-thread sends
// (codex-rs core/src/client.rs, codex-api/src/common.rs).

const (
	chatGPTBaseURL         = "https://chatgpt.com/backend-api/codex"
	apiBaseURL             = "https://api.openai.com/v1"
	refreshURL             = "https://auth.openai.com/oauth/token"
	refreshClientID        = "app_EMoamEEZ73f0CkXaXp7hrann"
	apiKeyReviewModel      = "gpt-5.6-luna"
	promptCacheKey         = "guardian:fennec-local-connector"
	accessTokenRefreshSkew = 5 * time.Minute
)

// Typed failures drive Review's retry, fallback and fail-closed behavior.
var (
	// errUnavailable: signed out, refresh token dead, or out of usage. The
	// user chose to be asked in that case.
	errUnavailable = errors.New("local Codex sign-in unavailable")
	// errModelNotFound: the dedicated reviewer model is not available.
	errModelNotFound = errors.New("review model not found")
)

// retryableError marks transient failures (5xx, overload, transport).
type retryableError struct{ cause string }

func (e retryableError) Error() string { return e.cause }

type codexAuth struct {
	path    string
	doc     map[string]any
	apiKey  string
	access  string
	refresh string
	account string
	chatGPT bool
}

func codexHome(home string) string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(home, ".codex")
}

func loadCodexAuth(home string) (*codexAuth, error) {
	return loadAuthPath(filepath.Join(codexHome(home), "auth.json"))
}

func loadAuthPath(path string) (*codexAuth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// Keyring-stored credentials are not read: that would raise a
		// Keychain prompt for Fennec. The user is asked instead.
		return nil, errUnavailable
	}
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil {
		return nil, errUnavailable
	}
	auth := &codexAuth{path: path, doc: doc}
	auth.apiKey, _ = doc["OPENAI_API_KEY"].(string)
	if tokens, ok := doc["tokens"].(map[string]any); ok {
		auth.access, _ = tokens["access_token"].(string)
		auth.refresh, _ = tokens["refresh_token"].(string)
		auth.account, _ = tokens["account_id"].(string)
	}
	mode, _ := doc["auth_mode"].(string)
	auth.chatGPT = auth.access != "" && (mode == "" || strings.HasPrefix(strings.ToLower(mode), "chatgpt"))
	if !auth.chatGPT && auth.apiKey == "" {
		return nil, errUnavailable
	}
	return auth, nil
}

// tokenExpiry reads the JWT exp claim without verifying the signature; the
// server validates the token.
func tokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

// refreshTokens follows Codex's AuthManager: POST the refresh token, then
// persist under $CODEX_HOME/auth.lock with compare-and-save so a concurrently
// running Codex never loses an update.
func (r *Reviewer) refreshTokens(ctx context.Context, auth *codexAuth, reason string) error {
	if auth.refresh == "" {
		return errUnavailable
	}
	endpoint, clientID := refreshURL, refreshClientID
	if tokens, ok := auth.doc["tokens"].(map[string]any); ok {
		if provenance, ok := tokens["oauth_provenance"].(map[string]any); ok {
			if issuer, _ := provenance["issuer"].(string); issuer != "" {
				endpoint = strings.TrimRight(issuer, "/") + "/oauth/token"
			}
			if id, _ := provenance["client_id"].(string); id != "" {
				clientID = id
			}
		}
	}
	if r.RefreshURL != "" {
		endpoint = r.RefreshURL
	}
	body, _ := json.Marshal(map[string]string{"client_id": clientID, "grant_type": "refresh_token", "refresh_reason": reason, "refresh_token": auth.refresh})
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	setClientHeaders(req)
	resp, err := r.client().Do(req)
	if err != nil {
		return retryableError{"token refresh failed: " + err.Error()}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest {
		return errUnavailable // refresh token expired, reused or invalidated
	}
	if resp.StatusCode/100 != 2 {
		return retryableError{fmt.Sprintf("token refresh failed: HTTP %d", resp.StatusCode)}
	}
	var fresh struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(data, &fresh) != nil || fresh.AccessToken == "" {
		return retryableError{"token refresh returned no access token"}
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(auth.path), "auth.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	current, err := loadAuthPath(auth.path)
	if err != nil {
		return errUnavailable
	}
	if current.refresh != auth.refresh || current.access != auth.access {
		// Codex refreshed meanwhile; use its tokens and discard ours.
		*auth = *current
		return nil
	}
	tokens, _ := current.doc["tokens"].(map[string]any)
	tokens["access_token"] = fresh.AccessToken
	if fresh.RefreshToken != "" {
		tokens["refresh_token"] = fresh.RefreshToken
	}
	if fresh.IDToken != "" {
		tokens["id_token"] = fresh.IDToken
	}
	current.doc["last_refresh"] = time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	out, err := json.MarshalIndent(current.doc, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(auth.path), ".auth.json.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), auth.path); err != nil {
		return err
	}
	updated, err := loadAuthPath(auth.path)
	if err != nil {
		return errUnavailable
	}
	*auth = *updated
	return nil
}

func (r *Reviewer) client() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return http.DefaultClient
}

func setClientHeaders(req *http.Request) {
	req.Header.Set("originator", "fennec")
	req.Header.Set("User-Agent", fmt.Sprintf("fennec (%s; %s)", runtime.GOOS, runtime.GOARCH))
}

func newID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// respond sends one review turn and returns the model's final text.
func (r *Reviewer) respond(ctx context.Context, model, instructions, prompt string) (string, error) {
	auth, err := loadCodexAuth(r.Home)
	if err != nil {
		return "", err
	}
	if auth.chatGPT && time.Until(tokenExpiry(auth.access)) < accessTokenRefreshSkew && !tokenExpiry(auth.access).IsZero() {
		if err := r.refreshTokens(ctx, auth, "proactive_expiry"); err != nil {
			return "", err
		}
	}
	if !auth.chatGPT {
		model = apiKeyReviewModel
	}
	for attempt := 0; attempt < 2; attempt++ {
		text, status, err := r.post(ctx, auth, model, instructions, prompt)
		if status == http.StatusUnauthorized && attempt == 0 && auth.chatGPT {
			// Codex 401 recovery: refresh once, then retry.
			if refreshErr := r.refreshTokens(ctx, auth, "unauthorized_recovery"); refreshErr != nil {
				return "", refreshErr
			}
			continue
		}
		if status == http.StatusUnauthorized {
			return "", errUnavailable
		}
		return text, err
	}
	return "", errUnavailable
}

func (r *Reviewer) post(ctx context.Context, auth *codexAuth, model, instructions, prompt string) (string, int, error) {
	base := apiBaseURL
	if auth.chatGPT {
		base = chatGPTBaseURL
	}
	if r.BaseURL != "" {
		base = r.BaseURL
	}
	message := func(role, text string) map[string]any {
		return map[string]any{"type": "message", "role": role, "content": []map[string]string{{"type": "input_text", "text": text}}}
	}
	var schema any
	json.Unmarshal([]byte(outputSchema), &schema)
	body, _ := json.Marshal(map[string]any{
		"model":               model,
		"stream":              true,
		"input":               []any{message("developer", instructions), message("user", prompt)},
		"tools":               []any{},
		"tool_choice":         "auto",
		"parallel_tool_calls": false,
		"reasoning":           map[string]string{"effort": "low", "summary": "auto"},
		"store":               false,
		"include":             []string{"reasoning.encrypted_content"},
		"prompt_cache_key":    promptCacheKey,
		"text":                map[string]any{"format": map[string]any{"type": "json_schema", "name": "codex_output_schema", "strict": false, "schema": schema}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	thread := newID()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	setClientHeaders(req)
	req.Header.Set("session-id", thread)
	req.Header.Set("thread-id", thread)
	req.Header.Set("x-client-request-id", thread)
	if auth.chatGPT {
		req.Header.Set("Authorization", "Bearer "+auth.access)
		if auth.account != "" {
			req.Header.Set("chatgpt-account-id", auth.account)
		}
		req.Header.Set("x-openai-subagent", "guardian")
		if model == defaultModel {
			req.Header.Set("x-codex-guardian", "reviewer")
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+auth.apiKey)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, retryableError{"review request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return "", resp.StatusCode, classifyHTTPError(resp.StatusCode, data)
	}
	text, err := readResponseStream(resp.Body)
	return text, resp.StatusCode, err
}

func classifyHTTPError(status int, data []byte) error {
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(data, &body)
	kind := body.Error.Type + " " + body.Error.Code
	switch {
	case strings.Contains(kind, "usage_limit_reached"), strings.Contains(kind, "usage_not_included"), strings.Contains(kind, "insufficient_quota"), strings.Contains(kind, "usage_limit_exceeded"), strings.Contains(kind, "credit_balance_exhausted"):
		return errUnavailable
	case strings.Contains(kind, "model_not_found") && (status == 400 || status == 403 || status == 404):
		return errModelNotFound
	case status == 429 || status >= 500 || status == 408:
		return retryableError{fmt.Sprintf("review request failed: HTTP %d", status)}
	}
	message := body.Error.Message
	if message == "" {
		message = strings.TrimSpace(string(data))
	}
	if len(message) > 200 {
		message = message[:200]
	}
	return fmt.Errorf("review request rejected: HTTP %d %s", status, message)
}

// readResponseStream parses Responses API server-sent events and returns the
// final assistant text, following codex-api/src/sse/responses.rs.
func readResponseStream(body io.Reader) (string, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	var deltas, final strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Item     json.RawMessage
			Response struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil {
			continue
		}
		switch event.Type {
		case "response.output_text.delta":
			deltas.WriteString(event.Delta)
		case "response.output_item.done":
			var item struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(event.Item, &item) == nil && item.Type == "message" && item.Role == "assistant" {
				final.Reset()
				for _, part := range item.Content {
					if part.Type == "output_text" {
						final.WriteString(part.Text)
					}
				}
			}
		case "response.completed":
			if final.Len() > 0 {
				return final.String(), nil
			}
			return deltas.String(), nil
		case "response.failed", "error", "response.incomplete":
			code, message := event.Response.Error.Code, event.Response.Error.Message
			if code == "" {
				code, message = event.Code, event.Message
			}
			switch code {
			case "insufficient_quota", "credit_balance_exhausted", "organization_spend_limit_exceeded", "project_spend_limit_exceeded", "usage_not_included":
				return "", errUnavailable
			case "model_not_found":
				return "", errModelNotFound
			case "rate_limit_exceeded", "slow_down", "server_is_overloaded", "":
				return "", retryableError{"review stream failed: " + strings.TrimSpace(code+" "+message)}
			}
			return "", fmt.Errorf("review stream failed: %s", strings.TrimSpace(code+" "+message))
		}
	}
	return "", retryableError{"stream closed before response.completed"}
}
