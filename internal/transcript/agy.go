package transcript

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Antigravity CLI (agy) writes one step a line to
// brain/<conversation>/.system_generated/logs/transcript_full.jsonl:
//
//	USER_INPUT        the person's message, inside <USER_REQUEST>, followed
//	                  by blocks agy adds (<ADDITIONAL_METADATA>, …)
//	PLANNER_RESPONSE  the model's text (content), thinking and tool_calls
//	                  ([{"name":"run_command","args":{"CommandLine":…}}])
//	GENERIC           a tool's result
//	SYSTEM_MESSAGE    agy's own notices; ERROR_MESSAGE a failed request
//
// agy records no turn end, so a finished reply without tool calls ends it.
var agyRequest = regexp.MustCompile(`(?s)<USER_REQUEST>\n?(.*?)\n?</USER_REQUEST>`)

func agy(kind string, record map[string]json.RawMessage) []Item {
	var status, content string
	json.Unmarshal(record["status"], &status)
	json.Unmarshal(record["content"], &content)
	switch kind {
	case "USER_INPUT":
		var source string
		json.Unmarshal(record["source"], &source)
		if source != "" && source != "USER_EXPLICIT" {
			return nil
		}
		if match := agyRequest.FindStringSubmatch(content); match != nil {
			content = match[1]
		} else if strings.HasPrefix(strings.TrimSpace(content), "<") {
			return nil
		}
		return userItem([]string{content}, nil)
	case "PLANNER_RESPONSE":
		var calls []struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		}
		json.Unmarshal(record["tool_calls"], &calls)
		items := agentItem(content)
		for _, call := range calls {
			items = append(items, agyDescribe(call.Name, call.Args)...)
		}
		if len(calls) == 0 && strings.TrimSpace(content) != "" && (status == "" || status == "DONE") {
			items = append(items, end(0, ""))
		}
		return items
	}
	return nil
}

// agyDescribe reads agy's tools (CamelCase arguments) as the steps describe
// gives other agents.
func agyDescribe(name string, raw json.RawMessage) []Item {
	var input map[string]any
	json.Unmarshal(raw, &input)
	text := func(keys ...string) string {
		for _, key := range keys {
			if value, _ := input[key].(string); strings.TrimSpace(value) != "" {
				return value
			}
		}
		return ""
	}
	path := func() string {
		return base(text("AbsolutePath", "TargetFile", "Path", "File", "FilePath", "DirectoryPath", "SearchPath", "SearchDirectory"))
	}
	switch name {
	case "run_command":
		return []Item{step("ran", firstLine(text("CommandLine", "Command")))}
	case "send_command_input":
		if input := text("Input"); input != "" {
			return []Item{step("typed", input)}
		}
		return []Item{step("waited", "for a running command")}
	case "command_status", "read_terminal":
		return []Item{step("waited", "for a running command")}
	case "view_file", "read_file", "view_file_outline", "view_code_item":
		return []Item{step("read", path())}
	case "list_dir":
		return []Item{step("read", path())}
	case "write_to_file", "create_file":
		return []Item{step("created", path())}
	case "replace_file_content", "multi_replace_file_content", "edit_file":
		return []Item{step("edited", path())}
	case "delete_file":
		return []Item{step("edited", path())}
	case "grep_search", "find_by_name", "codebase_search", "search_web":
		return []Item{step("searched", text("Query", "Pattern", "SearchPattern"))}
	case "read_url_content":
		return []Item{step("fetched", text("Url", "URL"))}
	case "invoke_subagent", "browser_subagent":
		return []Item{step("delegated", text("Description", "TaskName", "Task", "Prompt"))}
	case "task_boundary", "notify_user":
		return nil
	}
	return describe(name, raw)
}
