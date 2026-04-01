package autodetect

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxVersionFileSize = 1 << 20 // 1 MiB

var (
	semverishPattern             = regexp.MustCompile(`\d+(?:\.\d+){0,2}`)
	goToolchainPattern           = regexp.MustCompile(`(?m)^\s*toolchain\s+go([0-9][0-9A-Za-z.\-]*)`)
	goDirectivePattern           = regexp.MustCompile(`(?m)^\s*go\s+([0-9][0-9A-Za-z.\-]*)`)
	pyprojectRequiresPattern     = regexp.MustCompile(`(?m)^\s*requires-python\s*=\s*["']([^"']+)["']`)
	pyprojectPoetryPythonPattern = regexp.MustCompile(`(?m)^\s*python\s*=\s*["']([^"']+)["']`)
	pipfilePythonPattern         = regexp.MustCompile(`(?m)^\s*python_(?:full_)?version\s*=\s*["']([^"']+)["']`)
	pomJavaPattern               = regexp.MustCompile(`(?s)<(maven\.compiler\.release|maven\.compiler\.source|maven\.compiler\.target|java\.version)>\s*([^<\s]+)`)
	gradleJavaVersionPattern     = regexp.MustCompile(`JavaVersion\.VERSION_([0-9_]+)`)
	gradleLanguageVersionPattern = regexp.MustCompile(`JavaLanguageVersion\.of\(\s*([0-9.]+)\s*\)`)
	gradleCompatibilityPattern   = regexp.MustCompile(`(?m)^\s*(sourceCompatibility|targetCompatibility)\s*=?\s*['"]?([0-9.]+)`)
	rustToolchainChannelPattern  = regexp.MustCompile(`(?m)^\s*channel\s*=\s*["']([^"']+)["']`)
)

func DetectRuntimeWithContext(repoRoot, appPath string) (string, string) {
	runtime := detectRuntimeByFiles(appPath)
	if runtime == "unknown" && repoRoot != "" && repoRoot != appPath {
		runtime = detectRuntimeByFiles(repoRoot)
	}

	version := detectVersionForRuntime(runtime, repoRoot, appPath)
	if version == "" && runtime != "unknown" {
		version = defaultDetectedVersionForRuntime(runtime)
	}
	if runtime == "unknown" {
		return "unknown", ""
	}
	return runtime, strings.TrimSpace(version)
}

func detectRuntimeByFiles(repoPath string) string {
	if fileExists(filepath.Join(repoPath, "bun.lock")) { // new version of bun is bun.lock
		return "bun"
	}
	if fileExists(filepath.Join(repoPath, "package.json")) {
		return "node"
	}
	if isPythonProject(repoPath) {
		return "python"
	}
	if fileExists(filepath.Join(repoPath, "mix.exs")) {
		return "elixir"
	}
	if fileExists(filepath.Join(repoPath, "go.mod")) {
		return "go"
	}
	if fileExists(filepath.Join(repoPath, "Cargo.toml")) {
		return "rust"
	}
	if fileExists(filepath.Join(repoPath, "composer.json")) {
		return "php"
	}
	if fileExists(filepath.Join(repoPath, "pom.xml")) || fileExists(filepath.Join(repoPath, "build.gradle")) || fileExists(filepath.Join(repoPath, "build.gradle.kts")) {
		return "java"
	}
	if fileExists(filepath.Join(repoPath, "index.html")) {
		return "static"
	}
	return "unknown"
}

func detectVersionForRuntime(runtime, repoRoot, appPath string) string {
	switch runtime {
	case "node":
		return detectNodeVersion(repoRoot, appPath)
	case "bun":
		return detectBunVersion(repoRoot, appPath)
	case "python":
		return detectPythonVersion(repoRoot, appPath)
	case "elixir":
		return detectElixirVersion(repoRoot, appPath)
	case "go":
		return detectGoVersion(repoRoot, appPath)
	case "rust":
		return detectRustVersion(repoRoot, appPath)
	case "php":
		return detectPHPVersion(repoRoot, appPath)
	case "java":
		return detectJavaVersion(repoRoot, appPath)
	case "static":
		return "latest"
	default:
		return ""
	}
}

