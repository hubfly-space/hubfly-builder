package autodetect

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"hubfly-builder/internal/allowlist"
)

func HasStructuredBuildPhases(cfg BuildConfig) bool {
	cfg.NormalizePhaseAliases()
	if strings.TrimSpace(cfg.InstallCommand) != "" {
		return true
	}
	if len(cfg.SetupCommands) > 0 {
		return true
	}
	if strings.TrimSpace(cfg.BuildCommand) != "" {
		return true
	}
	if len(cfg.PostBuildCommands) > 0 {
		return true
	}
	if strings.TrimSpace(cfg.RunCommand) != "" {
		return true
	}
	if strings.TrimSpace(cfg.RuntimeInitCommand) != "" {
		return true
	}
	return false
}

func FinalizeBuildConfigWithOptions(opts AutoDetectOptions, cfg BuildConfig, allowed *allowlist.AllowedCommands) (BuildConfig, error) {
	return FinalizeBuildConfigWithEnvOptions(opts, cfg, allowed, nil, nil)
}

func FinalizeBuildConfigWithEnvOptions(opts AutoDetectOptions, cfg BuildConfig, allowed *allowlist.AllowedCommands, buildArgKeys, secretBuildKeys []string) (BuildConfig, error) {
	plan, err := manualBuildPlanFromConfig(opts, cfg)
	if err != nil {
		return BuildConfig{}, err
	}
	if err := validateBuildPlanCommands(plan, allowed); err != nil {
		return BuildConfig{}, err
	}
	return buildConfigFromPlan(plan, false, buildArgKeys, secretBuildKeys)
}

func buildConfigFromPlan(plan buildPlan, isAutoBuild bool, buildArgKeys, secretBuildKeys []string) (BuildConfig, error) {
	dockerfile, err := generateDockerfileForPlan(plan, buildArgKeys, secretBuildKeys)
	if err != nil {
		return BuildConfig{}, err
	}

	cfg := BuildConfig{
		IsAutoBuild:        isAutoBuild,
		Runtime:            strings.TrimSpace(plan.Runtime),
		Framework:          strings.TrimSpace(plan.Framework),
		Version:            strings.TrimSpace(plan.Version),
		InstallCommand:     strings.TrimSpace(plan.InstallCommand),
		PrebuildCommand:    strings.TrimSpace(plan.InstallCommand),
		SetupCommands:      cloneStringSlice(plan.SetupCommands),
		BuildCommand:       strings.TrimSpace(plan.BuildCommand),
		PostBuildCommands:  cloneStringSlice(plan.PostBuildCommands),
		RunCommand:         strings.TrimSpace(plan.RunCommand),
		RuntimeInitCommand: strings.TrimSpace(plan.RuntimeInitCommand),
		ExposePort:         strings.TrimSpace(plan.ExposePort),
		BuildContextDir:    normalizePlanDirOrDefault(plan.BuildContextDir, "."),
		AppDir:             normalizePlanDirOrDefault(plan.AppDir, "."),
		ValidationWarnings: cloneStringSlice(plan.ValidationWarnings),
		UseStaticRuntime:   plan.UseStaticRuntime,
		StaticOutputDir:    strings.TrimSpace(plan.StaticOutputDir),
		DockerfileContent:  dockerfile,
	}
	cfg.NormalizePhaseAliases()
	return cfg, nil
}

