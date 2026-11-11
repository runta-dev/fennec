package localssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"golang.org/x/crypto/ssh"
	"strings"
	"testing"
)

func TestDeviceAliases(t *testing.T) {
	a, e := DeviceAlias("Shiqi’s MacBook", "aabbccdd11223344")
	if e != nil || a != "Shiqi-s-MacBook" {
		t.Fatalf("%s %v", a, e)
	}
	b, _ := DeviceAlias("中文电脑", "11223344")
	if b != "computer" {
		t.Fatal(b)
	}
}
func TestStrictAliasConfig(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(pub)
	text := string(ssh.MarshalAuthorizedKey(key))
	cfg, known, e := AliasConfig("macbook", "127.0.0.1:3022", "aabbccdd", text, true)
	if e != nil || !strings.Contains(cfg, "Host macbook fennec-device-aabbccdd machine\n") || !strings.Contains(cfg, "StrictHostKeyChecking yes") || !strings.HasPrefix(known, "fennec-aabbccdd ") {
		t.Fatalf("%s %s %v", cfg, known, e)
	}
	for _, alias := range []string{"-oProxyCommand=bad", "mac\nHost *", "mac book"} {
		if _, _, e := AliasConfig(alias, "127.0.0.1:3022", "aabbccdd", text, false); e == nil {
			t.Fatal("accepted unsafe alias")
		}
	}
}
