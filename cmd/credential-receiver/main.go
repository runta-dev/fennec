package main

import (
	"encoding/json"
	"fennec/internal/setup"
	"fmt"
	"os"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		os.Exit(1)
	}
	report, err := setup.ReceiveCredentials(home, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(report)
}
