package autodetect

import (
	"path/filepath"
	"regexp"
	"strings"
)

var rustAxumDepPattern = regexp.MustCompile(`(?m)^\s*axum\s*=`) // axum = "..." or axum = { ... }
var rustActixWebDepPattern = regexp.MustCompile(`(?m)^\s*actix-web\s*=`) // actix-web = "..." or actix-web = { ... }
var rustRocketDepPattern = regexp.MustCompile(`(?m)^\s*rocket\s*=`) // rocket = "..." or rocket = { ... }
var rustDefaultRunPattern = regexp.MustCompile(`(?ms)\[package\].*?^\s*default-run\s*=\s*"([^"]+)"`)
var rustPackageNamePattern = regexp.MustCompile(`(?ms)\[package\].*?^\s*name\s*=\s*"([^"]+)"`)
var rustBinNamePattern = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)

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
	if rustActixWebDepPattern.MatchString(data) {
		return "actix-web"
	}
	if rustRocketDepPattern.MatchString(data) {
		return "rocket"
	}
	if strings.Contains(data, "axum::") {
		return "axum"
	}
	if strings.Contains(data, "actix_web::") {
		return "actix-web"
	}
	if strings.Contains(data, "rocket::") {
		return "rocket"
	}
	return ""
}

func detectRustBinaryName(repoPath string) string {
	if repoPath == "" {
		return ""
	}
	data := readFileLimited(filepath.Join(repoPath, "Cargo.toml"))
	if data == "" {
		return ""
	}
	if matches := rustDefaultRunPattern.FindStringSubmatch(data); len(matches) == 2 {
		return strings.TrimSpace(matches[1])
	}

	binNames := make([]string, 0, 1)
	parts := strings.Split(data, "[[bin]]")
	for _, block := range parts[1:] {
		if matches := rustBinNamePattern.FindStringSubmatch(block); len(matches) == 2 {
			name := strings.TrimSpace(matches[1])
			if name != "" {
				binNames = append(binNames, name)
			}
		}
	}
	if len(binNames) == 1 {
		return binNames[0]
	}

	if matches := rustPackageNamePattern.FindStringSubmatch(data); len(matches) == 2 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}
