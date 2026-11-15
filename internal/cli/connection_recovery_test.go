package cli

import (
	"context"
	"io"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestRecoveryStartsReplacementBeforeOldProbeFinishes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := make(chan struct{})
	observed := make(chan struct{})
	client := &ssh.Client{}
	reused, got, err := raceConnectionRecovery(ctx, func(ctx context.Context) error {
		select {
		case <-started:
			close(observed)
			return io.EOF
		case <-ctx.Done():
			return ctx.Err()
		}
	}, func(ctx context.Context) (*ssh.Client, error) {
		close(started)
		select {
		case <-observed:
			return client, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, func(*ssh.Client) { t.Error("discarded the winning connection") })
	if err != nil || reused || got != client {
		t.Fatalf("replacement not selected: reused=%v err=%v", reused, err)
	}
}

func TestRecoveryDiscardsLateReplacementWhenOldConnectionSurvives(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	closed := make(chan struct{})
	reused, got, err := raceConnectionRecovery(ctx, func(context.Context) error { return nil }, func(ctx context.Context) (*ssh.Client, error) {
		<-ctx.Done()
		// A handshake can complete concurrently with cancellation.
		return &ssh.Client{}, nil
	}, func(*ssh.Client) { close(closed) })
	if err != nil || !reused || got != nil {
		t.Fatalf("surviving connection not selected: reused=%v err=%v", reused, err)
	}
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("late losing connection leaked")
	}
}

func TestRecoveryCancelsOldProbeWhenReplacementIsReady(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stopped := make(chan struct{})
	client := &ssh.Client{}
	reused, got, err := raceConnectionRecovery(ctx, func(ctx context.Context) error {
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}, func(context.Context) (*ssh.Client, error) { return client, nil }, func(*ssh.Client) { t.Error("discarded winner") })
	if err != nil || reused || got != client {
		t.Fatalf("ready replacement waited: reused=%v err=%v", reused, err)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("losing probe was not canceled")
	}
}
