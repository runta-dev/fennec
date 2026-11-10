package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReceiptOutputHandlesFragmentationAndClearsBeforeTUI(t *testing.T) {
	var output bytes.Buffer
	accepted, cleared := false, false
	session := strings.Repeat("a", 32)
	writer := receiptOutput{plan: LaunchPlan{SessionID: session, Agent: "codex"}, writer: &output, ready: func() {
		if !accepted {
			t.Fatal("output before receipt persistence")
		}
		cleared = true
	}, accept: func(receipt LaunchReceipt) error { accepted = true; return nil }}
	header, _ := json.Marshal(map[string]string{"id": session, "agent": "codex", "address": "t1", "state": "live"})
	data := append([]byte("\x1eFENNEC_RECEIPT "), header...)
	data = append(data, []byte("\r\nTUI")...)
	for _, value := range data {
		if _, err := writer.Write([]byte{value}); err != nil {
			t.Fatal(err)
		}
	}
	if !accepted || !cleared || output.String() != "TUI" {
		t.Fatal("receipt leaked or output lost", output.String())
	}
}

func TestReceiptOutputRejectsDifferentSession(t *testing.T) {
	writer := receiptOutput{plan: LaunchPlan{SessionID: strings.Repeat("a", 32), Agent: "codex"}, accept: func(LaunchReceipt) error { t.Fatal("accepted wrong session"); return nil }}
	if _, err := writer.Write([]byte("\x1eFENNEC_RECEIPT {\"id\":\"wrong\",\"agent\":\"codex\",\"state\":\"live\"}\n")); err == nil {
		t.Fatal("receipt identity not checked")
	}
}

func TestReservedLaunchCannotReplayBeforeConfirmation(t *testing.T) {
	journal := LaunchJournal{Directory: t.TempDir()}
	intent := LaunchIntent{RequestID: strings.Repeat("b", 32), Agent: "codex", Directory: "/project", Prepare: true}
	if err := journal.Reserve(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err := journal.Reserve(context.Background(), intent); err == nil {
		t.Fatal("pending launch replayed")
	}
	receipt := LaunchReceipt{SessionID: intent.RequestID, Agent: intent.Agent, Address: "t1"}
	if err := journal.Complete(intent, receipt); err != nil {
		t.Fatal(err)
	}
	if err := journal.Reserve(context.Background(), intent); err == nil {
		t.Fatal("confirmed launch replayed instead of reconnecting")
	}
}
