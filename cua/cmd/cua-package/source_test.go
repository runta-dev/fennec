package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchSourceRetriesTransientFailures(t *testing.T) {
	for _, status := range []int{http.StatusGatewayTimeout, http.StatusTooManyRequests, http.StatusNotFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					w.WriteHeader(status)
					return
				}
				fmt.Fprint(w, base64.StdEncoding.EncodeToString([]byte("The Chromium Authors")))
			}))
			defer server.Close()
			got, err := fetchSourceURL(context.Background(), server.Client(), server.URL, 1<<20, 0)
			if status == http.StatusNotFound {
				if err == nil || calls != 1 {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			} else if err != nil || calls != 2 || string(got) != "The Chromium Authors" {
				t.Fatalf("got=%q calls=%d err=%v", got, calls, err)
			}
		})
	}
}

func TestFetchSourceStopsAfterThreeAttempts(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(504) }))
	defer server.Close()
	if _, err := fetchSourceURL(context.Background(), server.Client(), server.URL, 1024, 0); err == nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestFetchSourceCancelsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cancel(); w.WriteHeader(504) }))
	defer server.Close()
	if _, err := fetchSourceURL(ctx, server.Client(), server.URL, 1024, time.Hour); err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
}

func TestFetchSourceRejectsInvalidEncoding(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, "not base64!") }))
	defer server.Close()
	if _, err := fetchSourceURL(context.Background(), server.Client(), server.URL, 1024, 0); err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
