package autodetect

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"hubfly-builder/internal/allowlist"
)

type buildPlan struct {
	Runtime            string
	RuntimeFlavor      string
	Framework          string
	Version            string
	InstallCommand     string
	SetupCommands      []string
	BuildCommand       string
	PostBuildCommands  []string
	RunCommand         string
	RuntimeInitCommand string
	ExposePort         string
	BuildContextDir    string
	AppDir             string
	ValidationWarnings []string

	BuilderImage      string
	RuntimeImage      string
	BootstrapCommands []string
	RuntimeEnv        map[string]string
	AptPackages       []string
	DocumentRoot      string
	StaticOutputDir   string
	UseStaticRuntime  bool
	appWorkDir        string
}

type jsProjectContext struct {
	RepoRoot           string
	AppDir             string
	AppPath            string
	Runtime            string
	Version            string
	BuildContextDir    string
	BuildContextPath   string
	PackageManager     string
	PackageManagerSpec string
	IsWorkspace        bool
	AppMetadata        *nodePackageJSON
	RootMetadata       *nodePackageJSON
	appWorkDir         string
}

func detectBuildPlan(opts AutoDetectOptions, allowed *allowlist.AllowedCommands) (buildPlan, error) {
	repoRoot := strings.TrimSpace(opts.RepoRoot)
	if repoRoot == "" {
		return buildPlan{}, fmt.Errorf("repository root is required")
	}

	appDir, err := normalizeRelativeDir(opts.WorkingDir)
	if err != nil {
		return buildPlan{}, err
	}

	appPath := repoRoot
	if appDir != "." {
		appPath = filepath.Join(repoRoot, filepath.FromSlash(appDir))
	}

	runtime, version := DetectRuntimeWithContext(repoRoot, appPath)
	switch runtime {
	case "node", "bun":
		plan, err := detectJavaScriptBuildPlan(repoRoot, appDir, appPath, runtime, version)
		if err != nil {
			return buildPlan{}, err
		}
		if err := validateBuildPlanCommands(plan, allowed); err != nil {
			return buildPlan{}, err
		}
		return plan, nil
	case "python":
		plan, err := detectPythonBuildPlan(appDir, appPath, version, allowed)
		if err != nil {
			return buildPlan{}, err
		}
		if err := validateBuildPlanCommands(plan, allowed); err != nil {
			return buildPlan{}, err
		}
		return plan, nil
	case "php":
		plan, err := detectPHPBuildPlan(appDir, appPath, version, allowed)
		if err != nil {
			return buildPlan{}, err
		}
		if err := validateBuildPlanCommands(plan, allowed); err != nil {
			return buildPlan{}, err
		}
		return plan, nil
	case "static":
		return buildPlan{
			Runtime:          "static",
			Framework:        "static-site",
			Version:          version,
			ExposePort:       "8080",
			BuildContextDir:  appDir,
			AppDir:           appDir,
			BuilderImage:     "",
			RuntimeImage:     "nginx:alpine",
			StaticOutputDir:  ".",
			UseStaticRuntime: true,
		}, nil
	default:
		prebuild, build, run := detectCommandsWithPath(appPath, runtime, allowed)
		plan, err := defaultBuildPlan(runtime, version, prebuild, build, run)
		if err != nil {
			return buildPlan{}, err
		}
		plan.BuildContextDir = appDir
		plan.AppDir = appDir
		plan.ExposePort = inferExposePort(defaultExposePort(runtime), run)
		if err := validateBuildPlanCommands(plan, allowed); err != nil {
			return buildPlan{}, err
		}
		return plan, nil
	}
}

