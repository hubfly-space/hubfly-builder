package executor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hubfly-builder/internal/envplan"
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
	if ctx != "." {
		t.Fatalf("expected root build context, got %q", ctx)
	}
}

func TestNormalizeDockerfileBuildContextAllowsAncestorContext(t *testing.T) {
	ctx, err := normalizeDockerfileBuildContextDir(".", "backend")
	if err != nil {
		t.Fatalf("expected root context to be allowed: %v", err)
	}
	if ctx != "." {
		t.Fatalf("expected root context, got %q", ctx)
	}

	ctx, err = normalizeDockerfileBuildContextDir("services", "services/api")
	if err != nil {
		t.Fatalf("expected ancestor context to be allowed: %v", err)
	}
	if ctx != "services" {
		t.Fatalf("expected services context, got %q", ctx)
	}
}

func TestNormalizeDockerfileBuildContextRejectsSiblingContext(t *testing.T) {
	if _, err := normalizeDockerfileBuildContextDir("frontend", "backend"); err == nil {
		t.Fatalf("expected sibling context to be rejected")
	}
}

func TestGenerateImageTagUsesRefFallbackWhenCommitMissing(t *testing.T) {
	worker := &Worker{
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
	if !strings.HasPrefix(tag, "hubcell.local/user-test/proj-test:") {
		t.Fatalf("expected hubcell.local image tag, got %q", tag)
	}
	if !strings.Contains(tag, ":main-bbuild_test-v") {
		t.Fatalf("expected ref fallback in image tag, got %q", tag)
	}
}

func TestHubcellBuildPathUsesDotForRootDockerfile(t *testing.T) {
	got := hubcellBuildPath("/tmp/repo", "/tmp/repo/Dockerfile")
	if got != "." {
		t.Fatalf("expected root Dockerfile path to use '.', got %q", got)
	}
}

func TestHubcellBuildPathUsesDirectoryForNestedDockerfile(t *testing.T) {
	got := hubcellBuildPath("/tmp/repo", "/tmp/repo/apps/web/Dockerfile")
	if got != "/tmp/repo/apps/web" {
		t.Fatalf("expected nested Dockerfile directory, got %q", got)
	}
}

func TestDefaultHubcellResourceLimits(t *testing.T) {
	cpu, memoryMB := defaultHubcellResourceLimits()
	if cpu != 2 {
		t.Fatalf("expected default cpu 2, got %v", cpu)
	}
	if memoryMB != 4096 {
		t.Fatalf("expected default memory 4096MB, got %d", memoryMB)
	}
}

func TestResolvedBuildEnvEntriesMergesArgsAndSecrets(t *testing.T) {
	got := resolvedBuildEnvEntries(envplan.Result{
		BuildArgs: map[string]string{
			"APP_ENV": "production",
		},
		BuildSecrets: map[string]string{
			"DATABASE_URL": "postgres://db/app",
		},
	})

	want := []string{
		`APP_ENV="production"`,
		`DATABASE_URL="postgres://db/app"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected env entries %v, got %v", want, got)
	}
}

func TestRedactBuildArgRemovesURLCredentials(t *testing.T) {
	got := redactBuildArg("https://x-access-token:secret-value@github.com/example/private.git")
	if strings.Contains(got, "secret-value") || strings.Contains(got, "x-access-token") {
		t.Fatalf("credential-bearing URL was not redacted: %q", got)
	}
	if got != "https://REDACTED@github.com/example/private.git" {
		t.Fatalf("unexpected redacted URL: %q", got)
	}
}
