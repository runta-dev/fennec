package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/byoc"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// sshRunner runs the user's own OpenSSH client, so their configuration, agent,
// known hosts and prompts apply to the server they already administer.
type sshRunner func(ctx context.Context, args []string, stdin string, stderr io.Writer) (string, error)

func systemSSH(ctx context.Context, args []string, stdin string, stderr io.Writer) (string, error) {
	command := exec.CommandContext(ctx, "/usr/bin/ssh", args...)
	command.Stdin = strings.NewReader(stdin)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = stderr
	err := command.Run()
	return stdout.String(), err
}

var byocSSH sshRunner = systemSSH

type byocDeployRequest struct {
	Target     string
	Address    string
	Deployment byoc.Deployment
}

func parseBYOCDeploy(args []string) (byocDeployRequest, error) {
	request := byocDeployRequest{Deployment: byoc.Deployment{Image: byoc.DefaultImage, Container: "fennec-cloud", Volume: "fennec-home", Port: 2222}}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || strings.ContainsAny(args[0], " \t\r\n'\"\\;`$") {
		return request, fmt.Errorf("%s", byocUsage)
	}
	request.Target = args[0]
	for rest := args[1:]; len(rest) > 0; rest = rest[2:] {
		if len(rest) < 2 {
			return request, fmt.Errorf("%s", byocUsage)
		}
		switch rest[0] {
		case "--port":
			port, err := strconv.Atoi(rest[1])
			if err != nil {
				return request, fmt.Errorf("invalid port %q", rest[1])
			}
			request.Deployment.Port = port
		case "--image":
			request.Deployment.Image = rest[1]
		case "--name":
			request.Deployment.Container = rest[1]
		case "--volume":
			request.Deployment.Volume = rest[1]
		case "--address":
			request.Address = rest[1]
		default:
			return request, fmt.Errorf("%s", byocUsage)
		}
	}
	return request, nil
}

// sshHostname resolves the host name OpenSSH would connect to for target,
// following the user's ssh configuration (aliases, HostName).
func sshHostname(ctx context.Context, run sshRunner, target string) (string, error) {
	output, err := run(ctx, []string{"-G", "--", target}, "", io.Discard)
	if err != nil {
		return "", fmt.Errorf("cannot read the SSH configuration for %s: %w", target, err)
	}
	for _, line := range strings.Split(output, "\n") {
		if value, ok := strings.CutPrefix(line, "hostname "); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", fmt.Errorf("cannot resolve the host name of %s", target)
}

// byocDeploy starts the cloud image on a server over the user's SSH, reads the
// computer's host key back over that same trusted connection, and pairs with
// it. Progress goes to stderr; the final pairing status is printed as JSON.
func byocDeploy(ctx context.Context, client cloudCaller, args []string, output, progress io.Writer, run sshRunner) error {
	request, err := parseBYOCDeploy(args)
	if err != nil {
		return err
	}
	var key struct {
		DeviceKey string `json:"device_key"`
	}
	if err := client.Call(ctx, "byoc.key", nil, &key); err != nil {
		return err
	}
	request.Deployment.DeviceKey = key.DeviceKey
	script, err := request.Deployment.Script()
	if err != nil {
		return err
	}
	host, port := request.Address, request.Deployment.Port
	if host == "" {
		if host, err = sshHostname(ctx, run, request.Target); err != nil {
			return err
		}
	} else if host, port, err = splitBYOCAddress(request.Address); err != nil {
		return err
	}
	fmt.Fprintf(progress, "Starting the Fennec cloud computer on %s…\n", request.Target)
	stdout, err := run(ctx, []string{"-T", "--", request.Target, "sh", "-s"}, script, progress)
	if err != nil {
		return fmt.Errorf("deployment on %s failed: %w", request.Target, err)
	}
	hostKey, err := byoc.ParseDeployOutput(stdout)
	if err != nil {
		return err
	}
	fmt.Fprintf(progress, "Pairing with %s…\n", netAddress(host, port))
	var status any
	if err := client.Call(ctx, "byoc.pair", byocPairRequest{Host: host, Port: port, User: byoc.DefaultUser, Fingerprint: hostKey}, &status); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(status)
}

func netAddress(host string, port int) string {
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return host + ":" + strconv.Itoa(port)
}

func byocDeployCommand(ctx context.Context, client cloudCaller, args []string, output io.Writer) error {
	return byocDeploy(ctx, client, args, output, os.Stderr, byocSSH)
}
