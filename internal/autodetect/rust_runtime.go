package autodetect

import (
	"fmt"
	"path/filepath"
	"strings"
)

func rustSelectBinaryCommand(binaryName string) string {
	binaryName = strings.TrimSpace(binaryName)
	if binaryName != "" {
		command := fmt.Sprintf(`set -e; for candidate in %q %q; do if [ -f "$candidate" ] && [ -x "$candidate" ]; then cp "$candidate" /app/app; exit 0; fi; done; echo "Rust binary not found: %s"; exit 1`, "target/release/"+binaryName, "target/release/"+strings.ReplaceAll(binaryName, "-", "_"), escapeDoubleQuotes(binaryName))
		return strings.TrimSpace(command)
	}
	command := `set -e; bin=""; for f in target/release/*; do if [ -f "$f" ] && [ -x "$f" ]; then case "$f" in *.d|*.rlib) continue ;; esac; bin="$f"; break; fi; done; if [ -z "$bin" ]; then echo "No runnable binary found"; exit 1; fi; cp "$bin" /app/app`
	return strings.TrimSpace(command)
}

func rustCargoBuildCommand(repoPath string) string {
	if repoPath != "" && fileExists(filepath.Join(repoPath, "Cargo.lock")) {
		return "cargo build --release --locked"
	}
	return "cargo build --release"
}

func configureRustCargoChefPlan(plan *buildPlan, appPath string) {
	plan.BuilderImage = "lukemathwalker/cargo-chef:latest-rust-1"
	plan.InstallCommand = "cargo chef prepare --recipe-path recipe.json"
	plan.BuildCommand = "cargo chef cook --release --recipe-path recipe.json"
	plan.PostBuildCommands = append(plan.PostBuildCommands, rustCargoBuildCommand(appPath), rustSelectBinaryCommand(detectRustBinaryName(appPath)))
	plan.RunCommand = "./app"
}

func defaultRustExposePort(framework string) string {
	switch strings.ToLower(strings.TrimSpace(framework)) {
	case "axum":
		return "3000"
	case "actix-web":
		return "8080"
	case "rocket":
		return "8000"
	default:
		return "8080"
	}
}

func rustRuntimeEnv(framework, exposePort string) map[string]string {
	env := map[string]string{}
	if strings.TrimSpace(exposePort) != "" {
		env["PORT"] = exposePort
	}

	switch strings.ToLower(strings.TrimSpace(framework)) {
	case "axum", "actix-web":
		env["HOST"] = "0.0.0.0"
	case "rocket":
		env["ROCKET_ADDRESS"] = "0.0.0.0"
		if strings.TrimSpace(exposePort) != "" {
			env["ROCKET_PORT"] = exposePort
		}
	}

	if len(env) == 0 {
		return nil
	}
	return env
}

func escapeDoubleQuotes(value string) string {
	return strings.ReplaceAll(value, `"`, `\"`)
}
