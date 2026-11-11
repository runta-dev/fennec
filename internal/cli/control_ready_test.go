package cli

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStartupRecoversDisappearingLaunchdRegistration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	present, ready, polls, starts := true, false, 0, 0
	err := waitControlReady(ctx, "build", func() (string, error) {
		polls++
		if ready {
			return "build", nil
		}
		if polls == 2 {
			present = false
		}
		return "", errors.New("socket unavailable")
	}, func() bool { return present }, func() error { starts++; present = true; ready = true; return nil })
	if err != nil || starts != 1 {
		t.Fatalf("retiring job stranded startup: starts=%d err=%v", starts, err)
	}
}

func TestStartupDoesNotDuplicateRegisteredService(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := waitControlReady(ctx, "build", func() (string, error) { cancel(); return "", errors.New("not ready") }, func() bool { return true }, func() error { t.Fatal("duplicated registered job"); return nil })
	if err == nil {
		t.Fatal("cancelled startup succeeded")
	}
}

func TestStartupReturnsFailedRegistration(t *testing.T) {
	failure := errors.New("registration refused")
	err := waitControlReady(context.Background(), "build", func() (string, error) { return "", errors.New("unavailable") }, func() bool { return false }, func() error { return failure })
	if !errors.Is(err, failure) {
		t.Fatalf("registration failure hidden: %v", err)
	}
}
