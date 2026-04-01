package autodetect

import (
	"path/filepath"
	"strings"
)

func rustSelectBinaryCommand() string {
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
	plan.PostBuildCommands = append(plan.PostBuildCommands, rustCargoBuildCommand(appPath), rustSelectBinaryCommand())
	plan.RunCommand = "./app"
}
