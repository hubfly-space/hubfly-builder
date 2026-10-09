package offline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectNestedProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project with spaces")
	app := filepath.Join(root, "apps", "web")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "index.html"), []byte("<h1>Hello</h1>\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"auto", "manual"} {
		t.Run(mode, func(t *testing.T) {
			cfg := configFile{Build: configBuild{
				Mode: mode, WorkingDir: filepath.Join("apps", "web"),
				ContextDir: filepath.Join("apps", "web"), Runtime: "static",
			}}
			output, err := inspect(root, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if output.BuildConfig.Runtime != "static" || output.BuildConfig.AppDir != "apps/web" || output.BuildConfig.BuildContextDir != "apps/web" {
				t.Fatalf("unexpected inspection: %+v", output.BuildConfig)
			}
			if !strings.Contains(output.Dockerfile, "FROM nginx:") || strings.Contains(output.Dockerfile, root) || strings.Contains(output.Dockerfile, `apps\web`) {
				t.Fatalf("expected a portable Linux Dockerfile, got %s", output.Dockerfile)
			}
		})
	}
}

func TestInspectBuildEnvironment(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := inspect(root, configFile{Env: []configEnvVar{
		{Name: "PUBLIC_URL", Value: "https://example.test", Scope: "build"},
		{Name: "BUILD_TOKEN", Value: "private-build-value", Secret: true, Scope: "both"},
		{Name: "RUNTIME_TOKEN", Value: "private-runtime-value", Scope: "runtime"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.BuildArgKeys) != 1 || output.BuildArgKeys[0] != "PUBLIC_URL" || len(output.BuildSecretKeys) != 1 || output.BuildSecretKeys[0] != "BUILD_TOKEN" {
		t.Fatalf("unexpected environment plan: %+v", output)
	}
	if strings.Contains(output.Dockerfile, "private-") {
		t.Fatal("environment values were embedded in the Dockerfile")
	}
}

func TestInspectRejectsEscapingPaths(t *testing.T) {
	for _, dir := range []string{"../outside", filepath.Join("..", "outside"), t.TempDir()} {
		if _, err := inspect(t.TempDir(), configFile{Build: configBuild{WorkingDir: dir}}); err == nil {
			t.Fatalf("accepted working directory outside the project: %q", dir)
		}
	}
}
