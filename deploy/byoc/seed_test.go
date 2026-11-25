package byocimage

import (
	"fennec/internal/setup"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The image seeds Node.js only to spare the first connection a download: its
// version and checksums must be the ones Fennec's preparation pins, or every
// computer would download Node.js again anyway.
func TestSeededNodeMatchesPreparation(t *testing.T) {
	data, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	arg := func(name string) string {
		match := regexp.MustCompile(`(?m)^ARG ` + name + `=(\S+)$`).FindStringSubmatch(string(data))
		if match == nil {
			t.Fatalf("Dockerfile lacks ARG %s", name)
		}
		return match[1]
	}
	script := setup.NodeSetupScript
	for _, want := range []string{
		"v" + arg("NODE_VERSION"),
		"x86_64) arch=x64; fd_arch=x86_64; sum=" + arg("NODE_SHA256_AMD64") + ";",
		"aarch64|arm64) arch=arm64; fd_arch=aarch64; sum=" + arg("NODE_SHA256_ARM64") + ";",
		`"$base/node/bin/node"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("NodeSetupScript and the Dockerfile disagree: %q", want)
		}
	}
}
