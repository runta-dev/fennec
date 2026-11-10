package main

import (
	"errors"
	"fmt"
	"os"

	"fennec/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdout); err != nil {
		var status interface{ ExitCode() int }
		if errors.As(err, &status) {
			os.Exit(status.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
