package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var stderr bytes.Buffer
	if exitCode := run([]string{server.URL}, &stderr); exitCode != 0 {
		t.Fatalf("run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
}

func TestRunUnhealthyStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	var stderr bytes.Buffer
	if exitCode := run([]string{server.URL}, &stderr); exitCode != 1 {
		t.Fatalf("run() exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "503 Service Unavailable") {
		t.Fatalf("stderr = %q, want HTTP status", stderr.String())
	}
}

func TestRunTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var stderr bytes.Buffer
	if exitCode := run([]string{"-timeout=1ms", server.URL}, &stderr); exitCode != 1 {
		t.Fatalf("run() exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "request failed") {
		t.Fatalf("stderr = %q, want request error", stderr.String())
	}
}

func TestRunRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"-timeout=0s", "http://127.0.0.1"}} {
		var stderr bytes.Buffer
		if exitCode := run(args, &stderr); exitCode != 1 {
			t.Fatalf("run(%q) exit code = %d, want 1", args, exitCode)
		}
	}
}
