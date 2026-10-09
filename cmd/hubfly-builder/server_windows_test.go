//go:build windows

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWindowsRejectsServer(t *testing.T) {
	for _, args := range [][]string{nil, {"serve"}} {
		if err := run(args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "offline inspect") {
			t.Fatalf("expected offline-only error, got %v", err)
		}
	}
}
