package envplan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hubfly-builder/internal/storage"
)

func boolPtr(v bool) *bool {
	return &v
}

func TestResolve_AutoDetectsWhenNoOverrides(t *testing.T) {
	result := Resolve("", map[string]string{
		"NEXT_PUBLIC_API_URL": "http://backend:8080",
	}, nil)

	entry := findEntry(result.Entries, "NEXT_PUBLIC_API_URL")
	if entry == nil {
		t.Fatalf("expected resolved entry for NEXT_PUBLIC_API_URL")
	}
	if entry.Scope != "both" {
		t.Fatalf("expected scope both, got %q", entry.Scope)
	}
	if entry.Secret {
		t.Fatalf("expected NEXT_PUBLIC_API_URL to be non-secret by default")
	}

	if _, ok := result.BuildArgs["NEXT_PUBLIC_API_URL"]; !ok {
		t.Fatalf("expected NEXT_PUBLIC_API_URL in build args")
	}
	if _, ok := result.BuildSecrets["NEXT_PUBLIC_API_URL"]; ok {
		t.Fatalf("did not expect NEXT_PUBLIC_API_URL in build secrets")
	}
}

func TestResolve_AppliesOverrides(t *testing.T) {
	result := Resolve("", map[string]string{
		"NEXT_PUBLIC_API_URL": "http://backend:8080",
	}, map[string]storage.EnvOverride{
		"NEXT_PUBLIC_API_URL": {
			Scope:  "build",
			Secret: boolPtr(true),
		},
	})

	entry := findEntry(result.Entries, "NEXT_PUBLIC_API_URL")
	if entry == nil {
		t.Fatalf("expected resolved entry for NEXT_PUBLIC_API_URL")
	}
	if entry.Scope != "build" {
		t.Fatalf("expected override scope build, got %q", entry.Scope)
	}
	if !entry.Secret {
		t.Fatalf("expected override to force secret=true")
	}
	if !strings.Contains(entry.Reason, "override-scope") {
		t.Fatalf("expected reason to include override-scope, got %q", entry.Reason)
	}
	if !strings.Contains(entry.Reason, "override-secret") {
		t.Fatalf("expected reason to include override-secret, got %q", entry.Reason)
	}

	if _, ok := result.BuildSecrets["NEXT_PUBLIC_API_URL"]; !ok {
		t.Fatalf("expected NEXT_PUBLIC_API_URL in build secrets")
	}
	if _, ok := result.BuildArgs["NEXT_PUBLIC_API_URL"]; ok {
		t.Fatalf("did not expect NEXT_PUBLIC_API_URL in build args")
	}
}

func TestResolve_OverrideCanForceSecretOnDockerfileArg(t *testing.T) {
	dir := t.TempDir()
	dockerfilePath := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte("FROM scratch\nARG API_TOKEN\n"), 0644); err != nil {
		t.Fatalf("failed to write dockerfile: %v", err)
	}

	result := Resolve(dir, map[string]string{
		"API_TOKEN": "abc123",
	}, map[string]storage.EnvOverride{
		"API_TOKEN": {
			Secret: boolPtr(true),
		},
	})

	entry := findEntry(result.Entries, "API_TOKEN")
	if entry == nil {
		t.Fatalf("expected resolved entry for API_TOKEN")
	}
	if entry.Scope != "build" {
		t.Fatalf("expected API_TOKEN to remain build scope, got %q", entry.Scope)
	}
	if !entry.Secret {
		t.Fatalf("expected override to force secret=true for dockerfile arg")
	}
	if !strings.Contains(entry.Reason, "override-secret") {
		t.Fatalf("expected reason to include override-secret, got %q", entry.Reason)
	}

	if _, ok := result.BuildSecrets["API_TOKEN"]; !ok {
		t.Fatalf("expected API_TOKEN in build secrets")
	}
	if _, ok := result.BuildArgs["API_TOKEN"]; ok {
		t.Fatalf("did not expect API_TOKEN in build args")
	}
}

func TestResolve_WarnsWhenPublicBuildEnvIsReferencedButMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vite.config.ts"), []byte("const value = import.meta.env.VITE_API_URL\n"), 0o644); err != nil {
		t.Fatalf("failed to write vite config: %v", err)
	}

	result := Resolve(dir, nil, nil)
	if len(result.Warnings) == 0 {
		t.Fatalf("expected missing build env warnings")
	}
	if !strings.Contains(result.Warnings[0], "VITE_API_URL") {
		t.Fatalf("expected warning to mention VITE_API_URL, got %#v", result.Warnings)
	}
}

func TestResolve_DoesNotWarnWhenReferencedPublicBuildEnvIsProvided(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "next.config.js"), []byte("process.env.NEXT_PUBLIC_API_URL\n"), 0o644); err != nil {
		t.Fatalf("failed to write next config: %v", err)
	}

	result := Resolve(dir, map[string]string{
		"NEXT_PUBLIC_API_URL": "https://api.example.com",
	}, nil)
	if len(result.Warnings) != 0 {
		t.Fatalf("expected no missing build env warnings, got %#v", result.Warnings)
	}
}

func findEntry(entries []storage.ResolvedEnvVar, key string) *storage.ResolvedEnvVar {
	for i := range entries {
		if entries[i].Key == key {
			return &entries[i]
		}
	}
	return nil
}
