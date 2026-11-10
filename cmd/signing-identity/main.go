// signing-identity resolves exactly one certificate from security find-identity.
package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
)

var identityLine = regexp.MustCompile(`\b([0-9A-Fa-f]{40})\s+"([^"]+)"`)

func resolve(input, requested string) (string, error) {
	matches := []string{}
	for _, match := range identityLine.FindAllStringSubmatch(input, -1) {
		if requested == match[1] || requested == match[2] {
			matches = append(matches, match[1])
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("signing identity must exactly match one certificate name or SHA-1 fingerprint")
	}
	return matches[0], nil
}
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: signing-identity <name-or-sha1>")
		os.Exit(2)
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read signing identities")
		os.Exit(1)
	}
	identity, err := resolve(string(input), os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(identity)
}
