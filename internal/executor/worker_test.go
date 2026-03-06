package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectDockerfileLayoutIgnoresDockerfileDirectory(t *testing.T) {
	repo := t.TempDir()

	if err := os.Mkdir(filepath.Join(repo, "Dockerfile"), 0o755); err != nil {
		t.Fatalf("failed to create Dockerfile directory: %v", err)
	}

	path, ctx := detectDockerfileLayout(repo, ".")
	if path != "" || ctx != "" {
		t.Fatalf("expected no Dockerfile to be detected, got path=%q ctx=%q", path, ctx)
	}
}

func TestDetectDockerfileLayoutPrefersAppDockerfile(t *testing.T) {
	repo := t.TempDir()

	if err := os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("failed to write root Dockerfile: %v", err)
	}
	appDir := filepath.Join(repo, "apps", "web")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("failed to create app dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "Dockerfile"), []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatalf("failed to write app Dockerfile: %v", err)
	}

	path, ctx := detectDockerfileLayout(repo, "apps/web")
	if path != filepath.Join(appDir, "Dockerfile") {
		t.Fatalf("expected app Dockerfile path, got %q", path)
	}
	if ctx != "apps/web" {
		t.Fatalf("expected app build context, got %q", ctx)
	}
}
