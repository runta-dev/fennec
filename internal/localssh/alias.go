package localssh

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/ssh"
)

// DeviceAlias preserves the hostname. Registration resolves collisions remotely.
func DeviceAlias(name, id string) (string, error) {
	if !regexp.MustCompile(`^[a-f0-9]{8,64}$`).MatchString(id) {
		return "", fmt.Errorf("invalid device ID")
	}
	var out strings.Builder
	for _, r := range name {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-') {
			out.WriteRune(r)
		} else if out.Len() > 0 && !strings.HasSuffix(out.String(), "-") {
			out.WriteByte('-')
		}
		if out.Len() >= 240 {
			break
		}
	}
	base := strings.Trim(out.String(), "-.")
	if base == "" {
		base = "computer"
	}
	return base, nil
}

// AliasConfig is written to a Fennec-owned included file, never over ~/.ssh/config.
// The private key referenced here must be generated on the runtime, not copied
// from local login directories. The host key is supplied by the paired device.
// Connections are multiplexed: over the cloud-to-Mac link a fresh SSH handshake
// costs about 1.5 s, while a session on a live master costs one exchange. The
// Mac server re-checks pairing for every session on a shared connection, and
// the master ends with the reverse tunnel that carries it.
func AliasConfig(alias, address, deviceID, hostPublicKey string, defaultDevice bool) (string, string, error) {
	if !validAlias(alias) || !regexp.MustCompile(`^[a-f0-9]{8,64}$`).MatchString(deviceID) {
		return "", "", fmt.Errorf("invalid alias identity")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return "", "", fmt.Errorf("remote listener must be loopback")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", "", fmt.Errorf("invalid remote port")
	}
	key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(hostPublicKey))
	if err != nil || len(strings.TrimSpace(string(rest))) > 0 {
		return "", "", fmt.Errorf("invalid device host key")
	}
	hosts := alias + " fennec-device-" + deviceID
	if defaultDevice {
		hosts += " machine"
	}
	identity := "fennec-" + deviceID
	config := fmt.Sprintf("Host %s\n  HostName 127.0.0.1\n  Port %d\n  User fennec\n  IdentityFile ~/.ssh/fennec/%s\n  IdentitiesOnly yes\n  HostKeyAlias %s\n  UserKnownHostsFile ~/.ssh/fennec/known_hosts\n  StrictHostKeyChecking yes\n  ForwardAgent no\n  ControlMaster auto\n  ControlPath ~/.ssh/fennec/mux-%%C\n  ControlPersist 10m\n  ServerAliveInterval 15\n  ServerAliveCountMax 3\n", hosts, port, deviceID, identity)
	return config, identity + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + "\n", nil
}
