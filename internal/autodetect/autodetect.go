package autodetect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hubfly-builder/internal/allowlist"
)

type BuildConfig struct {
	IsAutoBuild        bool     `json:"isAutoBuild"`
	Runtime            string   `json:"runtime"`
	Framework          string   `json:"framework,omitempty"`
	Version            string   `json:"version"`
	InstallCommand     string   `json:"installCommand,omitempty"`
	PrebuildCommand    string   `json:"prebuildCommand"`
	SetupCommands      []string `json:"setupCommands,omitempty"`
	BuildCommand       string   `json:"buildCommand"`
	PostBuildCommands  []string `json:"postBuildCommands,omitempty"`
	RunCommand         string   `json:"runCommand"`
	RuntimeInitCommand string   `json:"runtimeInitCommand,omitempty"`
	ExposePort         string   `json:"exposePort,omitempty"`
	BuildContextDir    string   `json:"buildContextDir,omitempty"`
	AppDir             string   `json:"appDir,omitempty"`
	ValidationWarnings []string `json:"validationWarnings,omitempty"`
	DockerfileContent  []byte   `json:"dockerfileContent"`
}

type nodePackageJSON struct {
	Scripts         map[string]string `json:"scripts"`
	PackageManager  string            `json:"packageManager"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Workspaces      interface{}       `json:"workspaces"`
}

type AutoDetectOptions struct {
	RepoRoot   string
	WorkingDir string
}

func (c *BuildConfig) NormalizePhaseAliases() {
	if strings.TrimSpace(c.InstallCommand) == "" {
		c.InstallCommand = strings.TrimSpace(c.PrebuildCommand)
	}
	if strings.TrimSpace(c.PrebuildCommand) == "" {
		c.PrebuildCommand = strings.TrimSpace(c.InstallCommand)
	}
}

func DetectRuntime(repoPath string) (string, string) {
	if fileExists(filepath.Join(repoPath, "bun.lock")) { //new version of bun is bun.lock
		return "bun", "1.2" // Simplified version detection
	}
	if fileExists(filepath.Join(repoPath, "package.json")) {
		return "node", "22" // Simplified version detection
	}
	if isPythonProject(repoPath) {
		return "python", "3.9" // Simplified version detection
	}
	if fileExists(filepath.Join(repoPath, "go.mod")) {
		return "go", "1.18" // Simplified version detection
	}
	if fileExists(filepath.Join(repoPath, "composer.json")) {
		return "php", "8.3"
	}
	if fileExists(filepath.Join(repoPath, "pom.xml")) || fileExists(filepath.Join(repoPath, "build.gradle")) || fileExists(filepath.Join(repoPath, "build.gradle.kts")) {
		return "java", "17"
	}
	if fileExists(filepath.Join(repoPath, "index.html")) {
		return "static", "latest"
	}
	return "unknown", ""
}

func DetectCommands(runtime string, allowed *allowlist.AllowedCommands) (string, string, string) {
	return detectCommandsWithPath("", runtime, allowed)
}

func detectCommandsWithPath(repoPath string, runtime string, allowed *allowlist.AllowedCommands) (string, string, string) {
	switch runtime {
	case "static":
		return "", "", ""
	case "node":
		return detectNodeCommands(repoPath, allowed)
	case "bun":
		return pickAllowed("bun install", allowed.Prebuild),
			pickAllowed("bun run build", allowed.Build),
			pickAllowed("bun run start", allowed.Run)
	case "python":
		return detectPythonCommands(repoPath, allowed)
	case "go":
		return detectGoCommands(repoPath, allowed)
	case "php":
		return detectPHPCommands(repoPath, allowed)
	case "java":
		return detectJavaCommands(repoPath, allowed)
	}
	return "", "", ""
}

func detectGoCommands(repoPath string, allowed *allowlist.AllowedCommands) (string, string, string) {
	prebuildCandidates := []string{"go mod download"}
	if repoPath != "" && fileExists(filepath.Join(repoPath, "go.work")) {
		prebuildCandidates = append([]string{"go work sync"}, prebuildCandidates...)
	}

	entrypoint := detectGoEntrypoint(repoPath)
	buildCandidates := []string{"go build ./..."}
	runCandidates := []string{"go run .", "go run main.go"}

	switch {
	case entrypoint == ".":
		buildCandidates = append([]string{"go build -o app ."}, buildCandidates...)
		runCandidates = append([]string{"./app"}, runCandidates...)
	case strings.HasPrefix(entrypoint, "./cmd/"):
		buildCandidates = append([]string{"go build -o app " + entrypoint}, buildCandidates...)
		runCandidates = append([]string{"./app", "go run " + entrypoint}, runCandidates...)
	case strings.HasPrefix(entrypoint, "./"):
		buildCandidates = append([]string{"go build -o app " + entrypoint}, buildCandidates...)
		runCandidates = append([]string{"./app", "go run " + entrypoint}, runCandidates...)
	}

	return pickFirstAllowed(prebuildCandidates, allowed.Prebuild),
		pickFirstAllowed(buildCandidates, allowed.Build),
		pickFirstAllowed(runCandidates, allowed.Run)
}

func detectGoEntrypoint(repoPath string) string {
	if repoPath == "" {
		return ""
	}

	entrypoints := discoverGoMainEntrypoints(repoPath)
	if len(entrypoints) == 0 {
		return ""
	}

	// Prefer conventional cmd/ binaries first, then root, then any other app dir.
	for _, entrypoint := range entrypoints {
		if strings.HasPrefix(entrypoint, "./cmd/") {
			return entrypoint
		}
	}
	for _, entrypoint := range entrypoints {
		if entrypoint == "." {
			return entrypoint
		}
	}
	return entrypoints[0]
}

func discoverGoMainEntrypoints(repoPath string) []string {
	excludedDirs := map[string]struct{}{
		".git":         {},
		"vendor":       {},
		"node_modules": {},
	}

	seen := make(map[string]struct{})
	var entrypoints []string

	err := filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if d.IsDir() {
			name := strings.TrimSpace(d.Name())
			if path != repoPath && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if _, excluded := excludedDirs[name]; excluded {
				return filepath.SkipDir
			}
			return nil
		}

		name := strings.TrimSpace(d.Name())
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}

		text := string(content)
		if !strings.Contains(text, "package main") || !strings.Contains(text, "func main(") {
			return nil
		}

		dir := filepath.Dir(path)
		rel, relErr := filepath.Rel(repoPath, dir)
		if relErr != nil {
			return nil
		}

		entrypoint := "."
		if rel != "." {
			entrypoint = "./" + filepath.ToSlash(rel)
		}

		if _, exists := seen[entrypoint]; exists {
			return nil
		}
		seen[entrypoint] = struct{}{}
		entrypoints = append(entrypoints, entrypoint)
		return nil
	})
	if err != nil {
		return nil
	}

	sort.Slice(entrypoints, func(i, j int) bool {
		return strings.ToLower(entrypoints[i]) < strings.ToLower(entrypoints[j])
	})
	return entrypoints
}

func detectPythonCommands(repoPath string, allowed *allowlist.AllowedCommands) (string, string, string) {
	prebuildCandidates := pythonPrebuildCandidates(repoPath)
	buildCandidates := pythonBuildCandidates(repoPath)
	runCandidates := pythonRunCandidates(repoPath)

	return pickFirstAllowed(prebuildCandidates, allowed.Prebuild),
		pickFirstAllowed(buildCandidates, allowed.Build),
		pickFirstAllowed(runCandidates, allowed.Run)
}

func pythonPrebuildCandidates(repoPath string) []string {
	candidates := make([]string, 0, 3)
	addCandidate := func(cmd string) {
		if cmd == "" {
			return
		}
		for _, existing := range candidates {
			if existing == cmd {
				return
			}
		}
		candidates = append(candidates, cmd)
	}

	if repoPath != "" && fileExists(filepath.Join(repoPath, "requirements.txt")) {
		addCandidate("pip install -r requirements.txt")
	}
	if repoPath != "" && fileExists(filepath.Join(repoPath, "Pipfile")) {
		addCandidate("pip install pipenv && pipenv install --system --deploy")
	}
	if repoPath != "" && (fileExists(filepath.Join(repoPath, "pyproject.toml")) || fileExists(filepath.Join(repoPath, "setup.py"))) {
		addCandidate("pip install .")
	}

	return candidates
}

func pythonBuildCandidates(repoPath string) []string {
	if repoPath != "" && fileExists(filepath.Join(repoPath, "setup.py")) {
		return []string{"python setup.py build"}
	}
	return nil
}

func pythonRunCandidates(repoPath string) []string {
	candidates := make([]string, 0, 10)
	addCandidate := func(cmd string) {
		if cmd == "" {
			return
		}
		for _, existing := range candidates {
			if existing == cmd {
				return
			}
		}
		candidates = append(candidates, cmd)
	}

	if repoPath != "" && fileExists(filepath.Join(repoPath, "manage.py")) {
		addCandidate("python manage.py runserver 0.0.0.0:${PORT:-8000}")
		addCandidate("python manage.py")
	}

	if cmd := detectASGIApplicationRunCommand(repoPath); cmd != "" {
		addCandidate(cmd)
	}
	if cmd := detectFastAPIRunCommand(repoPath); cmd != "" {
		addCandidate(cmd)
	}
	if cmd := detectGunicornRunCommand(repoPath); cmd != "" {
		addCandidate(cmd)
	}

	for _, file := range []string{"main.py", "app.py", "server.py", "run.py"} {
		if repoPath != "" && fileExists(filepath.Join(repoPath, file)) {
			addCandidate("python " + file)
		}
	}

	if module := detectPythonMainModule(repoPath); module != "" {
		addCandidate("python -m " + module)
	}

	if len(candidates) == 0 {
		addCandidate("python main.py")
	}

	return candidates
}

func detectFastAPIRunCommand(repoPath string) string {
	if repoPath == "" {
		return ""
	}

	candidates := []string{
		"main.py",
		"app.py",
		"server.py",
		"src/main.py",
		"src/app.py",
		"src/server.py",
	}

	for _, path := range candidates {
		fullPath := filepath.Join(repoPath, path)
		if !fileExists(fullPath) {
			continue
		}

		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		text := string(content)
		lower := strings.ToLower(text)
		if !strings.Contains(lower, "fastapi") && !strings.Contains(lower, "starlette") {
			continue
		}

		appName := detectAssignedName(text, "FastAPI(", "")
		if appName == "" {
			appName = detectAssignedName(text, "Starlette(", "app")
		}
		if appName == "" {
			appName = "app"
		}

		module := pythonModuleFromPath(path)
		if module == "" {
			continue
		}
		return fmt.Sprintf("uvicorn %s:%s --host 0.0.0.0 --port ${PORT:-8000}", module, appName)
	}

	return ""
}

func detectASGIApplicationRunCommand(repoPath string) string {
	if repoPath == "" {
		return ""
	}

	candidates := []string{
		"asgi.py",
		"src/asgi.py",
	}

	for _, path := range candidates {
		fullPath := filepath.Join(repoPath, path)
		if !fileExists(fullPath) {
			continue
		}

		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		text := string(content)
		lower := strings.ToLower(text)
		if !strings.Contains(lower, "application") {
			continue
		}

		module := pythonModuleFromPath(path)
		if module == "" {
			continue
		}
		return fmt.Sprintf("uvicorn %s:application --host 0.0.0.0 --port ${PORT:-8000}", module)
	}

	return ""
}

func detectGunicornRunCommand(repoPath string) string {
	if repoPath == "" {
		return ""
	}

	candidates := []string{
		"wsgi.py",
		"app.py",
		"main.py",
		"server.py",
		"src/wsgi.py",
		"src/app.py",
	}

	for _, path := range candidates {
		fullPath := filepath.Join(repoPath, path)
		if !fileExists(fullPath) {
			continue
		}

		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		text := string(content)
		lower := strings.ToLower(text)
		module := pythonModuleFromPath(path)
		if module == "" {
			continue
		}

		if strings.Contains(lower, "flask") {
			appName := detectAssignedName(text, "Flask(", "app")
			if appName == "" {
				appName = "app"
			}
			return fmt.Sprintf("gunicorn %s:%s --bind 0.0.0.0:${PORT:-8000}", module, appName)
		}

		isWSGIModule := module == "wsgi" || strings.HasSuffix(module, ".wsgi")
		if strings.Contains(lower, "application") && (isWSGIModule || strings.Contains(lower, "wsgi") || strings.Contains(lower, "django.core.wsgi")) {
			return fmt.Sprintf("gunicorn %s:application --bind 0.0.0.0:${PORT:-8000}", module)
		}
	}

	return ""
}

func detectAssignedName(source, constructor, fallback string) string {
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "=") || !strings.Contains(trimmed, constructor) {
			continue
		}
		left := strings.TrimSpace(strings.SplitN(trimmed, "=", 2)[0])
		if isPythonIdentifier(left) {
			return left
		}
	}
	return fallback
}

func detectPythonMainModule(repoPath string) string {
	if repoPath == "" {
		return ""
	}

	entries, err := os.ReadDir(repoPath)
	if err != nil {
		return ""
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})

	for _, name := range names {
		if !isPythonIdentifier(name) {
			continue
		}
		if fileExists(filepath.Join(repoPath, name, "__main__.py")) {
			return name
		}
	}

	if !fileExists(filepath.Join(repoPath, "pyproject.toml")) && !fileExists(filepath.Join(repoPath, "setup.py")) {
		return ""
	}

	srcDir := filepath.Join(repoPath, "src")
	srcEntries, err := os.ReadDir(srcDir)
	if err != nil {
		return ""
	}
	srcNames := make([]string, 0, len(srcEntries))
	for _, entry := range srcEntries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		srcNames = append(srcNames, name)
	}
	sort.Slice(srcNames, func(i, j int) bool {
		return strings.ToLower(srcNames[i]) < strings.ToLower(srcNames[j])
	})

	for _, name := range srcNames {
		if !isPythonIdentifier(name) {
			continue
		}
		if fileExists(filepath.Join(srcDir, name, "__main__.py")) {
			return name
		}
	}

	return ""
}

func isPythonIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}

	for i, r := range value {
		switch {
		case r == '_':
			continue
		case r >= 'a' && r <= 'z':
			continue
		case r >= 'A' && r <= 'Z':
			continue
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
			continue
		default:
			return false
		}
	}

	return true
}

func pythonModuleFromPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimSuffix(path, ".py")
	path = strings.Trim(path, "/")
	if path == "" {
		return ""
	}

	path = strings.ReplaceAll(path, "\\", "/")
	parts := strings.Split(path, "/")
	moduleParts := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !isPythonIdentifier(part) {
			return ""
		}
		moduleParts = append(moduleParts, part)
	}

	return strings.Join(moduleParts, ".")
}

func detectNodeCommands(repoPath string, allowed *allowlist.AllowedCommands) (string, string, string) {
	metadata := loadNodePackageJSON(repoPath)
	packageManager := detectNodePackageManager(repoPath, metadata)
	scripts := map[string]string{}
	if metadata != nil && metadata.Scripts != nil {
		scripts = metadata.Scripts
	}

	prebuildCandidates := nodePrebuildCandidates(repoPath, packageManager)
	buildCandidates := nodeBuildCandidates(packageManager, scripts)
	runCandidates := nodeRunCandidates(repoPath, packageManager, scripts)

	return pickFirstAllowed(prebuildCandidates, allowed.Prebuild),
		pickFirstAllowed(buildCandidates, allowed.Build),
		pickFirstAllowed(runCandidates, allowed.Run)
}

func loadNodePackageJSON(repoPath string) *nodePackageJSON {
	if repoPath == "" {
		return nil
	}

	path := filepath.Join(repoPath, "package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var parsed nodePackageJSON
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	return &parsed
}

func detectNodePackageManager(repoPath string, metadata *nodePackageJSON) string {
	if metadata != nil {
		pm := strings.ToLower(strings.TrimSpace(metadata.PackageManager))
		switch {
		case strings.HasPrefix(pm, "pnpm@"), pm == "pnpm":
			return "pnpm"
		case strings.HasPrefix(pm, "yarn@"), pm == "yarn":
			return "yarn"
		case strings.HasPrefix(pm, "npm@"), pm == "npm":
			return "npm"
		}
	}

	if repoPath != "" {
		switch {
		case fileExists(filepath.Join(repoPath, "pnpm-lock.yaml")):
			return "pnpm"
		case fileExists(filepath.Join(repoPath, "yarn.lock")):
			return "yarn"
		case fileExists(filepath.Join(repoPath, "package-lock.json")), fileExists(filepath.Join(repoPath, "npm-shrinkwrap.json")):
			return "npm"
		}
	}

	return "npm"
}

func nodePrebuildCandidates(repoPath, packageManager string) []string {
	switch packageManager {
	case "pnpm":
		return []string{"pnpm install"}
	case "yarn":
		return []string{"yarn install"}
	default:
		if repoPath != "" && (fileExists(filepath.Join(repoPath, "package-lock.json")) || fileExists(filepath.Join(repoPath, "npm-shrinkwrap.json"))) {
			return []string{"npm ci", "npm install"}
		}
		return []string{"npm install", "npm ci"}
	}
}

func nodeBuildCandidates(packageManager string, scripts map[string]string) []string {
	scriptNames := make([]string, 0, 4)
	added := make(map[string]struct{})

	addScript := func(name string) {
		if !hasNodeScript(scripts, name) {
			return
		}
		if _, exists := added[name]; exists {
			return
		}
		added[name] = struct{}{}
		scriptNames = append(scriptNames, name)
	}

	addScript("build")
	for _, name := range sortedScriptNames(scripts) {
		lowerName := strings.ToLower(name)
		if strings.HasPrefix(lowerName, "build:") || strings.Contains(lowerName, ":build") {
			addScript(name)
		}
	}

	candidates := make([]string, 0, len(scriptNames)*2)
	for _, name := range scriptNames {
		candidates = append(candidates, nodeScriptCandidates(packageManager, name)...)
	}
	return candidates
}

func nodeRunCandidates(repoPath, packageManager string, scripts map[string]string) []string {
	candidates := make([]string, 0, 10)
	addedScripts := make(map[string]struct{})

	addScript := func(name string) {
		if !hasNodeScript(scripts, name) {
			return
		}
		if _, exists := addedScripts[name]; exists {
			return
		}
		addedScripts[name] = struct{}{}
		candidates = append(candidates, nodeScriptCandidates(packageManager, name)...)
	}

	for _, name := range []string{"start", "serve"} {
		addScript(name)
	}

	for _, name := range sortedScriptNames(scripts) {
		lowerName := strings.ToLower(name)
		if strings.Contains(lowerName, "dev") || strings.Contains(lowerName, "preview") {
			continue
		}
		if strings.Contains(lowerName, "start") || strings.Contains(lowerName, "serve") || strings.Contains(lowerName, "prod") {
			addScript(name)
		}
	}

	if len(addedScripts) == 0 {
		for _, name := range sortedScriptNames(scripts) {
			if isNodeUtilityScript(name) {
				continue
			}
			addScript(name)
			break
		}
	}

	if repoPath != "" && fileExists(filepath.Join(repoPath, "server.js")) {
		candidates = append(candidates, "node server.js")
	}
	return candidates
}

func sortedScriptNames(scripts map[string]string) []string {
	if len(scripts) == 0 {
		return nil
	}

	names := make([]string, 0, len(scripts))
	for name := range scripts {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	return names
}

func nodeScriptCandidates(packageManager, script string) []string {
	switch packageManager {
	case "pnpm":
		return []string{"pnpm run " + script, "pnpm " + script}
	case "yarn":
		return []string{"yarn " + script, "yarn run " + script}
	default:
		if script == "start" {
			return []string{"npm start", "npm run start"}
		}
		return []string{"npm run " + script}
	}
}

func isNodeUtilityScript(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))

	switch {
	case name == "build",
		name == "test",
		name == "lint",
		name == "typecheck",
		name == "format",
		name == "clean",
		name == "prepare",
		name == "preinstall",
		name == "postinstall",
		name == "install":
		return true
	case strings.HasPrefix(name, "build:"),
		strings.HasPrefix(name, "test:"),
		strings.HasPrefix(name, "lint:"),
		strings.HasPrefix(name, "typecheck:"),
		strings.HasPrefix(name, "format:"),
		strings.HasPrefix(name, "clean:"):
		return true
	default:
		return false
	}
}

func hasNodeScript(scripts map[string]string, key string) bool {
	if len(scripts) == 0 {
		return false
	}

	value, ok := scripts[key]
	return ok && strings.TrimSpace(value) != ""
}

func detectJavaCommands(repoPath string, allowed *allowlist.AllowedCommands) (string, string, string) {
	isGradle := repoPath != "" && (fileExists(filepath.Join(repoPath, "build.gradle")) || fileExists(filepath.Join(repoPath, "build.gradle.kts")))
	hasMavenWrapper := repoPath != "" && fileExists(filepath.Join(repoPath, "mvnw"))
	hasGradleWrapper := repoPath != "" && fileExists(filepath.Join(repoPath, "gradlew"))

	if isGradle {
		prebuildCandidates := []string{"gradle dependencies"}
		buildCandidates := []string{"gradle build -x test"}
		if hasGradleWrapper {
			prebuildCandidates = []string{"./gradlew dependencies", "gradle dependencies"}
			buildCandidates = []string{"./gradlew build -x test", "gradle build -x test"}
		}

		return pickFirstAllowed(prebuildCandidates, allowed.Prebuild),
			pickFirstAllowed(buildCandidates, allowed.Build),
			pickFirstAllowed([]string{"java -jar build/libs/*.jar"}, allowed.Run)
	}

	prebuildCandidates := []string{"mvn clean"}
	buildCandidates := []string{"mvn install -DskipTests"}
	if hasMavenWrapper {
		prebuildCandidates = []string{"./mvnw clean", "mvn clean"}
		buildCandidates = []string{"./mvnw install -DskipTests", "mvn install -DskipTests"}
	}

	return pickFirstAllowed(prebuildCandidates, allowed.Prebuild),
		pickFirstAllowed(buildCandidates, allowed.Build),
		pickFirstAllowed([]string{"java -jar target/*.jar"}, allowed.Run)
}

func pickAllowed(preferred string, allowed []string) string {
	if allowlist.IsCommandAllowed(preferred, allowed) {
		return preferred
	}
	if len(allowed) > 0 {
		return allowed[0]
	}
	return ""
}

func pickFirstAllowed(candidates []string, allowed []string) string {
	for _, candidate := range candidates {
		if allowlist.IsCommandAllowed(candidate, allowed) {
			return candidate
		}
	}
	return ""
}

func AutoDetectBuildConfig(repoPath string, allowed *allowlist.AllowedCommands) (BuildConfig, error) {
	return AutoDetectBuildConfigWithEnvOptions(AutoDetectOptions{RepoRoot: repoPath}, allowed, nil, nil)
}

func AutoDetectBuildConfigWithOptions(opts AutoDetectOptions, allowed *allowlist.AllowedCommands) (BuildConfig, error) {
	return AutoDetectBuildConfigWithEnvOptions(opts, allowed, nil, nil)
}

func AutoDetectBuildConfigWithEnvOptions(opts AutoDetectOptions, allowed *allowlist.AllowedCommands, buildArgKeys, secretBuildKeys []string) (BuildConfig, error) {
	plan, err := detectBuildPlan(opts, allowed)
	if err != nil {
		return BuildConfig{}, err
	}
	return buildConfigFromPlan(plan, true, buildArgKeys, secretBuildKeys)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	out := make([]string, len(values))
	copy(out, values)
	return out
}

func isPythonProject(repoPath string) bool {
	if repoPath == "" {
		return false
	}

	return fileExists(filepath.Join(repoPath, "requirements.txt")) ||
		fileExists(filepath.Join(repoPath, "pyproject.toml")) ||
		fileExists(filepath.Join(repoPath, "setup.py")) ||
		fileExists(filepath.Join(repoPath, "Pipfile"))
}
