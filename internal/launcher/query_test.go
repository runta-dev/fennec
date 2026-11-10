package launcher

import "testing"

func TestLocalQueryOnlyRecognizesStandaloneFlags(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"--help"}, {"-h"}} {
		if !LocalQuery(args) {
			t.Fatal(args)
		}
	}
	for _, args := range [][]string{nil, {"-p", "--help"}, {"--model", "--version"}, {"help with my code"}, {"--", "--help"}} {
		if LocalQuery(args) {
			t.Fatal("launch mistaken for query", args)
		}
	}
}