func manualBuildPlanFromConfig(opts AutoDetectOptions, cfg BuildConfig) (buildPlan, error) {
	cfg.NormalizePhaseAliases()

	repoRoot := strings.TrimSpace(opts.RepoRoot)
	if repoRoot == "" {
		return buildPlan{}, fmt.Errorf("repository root is required")
	}

	appDirInput := strings.TrimSpace(cfg.AppDir)
	if appDirInput == "" {
		appDirInput = opts.WorkingDir
	}
	appDir, err := normalizeRelativeDir(appDirInput)
	if err != nil {
		return buildPlan{}, err
	}

	appPath := repoRoot
	if appDir != "." {
		appPath = filepath.Join(repoRoot, filepath.FromSlash(appDir))
	}

	detectedRuntime, detectedVersion := DetectRuntimeWithContext(repoRoot, appPath)
	runtime := strings.TrimSpace(cfg.Runtime)
	if runtime == "" {
		runtime = detectedRuntime
	}
	if runtime == "unknown" || runtime == "" {
		return buildPlan{}, fmt.Errorf("could not determine runtime for submitted build config")
	}

	version := strings.TrimSpace(cfg.Version)
	if version == "" {
		if runtime == detectedRuntime && detectedVersion != "" {
			version = detectedVersion
		} else {
			version = defaultVersionForRuntime(runtime)
		}
	}

	buildContextDir := strings.TrimSpace(cfg.BuildContextDir)
	if buildContextDir != "" {
		buildContextDir, err = normalizeRelativeDir(buildContextDir)
		if err != nil {
			return buildPlan{}, fmt.Errorf("invalid build context dir: %w", err)
		}
	}

	switch {
	case runtime == "node" || runtime == "bun":
		return manualJavaScriptBuildPlan(repoRoot, appDir, appPath, runtime, version, buildContextDir, cfg)
	case runtime == "static" && (detectedRuntime == "node" || detectedRuntime == "bun"):
		jsVersion := strings.TrimSpace(cfg.Version)
		if jsVersion == "" {
			jsVersion = defaultString(detectedVersion, defaultVersionForRuntime(detectedRuntime))
		}
		return manualJavaScriptBuildPlan(repoRoot, appDir, appPath, detectedRuntime, jsVersion, buildContextDir, cfg)
	default:
		return manualRuntimeBuildPlan(appPath, runtime, version, appDir, buildContextDir, cfg)
	}
}

