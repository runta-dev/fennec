package runtime

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Parse every record before discarding anything. Partial writes, unsupported
// checkpoints and unresolved retained ranges travel unchanged.
func transcriptRecords(data []byte) ([][]byte, []map[string]json.RawMessage, bool) {
	lines := bytes.SplitAfter(data, []byte("\n"))
	records := make([]map[string]json.RawMessage, len(lines))
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if json.Unmarshal(line, &records[i]) != nil || records[i] == nil {
			return nil, nil, false
		}
	}
	return lines, records, true
}

func recordString(r map[string]json.RawMessage, key string) string {
	var value string
	json.Unmarshal(r[key], &value)
	return value
}

func recordObject(r map[string]json.RawMessage, key string) map[string]json.RawMessage {
	var value map[string]json.RawMessage
	json.Unmarshal(r[key], &value)
	return value
}

func encodeRecord(r map[string]json.RawMessage) []byte {
	data, _ := json.Marshal(r)
	return append(data, '\n')
}

// Codex replacement_history is a complete model-history checkpoint. Keep all
// turn/session/window metadata: older clients reconstruct settings from it.
// Rollback can invalidate a checkpoint, so those rollouts remain untouched.
func trimCodexTranscript(data []byte) []byte {
	lines, records, ok := transcriptRecords(data)
	if !ok {
		return data
	}
	boundary := -1
	for i, r := range records {
		p := recordObject(r, "payload")
		if recordString(r, "type") == "event_msg" && recordString(p, "type") == "thread_rolled_back" {
			return data
		}
		if recordString(r, "type") == "compacted" {
			boundary = i
		}
	}
	if boundary < 1 {
		return data
	}
	var history []json.RawMessage
	if raw := recordObject(records[boundary], "payload")["replacement_history"]; len(raw) == 0 || bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &history) != nil {
		return data
	}
	var out bytes.Buffer
	for i, r := range records {
		if i < boundary {
			switch recordString(r, "type") {
			case "response_item":
				continue
			case "compacted":
				p := recordObject(r, "payload")
				// Preserve the number and metadata of previous context windows.
				if p["replacement_history"] != nil {
					p["replacement_history"] = json.RawMessage("[]")
				}
				p["message"] = json.RawMessage(`""`)
				r["payload"], _ = json.Marshal(p)
				out.Write(encodeRecord(r))
				continue
			case "event_msg":
				p := recordObject(r, "payload")
				if recordString(p, "type") == "user_message" {
					for _, k := range []string{"images", "local_images"} {
						if p[k] != nil {
							p[k] = json.RawMessage("[]")
						}
					}
					r["payload"], _ = json.Marshal(p)
					out.Write(encodeRecord(r))
					continue
				}
			}
		}
		out.Write(lines[i])
	}
	return out.Bytes()
}

// Pi walks parent IDs even across compaction. Retain the tree skeleton,
// settings and extension state, removing only message bodies outside the kept
// range. Branched histories remain whole so other leaves remain resumable.
func trimPiTranscript(data []byte) []byte {
	lines, records, ok := transcriptRecords(data)
	if !ok || len(records) == 0 || recordString(records[0], "type") != "session" {
		return data
	}
	var version int
	json.Unmarshal(records[0]["version"], &version)
	if version < 2 || version > 3 {
		return data
	}
	boundary, last, ids := -1, "", map[string]int{}
	for i, r := range records[1:] {
		if r == nil {
			continue
		}
		id := recordString(r, "id")
		if id == "" || recordString(r, "parentId") != last {
			return data
		}
		if _, exists := ids[id]; exists {
			return data
		}
		ids[id], last = i+1, id
		if recordString(r, "type") == "compaction" {
			boundary = i + 1
		}
	}
	if boundary < 1 {
		return data
	}
	first, found := ids[recordString(records[boundary], "firstKeptEntryId")]
	if !found || first > boundary {
		return data
	}
	var out bytes.Buffer
	for i, r := range records {
		if i > 0 && i < first && recordString(r, "type") == "message" {
			message := recordObject(r, "message")
			if message == nil {
				return data
			}
			message["content"] = json.RawMessage("[]")
			r["message"], _ = json.Marshal(message)
			out.Write(encodeRecord(r))
		} else {
			out.Write(lines[i])
		}
	}
	return out.Bytes()
}

