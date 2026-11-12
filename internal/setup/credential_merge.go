package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

type CredentialMigrationReport struct {
	Received  int `json:"received"`
	Updated   int `json:"updated"`
	Preserved int `json:"preserved"`
	Ambiguous int `json:"ambiguous"`
}
type credentialPair struct {
	Source string `json:"source"`
	Remote string `json:"remote"`
}
type credentialWrite struct {
	path     string
	data     []byte
	previous []byte
	existed  bool
}

var errCredentialChanged = errors.New("remote credential changed during migration")

func credentialHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func parseCredentialJSON(data []byte) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, fmt.Errorf("invalid credential JSON")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid credential JSON")
	}
	return value, nil
}
func normalizedCredentialHash(value any) string {
	data, _ := json.Marshal(value)
	return credentialHash(data)
}

// This encoder reads the old Python baseline without changing its interpretation.
// New per-login baselines retain separate source and remote digests.
func legacyCredentialJSON(value any) []byte {
	switch v := value.(type) {
	case nil:
		return []byte("null")
	case bool:
		if v {
			return []byte("true")
		}
		return []byte("false")
	case string:
		var out strings.Builder
		out.WriteByte('"')
		for _, r := range v {
			switch r {
			case '"', '\\':
				out.WriteByte('\\')
				out.WriteRune(r)
			case '\b':
				out.WriteString(`\b`)
			case '\f':
				out.WriteString(`\f`)
			case '\n':
				out.WriteString(`\n`)
			case '\r':
				out.WriteString(`\r`)
			case '\t':
				out.WriteString(`\t`)
			default:
				if r < 32 || r >= 128 {
					if r > 65535 {
						a, b := utf16.EncodeRune(r)
						fmt.Fprintf(&out, `\u%04x\u%04x`, a, b)
					} else {
						fmt.Fprintf(&out, `\u%04x`, r)
					}
				} else {
					out.WriteRune(r)
				}
			}
		}
		out.WriteByte('"')
		return []byte(out.String())
	case json.Number:
		text := string(v)
		if !strings.ContainsAny(text, ".eE") {
			return []byte(text)
		}
		f, err := v.Float64()
		if err != nil {
			return []byte(text)
		}
		format := byte('f')
		if math.Abs(f) >= 1e16 || (f != 0 && math.Abs(f) < 1e-4) {
			format = 'e'
		}
		text = strconv.FormatFloat(f, format, -1, 64)
		if format == 'f' && !strings.Contains(text, ".") {
			text += ".0"
		}
		return []byte(text)
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = string(legacyCredentialJSON(item))
		}
		return []byte("[" + strings.Join(parts, ", ") + "]")
	case map[string]any:
		keys := []string{}
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := []string{}
		for _, key := range keys {
			parts = append(parts, string(legacyCredentialJSON(key))+": "+string(legacyCredentialJSON(v[key])))
		}
		return []byte("{" + strings.Join(parts, ", ") + "}")
	default:
		data, _ := json.Marshal(value)
		return data
	}
}
func credentialTruth(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case map[string]any:
		return len(v) > 0
	case []any:
		return len(v) > 0
	case json.Number:
		value, err := v.Float64()
		return err == nil && value != 0
	default:
		return true
	}
}
func credentialText(value any) string { s, _ := value.(string); return s }
func readCredentialFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, fmt.Errorf("invalid credential store")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("credential store unreadable")
	}
	return data, nil
}
func atomicCredential(path string, data []byte, expected ...credentialWrite) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".fennec-")
	if err != nil {
		return fmt.Errorf("credential staging failed")
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("credential write failed")
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("credential sync failed")
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("credential close failed")
	}
	if len(expected) > 0 {
		current, err := readCredentialFile(path)
		if expected[0].existed {
			if err != nil || !bytes.Equal(current, expected[0].previous) {
				return errCredentialChanged
			}
		} else if !os.IsNotExist(err) {
			return errCredentialChanged
		}
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("credential publication failed")
	}
	return nil
}