func manualJavaScriptBuildPlan(repoRoot, appDir, appPath, runtime, version, buildContextDir string, cfg BuildConfig) (buildPlan, error) {
	ctx := newJSProjectContext(repoRoot, appDir, appPath, runtime, version)
	if buildContextDir != "" {
		ctx.BuildContextDir = buildContextDir
		ctx.BuildContextPath = repoRoot
		if buildContextDir != "." {
			ctx.BuildContextPath = filepath.Join(repoRoot, filepath.FromSlash(buildContextDir))
		}
		ctx.appWorkDir = containerRelativeDir(buildContextDir, appDir)
	}

	framework := strings.TrimSpace(cfg.Framework)
	if framework == "" {
		framework = detectJSFramework(ctx)
	}

	buildScript := selectJSBuildScript(ctx.AppMetadata)
	if framework == "angular" && detectAngularSSR(ctx) && hasNodeScript(ctx.AppMetadata.Scripts, "build:ssr") {
		buildScript = "build:ssr"
	}
	runScript := selectJSRunScript(ctx.AppMetadata)
	installCommand := strings.TrimSpace(cfg.InstallCommand)
	if installCommand == "" {
		installCommand = detectJavaScriptInstallCommand(ctx)
	}

	buildCommand := strings.TrimSpace(cfg.BuildCommand)
	if buildCommand == "" && buildScript != "" {
		buildCommand = prefixCommand(ctx.appWorkDir, packageManagerScriptCommand(ctx.PackageManager, buildScript))
	}

	runCommand := strings.TrimSpace(cfg.RunCommand)
	if runCommand == "" {
		switch {
		case runtime == "node" && framework == "next":
			port := inferJavaScriptExposePort(ctx, framework, runScript)
			runCommand = prefixCommand(ctx.appWorkDir, fmt.Sprintf("./node_modules/.bin/next start --hostname 0.0.0.0 --port ${PORT:-%s}", port))
		case runtime == "node" && framework == "angular":
			port := inferJavaScriptExposePort(ctx, framework, runScript)
			if detectAngularSSR(ctx) && port == "4200" {
				port = "4000"
			}
			runCommand = detectAngularRunCommand(ctx, port)
		case runtime == "node" && framework == "astro":
			runCommand = detectAstroRunCommand(ctx, inferJavaScriptExposePort(ctx, framework, runScript))
		case runtime == "node" && framework == "remix":
			runCommand = detectRemixRunCommand(ctx, inferJavaScriptExposePort(ctx, framework, runScript))
		case runtime == "node" && framework == "nuxt":
			runCommand = detectJavaScriptRunCommand(ctx, runScript)
			if strings.TrimSpace(runCommand) == "" {
				port := inferJavaScriptExposePort(ctx, framework, runScript)
				runCommand = prefixCommand(ctx.appWorkDir, fmt.Sprintf("HOST=0.0.0.0 PORT=${PORT:-%s} node .output/server/index.mjs", port))
			}
		default:
			runCommand = detectJavaScriptRunCommand(ctx, runScript)
		}
	}

	plan := buildPlan{
		Runtime:            runtime,
		Framework:          framework,
		Version:            version,
		InstallCommand:     installCommand,
		DependencyFiles:    nil,
		SetupCommands:      mergeUniqueCommands(detectJavaScriptSetupCommands(ctx), cfg.SetupCommands),
		BuildCommand:       buildCommand,
		PostBuildCommands:  cloneStringSlice(cfg.PostBuildCommands),
		RunCommand:         runCommand,
		RuntimeInitCommand: strings.TrimSpace(cfg.RuntimeInitCommand),
		BuildContextDir:    defaultString(ctx.BuildContextDir, appDir, "."),
		AppDir:             appDir,
		ValidationWarnings: cloneStringSlice(cfg.ValidationWarnings),
		BuilderImage:       selectJavaScriptBuilderImage(runtime, version),
		BootstrapCommands:  detectJavaScriptBootstrapCommands(ctx),
		RuntimeEnv: map[string]string{
			"HOST": "0.0.0.0",
		},
		AptPackages: detectJavaScriptSystemPackages(ctx.AppMetadata),
		appWorkDir:  ctx.appWorkDir,
	}
	if canInstallWithoutFullSource(ctx) {
		plan.DependencyFiles = detectJavaScriptDependencyFiles(ctx)
	}
	if shouldAutoInstallNextSharp(ctx, framework) {
		plan.AptPackages = appendJavaScriptSystemPackages(plan.AptPackages, "ca-certificates", "git", "openssl", "python3", "make", "g++", "pkg-config")
		plan.ValidationWarnings = appendUniqueString(plan.ValidationWarnings, "Next.js app does not declare sharp; builder will install it for production image optimization")
	}

	if runtime != "bun" {
		plan.RuntimeEnv["NODE_ENV"] = "production"
	}
	if detectJavaScriptPlaywright(ctx.AppMetadata) {
		plan.SetupCommands = appendUniqueString(plan.SetupCommands, prefixCommand(ctx.appWorkDir, jsExecCommand(runtime, "playwright install chromium")))
	}
	if plan.RuntimeInitCommand == "" && detectJavaScriptPrisma(ctx.AppPath, ctx.BuildContextPath, ctx.AppMetadata) {
		plan.RuntimeInitCommand = prefixCommand(ctx.appWorkDir, "if [ \"${PRISMA_RUN_MIGRATIONS:-0}\" = \"1\" ]; then "+jsExecCommand(runtime, "prisma migrate deploy")+"; fi")
	}

	if strings.TrimSpace(cfg.ExposePort) != "" {
		plan.ExposePort = strings.TrimSpace(cfg.ExposePort)
	} else {
		plan.ExposePort = inferExposePort(inferJavaScriptExposePort(ctx, framework, runScript), runCommand)
	}

	useStaticRuntime := strings.EqualFold(strings.TrimSpace(cfg.Runtime), "static") || shouldUseStaticRuntime(ctx, framework, buildScript, runScript)
	if useStaticRuntime {
		if strings.TrimSpace(plan.BuildCommand) == "" {
			return buildPlan{}, fmt.Errorf("static frontend requires a build command")
		}
		if strings.TrimSpace(cfg.RunCommand) != "" {
			plan.ValidationWarnings = appendUniqueString(plan.ValidationWarnings, "ignoring submitted run command for static frontend runtime")
		}
		plan.Runtime = "static"
		plan.Framework = normalizeStaticFramework(framework)
		plan.RuntimeImage = "nginx:alpine"
		plan.StaticOutputDir = detectStaticOutputDir(ctx, framework)
		plan.UseStaticRuntime = true
		plan.RunCommand = ""
		plan.RuntimeInitCommand = ""
		plan.RuntimeEnv = nil
		if strings.TrimSpace(cfg.ExposePort) == "" {
			plan.ExposePort = "8080"
		}
		return plan, nil
	}

	if strings.TrimSpace(plan.RunCommand) == "" {
		return buildPlan{}, fmt.Errorf("no production run command detected")
	}
	plan.RuntimeEnv["PORT"] = plan.ExposePort
	return plan, nil
}