func detectJavaScriptBuildPlan(repoRoot, appDir, appPath, runtime, version string) (buildPlan, error) {
	ctx := newJSProjectContext(repoRoot, appDir, appPath, runtime, version)
	framework := detectJSFramework(ctx)
	buildScript := selectJSBuildScript(ctx.AppMetadata)
	runScript := selectJSRunScript(ctx.AppMetadata)

	plan := buildPlan{
		Runtime:         runtime,
		Framework:       framework,
		Version:         version,
		InstallCommand:  detectJavaScriptInstallCommand(ctx),
		BuildCommand:    prefixCommand(ctx.appWorkDir, packageManagerScriptCommand(ctx.PackageManager, buildScript)),
		BuildContextDir: ctx.BuildContextDir,
		AppDir:          ctx.AppDir,
		BuilderImage:    selectJavaScriptBuilderImage(ctx.Runtime, ctx.Version),
		appWorkDir:      ctx.appWorkDir,
	}
	plan.ExposePort = inferJavaScriptExposePort(ctx, framework, runScript)
	plan.RuntimeEnv = map[string]string{"HOST": "0.0.0.0", "PORT": plan.ExposePort}

	if ctx.Runtime != "bun" {
		plan.RuntimeEnv["NODE_ENV"] = "production"
	}

	if packages := detectJavaScriptSystemPackages(ctx.AppMetadata); len(packages) > 0 {
		plan.AptPackages = packages
	}
	if shouldAutoInstallNextSharp(ctx, framework) {
		plan.AptPackages = appendJavaScriptSystemPackages(plan.AptPackages, "ca-certificates", "git", "openssl", "python3", "make", "g++", "pkg-config")
		plan.ValidationWarnings = appendUniqueString(plan.ValidationWarnings, "Next.js app does not declare sharp; builder will install it for production image optimization")
	}
	plan.BootstrapCommands = detectJavaScriptBootstrapCommands(ctx)
	plan.SetupCommands = detectJavaScriptSetupCommands(ctx)
	if detectJavaScriptPrisma(ctx.AppPath, ctx.BuildContextPath, ctx.AppMetadata) {
		plan.RuntimeInitCommand = prefixCommand(plan.appWorkDir, "if [ \"${PRISMA_RUN_MIGRATIONS:-0}\" = \"1\" ]; then "+jsExecCommand(ctx.Runtime, "prisma migrate deploy")+"; fi")
	}
	if detectJavaScriptPlaywright(ctx.AppMetadata) {
		plan.SetupCommands = appendUniqueString(plan.SetupCommands, prefixCommand(plan.appWorkDir, jsExecCommand(ctx.Runtime, "playwright install chromium")))
	}

	if shouldUseStaticRuntime(ctx, framework, buildScript, runScript) {
		if strings.TrimSpace(plan.BuildCommand) == "" {
			return buildPlan{}, fmt.Errorf("no production build command detected for static frontend")
		}
		plan.Runtime = "static"
		plan.Framework = normalizeStaticFramework(framework)
		plan.ExposePort = "8080"
		plan.RuntimeEnv = nil
		plan.RunCommand = ""
		plan.RuntimeInitCommand = ""
		plan.RuntimeImage = "nginx:alpine"
		plan.StaticOutputDir = detectStaticOutputDir(ctx, framework)
		plan.UseStaticRuntime = true
		return plan, nil
	}

	if runtime == "node" && framework == "next" {
		plan.RunCommand = prefixCommand(plan.appWorkDir, fmt.Sprintf("./node_modules/.bin/next start --hostname 0.0.0.0 --port ${PORT:-%s}", plan.ExposePort))
	} else if runtime == "node" && framework == "nuxt" {
		plan.RunCommand = prefixCommand(plan.appWorkDir, fmt.Sprintf("HOST=0.0.0.0 PORT=${PORT:-%s} node .output/server/index.mjs", plan.ExposePort))
	} else {
		plan.RunCommand = detectJavaScriptRunCommand(ctx, runScript)
	}

	if strings.TrimSpace(plan.RunCommand) == "" {
		return buildPlan{}, fmt.Errorf("no production run command detected")
	}

	return plan, nil
}

