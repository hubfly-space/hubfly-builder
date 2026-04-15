package autodetect

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"hubfly-builder/internal/allowlist"
)

type buildPlan struct {
	Runtime            string
	RuntimeFlavor      string
	Framework          string
	Version            string
	InstallCommand     string
	DependencyFiles    []string
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
	case "go":
		prebuild, build, run := detectCommandsWithPath(appPath, runtime, allowed)
		plan, err := defaultBuildPlan(runtime, version, prebuild, build, run)
		if err != nil {
			return buildPlan{}, err
		}
		plan.BuildContextDir = appDir
		plan.AppDir = appDir
		plan.Framework = detectGoFramework(repoRoot, appPath)
		plan.DependencyFiles = detectGoDependencyFiles(appPath)
		plan.ExposePort = inferExposePort(defaultExposePort(runtime), run)
		if err := validateBuildPlanCommands(plan, allowed); err != nil {
			return buildPlan{}, err
		}
		return plan, nil
	case "dotnet":
		prebuild, build, run := detectCommandsWithPath(appPath, runtime, allowed)
		plan, err := defaultBuildPlan(runtime, version, prebuild, build, run)
		if err != nil {
			return buildPlan{}, err
		}
		plan.BuildContextDir = appDir
		plan.AppDir = appDir
		plan.Framework = "aspnet-core"
		plan.DependencyFiles = detectDotnetDependencyFiles(appPath)
		plan.ExposePort = inferExposePort(defaultExposePort(runtime), run)
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
	case "elixir":
		plan, err := detectElixirBuildPlan(appDir, appPath, version, allowed)
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
		if runtime == "rust" {
			plan.Framework = detectRustFramework(appPath)
			if plan.Framework == "axum" || plan.Framework == "rocket" || plan.Framework == "actix-web" {
				configureRustCargoChefPlan(&plan, appPath)
			} else {
				plan.PostBuildCommands = append(plan.PostBuildCommands, rustSelectBinaryCommand(detectRustBinaryName(appPath)))
				plan.RunCommand = "./app"
			}
			plan.ExposePort = inferExposePort(defaultRustExposePort(plan.Framework), run)
			plan.RuntimeEnv = rustRuntimeEnv(plan.Framework, plan.ExposePort)
		}
		if runtime == "java" {
			plan.PostBuildCommands = append(plan.PostBuildCommands, javaSelectJarCommand())
			plan.RunCommand = "java -jar app.jar"
		}
		plan.BuildContextDir = appDir
		plan.AppDir = appDir
		if plan.ExposePort == "" {
			plan.ExposePort = inferExposePort(defaultExposePort(runtime), run)
		}
		if err := validateBuildPlanCommands(plan, allowed); err != nil {
			return buildPlan{}, err
		}
		return plan, nil
	}
}

