package setup

import (
	"encoding/json"
	"time"
)

// newerNativeCredential resolves an OAuth credential file that both computers
// refreshed independently. Each side's refresh rotates the token, so the copy
// with the later expiry is the live one; keeping the older copy would strand
// the cloud agent on a revoked refresh token. Provider-keyed files (Pi,
// OpenCode) resolve each provider on its own. It reports false when freshness
// cannot be established, so the conflict still surfaces instead of guessing.
func newerNativeCredential(name string, cloud, local []byte) ([]byte, bool) {
	var c, l map[string]any
	if json.Unmarshal(cloud, &c) != nil || json.Unmarshal(local, &l) != nil || c == nil || l == nil {
		return nil, false
	}
	switch name {
	case "claude/.credentials.json":
		return pickNewer(cloud, local, claudeExpiry(c), claudeExpiry(l))
	case "codex/auth.json":
		return pickNewer(cloud, local, codexRefreshed(c), codexRefreshed(l))
	case "pi/auth.json", "opencode/data/opencode/auth.json":
		merged := map[string]any{}
		for key, value := range l {
			merged[key] = value
		}
		for key, cv := range c {
			lv, exists := l[key]
			if !exists {
				merged[key] = cv
				continue
			}
			ce, le := providerExpiry(cv), providerExpiry(lv)
			switch {
			case ce > le && le >= 0:
				merged[key] = cv
			case le > ce && ce >= 0:
				merged[key] = lv
			case jsonEqual(cv, lv):
			default:
				return nil, false
			}
		}
		out, err := json.Marshal(merged)
		return out, err == nil
	}
	return nil, false
}

func pickNewer(cloud, local []byte, cloudAt, localAt int64) ([]byte, bool) {
	switch {
	case cloudAt <= 0 || localAt <= 0 || cloudAt == localAt:
		return nil, false
	case cloudAt > localAt:
		return cloud, true
	default:
		return local, true
	}
}

func claudeExpiry(v map[string]any) int64 {
	oauth, _ := v["claudeAiOauth"].(map[string]any)
	n, _ := oauth["expiresAt"].(float64)
	return int64(n)
}

func codexRefreshed(v map[string]any) int64 {
	s, _ := v["last_refresh"].(string)
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixNano()
}

// providerExpiry returns an OAuth provider entry's expiry, 0 for entries
// without one (API keys), or -1 for malformed entries.
func providerExpiry(v any) int64 {
	entry, ok := v.(map[string]any)
	if !ok {
		return -1
	}
	n, _ := entry["expires"].(float64)
	return int64(n)
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
