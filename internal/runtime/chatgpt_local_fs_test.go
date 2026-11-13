package runtime

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer collects what ChatGPT receives.
type lockedBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data.Bytes()...)
}

// chatgptMessages decodes ChatGPT's received stream after the handshake.
func chatgptMessages(t *testing.T, stream []byte) []map[string]any {
	t.Helper()
	end := bytes.Index(stream, []byte("\r\n\r\n"))
	if end < 0 {
		return nil
	}
	stream = stream[end+4:]
	var messages []map[string]any
	var pending []byte
	for len(stream) > 0 {
		frame, size, err := parseWebSocketFrame(stream)
		if err != nil {
			t.Fatal(err)
		}
		if size == 0 {
			t.Fatalf("partial frame in ChatGPT's stream: %q", stream)
		}
		stream = stream[size:]
		pending = append(pending, frame.payload...)
		if !frame.fin {
			continue
		}
		var message map[string]any
		if json.Unmarshal(pending, &message) != nil {
			t.Fatalf("not a whole message: %q", pending)
		}
		pending = nil
		messages = append(messages, message)
	}
	return messages
}

func waitForMessage(t *testing.T, output *lockedBuffer, match func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, message := range chatgptMessages(t, output.Bytes()) {
			if match(message) {
				return message
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("message never arrived")
	return nil
}

func byID(id float64) func(map[string]any) bool {
	return func(message map[string]any) bool { return message["id"] == id }
}

func connectedFiles(t *testing.T) (*ChatGPTLocalFiles, *lockedBuffer) {
	t.Helper()
	output := &lockedBuffer{}
	files := NewChatGPTLocalFiles(output, true)
	t.Cleanup(files.Close)
	// The cloud answers the handshake and initialize, naming its CODEX_HOME.
	initialize, _ := json.Marshal(map[string]any{"id": 0, "result": map[string]any{"codexHome": "/root/.local/fennec/chatgpt/codex"}})
	if _, err := files.Write(append([]byte("HTTP/1.1 101 Switching Protocols\r\n\r\n"), encodeWebSocketFrame(0x1, initialize, false)...)); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Feed([]byte("GET /rpc HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	return files, output
}

func ask(t *testing.T, files *ChatGPTLocalFiles, id int, method string, params any) []byte {
	t.Helper()
	message, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	forward, err := files.Feed(maskedFrame(0x1, true, message))
	if err != nil {
		t.Fatal(err)
	}
	return forward
}

func TestChatGPTFileRequestsForMacPathsAreAnsweredOnTheMac(t *testing.T) {
	files, output := connectedFiles(t)
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("rules"), 0644)
	os.Mkdir(filepath.Join(project, "skills"), 0755)

	if forward := ask(t, files, 1, "fs/readFile", map[string]any{"path": filepath.Join(project, "AGENTS.md")}); len(forward) != 0 {
		t.Fatal("Mac file request went to the cloud")
	}
	read := waitForMessage(t, output, byID(1))
	data, _ := base64.StdEncoding.DecodeString(read["result"].(map[string]any)["dataBase64"].(string))
	if string(data) != "rules" {
		t.Fatal(read)
	}

	ask(t, files, 2, "fs/readDirectory", map[string]any{"path": project})
	listing := waitForMessage(t, output, byID(2))["result"].(map[string]any)["entries"].([]any)
	names := map[string]bool{}
	for _, entry := range listing {
		e := entry.(map[string]any)
		names[e["fileName"].(string)] = e["isDirectory"].(bool)
	}
	if len(names) != 2 || names["AGENTS.md"] || !names["skills"] {
		t.Fatal(listing)
	}

	ask(t, files, 3, "fs/getMetadata", map[string]any{"path": filepath.Join(project, "skills")})
	metadata := waitForMessage(t, output, byID(3))["result"].(map[string]any)
	if metadata["isDirectory"] != true || metadata["isSymlink"] != false || metadata["modifiedAtMs"].(float64) <= 0 {
		t.Fatal(metadata)
	}

	// Missing files fail the way the app-server words them.
	ask(t, files, 4, "fs/getMetadata", map[string]any{"path": filepath.Join(project, "missing")})
	failure := waitForMessage(t, output, byID(4))["error"].(map[string]any)
	if failure["message"] != "No such file or directory (os error 2)" {
		t.Fatal(failure)
	}

	// Writes, directories, copies and removal stay on the Mac too.
	target := filepath.Join(project, "out", "note.txt")
	ask(t, files, 5, "fs/createDirectory", map[string]any{"path": filepath.Dir(target)})
	waitForMessage(t, output, byID(5))
	ask(t, files, 6, "fs/writeFile", map[string]any{"path": target, "dataBase64": base64.StdEncoding.EncodeToString([]byte("hi"))})
	waitForMessage(t, output, byID(6))
	ask(t, files, 7, "fs/copy", map[string]any{"sourcePath": target, "destinationPath": target + ".copy"})
	waitForMessage(t, output, byID(7))
	if copied, _ := os.ReadFile(target + ".copy"); string(copied) != "hi" {
		t.Fatal("copy missing")
	}
	ask(t, files, 8, "fs/remove", map[string]any{"path": filepath.Dir(target)})
	waitForMessage(t, output, byID(8))
	if _, err := os.Stat(filepath.Dir(target)); !os.IsNotExist(err) {
		t.Fatal("remove did not happen")
	}
}

func TestChatGPTCloudFileRequestsAndOtherMethodsGoToTheCloud(t *testing.T) {
	files, _ := connectedFiles(t)
	if forward := ask(t, files, 1, "fs/readFile", map[string]any{"path": "/root/.local/fennec/chatgpt/codex/attachments/registry.json"}); len(forward) == 0 {
		t.Fatal("cloud CODEX_HOME request answered on the Mac")
	}
	if forward := ask(t, files, 2, "fs/readFile", map[string]any{"path": "/root/.fen/abc/AGENTS.md"}); len(forward) == 0 {
		t.Fatal("cloud home request answered on the Mac")
	}
	if forward := ask(t, files, 3, "thread/list", map[string]any{}); !bytes.Contains(forward, []byte{0x81}) {
		t.Fatal("other requests must pass through")
	}
	// Before the cloud has named its home, nothing is answered locally.
	early := NewChatGPTLocalFiles(&lockedBuffer{}, true)
	defer early.Close()
	early.Feed([]byte("GET /rpc HTTP/1.1\r\n\r\n"))
	if forward := ask(t, early, 4, "fs/readFile", map[string]any{"path": "/Users/someone/x"}); len(forward) == 0 {
		t.Fatal("answered before initialize")
	}
}

// Answers never land inside a cloud frame that is still arriving, nor between
// the fragments of a cloud message.
func TestChatGPTLocalAnswersWaitForMessageBoundaries(t *testing.T) {
	files, output := connectedFiles(t)
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "a"), []byte("x"), 0644)
	first := encodeWebSocketFrame(0x1, []byte(`{"method":"part`), false)
	first[0] &^= 0x80 // not the final fragment
	half := len(first) / 2
	files.Write(first[:half])
	ask(t, files, 9, "fs/readFile", map[string]any{"path": filepath.Join(project, "a")})
	time.Sleep(100 * time.Millisecond)
	files.Write(first[half:])
	time.Sleep(100 * time.Millisecond)
	if bytes.Contains(output.Bytes(), []byte(`"id":9`)) {
		t.Fatal("answer placed inside a fragmented cloud message")
	}
	files.Write(encodeWebSocketFrame(0x0, []byte(`ial"}`), false))
	waitForMessage(t, output, byID(9))
	stream := output.Bytes()
	if bytes.Index(stream, []byte(`ial"}`)) > bytes.Index(stream, []byte(`"id":9`)) {
		t.Fatal("answer overtook the cloud message it was waiting for")
	}
}

func TestChatGPTLocalWatchReportsChangesUntilUnwatched(t *testing.T) {
	files, output := connectedFiles(t)
	project := t.TempDir()
	ask(t, files, 1, "fs/watch", map[string]any{"path": project, "watchId": "w1"})
	if result := waitForMessage(t, output, byID(1))["result"].(map[string]any); result["path"] != project {
		t.Fatal(result)
	}
	created := filepath.Join(project, "new.txt")
	os.WriteFile(created, []byte("x"), 0644)
	changed := waitForMessage(t, output, func(m map[string]any) bool { return m["method"] == "fs/changed" })
	params := changed["params"].(map[string]any)
	if params["watchId"] != "w1" || !strings.Contains(string(mustJSON(params["changedPaths"])), "new.txt") {
		t.Fatal(changed)
	}
	if forward := ask(t, files, 2, "fs/unwatch", map[string]any{"watchId": "w1"}); len(forward) != 0 {
		t.Fatal("unwatch of a Mac watch went to the cloud")
	}
	waitForMessage(t, output, byID(2))
	before := strings.Count(string(output.Bytes()), "fs/changed")
	os.WriteFile(filepath.Join(project, "later.txt"), []byte("x"), 0644)
	time.Sleep(2500 * time.Millisecond)
	if strings.Count(string(output.Bytes()), "fs/changed") != before {
		t.Fatal("watch kept reporting after unwatch")
	}
	// Unknown watch IDs belong to the cloud.
	if forward := ask(t, files, 3, "fs/unwatch", map[string]any{"watchId": "cloud"}); len(forward) == 0 {
		t.Fatal("cloud unwatch answered on the Mac")
	}
}

func TestNonWebSocketStreamsPassThroughUntouched(t *testing.T) {
	output := &lockedBuffer{}
	files := NewChatGPTLocalFiles(output, true)
	defer files.Close()
	if forward, err := files.Feed([]byte("GET /rpc HTTP/1.1\r\n\r\nping")); err != nil || !bytes.HasSuffix(forward, []byte("ping")) {
		t.Fatalf("%q %v", forward, err)
	}
	files.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n\r\npong"))
	if !bytes.HasSuffix(output.Bytes(), []byte("pong")) {
		t.Fatalf("%q", output.Bytes())
	}
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
