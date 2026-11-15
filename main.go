package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fennec/internal/cli"
	"fennec/internal/launchprofile"
)

func main() {
	launchprofile.Record("process.enter", time.Now(), 0)
	arguments := os.Args[1:]
	if filepath.Base(os.Args[0]) == "fen" && cli.PublicInvocation(arguments) {
		arguments = append([]string{"fen"}, arguments...)
	}
	if err := cli.Run(arguments, os.Stdout); err != nil {
		var status interface{ ExitCode() int }
		if errors.As(err, &status) {
			var noticed interface{ Notice() string }
			if errors.As(err, &noticed) {
				if text := noticed.Notice(); text != "" {
					fmt.Fprintln(os.Stderr, text)
				}
			}
			os.Exit(status.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