func detectNodeVersion(repoRoot, appPath string) string {
	for _, base := range versionSearchPaths(appPath, repoRoot) {
		if v := readFirstNonEmptyLine(filepath.Join(base, ".nvmrc")); v != "" {
			return normalizeNodeVersion(v)
		}
		if v := readFirstNonEmptyLine(filepath.Join(base, ".node-version")); v != "" {
			return normalizeNodeVersion(v)
		}
		if v := readToolVersion(filepath.Join(base, ".tool-versions"), "nodejs", "node"); v != "" {
			return normalizeNodeVersion(v)
		}
		if metadata := loadNodePackageJSON(base); metadata != nil && len(metadata.Engines) > 0 {
			if v := strings.TrimSpace(metadata.Engines["node"]); v != "" {
				return normalizeNodeVersion(v)
			}
		}
	}
	return ""
}

func detectBunVersion(repoRoot, appPath string) string {
	for _, base := range versionSearchPaths(appPath, repoRoot) {
		if v := readFirstNonEmptyLine(filepath.Join(base, ".bun-version")); v != "" {
			return normalizeBunVersion(v)
		}
		if v := readToolVersion(filepath.Join(base, ".tool-versions"), "bun"); v != "" {
			return normalizeBunVersion(v)
		}
		if metadata := loadNodePackageJSON(base); metadata != nil && len(metadata.Engines) > 0 {
			if v := strings.TrimSpace(metadata.Engines["bun"]); v != "" {
				return normalizeBunVersion(v)
			}
		}
	}
	return ""
}

func detectGoVersion(repoRoot, appPath string) string {
	for _, base := range versionSearchPaths(appPath, repoRoot) {
		if v := goVersionFromFile(filepath.Join(base, "go.mod")); v != "" {
			return normalizeGoVersion(v)
		}
		if v := goVersionFromFile(filepath.Join(base, "go.work")); v != "" {
			return normalizeGoVersion(v)
		}
		if v := readToolVersion(filepath.Join(base, ".tool-versions"), "golang", "go"); v != "" {
			return normalizeGoVersion(v)
		}
	}
	return ""
}

func detectPythonVersion(repoRoot, appPath string) string {
	for _, base := range versionSearchPaths(appPath, repoRoot) {
		if v := readFirstNonEmptyLine(filepath.Join(base, ".python-version")); v != "" {
			return normalizePythonVersion(v)
		}
		if v := readFirstNonEmptyLine(filepath.Join(base, "runtime.txt")); v != "" {
			return normalizePythonVersion(strings.TrimPrefix(strings.ToLower(v), "python-"))
		}
		if v := readToolVersion(filepath.Join(base, ".tool-versions"), "python"); v != "" {
			return normalizePythonVersion(v)
		}
		if v := pythonVersionFromPyproject(filepath.Join(base, "pyproject.toml")); v != "" {
			return normalizePythonVersion(v)
		}
		if v := pythonVersionFromPipfile(filepath.Join(base, "Pipfile")); v != "" {
			return normalizePythonVersion(v)
		}
	}
	return ""
}

func detectJavaVersion(repoRoot, appPath string) string {
	for _, base := range versionSearchPaths(appPath, repoRoot) {
		if v := readToolVersion(filepath.Join(base, ".tool-versions"), "java"); v != "" {
			return normalizeJavaVersion(v)
		}
		if v := javaVersionFromPom(filepath.Join(base, "pom.xml")); v != "" {
			return normalizeJavaVersion(v)
		}
		if v := javaVersionFromGradle(filepath.Join(base, "build.gradle")); v != "" {
			return normalizeJavaVersion(v)
		}
		if v := javaVersionFromGradle(filepath.Join(base, "build.gradle.kts")); v != "" {
			return normalizeJavaVersion(v)
		}
	}
	return ""
}