func inferJavaScriptExposePort(ctx jsProjectContext, framework, runScript string) string {
	defaultPort := "3000"
	switch framework {
	case "vite":
		defaultPort = "5173"
	case "angular":
		defaultPort = "4200"
	}

	sources := make([]string, 0, 8)
	if runScript != "" && ctx.AppMetadata != nil {
		sources = append(sources, ctx.AppMetadata.Scripts[runScript])
	}
	if ctx.AppMetadata != nil {
		for _, scriptName := range []string{"start", "serve", "prod", "start:prod", "start:production"} {
			if body := strings.TrimSpace(ctx.AppMetadata.Scripts[scriptName]); body != "" {
				sources = append(sources, body)
			}
		}
	}
	for _, fileName := range []string{"vite.config.ts", "vite.config.js", "vite.config.mjs", "vite.config.cjs"} {
		path := filepath.Join(ctx.AppPath, fileName)
		if data, err := os.ReadFile(path); err == nil {
			sources = append(sources, string(data))
		}
	}
	return inferExposePort(defaultPort, sources...)
}

func newJSProjectContext(repoRoot, appDir, appPath, runtime, version string) jsProjectContext {
	rootMeta := loadNodePackageJSON(repoRoot)
	appMeta := rootMeta
	if appPath != repoRoot {
		appMeta = loadNodePackageJSON(appPath)
	}

	isWorkspace := detectJavaScriptWorkspace(repoRoot, appDir, rootMeta)
	buildContextDir := appDir
	if isWorkspace || buildContextDir == "" {
		buildContextDir = "."
	}

	buildContextPath := repoRoot
	if buildContextDir != "." {
		buildContextPath = filepath.Join(repoRoot, filepath.FromSlash(buildContextDir))
	}

	appWorkDir := "."
	if buildContextDir == "." {
		appWorkDir = appDir
	}

	packageManager, spec := detectJavaScriptPackageManager(repoRoot, appPath, runtime, isWorkspace, rootMeta, appMeta)
	return jsProjectContext{
		RepoRoot:           repoRoot,
		AppDir:             appDir,
		AppPath:            appPath,
		Runtime:            runtime,
		Version:            version,
		BuildContextDir:    buildContextDir,
		BuildContextPath:   buildContextPath,
		PackageManager:     packageManager,
		PackageManagerSpec: spec,
		IsWorkspace:        isWorkspace,
		AppMetadata:        appMeta,
		RootMetadata:       rootMeta,
		appWorkDir:         appWorkDir,
	}
}

func normalizeRelativeDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" || dir == "." {
		return ".", nil
	}

	cleaned := filepath.Clean(dir)
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("working directory must be relative")
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("working directory escapes repository root")
	}

	return filepath.ToSlash(cleaned), nil
}

func defaultExposePort(runtime string) string {
	switch runtime {
	case "python":
		return "8000"
	case "go", "java", "php":
		return "8080"
	case "static":
		return "8080"
	default:
		return "3000"
	}
}

func detectJavaScriptWorkspace(repoRoot, appDir string, rootMeta *nodePackageJSON) bool {
	if appDir == "." {
		return false
	}
	if rootMeta != nil && rootMeta.Workspaces != nil {
		return true
	}

	for _, fileName := range []string{"pnpm-workspace.yaml", "turbo.json", "nx.json", "lerna.json"} {
		if fileExists(filepath.Join(repoRoot, fileName)) {
			return true
		}
	}
	return false
}

func detectJavaScriptPackageManager(repoRoot, appPath, runtime string, isWorkspace bool, rootMeta, appMeta *nodePackageJSON) (string, string) {
	if runtime == "bun" {
		return "bun", ""
	}

	installPath := appPath
	installMeta := appMeta
	if isWorkspace {
		installPath = repoRoot
		installMeta = rootMeta
	}

	spec := ""
	for _, meta := range []*nodePackageJSON{installMeta, appMeta, rootMeta} {
		if meta == nil {
			continue
		}
		if strings.TrimSpace(meta.PackageManager) != "" {
			spec = strings.TrimSpace(meta.PackageManager)
			break
		}
	}

	name := detectNodePackageManager(installPath, installMeta)
	if specName, _ := parsePackageManagerSpec(spec); specName != "" {
		name = specName
	}
	return name, spec
}

