package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/byoc"
	"io"
	"strings"
	"testing"
)

type recordedCaller struct {
	calls  []string
	params []any
	key    string
}

func (c *recordedCaller) Call(_ context.Context, method string, params any, result any) error {
	c.calls = append(c.calls, method)
	c.params = append(c.params, params)
	var reply any = map[string]any{"paired": true}
	if method == "byoc.key" {
		reply = map[string]string{"device_key": c.key}
	}
	data, _ := json.Marshal(reply)
	return json.Unmarshal(data, result)
}

func TestByocDeployPairsWithTheKeyReadOverSSH(t *testing.T) {
	hostKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIE8bcnb1JkdcVM1C2FSSmJbV4PuqaHT0ICYAPpp7GdO"
	device := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2r0BSzk7ru9MVrZQ6jZnfdNnL7bpBuu8ilmgEqvPCw Fennec device"
	caller := &recordedCaller{key: device}
	var sshCalls [][]string
	var script string
	run := func(_ context.Context, args []string, stdin string, _ io.Writer) (string, error) {
		sshCalls = append(sshCalls, args)
		if args[0] == "-G" {
			return "user admin\nhostname 198.51.100.7\nport 22\n", nil
		}
		script = stdin
		return "Device authorized.\nFENNEC_HOST_KEY=" + hostKey + " root@abc\n", nil
	}
	var output, progress bytes.Buffer
	if err := byocDeploy(context.Background(), caller, []string{"myvps", "--port", "2200"}, &output, &progress, run); err != nil {
		t.Fatal(err)
	}
	if len(sshCalls) != 2 || strings.Join(sshCalls[0], " ") != "-G -- myvps" || strings.Join(sshCalls[1], " ") != "-T -- myvps sh -s" {
		t.Fatalf("unexpected ssh invocations %q", sshCalls)
	}
	if !strings.Contains(script, "key='"+device+"'") || !strings.Contains(script, "port=2200") || !strings.Contains(script, "image='"+byoc.DefaultImage+"'") {
		t.Fatalf("script does not carry the deployment:\n%s", script)
	}
	if strings.Join(caller.calls, ",") != "byoc.key,byoc.pair" {
		t.Fatalf("unexpected control calls %q", caller.calls)
	}
	pair := caller.params[1].(byocPairRequest)
	if pair.Host != "198.51.100.7" || pair.Port != 2200 || pair.User != byoc.DefaultUser || pair.Fingerprint != hostKey {
		t.Fatalf("unexpected pairing %+v", pair)
	}
	if !strings.Contains(output.String(), `"paired":true`) {
		t.Fatalf("unexpected output %q", output.String())
	}

	// --address overrides where the Mac reaches the computer (for example
	// through a private network rather than the SSH jump host).
	caller = &recordedCaller{key: device}
	sshCalls = nil
	if err := byocDeploy(context.Background(), caller, []string{"admin@jump", "--address", "10.8.0.4:2222"}, io.Discard, io.Discard, run); err != nil {
		t.Fatal(err)
	}
	if len(sshCalls) != 1 {
		t.Fatalf("resolved the host name despite --address: %q", sshCalls)
	}
	if pair := caller.params[1].(byocPairRequest); pair.Host != "10.8.0.4" || pair.Port != 2222 {
		t.Fatalf("unexpected pairing %+v", pair)
	}
}

func TestByocDeployNeverPairsAfterAFailedDeployment(t *testing.T) {
	device := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2r0BSzk7ru9MVrZQ6jZnfdNnL7bpBuu8ilmgEqvPCw"
	for name, run := range map[string]sshRunner{
		"ssh failed": func(_ context.Context, args []string, _ string, _ io.Writer) (string, error) {
			if args[0] == "-G" {
				return "hostname h\n", nil
			}
			return "", errors.New("exit status 69")
		},
		"no host key": func(_ context.Context, args []string, _ string, _ io.Writer) (string, error) {
			return "hostname h\n", nil
		},
	} {
		caller := &recordedCaller{key: device}
		if err := byocDeploy(context.Background(), caller, []string{"server"}, io.Discard, io.Discard, run); err == nil {
			t.Fatalf("%s: deployment succeeded", name)
		}
		for _, call := range caller.calls {
			if call == "byoc.pair" {
				t.Fatalf("%s: paired after a failed deployment", name)
			}
		}
	}
	for _, args := range [][]string{{}, {"-oProxyCommand=x"}, {"a b"}, {"host;rm"}, {"host", "--port"}, {"host", "--port", "x"}, {"host", "--bogus", "1"}} {
		if err := byocDeploy(context.Background(), &recordedCaller{key: device}, args, io.Discard, io.Discard, nil); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
