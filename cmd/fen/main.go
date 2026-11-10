package main

import (
	"fennec/internal/cli"
	"fmt"
	"os"
)

func main() {
	if err := cli.Run(append([]string{"fen"}, os.Args[1:]...), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