func detectPHPVersion(repoRoot, appPath string) string {
	for _, base := range versionSearchPaths(appPath, repoRoot) {
		if v := phpVersionFromComposer(base); v != "" {
			return normalizePHPVersion(v)
		}
		if v := readToolVersion(filepath.Join(base, ".tool-versions"), "php"); v != "" {
			return normalizePHPVersion(v)
		}
	}
	return ""
}

func phpVersionFromComposer(repoPath string) string {
	metadata := loadComposerJSON(repoPath)
	if metadata == nil {
		return ""
	}
	if metadata.Config != nil {
		if v := strings.TrimSpace(metadata.Config.Platform["php"]); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(metadata.Require["php"]); v != "" {
		return v
	}
	return ""
}

func javaVersionFromPom(path string) string {
	data := readFileLimited(path)
	if data == "" {
		return ""
	}
	match := pomJavaPattern.FindStringSubmatch(data)
	if len(match) != 3 {
		return ""
	}
	return match[2]
}

func javaVersionFromGradle(path string) string {
	data := readFileLimited(path)
	if data == "" {
		return ""
	}
	if match := gradleLanguageVersionPattern.FindStringSubmatch(data); len(match) == 2 {
		return match[1]
	}
	if match := gradleJavaVersionPattern.FindStringSubmatch(data); len(match) == 2 {
		return strings.ReplaceAll(match[1], "_", ".")
	}
	if match := gradleCompatibilityPattern.FindStringSubmatch(data); len(match) == 3 {
		return match[2]
	}
	return ""
}

func pythonVersionFromPyproject(path string) string {
	data := readFileLimited(path)
	if data == "" {
		return ""
	}
	if match := pyprojectRequiresPattern.FindStringSubmatch(data); len(match) == 2 {
		return match[1]
	}
	if match := pyprojectPoetryPythonPattern.FindStringSubmatch(data); len(match) == 2 {
		return match[1]
	}
	return ""
}

func pythonVersionFromPipfile(path string) string {
	data := readFileLimited(path)
	if data == "" {
		return ""
	}
	if match := pipfilePythonPattern.FindStringSubmatch(data); len(match) == 2 {
		return match[1]
	}
	return ""
}

func goVersionFromFile(path string) string {
	data := readFileLimited(path)
	if data == "" {
		return ""
	}
	if match := goToolchainPattern.FindStringSubmatch(data); len(match) == 2 {
		return match[1]
	}
	if match := goDirectivePattern.FindStringSubmatch(data); len(match) == 2 {
		return match[1]
	}
	return ""
}

func defaultDetectedVersionForRuntime(runtime string) string {
	switch strings.TrimSpace(runtime) {
	case "bun":
		return "1.2"
	case "node":
		return "22"
	case "python":
		return "3.9"
	case "elixir":
		return "1.17"
	case "go":
		return "1.18"
	case "rust":
		return "stable"
	case "php":
		return "8.3"
	case "java":
		return "21"
	case "static":
		return "latest"
	default:
		return ""
	}
}

func detectRustVersion(repoRoot, appPath string) string {
	for _, base := range versionSearchPaths(appPath, repoRoot) {
		if v := readRustToolchainFile(filepath.Join(base, "rust-toolchain.toml")); v != "" {
			return v
		}
		if v := readFirstNonEmptyLine(filepath.Join(base, "rust-toolchain")); v != "" {
			return strings.TrimSpace(v)
		}
		if v := readToolVersion(filepath.Join(base, ".tool-versions"), "rust"); v != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func detectElixirVersion(repoRoot, appPath string) string {
	for _, base := range versionSearchPaths(appPath, repoRoot) {
		if v := readFirstNonEmptyLine(filepath.Join(base, ".elixir-version")); v != "" {
			return strings.TrimSpace(v)
		}
		if v := readToolVersion(filepath.Join(base, ".tool-versions"), "elixir"); v != "" {
			return strings.TrimSpace(v)
		}
		if v := elixirVersionFromMixExs(filepath.Join(base, "mix.exs")); v != "" {
			return v
		}
	}
	return ""
}

func elixirVersionFromMixExs(path string) string {
	data := readFileLimited(path)
	if data == "" {
		return ""
	}

	re := regexp.MustCompile(`(?m)^\s*elixir:\s*["']([^"']+)["']`)
	if match := re.FindStringSubmatch(data); len(match) == 2 {
		if v := semverishPattern.FindString(match[1]); v != "" {
			return v
		}
	}
	return ""
}

func readRustToolchainFile(path string) string {
	data := readFileLimited(path)
	if data == "" {
		return ""
	}
	if match := rustToolchainChannelPattern.FindStringSubmatch(data); len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func versionSearchPaths(appPath, repoRoot string) []string {
	paths := make([]string, 0, 2)
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		path = filepath.Clean(path)
		for _, existing := range paths {
			if existing == path {
				return
			}
		}
		paths = append(paths, path)
	}
	add(appPath)
	add(repoRoot)
	return paths
}

func readToolVersion(path string, keys ...string) string {
	data := readFileLimited(path)
	if data == "" {
		return ""
	}

	keySet := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(strings.ToLower(key))
		if key != "" {
			keySet[key] = struct{}{}
		}
	}

	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(fields[0]))
		if _, ok := keySet[key]; !ok {
			continue
		}
		return strings.TrimSpace(fields[1])
	}
	return ""
}

