package byoc

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// DefaultImage is the published self-hosted cloud image.
const DefaultImage = "docker.io/shiqimei/fennec-cloud:1"

// Deployment describes one self-hosted computer to start with Docker on a
// server the user already reaches over SSH.
type Deployment struct {
	Image     string
	Container string
	Volume    string
	Port      int
	DeviceKey string
}

var (
	imagePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(?::[0-9]+)?(?:/[a-z0-9._-]+)*(?::[A-Za-z0-9._-]{1,128})?(?:@sha256:[a-f0-9]{64})?$`)
	containerPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
)

// Validate rejects any value that could change the meaning of the script.
func (d Deployment) Validate() error {
	if !imagePattern.MatchString(d.Image) || strings.Contains(d.Image, "..") {
		return errors.New("invalid image reference")
	}
	if !containerPattern.MatchString(d.Container) || !containerPattern.MatchString(d.Volume) {
		return errors.New("invalid container or volume name")
	}
	if d.Port < 1 || d.Port > 65535 {
		return errors.New("invalid port")
	}
	if _, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(d.DeviceKey)); err != nil || len(options) != 0 || strings.TrimSpace(string(rest)) != "" || strings.ContainsAny(d.DeviceKey, "'\n\r") {
		return errors.New("invalid device key")
	}
	return nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'" }

// hostKeyMarker prefixes the line that carries the computer's host public key.
const hostKeyMarker = "FENNEC_HOST_KEY="

// Script is a POSIX shell script that starts the computer, or reuses an
// existing container on the same volume and authorizes the device on it, then
// prints the computer's host public key. It runs as the SSH user on the
// server and uses sudo for Docker only when it must and can without a prompt.
func (d Deployment) Script() (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	return fmt.Sprintf(`set -eu
image=%s
name=%s
volume=%s
port=%s
key=%s
docker=docker
if ! docker info >/dev/null 2>&1; then
  if command -v docker >/dev/null 2>&1 && sudo -n docker info >/dev/null 2>&1; then
    docker="sudo -n docker"
  else
    echo "Docker is not available to this user on the server" >&2
    exit 69
  fi
fi
if $docker container inspect "$name" >/dev/null 2>&1; then
  $docker start "$name" >/dev/null
else
  # Pull the current release of the tag; an image already on the server (a local
  # build, or a registry that is unreachable right now) is used as is.
  if ! $docker pull "$image" >&2; then
    $docker image inspect "$image" >/dev/null 2>&1 || exit 69
    echo "Using the image already on this server" >&2
  fi
  $docker run -d --name "$name" --restart unless-stopped -p "$port:22" -v "$volume:/home/fennec" -e "FENNEC_AUTHORIZED_KEY=$key" "$image" >/dev/null
fi
tries=0
until $docker exec "$name" fennec-cloud fingerprint >/dev/null 2>&1; do
  tries=$((tries + 1))
  if [ "$tries" -ge 60 ]; then
    $docker logs --tail 20 "$name" >&2 || true
    echo "The Fennec cloud computer did not start" >&2
    exit 70
  fi
  sleep 1
done
$docker exec "$name" fennec-cloud authorize "$key" >&2
printf '%%s%%s\n' %s "$($docker exec "$name" cat /home/fennec/.fennec-host/ssh_host_ed25519_key.pub)"
`, shellQuote(d.Image), shellQuote(d.Container), shellQuote(d.Volume), strconv.Itoa(d.Port), shellQuote(d.DeviceKey), shellQuote(hostKeyMarker)), nil
}

// ParseDeployOutput returns the host public key the script printed.
func ParseDeployOutput(output string) (string, error) {
	var found string
	for _, line := range strings.Split(output, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), hostKeyMarker)
		if !ok {
			continue
		}
		if found != "" {
			return "", errors.New("the server reported more than one host key")
		}
		key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(value))
		if err != nil || len(options) != 0 || strings.TrimSpace(string(rest)) != "" {
			return "", errors.New("the server reported an invalid host key")
		}
		found = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	}
	if found == "" {
		return "", errors.New("the server did not report the computer's host key")
	}
	return found, nil
}
