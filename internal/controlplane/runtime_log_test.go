package controlplane

import (
	"bytes"
	"errors"
	"fennec/internal/runta"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"
)

func TestPreparationResultIsLoggedWithRuntaCode(t *testing.T) {
	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(previous)
	logPreparationResult(7, 1500*time.Millisecond, fmt.Errorf("create runtime: %w", &runta.APIError{Status: 402, Code: "balance_exhausted"}), nil)
	logPreparationResult(8, time.Second, nil, nil)
	logPreparationResult(9, time.Second, nil, errors.New("context deadline exceeded"))
	got := out.String()
	for _, want := range []string{
		`preparation 7 failed after 1.5s: create runtime: Runta HTTP 402 [runta status=402 code="balance_exhausted"]`,
		"preparation 8 ready after 1s",
		"preparation 9 failed after 1s: context deadline exceeded",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log missing %q:\n%s", want, got)
		}
	}
}
