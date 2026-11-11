package main

import (
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
