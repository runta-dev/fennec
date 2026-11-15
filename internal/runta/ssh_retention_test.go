package runta

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRetainedRecoveryDialerCannotFallBackToDirectSSH(t *testing.T) {
	home := t.TempDir()
	client := &Client{RetainSSHHome: home, Endpoint: "https://api.example.com", Token: "test-only"}
	// A missing private key must fail locally, even if a direct SSH connection
	// could be established. Initial startup and recovery share this dialer.
	connection, err := client.retainedSSHDialer(filepath.Join(home, "cloud"))(context.Background(), testSSHConfig(t))
	if connection != nil || !os.IsNotExist(err) {
		t.Fatalf("retention silently bypassed: client=%v error=%v", connection, err)
	}
}
