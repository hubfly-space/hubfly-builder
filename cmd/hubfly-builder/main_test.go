package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"version"}, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != version+"\n" {
		t.Fatalf("unexpected version: %q", output.String())
	}
}

func TestInvalidOfflineCommand(t *testing.T) {
	if err := run([]string{"offline", "unknown"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "unknown offline command") {
		t.Fatalf("expected offline command error, got %v", err)
	}
}
