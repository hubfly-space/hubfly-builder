package autodetect

import (
	"path/filepath"
	"regexp"
	"strings"
)

var rustAxumDepPattern = regexp.MustCompile(`(?m)^\s*axum\s*=`) // axum = "..." or axum = { ... }
var rustRocketDepPattern = regexp.MustCompile(`(?m)^\s*rocket\s*=`) // rocket = "..." or rocket = { ... }

func detectRustFramework(repoPath string) string {
	if repoPath == "" {
		return ""
	}
	data := readFileLimited(filepath.Join(repoPath, "Cargo.toml"))
	if data == "" {
		return ""
	}
	if rustAxumDepPattern.MatchString(data) {
		return "axum"
	}
	if rustRocketDepPattern.MatchString(data) {
		return "rocket"
	}
	if strings.Contains(data, "axum::") {
		return "axum"
	}
	if strings.Contains(data, "rocket::") {
		return "rocket"
	}
	return ""
}
