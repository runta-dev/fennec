// signing-identity resolves exactly one certificate from security find-identity.
package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var identityLine = regexp.MustCompile(`\b([0-9A-Fa-f]{40})\s+"([^"]+)"`)

func resolve(input, requested string) (string, error) {
	matches := []string{}
	seen := map[string]bool{}
	for _, match := range identityLine.FindAllStringSubmatch(input, -1) {
		if requested == match[1] || requested == match[2] {
			fingerprint := strings.ToUpper(match[1])
			if !seen[fingerprint] {
				matches = append(matches, fingerprint)
				seen[fingerprint] = true
			}
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