// Modern Kimi compaction selects user messages again while replaying its wire.
// Preserve those inputs verbatim and preserve record positions/counts (also
// used by wireLines). Only non-user append bodies covered by the checkpoint
// can be removed; event folds, tool-call identities and side state stay intact.
func trimKimiTranscript(data []byte) []byte {
	lines, records, ok := transcriptRecords(data)
	if !ok {
		return data
	}
	boundary := -1
	for i, r := range records {
		if recordString(r, "type") == "context.undo" {
			return data
		}
		kind := recordString(r, "type")
		if strings.HasPrefix(kind, "context.") && kind != "context.append_message" && kind != "context.append_loop_event" && kind != "context.apply_compaction" && kind != "context.clear" {
			return data
		}
		if recordString(r, "type") == "context.apply_compaction" {
			boundary = i
		}
	}
	if boundary < 1 {
		return data
	}
	c := records[boundary]
	var kept *int
	if json.Unmarshal(c["keptUserMessageCount"], &kept) != nil || kept == nil || *kept < 0 || bytes.Equal(c["legacyTail"], []byte("true")) || (recordString(c, "summary") == "" && recordString(c, "contextSummary") == "") {
		return data
	}
	var out bytes.Buffer
	for i, r := range records {
		if i < boundary && recordString(r, "type") == "context.append_loop_event" {
			event := recordObject(r, "event")
			changed := false
			switch recordString(event, "type") {
			case "tool.result":
				result := recordObject(event, "result")
				if result["output"] != nil {
					result["output"] = json.RawMessage(`"[Compacted]"`)
					event["result"], _ = json.Marshal(result)
					changed = true
				}
			case "content.part":
				part := recordObject(event, "part")
				nonvacuous := false
				switch recordString(part, "type") {
				case "image_url", "audio_url", "video_url":
					nonvacuous = true
				case "text":
					nonvacuous = len(bytes.TrimSpace([]byte(recordString(part, "text")))) > 0 || recordObject(part, "meta")["thought_signature_b64"] != nil
				case "think":
					nonvacuous = len(bytes.TrimSpace([]byte(recordString(part, "think")))) > 0 || part["encrypted"] != nil || bytes.Equal(part["hidden"], []byte("true"))
				}
				if nonvacuous {
					event["part"] = json.RawMessage(`{"type":"text","text":"[Compacted]"}`)
					changed = true
				}
			}
			if changed {
				r["event"], _ = json.Marshal(event)
				out.Write(encodeRecord(r))
				continue
			}
		}
		if i < boundary && recordString(r, "type") == "context.append_message" {
			m := recordObject(r, "message")
			role := recordString(m, "role")
			if role == "assistant" || role == "tool" {
				// A nonempty placeholder preserves the fold's vacuous-message
				// decision and therefore compactedCount and injection positions.
				var parts []map[string]json.RawMessage
				if content := m["content"]; json.Unmarshal(content, &parts) == nil && len(parts) > 0 && (role == "tool" || !bytes.Equal(m["partial"], []byte("true"))) {
					m["content"] = json.RawMessage(`[{"type":"text","text":"[Compacted]"}]`)
					r["message"], _ = json.Marshal(m)
					out.Write(encodeRecord(r))
					continue
				}
			}
		}
		out.Write(lines[i])
	}
	return out.Bytes()
}

// OpenCode's public export owns attachment hydration and database versioning.
// Trim its chronological export, keeping the compaction parent and retained
// tail. Do not reorder messages into model order: the native loader does that.
func trimOpenCodeExport(data []byte) []byte {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil {
		return data
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(root["messages"], &messages) != nil {
		return data
	}
	info := recordObject(root, "info")
	if raw := info["revert"]; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		return data
	}
	ids := map[string]int{}
	start := -1
	for i, m := range messages {
		mi := recordObject(m, "info")
		if mi == nil {
			mi = m
		}
		id := recordString(mi, "id")
		if id == "" {
			return data
		}
		if _, exists := ids[id]; exists {
			return data
		}
		ids[id] = i
		// v2 local summaries may refer to a retained range. Provider-native
		// checkpoints need the old transcript when switching providers.
		if recordString(mi, "type") == "compaction" && recordString(mi, "status") == "completed" {
			if recordString(mi, "summary") == "" {
				return data
			}
			if mi["native"] != nil || mi["providerContext"] != nil {
				return data
			}
			start = i
			if keep := recordString(recordObject(mi, "metadata"), "keepID"); keep != "" {
				k, ok := ids[keep]
				if !ok {
					return data
				}
				start = k
			}
		}
		var summary bool
		json.Unmarshal(mi["summary"], &summary)
		if recordString(mi, "role") != "assistant" || !summary || recordString(mi, "finish") == "" || (mi["error"] != nil && !bytes.Equal(mi["error"], []byte("null"))) {
			continue
		}
		parent, ok := ids[recordString(mi, "parentID")]
		if !ok {
			return data
		}
		var parts []map[string]json.RawMessage
		if json.Unmarshal(messages[parent]["parts"], &parts) != nil {
			return data
		}
		for _, p := range parts {
			if recordString(p, "type") == "compaction" {
				start = parent
				if keep := recordString(p, "tail_start_id"); keep != "" {
					k, ok := ids[keep]
					if !ok || k > parent {
						return data
					}
					start = k
				}
				break
			}
		}
	}
	if start <= 0 {
		return data
	}
	root["messages"], _ = json.Marshal(messages[start:])
	result, err := json.Marshal(root)
	if err != nil {
		return data
	}
	return result
}