func manualRuntimeBuildPlan(appPath, runtime, version, appDir, buildContextDir string, cfg BuildConfig) (buildPlan, error) {
	detectedInstall, detectedBuild, detectedRun := detectCommandsWithoutAllowlist(appPath, runtime)
	plan, err := defaultBuildPlan(runtime, version, "", "", "")
	if err != nil {
		return buildPlan{}, err
	}

	plan.Framework = strings.TrimSpace(cfg.Framework)
	plan.InstallCommand = defaultString(strings.TrimSpace(cfg.InstallCommand), detectedInstall)
	plan.SetupCommands = cloneStringSlice(cfg.SetupCommands)
	plan.BuildCommand = defaultString(strings.TrimSpace(cfg.BuildCommand), detectedBuild)
	plan.PostBuildCommands = cloneStringSlice(cfg.PostBuildCommands)
	plan.RunCommand = defaultString(strings.TrimSpace(cfg.RunCommand), detectedRun)
	plan.RuntimeInitCommand = strings.TrimSpace(cfg.RuntimeInitCommand)
	plan.BuildContextDir = normalizePlanDirOrDefault(buildContextDir, appDir)
	plan.AppDir = appDir
	plan.ValidationWarnings = cloneStringSlice(cfg.ValidationWarnings)

	switch runtime {
	case "python":
		plan.AptPackages = detectPythonSystemPackages(appPath)
		plan.SetupCommands = mergeUniqueCommands(detectPythonSetupCommands(appPath), plan.SetupCommands)
	case "elixir":
		if plan.RuntimeEnv == nil {
			plan.RuntimeEnv = map[string]string{"MIX_ENV": "prod"}
		}
		if isPhoenixProject(appPath) {
			plan.RuntimeEnv["PHX_SERVER"] = "true"
		}
	case "php":
		if err := applyPHPPlanDefaults(appPath, &plan); err != nil {
			return buildPlan{}, err
		}
	}

	if strings.TrimSpace(cfg.ExposePort) != "" {
		plan.ExposePort = strings.TrimSpace(cfg.ExposePort)
	} else {
		defaultPort := defaultExposePort(runtime)
		if runtime == "php" && strings.TrimSpace(plan.RuntimeFlavor) == "cli" {
			defaultPort = ""
		}
		plan.ExposePort = inferExposePort(defaultPort, plan.RunCommand)
	}
	if runtime == "php" {
		if plan.RuntimeEnv == nil {
			plan.RuntimeEnv = map[string]string{"APP_ENV": "production"}
		}
		switch strings.TrimSpace(plan.RuntimeFlavor) {
		case "apache":
			plan.RuntimeEnv["PORT"] = plan.ExposePort
			if strings.TrimSpace(cfg.RuntimeInitCommand) == "" {
				plan.RuntimeInitCommand = detectPHPRuntimeInitCommand(plan.ExposePort)
			}
		case "fpm":
			plan.RuntimeEnv["PORT"] = plan.ExposePort
			if strings.TrimSpace(cfg.RuntimeInitCommand) == "" {
				plan.RuntimeInitCommand = detectPHPFPMRuntimeInitCommand(plan.ExposePort)
			}
		default:
			delete(plan.RuntimeEnv, "PORT")
			if plan.ExposePort != "" {
				plan.RuntimeEnv["PORT"] = plan.ExposePort
			}
			if strings.TrimSpace(cfg.RuntimeInitCommand) == "" {
				plan.RuntimeInitCommand = ""
			}
		}
	}
	if runtime == "elixir" {
		if plan.RuntimeEnv == nil {
			plan.RuntimeEnv = map[string]string{"MIX_ENV": "prod"}
		}
		if plan.ExposePort != "" {
			plan.RuntimeEnv["PORT"] = plan.ExposePort
		}
	}

	if runtime == "static" {
		if strings.TrimSpace(plan.InstallCommand) != "" || len(plan.SetupCommands) > 0 || strings.TrimSpace(plan.BuildCommand) != "" || len(plan.PostBuildCommands) > 0 {
			return buildPlan{}, fmt.Errorf("static runtime with build phases requires a detected JavaScript build context or a custom Dockerfile")
		}
		plan.RuntimeImage = "nginx:alpine"
		plan.ExposePort = defaultString(strings.TrimSpace(cfg.ExposePort), "8080")
		plan.UseStaticRuntime = true
		return plan, nil
	}

	if strings.TrimSpace(plan.RunCommand) == "" {
		return buildPlan{}, fmt.Errorf("run command is required for runtime %s", runtime)
	}
	return plan, nil
}

