package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"fennec/internal/cli"
)

func main() {
	arguments := os.Args[1:]
	if filepath.Base(os.Args[0]) == "fen" && cli.PublicInvocation(arguments) {
		arguments = append([]string{"fen"}, arguments...)
	}
	if err := cli.Run(arguments, os.Stdout); err != nil {
		var status interface{ ExitCode() int }
		if errors.As(err, &status) {
			os.Exit(status.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
