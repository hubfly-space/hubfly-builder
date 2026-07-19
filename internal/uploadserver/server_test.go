package uploadserver

import (
	"strings"
	"testing"
)

func TestGenerateImageTagUsesHubcellRegistryAndProjectScopedName(t *testing.T) {
	server := &Server{}
	tag := server.generateImageTag("prj_123", "build_456")

	if len(tag) == 0 {
		t.Fatal("expected non-empty tag")
	}
	if !strings.HasPrefix(tag, "hubcell.local/") {
		t.Fatalf("expected hubcell runtime tag, got %q", tag)
	}
	if !strings.Contains(tag, "/prj_123:build_456-") {
		t.Fatalf("tag did not include project scoping: %q", tag)
	}
}

func TestGenerateRegistryPushTagUsesLoopbackRegistry(t *testing.T) {
	server := &Server{}
	tag := server.generateRegistryPushTag("prj_123", "build_456")

	if len(tag) == 0 {
		t.Fatal("expected non-empty tag")
	}
	if !strings.HasPrefix(tag, "127.0.0.1:10017/") {
		t.Fatalf("expected loopback registry target, got %q", tag)
	}
	if !strings.Contains(tag, "/prj_123:build_456-") {
		t.Fatalf("tag did not include project scoping: %q", tag)
	}
}

func TestParseTarballTagRejectsEmptyValues(t *testing.T) {
	if _, err := parseTarballTag(""); err == nil {
		t.Fatal("expected empty tag to fail")
	}
}