func parsePackageManagerSpec(spec string) (string, string) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	if spec == "" {
		return "", ""
	}

	name, version, found := strings.Cut(spec, "@")
	if !found {
		return spec, ""
	}
	if idx := strings.Index(version, "+"); idx >= 0 {
		version = version[:idx]
	}
	return strings.TrimSpace(name), strings.TrimSpace(version)
}

func selectJavaScriptBuilderImage(runtime, version string) string {
	switch runtime {
	case "bun":
		return "oven/bun:" + version
	default:
		return "node:" + version + "-bookworm-slim"
	}
}

func detectJavaScriptBootstrapCommands(ctx jsProjectContext) []string {
	if ctx.Runtime == "bun" {
		return nil
	}

	name, version := parsePackageManagerSpec(ctx.PackageManagerSpec)
	if name == "" {
		name = ctx.PackageManager
	}

	switch name {
	case "pnpm", "yarn":
		commands := []string{"corepack enable"}
		if version != "" {
			commands = append(commands, fmt.Sprintf("corepack prepare %s@%s --activate", name, version))
		}
		return commands
	case "npm":
		if version != "" {
			return []string{fmt.Sprintf("npm install -g npm@%s", version)}
		}
	}
	return nil
}

func detectJavaScriptInstallCommand(ctx jsProjectContext) string {
	installPath := ctx.BuildContextPath
	switch ctx.PackageManager {
	case "bun":
		if fileExists(filepath.Join(installPath, "bun.lock")) || fileExists(filepath.Join(installPath, "bun.lockb")) {
			return "bun install --frozen-lockfile"
		}
		return "bun install"
	case "pnpm":
		if fileExists(filepath.Join(installPath, "pnpm-lock.yaml")) {
			return "pnpm install --frozen-lockfile"
		}
		return "pnpm install"
	case "yarn":
		if fileExists(filepath.Join(installPath, ".yarnrc.yml")) {
			if fileExists(filepath.Join(installPath, "yarn.lock")) {
				return "yarn install --immutable"
			}
			return "yarn install"
		}
		if fileExists(filepath.Join(installPath, "yarn.lock")) {
			return "yarn install --frozen-lockfile"
		}
		return "yarn install"
	default:
		if fileExists(filepath.Join(installPath, "package-lock.json")) || fileExists(filepath.Join(installPath, "npm-shrinkwrap.json")) {
			return "npm ci"
		}
		return "npm install"
	}
}

func detectJavaScriptSetupCommands(ctx jsProjectContext) []string {
	commands := make([]string, 0, 2)
	if detectJavaScriptPrisma(ctx.AppPath, ctx.BuildContextPath, ctx.AppMetadata) {
		commands = append(commands, prefixCommand(ctx.appWorkDir, jsExecCommand(ctx.Runtime, "prisma generate")))
	}
	if shouldAutoInstallNextSharp(ctx, detectJSFramework(ctx)) {
		commands = append(commands, prefixCommand(ctx.appWorkDir, packageManagerAddCommand(ctx.PackageManager, "sharp")))
	}
	return commands
}

func detectJavaScriptPrisma(appPath, buildContextPath string, metadata *nodePackageJSON) bool {
	if fileExists(filepath.Join(appPath, "prisma", "schema.prisma")) {
		return true
	}
	if buildContextPath != appPath && fileExists(filepath.Join(buildContextPath, "prisma", "schema.prisma")) {
		return true
	}
	return hasAnyPackage(metadata, "@prisma/client", "prisma")
}

func detectJavaScriptPlaywright(metadata *nodePackageJSON) bool {
	return hasAnyPackage(metadata, "playwright", "@playwright/test")
}