// ReceiveCredentials never logs or returns credential material. Conflicting
// refreshes of an existing subscription are reconciled per identity; they do
// not prevent unrelated agents or the cloud environment from being prepared.
func ReceiveCredentials(home string, input io.Reader) (CredentialMigrationReport, error) {
	report := CredentialMigrationReport{}
	root := filepath.Join(home, ".config/magpie")
	if err := os.MkdirAll(root, 0700); err != nil {
		return report, fmt.Errorf("credential directory unavailable")
	}
	lock, err := os.OpenFile(filepath.Join(root, ".fennec-migration.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return report, fmt.Errorf("credential lock unavailable")
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		return report, fmt.Errorf("credential lock failed")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	marker := filepath.Join(root, ".fennec-migration.json")
	baseline := map[string]json.RawMessage{}
	if data, err := readCredentialFile(marker); err == nil {
		if json.Unmarshal(data, &baseline) != nil {
			return report, fmt.Errorf("invalid migration baseline")
		}
	} else if !os.IsNotExist(err) {
		return report, err
	}
	var incoming map[string]string
	decoder := json.NewDecoder(io.LimitReader(input, (16<<20)+1024))
	if decoder.Decode(&incoming) != nil {
		return report, fmt.Errorf("invalid migration envelope")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return report, fmt.Errorf("invalid migration envelope")
	}
	names := []string{}
	for name := range incoming {
		if name != "providers.json" && name != "logins.json" && name != "plugin-auth.json" && name != "migrations.json" && name != "plugin-providers.json" {
			return report, fmt.Errorf("unsupported credential store")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	writes := []credentialWrite{}
	getLegacy := func(key string) string { var value string; json.Unmarshal(baseline[key], &value); return value }
	setLegacy := func(key, value string) { baseline[key], _ = json.Marshal(value) }
	for _, name := range names {
		data, err := base64.StdEncoding.DecodeString(incoming[name])
		if err != nil || len(data) > 4<<20 {
			return report, fmt.Errorf("invalid credential payload")
		}
		parsed, err := parseCredentialJSON(data)
		if err != nil {
			return report, err
		}
		report.Received++
		path := filepath.Join(root, name)
		previous, err := readCredentialFile(path)
		exists := err == nil
		if err != nil && !os.IsNotExist(err) {
			return report, err
		}
		switch name {
		case "logins.json":
			source, ok := parsed.([]any)
			if !ok {
				return report, fmt.Errorf("invalid subscription store")
			}
			remote := []any{}
			if exists {
				value, err := parseCredentialJSON(previous)
				if err != nil {
					return report, err
				}
				var ok bool
				remote, ok = value.([]any)
				if !ok {
					return report, fmt.Errorf("invalid remote subscription store")
				}
			}
			for _, value := range remote {
				if _, ok := value.(map[string]any); !ok {
					return report, fmt.Errorf("invalid remote subscription record")
				}
			}
			for _, value := range source {
				entry, ok := value.(map[string]any)
				if !ok {
					return report, fmt.Errorf("invalid subscription record")
				}
				agent, id, user := credentialText(entry["agent"]), credentialText(entry["id"]), credentialText(entry["user"])
				if id == "" || !credentialTruth(entry["auth"]) || credentialTruth(entry["home"]) || credentialTruth(entry["hidden"]) || credentialTruth(entry["own"]) || strings.HasPrefix(agent, "plugin:") {
					continue
				}
				matches := []int{}
				for i, value := range remote {
					existing := value.(map[string]any)
					if existing["agent"] == agent && existing["id"] == id {
						matches = append(matches, i)
					}
				}
				// A stable ID takes precedence over display-name/user fallbacks. Missing
				// user names must never cause every login for an agent to match.
				if len(matches) == 0 && user != "" {
					for i, value := range remote {
						existing := value.(map[string]any)
						if existing["agent"] == agent && existing["user"] == user {
							matches = append(matches, i)
						}
					}
				}
				key := "login:" + credentialHash(legacyCredentialJSON([]any{agent, id}))
				sourceHash := normalizedCredentialHash(entry["auth"])
				if len(matches) == 0 {
					remote = append(remote, entry)
					setLegacy(key, credentialHash(legacyCredentialJSON(entry["auth"])))
					baseline["v2:"+key], _ = json.Marshal(credentialPair{sourceHash, sourceHash})
					continue
				}
				if len(matches) != 1 {
					report.Ambiguous++
					report.Preserved++
					continue
				}
				existing := remote[matches[0]].(map[string]any)
				if credentialTruth(existing["hidden"]) || credentialTruth(existing["home"]) || credentialTruth(existing["own"]) {
					report.Preserved++
					continue
				}
				currentHash := normalizedCredentialHash(existing["auth"])
				localID, localIAT, _, localKnown := subscriptionTokenMetadata(entry["auth"])
				remoteID, remoteIAT, _, remoteKnown := subscriptionTokenMetadata(existing["auth"])
				if localKnown && remoteKnown && localID != remoteID {
					report.Preserved++
					report.Ambiguous++
					continue
				}
				var pair credentialPair
				json.Unmarshal(baseline["v2:"+key], &pair)
				localChanged, remoteChanged := true, true
				if pair.Source != "" && pair.Remote != "" {
					localChanged = sourceHash != pair.Source
					remoteChanged = currentHash != pair.Remote
				} else if old := getLegacy(key); old != "" {
					localChanged = credentialHash(legacyCredentialJSON(entry["auth"])) != old
					remoteChanged = credentialHash(legacyCredentialJSON(existing["auth"])) != old
				}
				replace := sourceHash != currentHash && localChanged && !remoteChanged
				if sourceHash != currentHash && !localChanged && remoteChanged {
					report.Preserved++
					continue
				}
				if sourceHash != currentHash && localChanged && remoteChanged {
					replace = newerSameSubscription(entry["auth"], existing["auth"])
				}
				if localKnown && remoteKnown && localIAT < remoteIAT {
					replace = false
				}
				if replace {
					existing["auth"] = entry["auth"]
					currentHash = sourceHash
					setLegacy(key, credentialHash(legacyCredentialJSON(entry["auth"])))
				} else if sourceHash != currentHash {
					report.Preserved++
					setLegacy(key, credentialHash(legacyCredentialJSON(entry["auth"])))
				}
				if sourceHash == currentHash {
					setLegacy(key, credentialHash(legacyCredentialJSON(existing["auth"])))
				}
				baseline["v2:"+key], _ = json.Marshal(credentialPair{Source: sourceHash, Remote: currentHash})
			}
			data, _ = json.Marshal(remote)
		case "plugin-auth.json":
			source, ok := parsed.(map[string]any)
			if !ok {
				return report, fmt.Errorf("invalid plugin store")
			}
			remote := map[string]any{}
			if exists {
				value, err := parseCredentialJSON(previous)
				if err != nil {
					return report, err
				}
				var ok bool
				remote, ok = value.(map[string]any)
				if !ok {
					return report, fmt.Errorf("invalid remote plugin store")
				}
			}
			for key, value := range source {
				base := strings.SplitN(key, "#", 2)[0]
				if !allowedPluginCredentialBase.MatchString(base) {
					return report, fmt.Errorf("unsupported plugin credential")
				}
				digest := credentialHash(legacyCredentialJSON(value))
				old := getLegacy("plugin:" + key)
				if current, ok := remote[key]; ok {
					currentHash := credentialHash(legacyCredentialJSON(current))
					if currentHash == digest {
						setLegacy("plugin:"+key, digest)
						continue
					}
					if digest == old {
						continue
					}
					if currentHash != old {
						return report, fmt.Errorf("plugin credentials changed on both devices; remote preserved")
					}
				}
				remote[key] = value
				setLegacy("plugin:"+key, digest)
			}
			data, _ = json.Marshal(remote)
		case "migrations.json":
			source, ok := parsed.(map[string]any)
			if !ok {
				return report, fmt.Errorf("invalid migrations store")
			}
			remote := map[string]any{}
			if exists {
				if value, err := parseCredentialJSON(previous); err == nil {
					if m, ok := value.(map[string]any); ok {
						remote = m
					}
				}
			}
			for key, value := range source {
				if allowedPluginCredentialBase.MatchString(key) {
					remote[key] = value
				}
			}
			data, _ = json.Marshal(remote)
		case "plugin-providers.json":
			sourceList, ok := parsed.([]any)
			if !ok {
				return report, fmt.Errorf("invalid plugin providers store")
			}
			pinnedSpecs := map[string]string{}
			if manifestRaw, mErr := os.ReadFile(filepath.Join(root, "plugins.json")); mErr == nil && len(manifestRaw) <= 1<<20 {
				var manifest struct {
					Plugins []struct {
						Spec string `json:"spec"`
					} `json:"plugins"`
				}
				if json.Unmarshal(manifestRaw, &manifest) == nil {
					for _, p := range manifest.Plugins {
						if pkg := splitPluginSpecPackage(p.Spec); pkg != "" {
							pinnedSpecs[pkg] = p.Spec
						}
					}
				}
			}
			var remoteList []any
			if exists {
				if value, err := parseCredentialJSON(previous); err == nil {
					if s, ok := value.([]any); ok {
						remoteList = s
					}
				}
			}
			remoteByID := map[string]map[string]any{}
			for _, item := range remoteList {
				if m, ok := item.(map[string]any); ok {
					if id, _ := m["id"].(string); id != "" {
						remoteByID[id] = m
					}
				}
			}
			merged := make([]any, 0, len(sourceList))
			seenID := map[string]bool{}
			for _, item := range sourceList {
				srcMap, ok := item.(map[string]any)
				if !ok {
					continue
				}
				id, _ := srcMap["id"].(string)
				if id == "" || !allowedPluginCredentialBase.MatchString(id) {
					continue
				}
				if spec, _ := srcMap["spec"].(string); spec != "" {
					if target := pinnedSpecs[splitPluginSpecPackage(spec)]; target != "" {
						srcMap["spec"] = target
					}
				}
				seenID[id] = true
				srcModels, _ := srcMap["models"].([]any)
				srcFellBack, _ := srcMap["fellBack"].(bool)
				if remMap, hasRem := remoteByID[id]; hasRem {
					remModels, _ := remMap["models"].([]any)
					remFellBack, _ := remMap["fellBack"].(bool)
					if srcFellBack && !remFellBack && len(remModels) > len(srcModels) {
						merged = append(merged, remMap)
						continue
					}
				}
				merged = append(merged, srcMap)
			}
			for _, item := range remoteList {
				if remMap, ok := item.(map[string]any); ok {
					if id, _ := remMap["id"].(string); id != "" && !seenID[id] {
						merged = append(merged, remMap)
					}
				}
			}
			data, _ = json.Marshal(merged)
		default:
			digest := credentialHash(data)
			if exists {
				current := credentialHash(previous)
				if current == digest {
					setLegacy(name, digest)
					continue
				}
				if digest == getLegacy(name) {
					continue
				}
				if current != getLegacy(name) {
					return report, fmt.Errorf("provider credentials changed on both devices; remote preserved")
				}
			}
			setLegacy(name, digest)
		}
		if !exists || !bytes.Equal(previous, data) {
			writes = append(writes, credentialWrite{path: path, data: data, previous: previous, existed: exists})
		}
	}
	// All parsing and conflict checks finish before any credential is published.
	for _, write := range writes {
		if err := atomicCredential(write.path, write.data, write); err != nil {
			if errors.Is(err, errCredentialChanged) {
				report.Preserved++
				continue
			}
			return report, err
		}
		report.Updated++
	}
	state, _ := json.Marshal(baseline)
	if err := atomicCredential(marker, state); err != nil {
		return report, err
	}
	return report, nil
}
func subscriptionTokenMetadata(value any) (string, int64, int64, bool) {
	auth, ok := value.(map[string]any)
	if !ok {
		return "", 0, 0, false
	}
	tokens, ok := auth["tokens"].(map[string]any)
	if !ok {
		return "", 0, 0, false
	}
	token, ok := tokens["access_token"].(string)
	if !ok || len(token) > 65536 {
		return "", 0, 0, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", 0, 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", 0, 0, false
	}
	value, err = parseCredentialJSON(raw)
	if err != nil {
		return "", 0, 0, false
	}
	claims, ok := value.(map[string]any)
	if !ok {
		return "", 0, 0, false
	}
	sub := credentialText(claims["sub"])
	if sub == "" {
		return "", 0, 0, false
	}
	scope, _ := claims["https://api.openai.com/auth"].(map[string]any)
	account := credentialText(scope["chatgpt_account_id"])
	issued, _ := claims["iat"].(json.Number)
	expires, _ := claims["exp"].(json.Number)
	iat, err1 := issued.Int64()
	exp, err2 := expires.Int64()
	if err1 != nil || err2 != nil {
		return "", 0, 0, false
	}
	return credentialText(claims["iss"]) + "\x00" + sub + "\x00" + account, iat, exp, true
}
func newerSameSubscription(local, remote any) bool {
	localID, localIAT, localEXP, localOK := subscriptionTokenMetadata(local)
	remoteID, remoteIAT, _, remoteOK := subscriptionTokenMetadata(remote)
	now := time.Now().Unix()
	return localOK && remoteOK && localID == remoteID && localIAT > remoteIAT && localIAT <= now+300 && localEXP > now
}
