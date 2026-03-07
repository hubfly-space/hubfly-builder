package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hubfly-builder/internal/storage"
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

func TestGenerateImageTagUsesRefFallbackWhenCommitMissing(t *testing.T) {
	worker := &Worker{
		registry: "registry.example.com:5000",
		job: &storage.BuildJob{
			ID:        "build_test",
			ProjectID: "proj_test",
			UserID:    "user_test",
			SourceInfo: storage.SourceInfo{
				Ref: "main",
			},
		},
	}

	tag := worker.generateImageTag()
	if strings.Contains(tag, ":-b") {
		t.Fatalf("expected non-empty image tag source component, got %q", tag)
	}
	if !strings.Contains(tag, ":main-bbuild_test-v") {
		t.Fatalf("expected ref fallback in image tag, got %q", tag)
	}
}