func readFirstNonEmptyLine(path string) string {
	data := readFileLimited(path)
	if data == "" {
		return ""
	}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return strings.Trim(line, "\"'")
	}
	return ""
}

func readFileLimited(path string) string {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > maxVersionFileSize {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func normalizeNodeVersion(raw string) string {
	raw = normalizeVersionValue(raw)
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "lts"):
		return "lts"
	case strings.HasPrefix(lower, "current"):
		return "current"
	case strings.HasPrefix(lower, "latest"):
		return "latest"
	case strings.HasPrefix(lower, "stable"):
		return "current"
	}
	if strings.HasPrefix(raw, "v") && len(raw) > 1 && raw[1] >= '0' && raw[1] <= '9' {
		raw = raw[1:]
	}
	if v := extractSemverish(raw); v != "" {
		return v
	}
	return raw
}

func normalizeBunVersion(raw string) string {
	raw = normalizeVersionValue(raw)
	if strings.HasPrefix(raw, "v") && len(raw) > 1 && raw[1] >= '0' && raw[1] <= '9' {
		raw = raw[1:]
	}
	if v := extractSemverish(raw); v != "" {
		return v
	}
	return raw
}

func normalizeGoVersion(raw string) string {
	raw = normalizeVersionValue(raw)
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "go") && len(raw) > 2 {
		raw = raw[2:]
	}
	if strings.HasPrefix(raw, "v") && len(raw) > 1 && raw[1] >= '0' && raw[1] <= '9' {
		raw = raw[1:]
	}
	if v := extractSemverish(raw); v != "" {
		return v
	}
	return raw
}

func normalizePythonVersion(raw string) string {
	raw = normalizeVersionValue(raw)
	raw = strings.TrimPrefix(strings.ToLower(raw), "python-")
	if v := extractSemverish(raw); v != "" {
		return v
	}
	return raw
}

func normalizeJavaVersion(raw string) string {
	raw = normalizeVersionValue(raw)
	raw = strings.ReplaceAll(raw, "_", ".")
	if v := extractSemverish(raw); v != "" {
		return v
	}
	return raw
}

func normalizePHPVersion(raw string) string {
	raw = normalizeVersionValue(raw)
	if v := extractSemverish(raw); v != "" {
		return v
	}
	return raw
}

func normalizeVersionValue(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, "\"'")
	return raw
}

func extractSemverish(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return semverishPattern.FindString(raw)
}