func detectJavaScriptBuildPlan(repoRoot, appDir, appPath, runtime, version string) (buildPlan, error) {
	ctx := newJSProjectContext(repoRoot, appDir, appPath, runtime, version)
	framework := detectJSFramework(ctx)
	if framework == "sveltekit" {
		if adapter := detectSvelteKitAdapter(ctx); adapter != "" {
			if reason, ok := svelteKitPlatformAdapterReason(adapter); ok {
				return buildPlan{}, fmt.Errorf("SvelteKit adapter %s detected; deploy to that platform or switch to adapter-node or adapter-static for container builds", reason)
			}
		}
	}
	buildScript := selectJSBuildScript(ctx.AppMetadata)
	if framework == "angular" && detectAngularSSR(ctx) && hasNodeScript(ctx.AppMetadata.Scripts, "build:ssr") {
		buildScript = "build:ssr"
	}
	if framework == "nuxt" && detectNuxtStatic(ctx) && hasNodeScript(ctx.AppMetadata.Scripts, "generate") {
		buildScript = "generate"
	}
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
	if canInstallWithoutFullSource(ctx) {
		plan.DependencyFiles = detectJavaScriptDependencyFiles(ctx)
	}
	plan.ExposePort = inferJavaScriptExposePort(ctx, framework, runScript)
	if framework == "angular" && detectAngularSSR(ctx) && plan.ExposePort == "4200" {
		plan.ExposePort = "4000"
	}
	plan.RuntimeEnv = map[string]string{"HOST": "0.0.0.0", "PORT": plan.ExposePort}
	plan.RuntimeEnv["NODE_ENV"] = "production"

	if shouldAutoInstallNextSharp(ctx, framework) {
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
	} else if runtime == "node" && framework == "angular" {
		plan.RunCommand = detectAngularRunCommand(ctx, plan.ExposePort)
	} else if runtime == "node" && framework == "astro" {
		plan.RunCommand = detectAstroRunCommand(ctx, plan.ExposePort)
	} else if runtime == "node" && framework == "remix" {
		plan.RunCommand = detectRemixRunCommand(ctx, plan.ExposePort)
	} else if runtime == "node" && framework == "nuxt" {
		plan.RunCommand = detectJavaScriptRunCommand(ctx, runScript)
		if strings.TrimSpace(plan.RunCommand) == "" {
			plan.RunCommand = prefixCommand(plan.appWorkDir, fmt.Sprintf("HOST=0.0.0.0 PORT=${PORT:-%s} node .output/server/index.mjs", plan.ExposePort))
		}
	} else {
		plan.RunCommand = detectJavaScriptRunCommand(ctx, runScript)
	}

	if runtime == "node" && strings.TrimSpace(plan.RunCommand) == "" && hasAnyPackage(ctx.AppMetadata, "@nestjs/core") {
		plan.RunCommand = prefixCommand(plan.appWorkDir, "HOST=0.0.0.0 PORT=${PORT:-"+plan.ExposePort+"} node dist/main.js")
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
	case "vue", "solid":
		defaultPort = "5173"
	case "angular":
		defaultPort = "4200"
	case "astro":
		defaultPort = "4321"
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
	case "go", "java", "php", "rust", "dotnet":
		return "8080"
	case "elixir":
		return "4000"
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

func canInstallWithoutFullSource(ctx jsProjectContext) bool {
	if scriptsRequireSource(ctx.RootMetadata) {
		return false
	}
	if scriptsRequireSource(ctx.AppMetadata) {
		return false
	}
	return true
}

func scriptsRequireSource(meta *nodePackageJSON) bool {
	if meta == nil || meta.Scripts == nil {
		return false
	}
	for _, name := range []string{"preinstall", "install", "postinstall", "prepare", "preprepare", "postprepare"} {
		if strings.TrimSpace(meta.Scripts[name]) != "" {
			return true
		}
	}
	return false
}

func detectJavaScriptDependencyFiles(ctx jsProjectContext) []string {
	candidates := []string{
		"package.json",
		"package-lock.json",
		"npm-shrinkwrap.json",
		"pnpm-lock.yaml",
		"pnpm-workspace.yaml",
		"yarn.lock",
		".yarnrc",
		".yarnrc.yml",
		".npmrc",
		"bun.lockb",
		"bun.lock",
		"bunfig.toml",
		".yarn",
	}

	files := make([]string, 0, len(candidates))
	addIfExists := func(relPath string) {
		if relPath == "" {
			return
		}
		checkPath := filepath.Join(ctx.BuildContextPath, filepath.FromSlash(relPath))
		if fileExists(checkPath) {
			files = append(files, relPath)
		}
	}
	for _, name := range candidates {
		addIfExists(name)
	}
	if ctx.AppDir != "." && ctx.AppDir != "" {
		for _, name := range candidates {
			addIfExists(path.Join(ctx.AppDir, name))
		}
	}
	return files
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
	switch ctx.PackageManager {
	case "bun":
		return "bun install"
	case "pnpm":
		return "pnpm install"
	case "yarn":
		return "yarn install"
	default:
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
	if isReactRouterApp(ctx) {
		return "remix"
	}
	switch {
	case hasAnyPackage(ctx.AppMetadata, "next"):
		return "next"
	case hasAnyPackage(ctx.AppMetadata, "@sveltejs/kit"):
		return "sveltekit"
	case hasAnyPackage(ctx.AppMetadata, "@remix-run/dev") ||
		hasAnyPackage(ctx.AppMetadata, "@remix-run/node") ||
		hasAnyPackage(ctx.AppMetadata, "remix") ||
		hasAnyPackage(ctx.AppMetadata, "@react-router/dev") ||
		hasAnyPackage(ctx.AppMetadata, "@react-router/node") ||
		hasAnyPackage(ctx.AppMetadata, "@react-router/serve") ||
		hasScriptBodyContaining(ctx.AppMetadata, "react-router build"):
		return "remix"
	case hasAnyPackage(ctx.AppMetadata, "nuxt"):
		return "nuxt"
	case isSolidStartApp(ctx):
		return "solidstart"
	case hasAnyPackage(ctx.AppMetadata, "sails"):
		return "sails"
	case hasAnyPackage(ctx.AppMetadata, "fastify"):
		return "fastify"
	case hasAnyPackage(ctx.AppMetadata, "vue"):
		return "vue"
	case hasAnyPackage(ctx.AppMetadata, "solid-js"):
		return "solid"
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

func isReactRouterApp(ctx jsProjectContext) bool {
	remixConfigFiles := []string{
		"react-router.config.ts",
		"react-router.config.js",
		"react-router.config.mjs",
		"react-router.config.cjs",
		"remix.config.ts",
		"remix.config.js",
		"remix.config.mjs",
		"remix.config.cjs",
	}
	if hasAnyFile(ctx.AppPath, remixConfigFiles) || (ctx.BuildContextPath != ctx.AppPath && hasAnyFile(ctx.BuildContextPath, remixConfigFiles)) {
		return true
	}
	if hasAnyPackage(ctx.AppMetadata, "@remix-run/dev", "@remix-run/node", "remix", "@react-router/dev", "@react-router/node", "@react-router/serve") {
		return true
	}
	if hasScriptBodyContaining(ctx.AppMetadata, "react-router build") {
		return true
	}
	return false
}

func isSolidStartApp(ctx jsProjectContext) bool {
	if hasAnyPackage(ctx.AppMetadata, "@solidjs/start", "@solidjs/start-node", "solid-start") {
		return true
	}
	if hasScriptBodyContaining(ctx.AppMetadata, "solid-start") {
		return true
	}
	return false
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

	preferred := []string{"start", "start:prod", "start:production", "serve", "serve:prod", "prod", "production"}
	if hasAnyPackage(metadata, "@nestjs/core") {
		preferred = []string{"start:prod", "start:production", "start", "serve", "serve:prod", "prod", "production"}
	}

	for _, name := range preferred {
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
	if isReactRouterApp(ctx) {
		return !detectRemixSSR(ctx)
	}
	switch framework {
	case "vite":
		return true
	case "vue", "solid":
		return true
	case "solidstart":
		return false
	case "next":
		return false
	case "nuxt":
		return detectNuxtStatic(ctx)
	case "angular":
		return !detectAngularSSR(ctx)
	case "astro":
		return !detectAstroSSR(ctx)
	case "remix":
		return !detectRemixSSR(ctx)
	case "sveltekit":
		switch detectSvelteKitAdapter(ctx) {
		case "static":
			return true
		case "node", "auto":
			return false
		default:
			return false
		}
	}
	if hasAnyPackage(ctx.AppMetadata, "express", "koa", "fastify", "@nestjs/core", "hono") {
		return false
	}
	if hasAnyPackage(ctx.AppMetadata, "sails") {
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
	case "vite", "vue", "solid", "astro", "cra", "angular":
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
	if isReactRouterApp(ctx) {
		return joinContainerPath(ctx.appWorkDir, "build/client")
	}
	switch framework {
	case "cra":
		return joinContainerPath(ctx.appWorkDir, "build")
	case "angular":
		if outputDir := detectAngularStaticOutputDir(ctx); outputDir != "" {
			return outputDir
		}
		return joinContainerPath(ctx.appWorkDir, "dist")
	case "nuxt":
		return joinContainerPath(ctx.appWorkDir, ".output/public")
	case "remix":
		return joinContainerPath(ctx.appWorkDir, "build/client")
	case "sveltekit":
		if outputDir := detectSvelteKitStaticOutputDir(ctx); outputDir != "" {
			return outputDir
		}
		return joinContainerPath(ctx.appWorkDir, "build")
	case "vite", "vue", "solid":
		return joinContainerPath(ctx.appWorkDir, detectViteOutputDir(ctx))
	default:
		return joinContainerPath(ctx.appWorkDir, "dist")
	}
}

func detectNuxtStatic(ctx jsProjectContext) bool {
	if ctx.AppMetadata != nil {
		if hasNodeScript(ctx.AppMetadata.Scripts, "generate") {
			return true
		}
		if hasScriptBodyContaining(ctx.AppMetadata, "nuxt generate") || hasScriptBodyContaining(ctx.AppMetadata, "nuxi generate") {
			return true
		}
	}
	for _, name := range []string{"nuxt.config.ts", "nuxt.config.js", "nuxt.config.mjs", "nuxt.config.cjs"} {
		configPath := filepath.Join(ctx.AppPath, name)
		if data, err := os.ReadFile(configPath); err == nil {
			lower := strings.ToLower(string(data))
			if strings.Contains(lower, "ssr: false") || strings.Contains(lower, "target: 'static'") || strings.Contains(lower, "target: \"static\"") || strings.Contains(lower, "preset: 'static'") || strings.Contains(lower, "preset: \"static\"") {
				return true
			}
		}
	}
	return false
}

type angularConfig struct {
	DefaultProject string                   `json:"defaultProject"`
	Projects       map[string]angularProject `json:"projects"`
}

type angularProject struct {
	Root      string                  `json:"root"`
	Architect map[string]angularTarget `json:"architect"`
	Targets   map[string]angularTarget `json:"targets"`
}

type angularTarget struct {
	Builder             string                    `json:"builder"`
	Executor            string                    `json:"executor"`
	DefaultConfiguration string                   `json:"defaultConfiguration"`
	Options             map[string]any            `json:"options"`
	Configurations      map[string]map[string]any `json:"configurations"`
}

func loadAngularConfig(ctx jsProjectContext) (angularConfig, string, string, bool) {
	configPath := filepath.Join(ctx.BuildContextPath, "angular.json")
	data, err := os.ReadFile(configPath)
	if err != nil && ctx.BuildContextPath != ctx.AppPath {
		configPath = filepath.Join(ctx.AppPath, "angular.json")
		data, err = os.ReadFile(configPath)
	}
	if err != nil {
		return angularConfig{}, "", "", false
	}

	var cfg angularConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return angularConfig{}, "", "", false
	}

	projectName := pickAngularProject(cfg, effectiveAngularAppDir(ctx))
	if projectName == "" {
		return angularConfig{}, "", "", false
	}
	configRoot := "."
	if configPath == filepath.Join(ctx.AppPath, "angular.json") && ctx.BuildContextPath != ctx.AppPath && ctx.AppDir != "" {
		configRoot = ctx.AppDir
	}
	configRoot = strings.TrimSpace(strings.Trim(configRoot, "/"))
	if configRoot == "" {
		configRoot = "."
	}
	return cfg, projectName, configRoot, true
}

func effectiveAngularAppDir(ctx jsProjectContext) string {
	if ctx.BuildContextDir != "." && ctx.AppDir == ctx.BuildContextDir {
		return "."
	}
	return ctx.AppDir
}

func detectAngularOutputPaths(ctx jsProjectContext) (string, string, string) {
	cfg, projectName, configRoot, ok := loadAngularConfig(ctx)
	if !ok {
		return "", "", ""
	}
	project, ok := cfg.Projects[projectName]
	if !ok {
		return "", "", ""
	}

	target := project.Architect["build"]
	if len(target.Options) == 0 {
		target = project.Targets["build"]
	}
	if len(target.Options) == 0 {
		// allow configurations-only outputPath
		target.Options = map[string]any{}
	}
	baseDefault := resolveAngularOutputPath(configRoot, joinContainerPath("", path.Join("dist", projectName)))
	raw, ok := target.Options["outputPath"]
	if !ok {
		raw, ok = angularOutputPathFromConfigurations(target)
		if !ok {
			return baseDefault, "", ""
		}
	}
	base, browser, server := parseAngularOutputPath(raw)
	base = resolveAngularOutputPath(configRoot, base)
	browser = resolveAngularOutputPath(configRoot, browser)
	server = resolveAngularOutputPath(configRoot, server)
	if base == "" {
		base = baseDefault
	}
	return base, browser, server
}

func angularOutputPathFromConfigurations(target angularTarget) (any, bool) {
	if len(target.Configurations) == 0 {
		return nil, false
	}
	configName := strings.TrimSpace(target.DefaultConfiguration)
	if configName == "" {
		if _, ok := target.Configurations["production"]; ok {
			configName = "production"
		}
	}
	if configName == "" && len(target.Configurations) == 1 {
		for name := range target.Configurations {
			configName = name
			break
		}
	}
	if configName == "" {
		return nil, false
	}
	config, ok := target.Configurations[configName]
	if !ok || len(config) == 0 {
		return nil, false
	}
	raw, ok := config["outputPath"]
	return raw, ok
}

func detectAngularOutputDir(ctx jsProjectContext) string {
	base, browser, server := detectAngularOutputPaths(ctx)
	if base == "" {
		if server != "" {
			server = strings.TrimRight(server, "/")
			if strings.HasSuffix(server, "/server") {
				base = strings.TrimSuffix(server, "/server")
			} else {
				base = path.Dir(server)
			}
		} else if browser != "" {
			browser = strings.TrimRight(browser, "/")
			if strings.HasSuffix(browser, "/browser") {
				base = strings.TrimSuffix(browser, "/browser")
			} else {
				base = browser
			}
		}
		base = normalizeAngularOutputDir(base)
	}
	return base
}

func detectAngularTargets(ctx jsProjectContext) (bool, bool, bool) {
	cfg, projectName, _, ok := loadAngularConfig(ctx)
	if !ok {
		return false, false, false
	}
	project, ok := cfg.Projects[projectName]
	if !ok {
		return false, false, false
	}
	hasServer := false
	if _, ok := project.Architect["server"]; ok {
		hasServer = true
	}
	if _, ok := project.Targets["server"]; ok {
		hasServer = true
	}
	hasPrerender := false
	if _, ok := project.Architect["prerender"]; ok {
		hasPrerender = true
	}
	if _, ok := project.Targets["prerender"]; ok {
		hasPrerender = true
	}
	return hasServer, hasPrerender, true
}

func hasAngularSSRScript(metadata *nodePackageJSON) bool {
	if metadata == nil || len(metadata.Scripts) == 0 {
		return false
	}
	for _, name := range []string{"serve:ssr", "start:ssr", "ssr", "build:ssr"} {
		if hasNodeScript(metadata.Scripts, name) {
			return true
		}
	}
	return false
}

func detectAngularSSR(ctx jsProjectContext) bool {
	hasServer, _, ok := detectAngularTargets(ctx)
	if ok {
		if hasServer {
			return true
		}
		if hasAngularSSRScript(ctx.AppMetadata) {
			return true
		}
		return false
	}
	if hasAngularSSRScript(ctx.AppMetadata) {
		return true
	}
	if hasAnyPackage(ctx.AppMetadata, "@angular/platform-server", "@angular/ssr", "@nguniversal/express-engine", "@nguniversal/common", "@nguniversal/builders") ||
		hasAnyPackage(ctx.RootMetadata, "@angular/platform-server", "@angular/ssr", "@nguniversal/express-engine", "@nguniversal/common", "@nguniversal/builders") {
		return true
	}
	return false
}

func detectAngularStaticOutputDir(ctx jsProjectContext) string {
	base, browser, _ := detectAngularOutputPaths(ctx)
	outputDir := ""
	if browser != "" {
		outputDir = browser
	} else if base != "" {
		outputDir = base
	}
	if outputDir == "" {
		outputDir = joinContainerPath(ctx.appWorkDir, "dist")
	}
	if browser == "" && base != "" {
		if target, ok := detectAngularBuildTarget(ctx); ok {
			builder := strings.ToLower(strings.TrimSpace(target.Builder))
			if builder == "" {
				builder = strings.ToLower(strings.TrimSpace(target.Executor))
			}
			if isAngularApplicationBuilder(builder) {
				outputDir = joinContainerPath(base, "browser")
			}
		}
	}
	_, hasPrerender, _ := detectAngularTargets(ctx)
	if hasPrerender && !strings.HasSuffix(outputDir, "/browser") {
		return joinContainerPath(outputDir, "browser")
	}
	return outputDir
}

func detectAngularBuildTarget(ctx jsProjectContext) (angularTarget, bool) {
	cfg, projectName, _, ok := loadAngularConfig(ctx)
	if !ok {
		return angularTarget{}, false
	}
	project, ok := cfg.Projects[projectName]
	if !ok {
		return angularTarget{}, false
	}
	target := project.Architect["build"]
	if len(target.Options) == 0 {
		target = project.Targets["build"]
	}
	if len(target.Options) == 0 && target.Builder == "" && target.Executor == "" {
		return angularTarget{}, false
	}
	return target, true
}

func isAngularApplicationBuilder(builder string) bool {
	return strings.Contains(builder, ":application")
}

func detectAngularRunCommand(ctx jsProjectContext, port string) string {
	if strings.TrimSpace(port) == "" {
		port = "4000"
	}
	if ctx.AppMetadata != nil {
		for _, name := range []string{"serve:ssr", "start:ssr", "ssr", "serve:prod:ssr", "start:prod:ssr"} {
			if hasNodeScript(ctx.AppMetadata.Scripts, name) {
				return prefixCommand(ctx.appWorkDir, packageManagerScriptCommand(ctx.PackageManager, name))
			}
		}
	}

	outputDir := detectAngularOutputDir(ctx)
	if outputDir == "" {
		outputDir = joinContainerPath(ctx.appWorkDir, "dist")
	}
	serverDir := joinContainerPath(outputDir, "server")
	mainMJS := joinContainerPath(serverDir, "main.mjs")
	mainJS := joinContainerPath(serverDir, "main.js")
	indexMJS := joinContainerPath(serverDir, "index.mjs")
	indexJS := joinContainerPath(serverDir, "index.js")

	quotedMainMJS := escapeSingleQuotes(mainMJS)
	quotedMainJS := escapeSingleQuotes(mainJS)
	quotedIndexMJS := escapeSingleQuotes(indexMJS)
	quotedIndexJS := escapeSingleQuotes(indexJS)

	fallback := fmt.Sprintf(
		"if [ -f '%s' ]; then HOST=0.0.0.0 PORT=${PORT:-%s} node '%s'; "+
			"elif [ -f '%s' ]; then HOST=0.0.0.0 PORT=${PORT:-%s} node '%s'; "+
			"elif [ -f '%s' ]; then HOST=0.0.0.0 PORT=${PORT:-%s} node '%s'; "+
			"else HOST=0.0.0.0 PORT=${PORT:-%s} node '%s'; fi",
		quotedMainMJS, port, quotedMainMJS,
		quotedMainJS, port, quotedMainJS,
		quotedIndexMJS, port, quotedIndexMJS,
		port, quotedIndexJS,
	)
	return prefixCommand(ctx.appWorkDir, fallback)
}

func pickAngularProject(cfg angularConfig, appDir string) string {
	if cfg.DefaultProject != "" {
		if _, ok := cfg.Projects[cfg.DefaultProject]; ok {
			return cfg.DefaultProject
		}
	}
	if len(cfg.Projects) == 1 {
		for name := range cfg.Projects {
			return name
		}
	}
	appDir = strings.TrimSpace(strings.Trim(appDir, "/"))
	if appDir == "" || appDir == "." {
		for name, project := range cfg.Projects {
			root := strings.TrimSpace(strings.Trim(project.Root, "/"))
			if root == "" || root == "." {
				return name
			}
		}
		return ""
	}
	best := ""
	bestLen := -1
	for name, project := range cfg.Projects {
		root := strings.TrimSpace(strings.Trim(project.Root, "/"))
		if root == "" {
			continue
		}
		if appDir == root || strings.HasPrefix(appDir, root+"/") {
			if len(root) > bestLen {
				best = name
				bestLen = len(root)
			}
		}
	}
	return best
}

func normalizeAngularOutputDir(value string) string {
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

func parseAngularOutputPath(raw any) (string, string, string) {
	switch value := raw.(type) {
	case string:
		return normalizeAngularOutputDir(value), "", ""
	case map[string]any:
		read := func(key string) string {
			val, ok := value[key]
			if !ok {
				return ""
			}
			text, ok := val.(string)
			if !ok {
				return ""
			}
			return normalizeAngularOutputDir(text)
		}
		base := read("base")
		browser := read("browser")
		server := read("server")
		if base == "" {
			base = read("outputPath")
			if base == "" {
				base = read("path")
			}
		}
		if browser == "" {
			browser = read("browserOutputPath")
		}
		if server == "" {
			server = read("serverOutputPath")
		}
		return base, browser, server
	default:
		return "", "", ""
	}
}

func resolveAngularOutputPath(configRoot, output string) string {
	output = strings.TrimSpace(output)
	if output == "" || output == "." {
		return ""
	}
	configRoot = strings.TrimSpace(strings.Trim(configRoot, "/"))
	if configRoot == "" || configRoot == "." {
		return output
	}
	return joinContainerPath(configRoot, output)
}

func detectAstroOutputMode(ctx jsProjectContext) string {
	for _, fileName := range []string{"astro.config.mjs", "astro.config.ts", "astro.config.js", "astro.config.cjs"} {
		if mode := detectAstroOutputModeFromFile(filepath.Join(ctx.AppPath, fileName)); mode != "" {
			return mode
		}
		if ctx.BuildContextPath != ctx.AppPath {
			if mode := detectAstroOutputModeFromFile(filepath.Join(ctx.BuildContextPath, fileName)); mode != "" {
				return mode
			}
		}
	}
	return ""
}

func detectAstroOutputModeFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	body := string(data)
	pattern := regexp.MustCompile("(?m)\\boutput\\s*:\\s*['\"](server|hybrid|static)['\"]")
	if matches := pattern.FindStringSubmatch(body); len(matches) == 2 {
		return strings.ToLower(matches[1])
	}
	return ""
}

func detectAstroUsesNodeAdapter(ctx jsProjectContext) bool {
	if hasAnyPackage(ctx.AppMetadata, "@astrojs/node") || hasAnyPackage(ctx.RootMetadata, "@astrojs/node") {
		return true
	}
	for _, fileName := range []string{"astro.config.mjs", "astro.config.ts", "astro.config.js", "astro.config.cjs"} {
		if hasAstroNodeAdapter(filepath.Join(ctx.AppPath, fileName)) {
			return true
		}
		if ctx.BuildContextPath != ctx.AppPath {
			if hasAstroNodeAdapter(filepath.Join(ctx.BuildContextPath, fileName)) {
				return true
			}
		}
	}
	return false
}

func hasAstroNodeAdapter(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	return strings.Contains(lower, "@astrojs/node")
}

func detectAstroSSR(ctx jsProjectContext) bool {
	mode := detectAstroOutputMode(ctx)
	if mode == "server" || mode == "hybrid" {
		return true
	}
	return detectAstroUsesNodeAdapter(ctx)
}

func detectAstroRunCommand(ctx jsProjectContext, port string) string {
	if strings.TrimSpace(port) == "" {
		port = "4321"
	}
	if ctx.AppMetadata != nil {
		for _, name := range []string{"start", "serve", "start:ssr", "serve:ssr", "start:prod", "serve:prod"} {
			body := strings.TrimSpace(ctx.AppMetadata.Scripts[name])
			if body == "" {
				continue
			}
			lower := strings.ToLower(body)
			if strings.Contains(lower, "astro preview") || strings.Contains(lower, "astro dev") || strings.Contains(lower, "preview") {
				continue
			}
			return prefixCommand(ctx.appWorkDir, packageManagerScriptCommand(ctx.PackageManager, name))
		}
	}
	return prefixCommand(ctx.appWorkDir, fmt.Sprintf("HOST=0.0.0.0 PORT=${PORT:-%s} node ./dist/server/entry.mjs", port))
}

func detectRemixSSR(ctx jsProjectContext) bool {
	for _, fileName := range []string{"react-router.config.ts", "react-router.config.js", "react-router.config.mjs", "react-router.config.cjs"} {
		if enabled, ok := detectRemixSSRFromFile(filepath.Join(ctx.AppPath, fileName)); ok {
			return enabled
		}
		if ctx.BuildContextPath != ctx.AppPath {
			if enabled, ok := detectRemixSSRFromFile(filepath.Join(ctx.BuildContextPath, fileName)); ok {
				return enabled
			}
		}
	}
	return true
}

func detectRemixSSRFromFile(path string) (bool, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	pattern := regexp.MustCompile("(?m)\\bssr\\s*:\\s*(true|false)\\b")
	if matches := pattern.FindStringSubmatch(string(data)); len(matches) == 2 {
		return strings.ToLower(matches[1]) == "true", true
	}
	return false, false
}

func detectRemixRunCommand(ctx jsProjectContext, port string) string {
	if strings.TrimSpace(port) == "" {
		port = "3000"
	}
	if ctx.AppMetadata != nil {
		for _, name := range []string{"start", "start:prod", "start:production", "serve", "serve:prod", "prod", "production"} {
			body := strings.TrimSpace(ctx.AppMetadata.Scripts[name])
			if body == "" {
				continue
			}
			lower := strings.ToLower(body)
			if strings.Contains(lower, "dev") || strings.Contains(lower, "watch") {
				continue
			}
			return prefixCommand(ctx.appWorkDir, packageManagerScriptCommand(ctx.PackageManager, name))
		}
	}
	return prefixCommand(ctx.appWorkDir, fmt.Sprintf("HOST=0.0.0.0 PORT=${PORT:-%s} node ./build/server/index.js", port))
}

func detectSvelteKitAdapter(ctx jsProjectContext) string {
	for _, fileName := range []string{"svelte.config.js", "svelte.config.mjs", "svelte.config.cjs", "svelte.config.ts"} {
		if adapter := detectSvelteKitAdapterFromFile(filepath.Join(ctx.AppPath, fileName)); adapter != "" {
			return adapter
		}
		if ctx.BuildContextPath != ctx.AppPath {
			if adapter := detectSvelteKitAdapterFromFile(filepath.Join(ctx.BuildContextPath, fileName)); adapter != "" {
				return adapter
			}
		}
	}
	return ""
}

func detectSvelteKitAdapterFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return detectSvelteKitAdapterFromConfig(string(data))
}

func detectSvelteKitAdapterFromConfig(configBody string) string {
	lower := strings.ToLower(configBody)
	switch {
	case strings.Contains(lower, "adapter-static"):
		return "static"
	case strings.Contains(lower, "adapter-node"):
		return "node"
	case strings.Contains(lower, "adapter-auto"):
		return "auto"
	case strings.Contains(lower, "adapter-vercel"):
		return "vercel"
	case strings.Contains(lower, "adapter-netlify"):
		return "netlify"
	case strings.Contains(lower, "adapter-cloudflare-workers"):
		return "cloudflare-workers"
	case strings.Contains(lower, "adapter-cloudflare-pages"):
		return "cloudflare-pages"
	case strings.Contains(lower, "adapter-cloudflare"):
		return "cloudflare"
	case strings.Contains(lower, "adapter-aws"):
		return "aws"
	case strings.Contains(lower, "adapter-azure"):
		return "azure"
	case strings.Contains(lower, "adapter-deno"):
		return "deno"
	default:
		return ""
	}
}

func svelteKitPlatformAdapterReason(adapter string) (string, bool) {
	switch adapter {
	case "vercel", "netlify", "cloudflare", "cloudflare-workers", "cloudflare-pages", "aws", "azure", "deno":
		return adapter, true
	case "auto":
		return "auto", true
	default:
		return "", false
	}
}

func detectSvelteKitStaticOutputDir(ctx jsProjectContext) string {
	if detectSvelteKitAdapter(ctx) != "static" {
		return ""
	}
	for _, fileName := range []string{"svelte.config.js", "svelte.config.mjs", "svelte.config.cjs", "svelte.config.ts"} {
		if outputDir := detectSvelteKitStaticOutputDirFromFile(filepath.Join(ctx.AppPath, fileName)); outputDir != "" {
			return outputDir
		}
		if ctx.BuildContextPath != ctx.AppPath {
			if outputDir := detectSvelteKitStaticOutputDirFromFile(filepath.Join(ctx.BuildContextPath, fileName)); outputDir != "" {
				return outputDir
			}
		}
	}
	return ""
}

func detectSvelteKitStaticOutputDirFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return detectSvelteKitStaticOutputDirFromConfig(string(data))
}

func detectSvelteKitStaticOutputDirFromConfig(configBody string) string {
	configBody = strings.TrimSpace(configBody)
	if configBody == "" {
		return ""
	}

	pagesPattern := regexp.MustCompile("(?m)\\bpages\\s*:\\s*['\"`]([^'\"`]+)['\"`]")
	if matches := pagesPattern.FindStringSubmatch(configBody); len(matches) == 2 {
		return normalizeSvelteKitOutputDir(matches[1])
	}
	assetsPattern := regexp.MustCompile("(?m)\\bassets\\s*:\\s*['\"`]([^'\"`]+)['\"`]")
	if matches := assetsPattern.FindStringSubmatch(configBody); len(matches) == 2 {
		return normalizeSvelteKitOutputDir(matches[1])
	}
	return ""
}

func normalizeSvelteKitOutputDir(value string) string {
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
	return joinContainerPath("", cleaned)
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