func shouldAutoInstallNextSharp(ctx jsProjectContext, framework string) bool {
	if framework != "next" {
		return false
	}
	return !hasAnyPackage(ctx.AppMetadata, "sharp") && !hasAnyPackage(ctx.RootMetadata, "sharp")
}

func detectJSFramework(ctx jsProjectContext) string {
	switch {
	case hasAnyPackage(ctx.AppMetadata, "next"):
		return "next"
	case hasAnyPackage(ctx.AppMetadata, "nuxt"):
		return "nuxt"
	case hasAnyPackage(ctx.AppMetadata, "vite") || hasAnyFile(ctx.AppPath, []string{"vite.config.ts", "vite.config.js", "vite.config.mjs", "vite.config.cjs"}) || hasScriptBodyContaining(ctx.AppMetadata, "vite"):
		return "vite"
	case hasAnyPackage(ctx.AppMetadata, "astro"):
		return "astro"
	case hasAnyPackage(ctx.AppMetadata, "react-scripts"):
		return "cra"
	case hasAnyPackage(ctx.AppMetadata, "@angular/core", "@angular/cli"):
		return "angular"
	default:
		return ""
	}
}

func hasAnyFile(dir string, names []string) bool {
	for _, name := range names {
		if fileExists(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

func hasScriptBodyContaining(metadata *nodePackageJSON, needle string) bool {
	if metadata == nil || len(metadata.Scripts) == 0 {
		return false
	}
	needle = strings.ToLower(strings.TrimSpace(needle))
	for _, body := range metadata.Scripts {
		if strings.Contains(strings.ToLower(body), needle) {
			return true
		}
	}
	return false
}

func selectJSBuildScript(metadata *nodePackageJSON) string {
	if metadata == nil {
		return ""
	}
	if hasNodeScript(metadata.Scripts, "build") {
		return "build"
	}
	for _, name := range sortedScriptNames(metadata.Scripts) {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "build:") || strings.Contains(lower, ":build") {
			return name
		}
	}
	return ""
}

func selectJSRunScript(metadata *nodePackageJSON) string {
	if metadata == nil {
		return ""
	}

	for _, name := range []string{"start", "start:prod", "start:production", "serve", "serve:prod", "prod", "production"} {
		if isProductionRunScript(name, metadata.Scripts[name]) {
			return name
		}
	}

	for _, name := range sortedScriptNames(metadata.Scripts) {
		if isProductionRunScript(name, metadata.Scripts[name]) {
			return name
		}
	}
	return ""
}

func isProductionRunScript(name, body string) bool {
	body = strings.TrimSpace(body)
	if body == "" {
		return false
	}

	name = strings.ToLower(strings.TrimSpace(name))
	lowerBody := strings.ToLower(body)
	if strings.Contains(name, "dev") || strings.Contains(name, "preview") {
		return false
	}
	if strings.Contains(lowerBody, " next dev") || strings.HasPrefix(lowerBody, "next dev") {
		return false
	}
	for _, marker := range []string{
		"vite preview",
		"vite dev",
		"nuxt dev",
		"webpack serve",
		"react-scripts start",
		"nodemon",
		"ts-node-dev",
		"wrangler dev",
		"astro dev",
	} {
		if strings.Contains(lowerBody, marker) {
			return false
		}
	}
	return strings.Contains(name, "start") ||
		strings.Contains(name, "serve") ||
		strings.Contains(name, "prod") ||
		strings.Contains(lowerBody, "start") ||
		strings.Contains(lowerBody, "serve") ||
		strings.Contains(lowerBody, "production")
}

func shouldUseStaticRuntime(ctx jsProjectContext, framework, buildScript, runScript string) bool {
	if strings.TrimSpace(buildScript) == "" {
		return false
	}
	switch framework {
	case "vite":
		return true
	case "next", "nuxt":
		return false
	}
	if hasAnyPackage(ctx.AppMetadata, "express", "koa", "fastify", "@nestjs/core", "hono") {
		return false
	}

	runBody := ""
	if runScript != "" && ctx.AppMetadata != nil {
		runBody = strings.ToLower(strings.TrimSpace(ctx.AppMetadata.Scripts[runScript]))
	}
	// Keep Node/Bun runtime when an explicit production run script exists.
	if runBody != "" && !strings.Contains(runBody, "preview") && !strings.Contains(runBody, "vite") {
		return false
	}

	switch framework {
	case "vite", "astro", "cra", "angular":
		if runBody == "" {
			return true
		}
		return strings.Contains(runBody, "preview") || strings.Contains(runBody, "vite")
	}

	if !hasAnyPackage(ctx.AppMetadata, "react", "react-dom", "vue", "svelte", "solid-js", "preact", "lit", "@angular/core") && !fileExists(filepath.Join(ctx.AppPath, "index.html")) {
		return false
	}
	if runBody == "" {
		return true
	}
	return strings.Contains(runBody, "preview") || strings.Contains(runBody, "vite")
}

func normalizeStaticFramework(framework string) string {
	if framework == "" {
		return "static-frontend"
	}
	return framework
}

func detectStaticOutputDir(ctx jsProjectContext, framework string) string {
	switch framework {
	case "cra":
		return joinContainerPath(ctx.appWorkDir, "build")
	case "vite":
		return joinContainerPath(ctx.appWorkDir, detectViteOutputDir(ctx))
	default:
		return joinContainerPath(ctx.appWorkDir, "dist")
	}
}

func detectViteOutputDir(ctx jsProjectContext) string {
	if outDir := detectViteOutputDirFromBuildScript(ctx.AppMetadata); outDir != "" {
		return outDir
	}

	for _, fileName := range []string{"vite.config.ts", "vite.config.js", "vite.config.mjs", "vite.config.cjs"} {
		data, err := os.ReadFile(filepath.Join(ctx.AppPath, fileName))
		if err != nil {
			continue
		}
		if outDir := detectViteOutputDirFromConfig(string(data)); outDir != "" {
			return outDir
		}
	}
	return "dist"
}

func detectViteOutputDirFromBuildScript(metadata *nodePackageJSON) string {
	if metadata == nil {
		return ""
	}

	buildScript := strings.TrimSpace(metadata.Scripts[selectJSBuildScript(metadata)])
	if buildScript == "" {
		return ""
	}

	outDirFlagPattern := regexp.MustCompile("(?i)--outdir(?:=|\\s+)([^\\s\"'`]+)")
	matches := outDirFlagPattern.FindStringSubmatch(buildScript)
	if len(matches) != 2 {
		return ""
	}
	return normalizeViteOutputDir(matches[1])
}

func detectViteOutputDirFromConfig(configBody string) string {
	configBody = strings.TrimSpace(configBody)
	if configBody == "" {
		return ""
	}

	outDirConfigPattern := regexp.MustCompile("(?m)outDir\\s*:\\s*[\"'`]([^\"'`]+)[\"'`]")
	matches := outDirConfigPattern.FindStringSubmatch(configBody)
	if len(matches) != 2 {
		return ""
	}
	return normalizeViteOutputDir(matches[1])
}

func normalizeViteOutputDir(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"'`")
	if value == "" {
		return ""
	}

	value = strings.ReplaceAll(value, "\\", "/")
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return ""
	}
	return cleaned
}

