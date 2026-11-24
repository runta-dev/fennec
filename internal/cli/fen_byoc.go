package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

const byocUsage = `usage: fen cloud byoc status
       fen cloud byoc key
       fen cloud byoc pair <host>[:<port>] --fingerprint <SHA256:...> [--user <name>]
       fen cloud byoc deploy <[user@]server> [--port 2222] [--image <ref>] [--name <container>] [--volume <volume>] [--address <host>[:<port>]]
       fen cloud byoc unpair [<machine-id>]`

// byocCommand pairs this Mac with a self-hosted cloud computer. The host key
// fingerprint is required: it is how the user vouches for the computer.
func byocCommand(ctx context.Context, client cloudCaller, args []string, output io.Writer) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	call := func(method string, params any) error {
		var result any
		if err := client.Call(ctx, method, params, &result); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(result)
	}
	switch {
	case args[0] == "status" && len(args) == 1:
		return call("byoc.status", nil)
	case args[0] == "key" && len(args) == 1:
		return call("byoc.key", nil)
	case args[0] == "deploy":
		return byocDeployCommand(ctx, client, args[1:], output)
	case args[0] == "pair":
		request, err := parseBYOCPair(args[1:])
		if err != nil {
			return err
		}
		return call("byoc.pair", request)
	case args[0] == "unpair" && len(args) <= 2:
		request := byocUnpairRequest{}
		if len(args) == 2 {
			request.MachineID = args[1]
		}
		return call("byoc.unpair", request)
	}
	return fmt.Errorf("%s", byocUsage)
}

func parseBYOCPair(args []string) (byocPairRequest, error) {
	var request byocPairRequest
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return request, fmt.Errorf("%s", byocUsage)
	}
	host, port, err := splitBYOCAddress(args[0])
	if err != nil {
		return request, err
	}
	request.Host, request.Port = host, port
	for rest := args[1:]; len(rest) > 0; rest = rest[2:] {
		if len(rest) < 2 {
			return request, fmt.Errorf("%s", byocUsage)
		}
		switch rest[0] {
		case "--fingerprint":
			request.Fingerprint = rest[1]
		case "--user":
			request.User = rest[1]
		default:
			return request, fmt.Errorf("%s", byocUsage)
		}
	}
	if request.Fingerprint == "" {
		return request, fmt.Errorf("--fingerprint is required: copy it from the computer's `docker logs` output")
	}
	return request, nil
}

// splitBYOCAddress accepts host, host:port, [ipv6] and [ipv6]:port.
func splitBYOCAddress(address string) (string, int, error) {
	if host, port, err := net.SplitHostPort(address); err == nil {
		number, convErr := strconv.Atoi(port)
		if convErr != nil || number < 1 || number > 65535 {
			return "", 0, fmt.Errorf("invalid port %q", port)
		}
		return host, number, nil
	}
	host := strings.TrimSuffix(strings.TrimPrefix(address, "["), "]")
	if host == "" || strings.Contains(host, "]") || (strings.Count(host, ":") == 1) {
		return "", 0, fmt.Errorf("invalid address %q", address)
	}
	return host, 22, nil
}
