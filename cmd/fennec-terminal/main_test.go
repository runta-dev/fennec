package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/cmuxhooks"
	"os"
	"os/exec"
	"strings"
	"time"

	"fennec/internal/terminalstream"
	"strconv"
	"testing"
)

func TestProtocolGateRejectsBeforeReadingLaunch(t *testing.T) {
	for _, args := range [][]string{{"serve", "--protocol=0"}, {"serve", "--protocol=999"}, {"serve", "--protocol="}, {"serve", "--protocol=6", "extra"}} {
		if serving, err := serveMode(args); err == nil || serving {
			t.Fatal("incompatible protocol accepted", args)
		}
	}
	if serving, err := serveMode([]string{"serve", "--protocol=" + strconv.Itoa(terminalstream.ProtocolVersion)}); err != nil || !serving {
		t.Fatal(serving, err)
	}
}

func TestCmuxHookCommandChild(t *testing.T) {
	if os.Getenv("FENNEC_CMUX_HOOK_COMMAND_CHILD") != "1" {
		return
	}
	os.Args = []string{"fennec-terminal", "cmux-hook", "petite-neat", "inbox-stop"}
	code, err := run()
	if err != nil {
		os.Exit(99)
	}
	os.Exit(code)
}
func TestCmuxHookCommandReturnsDecisionAndHandlesOfflineMac(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fn-cmux-command-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closeListener, err := cmuxhooks.Listen(ctx, home, "petite-neat", func(context.Context, string, []byte) cmuxhooks.Response {
		return cmuxhooks.Response{Code: 2, Data: json.RawMessage(`{"decision":"block","reason":"fixture"}`)}
	})
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	invoke := func() ([]byte, error) {
		command := exec.CommandContext(ctx, binary, "-test.run=^TestCmuxHookCommandChild$")
		command.Env = append(os.Environ(), "HOME="+home, "FENNEC_CMUX_HOOK_COMMAND_CHILD=1")
		command.Stdin = strings.NewReader(`{"session_id":"fixture"}`)
		return command.Output()
	}
	data, err := invoke()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 2 || !bytes.Contains(data, []byte(`"decision":"block"`)) {
		t.Fatal("hook decision not returned", err, string(data))
	}
	closeListener()
	data, err = invoke()
	if err != nil || string(data) != "{}\n" {
		t.Fatal("offline desktop became code 127", err, string(data))
	}
}