func detectJavaScriptRunCommand(ctx jsProjectContext, runScript string) string {
	if runScript != "" {
		return prefixCommand(ctx.appWorkDir, packageManagerScriptCommand(ctx.PackageManager, runScript))
	}

	if ctx.Runtime == "bun" {
		for _, fileName := range []string{"server.ts", "server.js", "app.ts", "app.js"} {
			if fileExists(filepath.Join(ctx.AppPath, fileName)) {
				return prefixCommand(ctx.appWorkDir, "bun "+fileName)
			}
		}
		return ""
	}

	for _, fileName := range []string{"server.js", "app.js", "main.js", "dist/server.js", "build/server.js"} {
		if fileExists(filepath.Join(ctx.AppPath, filepath.FromSlash(fileName))) {
			return prefixCommand(ctx.appWorkDir, "node "+fileName)
		}
	}
	return ""
}

func packageManagerScriptCommand(packageManager, script string) string {
	script = strings.TrimSpace(script)
	if script == "" {
		return ""
	}

	switch packageManager {
	case "pnpm":
		return "pnpm run " + script
	case "yarn":
		return "yarn " + script
	case "bun":
		return "bun run " + script
	default:
		if script == "start" {
			return "npm start"
		}
		return "npm run " + script
	}
}

func packageManagerAddCommand(packageManager, dependency string) string {
	dependency = strings.TrimSpace(dependency)
	if dependency == "" {
		return ""
	}

	switch packageManager {
	case "pnpm":
		return "pnpm add " + dependency
	case "yarn":
		return "yarn add " + dependency
	case "bun":
		return "bun add " + dependency
	default:
		return "npm install " + dependency
	}
}