func detectCommandsWithoutAllowlist(repoPath, runtime string) (string, string, string) {
	switch runtime {
	case "static":
		return "", "", ""
	case "node":
		metadata := loadNodePackageJSON(repoPath)
		packageManager := detectNodePackageManager(repoPath, metadata)
		scripts := map[string]string{}
		if metadata != nil && metadata.Scripts != nil {
			scripts = metadata.Scripts
		}
		return pickFirstNonEmpty(nodePrebuildCandidates(repoPath, packageManager)),
			pickFirstNonEmpty(nodeBuildCandidates(packageManager, scripts)),
			pickFirstNonEmpty(nodeRunCandidates(repoPath, packageManager, scripts))
	case "bun":
		return "bun install", "bun run build", "bun run start"
	case "python":
		return pickFirstNonEmpty(pythonPrebuildCandidates(repoPath)),
			pickFirstNonEmpty(pythonBuildCandidates(repoPath)),
			pickFirstNonEmpty(pythonRunCandidates(repoPath))
	case "elixir":
		prebuildCandidates := []string{"MIX_ENV=prod mix deps.get", "mix deps.get"}
		if isDistilleryProject(repoPath) {
			prebuildCandidates = []string{"mix local.hex --force", "MIX_ENV=prod mix deps.get", "mix deps.get"}
			buildCandidates := []string{"MIX_ENV=prod mix distillery.release --env=prod"}
			return pickFirstNonEmpty(prebuildCandidates),
				pickFirstNonEmpty(buildCandidates),
				pickFirstNonEmpty(elixirDistilleryRunCandidates(repoPath))
		}
		hasRelease := hasElixirReleaseConfig(repoPath)
		buildCandidates := []string{"MIX_ENV=prod mix compile"}
		if hasRelease {
			buildCandidates = []string{"MIX_ENV=prod mix release", "MIX_ENV=prod mix compile"}
		}
		return pickFirstNonEmpty(prebuildCandidates),
			pickFirstNonEmpty(buildCandidates),
			pickFirstNonEmpty(elixirRunCandidates(repoPath, hasRelease))
	case "go":
		prebuildCandidates := []string{"go mod download"}
		if fileExists(filepath.Join(repoPath, "go.work")) {
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
		return pickFirstNonEmpty(prebuildCandidates), pickFirstNonEmpty(buildCandidates), pickFirstNonEmpty(runCandidates)
	case "rust":
		locked := fileExists(filepath.Join(repoPath, "Cargo.lock"))
		prebuildCandidates := []string{"cargo fetch"}
		buildCandidates := []string{"cargo build --release"}
		if locked {
			buildCandidates = []string{"cargo build --release --locked", "cargo build --release"}
		}
		runCandidates := []string{"./app"}
		return pickFirstNonEmpty(prebuildCandidates), pickFirstNonEmpty(buildCandidates), pickFirstNonEmpty(runCandidates)
	case "java":
		isGradle := fileExists(filepath.Join(repoPath, "build.gradle")) || fileExists(filepath.Join(repoPath, "build.gradle.kts"))
		hasMavenWrapper := fileExists(filepath.Join(repoPath, "mvnw"))
		hasGradleWrapper := fileExists(filepath.Join(repoPath, "gradlew"))
		if isGradle {
			prebuildCandidates := []string{"gradle dependencies"}
			buildCandidates := []string{"gradle build -x test"}
			if hasGradleWrapper {
				prebuildCandidates = []string{"chmod +x gradlew", "./gradlew dependencies", "gradle dependencies"}
				buildCandidates = []string{"./gradlew build -x test", "gradle build -x test"}
			}
			runCandidates := javaRunCandidates(repoPath, true)
			return pickFirstNonEmpty(prebuildCandidates), pickFirstNonEmpty(buildCandidates), pickFirstNonEmpty(runCandidates)
		}
		prebuildCandidates := []string{}
		buildCandidates := []string{"mvn -DoutputFile=target/mvn-dependency-list.log -B -DskipTests clean dependency:list install -Pproduction", "mvn install -DskipTests"}
		if hasMavenWrapper {
			prebuildCandidates = []string{"chmod +x mvnw"}
			buildCandidates = []string{
				"./mvnw -DoutputFile=target/mvn-dependency-list.log -B -DskipTests clean dependency:list install -Pproduction",
				"./mvnw install -DskipTests",
				"mvn -DoutputFile=target/mvn-dependency-list.log -B -DskipTests clean dependency:list install -Pproduction",
				"mvn install -DskipTests",
			}
		}
		runCandidates := javaRunCandidates(repoPath, false)
		return pickFirstNonEmpty(prebuildCandidates), pickFirstNonEmpty(buildCandidates), pickFirstNonEmpty(runCandidates)
	case "php":
		return pickFirstNonEmpty(phpInstallCandidates(repoPath)),
			pickFirstNonEmpty(phpBuildCandidates(repoPath)),
			pickFirstNonEmpty(phpRunCandidates(repoPath))
	default:
		return "", "", ""
	}
}

func pickFirstNonEmpty(candidates []string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func mergeUniqueCommands(primary, secondary []string) []string {
	if len(primary) == 0 && len(secondary) == 0 {
		return nil
	}
	merged := cloneStringSlice(primary)
	for _, command := range secondary {
		merged = appendUniqueString(merged, command)
	}
	return merged
}

func normalizePlanDirOrDefault(dir, fallback string) string {
	dir = strings.TrimSpace(dir)
	if dir != "" {
		return dir
	}
	fallback = strings.TrimSpace(fallback)
	if fallback != "" {
		return fallback
	}
	return "."
}

func defaultVersionForRuntime(runtime string) string {
	switch strings.TrimSpace(runtime) {
	case "bun":
		return "1.2"
	case "node":
		return "22"
	case "python":
		return "3.9"
	case "go":
		return "1.18"
	case "rust":
		return "stable"
	case "php":
		return "8.3"
	case "java":
		return "17"
	case "static":
		return "latest"
	default:
		return ""
	}
}

func defaultString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func containerRelativeDir(buildContextDir, appDir string) string {
	buildContextDir = normalizePlanDirOrDefault(buildContextDir, ".")
	appDir = normalizePlanDirOrDefault(appDir, ".")
	if buildContextDir == "." {
		return appDir
	}
	if appDir == "." || buildContextDir == appDir {
		return "."
	}
	rel, err := filepath.Rel(filepath.FromSlash(buildContextDir), filepath.FromSlash(appDir))
	if err != nil {
		return "."
	}
	rel = path.Clean(filepath.ToSlash(rel))
	if rel == "." || rel == "" || rel == ".." || strings.HasPrefix(rel, "../") {
		return "."
	}
	return rel
}
