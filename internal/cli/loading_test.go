package cli

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func TestLaunchLoadingLifecycle(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		output := &synchronizedBuffer{}
		loading := startLaunchLoading(output, interactive, true)
		if interactive {
			time.Sleep(400 * time.Millisecond)
		}
		loading.Clear()
		loading.Stop()
		text := output.String()
		if interactive && !strings.HasSuffix(text, "\r\033[2K") {
			t.Fatal("loading was not erased", text)
		}
		for _, unwanted := range []string{"Fennec:", "waiting for cloud session", "reconnect", "session_id"} {
			if strings.Contains(text, unwanted) {
				t.Fatal("debug output leaked into loading", text)
			}
		}
		if !interactive && text != "" {
			t.Fatal("noninteractive launch rendered animation", text)
		}
		if interactive && !strings.Contains(text, "⠙") {
			t.Fatal("interactive launch did not animate", text)
		}
		if strings.Contains(text, "Connecting") {
			t.Fatal("loading contains text", text)
		}
	}
}

func TestLaunchLoadingDoesNotPollutePipes(t *testing.T) {
	output := &synchronizedBuffer{}
	loading := startLaunchLoading(output, false, true)
	loading.Stop()
	loading.Clear()
	loading.Fail()
	if output.String() != "" {
		t.Fatal("noninteractive launch emitted control characters", output.String())
	}
}