func jsExecCommand(runtime, subcommand string) string {
	subcommand = strings.TrimSpace(subcommand)
	if runtime == "bun" {
		return "bunx " + subcommand
	}
	return "npx " + subcommand
}

func prefixCommand(dir, cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	if dir == "" || dir == "." {
		return cmd
	}
	return fmt.Sprintf("cd '%s' && %s", escapeSingleQuotes(dir), cmd)
}

func joinContainerPath(base, child string) string {
	base = strings.TrimSpace(base)
	child = strings.TrimSpace(child)
	switch {
	case base == "", base == ".":
		return path.Clean(child)
	case child == "", child == ".":
		return path.Clean(base)
	default:
		return path.Clean(path.Join(base, child))
	}
}

func appendUniqueString(values []string, extra string) []string {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return values
	}
	for _, existing := range values {
		if existing == extra {
			return values
		}
	}
	return append(values, extra)
}

func appendJavaScriptSystemPackages(values []string, extras ...string) []string {
	for _, extra := range extras {
		values = appendUniqueString(values, extra)
	}
	sort.Strings(values)
	return values
}

func hasAnyPackage(metadata *nodePackageJSON, names ...string) bool {
	if metadata == nil {
		return false
	}
	for _, name := range names {
		if _, ok := metadata.Dependencies[name]; ok {
			return true
		}
		if _, ok := metadata.DevDependencies[name]; ok {
			return true
		}
	}
	return false
}

func detectJavaScriptSystemPackages(metadata *nodePackageJSON) []string {
	if metadata == nil {
		return nil
	}

	packages := make([]string, 0, 16)
	add := func(names ...string) {
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			found := false
			for _, existing := range packages {
				if existing == name {
					found = true
					break
				}
			}
			if !found {
				packages = append(packages, name)
			}
		}
	}

	if hasAnyPackage(metadata, "@prisma/client", "prisma", "sharp", "canvas", "better-sqlite3", "sqlite3", "bcrypt", "argon2", "playwright", "@playwright/test", "puppeteer") {
		add("ca-certificates", "git", "openssl", "python3", "make", "g++", "pkg-config")
	}
	if hasAnyPackage(metadata, "canvas") {
		add("libcairo2-dev", "libpango1.0-dev", "libjpeg62-turbo-dev", "libgif-dev", "librsvg2-dev")
	}
	if hasAnyPackage(metadata, "playwright", "@playwright/test", "puppeteer") {
		add(
			"fonts-liberation",
			"libasound2",
			"libatk-bridge2.0-0",
			"libatk1.0-0",
			"libcups2",
			"libdbus-1-3",
			"libdrm2",
			"libgbm1",
			"libgtk-3-0",
			"libnss3",
			"libxcomposite1",
			"libxdamage1",
			"libxfixes3",
			"libxkbcommon0",
			"libxrandr2",
		)
	}

	sort.Strings(packages)
	return packages
}
