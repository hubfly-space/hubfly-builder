package autodetect

import (
	"fmt"
	"sort"
	"strings"
)

// GenerateDockerfile creates Dockerfile content based on the runtime and version.
func GenerateDockerfile(runtime, version, prebuildCommand, buildCommand, runCommand string) ([]byte, error) {
	return GenerateDockerfileWithBuildEnv(runtime, version, prebuildCommand, buildCommand, runCommand, nil, nil)
}

// GenerateDockerfileWithBuildEnv creates Dockerfile content and wires build-time env support.
func GenerateDockerfileWithBuildEnv(runtime, version, prebuildCommand, buildCommand, runCommand string, buildArgKeys, secretBuildKeys []string) ([]byte, error) {
	plan, err := defaultBuildPlan(runtime, version, prebuildCommand, buildCommand, runCommand)
	if err != nil {
		return nil, err
	}
	return generateDockerfileForPlan(plan, buildArgKeys, secretBuildKeys)
}

func defaultBuildPlan(runtime, version, installCommand, buildCommand, runCommand string) (buildPlan, error) {
	switch runtime {
	case "node":
		return buildPlan{
			Runtime:        "node",
			Version:        version,
			InstallCommand: installCommand,
			BuildCommand:   buildCommand,
			RunCommand:     runCommand,
			ExposePort:     "3000",
			BuilderImage:   selectJavaScriptBuilderImage("node", version),
			RuntimeEnv: map[string]string{
				"HOST":     "0.0.0.0",
				"PORT":     "3000",
				"NODE_ENV": "production",
			},
		}, nil
	case "python":
		return buildPlan{
			Runtime:        "python",
			Version:        version,
			InstallCommand: installCommand,
			BuildCommand:   buildCommand,
			RunCommand:     runCommand,
			ExposePort:     "8000",
			BuilderImage:   "python:" + version + "-slim",
			RuntimeImage:   "python:" + version + "-slim",
		}, nil
	case "go":
		return buildPlan{
			Runtime:        "go",
			Version:        version,
			InstallCommand: installCommand,
			BuildCommand:   buildCommand,
			RunCommand:     runCommand,
			ExposePort:     "8080",
			BuilderImage:   "golang:" + version + "-alpine",
			RuntimeImage:   "alpine:3.20",
		}, nil
	case "bun":
		return buildPlan{
			Runtime:        "bun",
			Version:        version,
			InstallCommand: installCommand,
			BuildCommand:   buildCommand,
			RunCommand:     runCommand,
			ExposePort:     "3000",
			BuilderImage:   selectJavaScriptBuilderImage("bun", version),
			RuntimeEnv: map[string]string{
				"HOST": "0.0.0.0",
				"PORT": "3000",
			},
		}, nil
	case "java":
		return buildPlan{
			Runtime:        "java",
			Version:        version,
			InstallCommand: installCommand,
			BuildCommand:   buildCommand,
			RunCommand:     runCommand,
			ExposePort:     "8080",
			BuilderImage:   selectJavaBaseImage(version, installCommand, buildCommand),
			RuntimeImage:   selectJavaRuntimeImage(version),
		}, nil
	case "php":
		return buildPlan{
			Runtime:        "php",
			RuntimeFlavor:  "apache",
			Version:        version,
			InstallCommand: installCommand,
			BuildCommand:   buildCommand,
			RunCommand:     runCommand,
			ExposePort:     "8080",
			BuilderImage:   selectPHPBaseImage(version, "apache"),
			RuntimeEnv: map[string]string{
				"APP_ENV": "production",
				"PORT":    "8080",
			},
		}, nil
	case "static":
		return buildPlan{
			Runtime:          "static",
			Version:          version,
			ExposePort:       "8080",
			RuntimeImage:     "nginx:alpine",
			StaticOutputDir:  ".",
			UseStaticRuntime: true,
		}, nil
	default:
		return buildPlan{}, fmt.Errorf("unsupported runtime: %s", runtime)
	}
}

func selectJavaBaseImage(version, prebuildCommand, buildCommand string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "17"
	}

	combined := strings.ToLower(strings.TrimSpace(prebuildCommand + " " + buildCommand))
	switch {
	case strings.Contains(combined, "gradle"), strings.Contains(combined, "./gradlew"):
		return "gradle:8-jdk" + version
	case strings.Contains(combined, "mvn"), strings.Contains(combined, "./mvnw"):
		return "maven:3.9-eclipse-temurin-" + version
	default:
		return "eclipse-temurin:" + version + "-jdk"
	}
}

func selectJavaRuntimeImage(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "17"
	}
	return "eclipse-temurin:" + version + "-jre"
}

func generateDockerfileForPlan(plan buildPlan, buildArgKeys, secretBuildKeys []string) ([]byte, error) {
	buildArgKeys = normalizeKeys(buildArgKeys)
	secretBuildKeys = normalizeKeys(secretBuildKeys)

	switch {
	case plan.UseStaticRuntime:
		return []byte(strings.TrimSpace(renderStaticDockerfile(plan, buildArgKeys, secretBuildKeys)) + "\n"), nil
	case plan.Runtime == "php":
		return []byte(strings.TrimSpace(renderPHPDockerfile(plan, buildArgKeys, secretBuildKeys)) + "\n"), nil
	case plan.Runtime == "python":
		return []byte(strings.TrimSpace(renderPythonDockerfile(plan, buildArgKeys, secretBuildKeys)) + "\n"), nil
	case plan.Runtime == "go":
		return []byte(strings.TrimSpace(renderGoDockerfile(plan, buildArgKeys, secretBuildKeys)) + "\n"), nil
	case strings.TrimSpace(plan.BuilderImage) != "":
		return []byte(strings.TrimSpace(renderApplicationDockerfile(plan, buildArgKeys, secretBuildKeys)) + "\n"), nil
	default:
		return nil, fmt.Errorf("unsupported runtime: %s", plan.Runtime)
	}
}

func renderApplicationDockerfile(plan buildPlan, buildArgKeys, secretBuildKeys []string) string {
	if !shouldUseMultiStage(plan) {
		return renderSingleStageDockerfile(plan, buildArgKeys, secretBuildKeys)
	}

	var builder strings.Builder
	builderImage := strings.TrimSpace(plan.BuilderImage)
	fmt.Fprintf(&builder, "FROM %s AS builder\n\n", builderImage)
	builder.WriteString("WORKDIR /app\n\n")

	if argLines := renderArgLines(buildArgKeys); argLines != "" {
		builder.WriteString(argLines)
	}
	cacheMounts := buildCacheMounts(plan)
	if len(cacheMounts) > 0 && !containsKey(buildArgKeys, "HBF_CACHE_ID") {
		builder.WriteString("ARG HBF_CACHE_ID=default\n\n")
	}
	if aptLine := renderAptInstallLine(plan.AptPackages); aptLine != "" {
		builder.WriteString(aptLine)
	}
	for _, command := range plan.BootstrapCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}

	preSetupCommands, postSetupCommands := splitSetupCommands(plan)

	depFiles := normalizeDependencyFiles(plan.DependencyFiles)
	if len(depFiles) > 0 {
		builder.WriteString("COPY ")
		builder.WriteString(strings.Join(depFiles, " "))
		builder.WriteString(" ./\n\n")
		for _, command := range []string{plan.InstallCommand} {
			if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
				builder.WriteString(runLine)
			}
		}
		for _, command := range preSetupCommands {
			if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
				builder.WriteString(runLine)
			}
		}
		builder.WriteString("COPY . .\n\n")
	} else {
		builder.WriteString("COPY . .\n\n")
		for _, command := range []string{plan.InstallCommand} {
			if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
				builder.WriteString(runLine)
			}
		}
		postSetupCommands = append(preSetupCommands, postSetupCommands...)
	}
	for _, command := range postSetupCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}
	if runLine := renderRunLineWithCaches(plan.BuildCommand, cacheMounts, secretBuildKeys); runLine != "" {
		builder.WriteString(runLine)
	}
	for _, command := range plan.PostBuildCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}

	if prune := runtimePruneCommand(plan); prune != "" {
		builder.WriteString(prune)
	}

	builder.WriteString("\n")

	runtimeImage := strings.TrimSpace(plan.RuntimeImage)
	if runtimeImage == "" {
		runtimeImage = builderImage
	}
	fmt.Fprintf(&builder, "FROM %s\n\n", runtimeImage)
	builder.WriteString("WORKDIR /app\n\n")

	if aptLine := renderAptInstallLine(runtimeAptPackages(plan)); aptLine != "" {
		builder.WriteString(aptLine)
	}
	builder.WriteString("COPY --from=builder /app/ /app/\n\n")

	if envLines := renderEnvLines(plan.RuntimeEnv); envLines != "" {
		builder.WriteString("\n")
		builder.WriteString(envLines)
	}
	if strings.TrimSpace(plan.ExposePort) != "" {
		fmt.Fprintf(&builder, "\nEXPOSE %s\n", strings.TrimSpace(plan.ExposePort))
	}
	if cmdLine := renderCmdLine(plan.RunCommand, plan.RuntimeInitCommand); cmdLine != "" {
		builder.WriteString("\n")
		builder.WriteString(cmdLine)
	}

	return builder.String()
}

func renderSingleStageDockerfile(plan buildPlan, buildArgKeys, secretBuildKeys []string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "FROM %s\n\n", strings.TrimSpace(plan.BuilderImage))
	builder.WriteString("WORKDIR /app\n\n")

	if argLines := renderArgLines(buildArgKeys); argLines != "" {
		builder.WriteString(argLines)
	}
	cacheMounts := buildCacheMounts(plan)
	if len(cacheMounts) > 0 && !containsKey(buildArgKeys, "HBF_CACHE_ID") {
		builder.WriteString("ARG HBF_CACHE_ID=default\n\n")
	}
	if aptLine := renderAptInstallLine(plan.AptPackages); aptLine != "" {
		builder.WriteString(aptLine)
	}
	for _, command := range plan.BootstrapCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}

	preSetupCommands, postSetupCommands := splitSetupCommands(plan)

	depFiles := normalizeDependencyFiles(plan.DependencyFiles)
	if len(depFiles) > 0 {
		builder.WriteString("COPY ")
		builder.WriteString(strings.Join(depFiles, " "))
		builder.WriteString(" ./\n\n")
		for _, command := range []string{plan.InstallCommand} {
			if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
				builder.WriteString(runLine)
			}
		}
		for _, command := range preSetupCommands {
			if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
				builder.WriteString(runLine)
			}
		}
		builder.WriteString("COPY . .\n\n")
	} else {
		builder.WriteString("COPY . .\n\n")
		for _, command := range []string{plan.InstallCommand} {
			if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
				builder.WriteString(runLine)
			}
		}
		postSetupCommands = append(preSetupCommands, postSetupCommands...)
	}
	for _, command := range postSetupCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}
	if runLine := renderRunLineWithCaches(plan.BuildCommand, cacheMounts, secretBuildKeys); runLine != "" {
		builder.WriteString(runLine)
	}
	for _, command := range plan.PostBuildCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}

	if prune := runtimePruneCommand(plan); prune != "" {
		builder.WriteString(prune)
	}

	if envLines := renderEnvLines(plan.RuntimeEnv); envLines != "" {
		builder.WriteString("\n")
		builder.WriteString(envLines)
	}
	if strings.TrimSpace(plan.ExposePort) != "" {
		fmt.Fprintf(&builder, "\nEXPOSE %s\n", strings.TrimSpace(plan.ExposePort))
	}
	if cmdLine := renderCmdLine(plan.RunCommand, plan.RuntimeInitCommand); cmdLine != "" {
		builder.WriteString("\n")
		builder.WriteString(cmdLine)
	}

	return builder.String()
}

func normalizeDependencyFiles(files []string) []string {
	if len(files) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(files))
	out := make([]string, 0, len(files))
	for _, file := range files {
		file = strings.TrimSpace(file)
		if file == "" || strings.HasPrefix(file, "/") {
			continue
		}
		if _, ok := seen[file]; ok {
			continue
		}
		seen[file] = struct{}{}
		out = append(out, file)
	}
	return out
}

func runtimeAptPackages(plan buildPlan) []string {
	switch strings.TrimSpace(strings.ToLower(plan.Runtime)) {
	case "python":
		return plan.AptPackages
	default:
		return nil
	}
}

func shouldUseMultiStage(plan buildPlan) bool {
	switch strings.TrimSpace(strings.ToLower(plan.Runtime)) {
	case "php":
		return false
	default:
		return true
	}
}

func runtimePruneCommand(plan buildPlan) string {
	switch strings.TrimSpace(strings.ToLower(plan.Runtime)) {
	case "node":
		return "RUN rm -rf .git .next/cache .turbo .cache node_modules/.cache && " +
			"if [ -f package-lock.json ] || [ -f npm-shrinkwrap.json ]; then rm -rf node_modules && npm ci --omit=dev; " +
			"elif [ -f pnpm-lock.yaml ]; then rm -rf node_modules && corepack enable && pnpm install --prod --frozen-lockfile; " +
			"elif [ -f yarn.lock ]; then rm -rf node_modules && corepack enable && yarn install --production --frozen-lockfile; " +
			"else npm prune --omit=dev || npm install --omit=dev; " +
			"fi && rm -rf /root/.npm /root/.cache /tmp/*\n"
	case "bun":
		return "RUN rm -rf .git .next/cache .turbo .cache node_modules/.cache && " +
			"if [ -f bun.lockb ] || [ -f bun.lock ]; then rm -rf node_modules && bun install --production --frozen-lockfile; " +
			"fi && rm -rf /root/.bun /root/.cache /tmp/*\n"
	default:
		return ""
	}
}

func renderPythonDockerfile(plan buildPlan, buildArgKeys, secretBuildKeys []string) string {
	var builder strings.Builder
	builderImage := strings.TrimSpace(plan.BuilderImage)
	fmt.Fprintf(&builder, "FROM %s AS builder\n\n", builderImage)
	builder.WriteString("WORKDIR /app\n\n")

	if argLines := renderArgLines(buildArgKeys); argLines != "" {
		builder.WriteString(argLines)
	}
	if aptLine := renderAptInstallLine(plan.AptPackages); aptLine != "" {
		builder.WriteString(aptLine)
	}

	builder.WriteString("RUN python -m venv /opt/venv\n")
	builder.WriteString("ENV VIRTUAL_ENV=/opt/venv\n")
	builder.WriteString("ENV PATH=\"/opt/venv/bin:$PATH\"\n\n")

	builder.WriteString("COPY . .\n\n")

	for _, command := range plan.BootstrapCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}
	if runLine := renderRunLine(plan.InstallCommand, secretBuildKeys); runLine != "" {
		builder.WriteString(runLine)
	}
	for _, command := range plan.SetupCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}
	if runLine := renderRunLine(plan.BuildCommand, secretBuildKeys); runLine != "" {
		builder.WriteString(runLine)
	}
	for _, command := range plan.PostBuildCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}
	builder.WriteString("RUN rm -rf /root/.cache/pip /tmp/*\n\n")

	runtimeImage := strings.TrimSpace(plan.RuntimeImage)
	if runtimeImage == "" {
		runtimeImage = builderImage
	}
	fmt.Fprintf(&builder, "FROM %s\n\n", runtimeImage)
	builder.WriteString("WORKDIR /app\n\n")

	if aptLine := renderAptInstallLine(plan.AptPackages); aptLine != "" {
		builder.WriteString(aptLine)
	}
	builder.WriteString("ENV VIRTUAL_ENV=/opt/venv\n")
	builder.WriteString("ENV PATH=\"/opt/venv/bin:$PATH\"\n\n")
	builder.WriteString("COPY --from=builder /opt/venv /opt/venv\n")
	builder.WriteString("COPY --from=builder /app/ /app/\n\n")

	if envLines := renderEnvLines(plan.RuntimeEnv); envLines != "" {
		builder.WriteString("\n")
		builder.WriteString(envLines)
	}
	if strings.TrimSpace(plan.ExposePort) != "" {
		fmt.Fprintf(&builder, "\nEXPOSE %s\n", strings.TrimSpace(plan.ExposePort))
	}
	if cmdLine := renderCmdLine(plan.RunCommand, plan.RuntimeInitCommand); cmdLine != "" {
		builder.WriteString("\n")
		builder.WriteString(cmdLine)
	}
	return builder.String()
}

func renderGoDockerfile(plan buildPlan, buildArgKeys, secretBuildKeys []string) string {
	if !shouldUseGoMultiStage(plan) {
		return renderSingleStageDockerfile(plan, buildArgKeys, secretBuildKeys)
	}

	var builder strings.Builder
	builderImage := strings.TrimSpace(plan.BuilderImage)
	fmt.Fprintf(&builder, "FROM %s AS builder\n\n", builderImage)
	builder.WriteString("WORKDIR /app\n\n")

	if argLines := renderArgLines(buildArgKeys); argLines != "" {
		builder.WriteString(argLines)
	}
	if aptLine := renderAptInstallLine(plan.AptPackages); aptLine != "" {
		builder.WriteString(aptLine)
	}
	builder.WriteString("COPY . .\n\n")
	for _, command := range plan.BootstrapCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}
	if runLine := renderRunLine(plan.InstallCommand, secretBuildKeys); runLine != "" {
		builder.WriteString(runLine)
	}
	for _, command := range plan.SetupCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}
	if runLine := renderRunLine(plan.BuildCommand, secretBuildKeys); runLine != "" {
		builder.WriteString(runLine)
	}
	for _, command := range plan.PostBuildCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}
	builder.WriteString("\n")

	runtimeImage := strings.TrimSpace(plan.RuntimeImage)
	if runtimeImage == "" {
		runtimeImage = builderImage
	}
	fmt.Fprintf(&builder, "FROM %s\n\n", runtimeImage)
	builder.WriteString("WORKDIR /app\n\n")

	if runtimeInstall := renderGoRuntimeInstall(runtimeImage); runtimeInstall != "" {
		builder.WriteString(runtimeInstall)
	}
	builder.WriteString("COPY --from=builder /app/ /app/\n\n")

	if envLines := renderEnvLines(plan.RuntimeEnv); envLines != "" {
		builder.WriteString("\n")
		builder.WriteString(envLines)
	}
	if strings.TrimSpace(plan.ExposePort) != "" {
		fmt.Fprintf(&builder, "\nEXPOSE %s\n", strings.TrimSpace(plan.ExposePort))
	}
	if cmdLine := renderCmdLine(plan.RunCommand, plan.RuntimeInitCommand); cmdLine != "" {
		builder.WriteString("\n")
		builder.WriteString(cmdLine)
	}
	return builder.String()
}

func shouldUseGoMultiStage(plan buildPlan) bool {
	run := strings.ToLower(strings.TrimSpace(plan.RunCommand))
	if strings.Contains(run, "go run") {
		return false
	}
	return true
}

func renderGoRuntimeInstall(runtimeImage string) string {
	image := strings.ToLower(strings.TrimSpace(runtimeImage))
	if strings.Contains(image, "alpine") {
		return "RUN apk add --no-cache ca-certificates\n\n"
	}
	return renderAptInstallLine([]string{"ca-certificates"})
}

func renderPHPDockerfile(plan buildPlan, buildArgKeys, secretBuildKeys []string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "FROM %s\n\n", strings.TrimSpace(plan.BuilderImage))
	builder.WriteString("WORKDIR /app\n\n")
	builder.WriteString("COPY --from=composer:2 /usr/bin/composer /usr/local/bin/composer\n\n")
	builder.WriteString("COPY . .\n\n")

	if argLines := renderArgLines(buildArgKeys); argLines != "" {
		builder.WriteString(argLines)
	}
	if aptLine := renderAptInstallLine(plan.AptPackages); aptLine != "" {
		builder.WriteString(aptLine)
	}
	for _, command := range plan.BootstrapCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}
	if strings.TrimSpace(plan.RuntimeFlavor) == "apache" {
		if docroot := renderPHPDocrootSetup(plan.DocumentRoot); docroot != "" {
			builder.WriteString(docroot)
		}
	}
	if strings.TrimSpace(plan.RuntimeFlavor) == "fpm" {
		builder.WriteString(renderPHPFPMNginxTemplate(plan.DocumentRoot))
	}
	if runLine := renderRunLine(plan.InstallCommand, secretBuildKeys); runLine != "" {
		builder.WriteString(runLine)
	}
	for _, command := range plan.SetupCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}
	if runLine := renderRunLine(plan.BuildCommand, secretBuildKeys); runLine != "" {
		builder.WriteString(runLine)
	}
	for _, command := range plan.PostBuildCommands {
		if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
	}

	if envLines := renderEnvLines(plan.RuntimeEnv); envLines != "" {
		builder.WriteString("\n")
		builder.WriteString(envLines)
	}
	if strings.TrimSpace(plan.ExposePort) != "" {
		fmt.Fprintf(&builder, "\nEXPOSE %s\n", strings.TrimSpace(plan.ExposePort))
	}
	if cmdLine := renderCmdLine(plan.RunCommand, plan.RuntimeInitCommand); cmdLine != "" {
		builder.WriteString("\n")
		builder.WriteString(cmdLine)
	}

	return builder.String()
}

func renderStaticDockerfile(plan buildPlan, buildArgKeys, secretBuildKeys []string) string {
	var builder strings.Builder

	if strings.TrimSpace(plan.BuilderImage) != "" {
		fmt.Fprintf(&builder, "FROM %s AS builder\n\n", strings.TrimSpace(plan.BuilderImage))
		builder.WriteString("WORKDIR /app\n\n")

		if argLines := renderArgLines(buildArgKeys); argLines != "" {
			builder.WriteString(argLines)
		}
		cacheMounts := buildCacheMounts(plan)
		if len(cacheMounts) > 0 && !containsKey(buildArgKeys, "HBF_CACHE_ID") {
			builder.WriteString("ARG HBF_CACHE_ID=default\n\n")
		}
		if aptLine := renderAptInstallLine(plan.AptPackages); aptLine != "" {
			builder.WriteString(aptLine)
		}
		for _, command := range plan.BootstrapCommands {
			if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
				builder.WriteString(runLine)
			}
		}

		preSetupCommands, postSetupCommands := splitSetupCommands(plan)
		depFiles := normalizeDependencyFiles(plan.DependencyFiles)
		if len(depFiles) > 0 {
			builder.WriteString("COPY ")
			builder.WriteString(strings.Join(depFiles, " "))
			builder.WriteString(" ./\n\n")
			if runLine := renderRunLine(plan.InstallCommand, secretBuildKeys); runLine != "" {
				builder.WriteString(runLine)
			}
			for _, command := range preSetupCommands {
				if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
					builder.WriteString(runLine)
				}
			}
			builder.WriteString("COPY . .\n\n")
		} else {
			builder.WriteString("COPY . .\n\n")
			if runLine := renderRunLine(plan.InstallCommand, secretBuildKeys); runLine != "" {
				builder.WriteString(runLine)
			}
			postSetupCommands = append(preSetupCommands, postSetupCommands...)
		}
		for _, command := range postSetupCommands {
			if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
				builder.WriteString(runLine)
			}
		}
		if runLine := renderRunLineWithCaches(plan.BuildCommand, cacheMounts, secretBuildKeys); runLine != "" {
			builder.WriteString(runLine)
		}
		for _, command := range plan.PostBuildCommands {
			if runLine := renderRunLine(command, secretBuildKeys); runLine != "" {
				builder.WriteString(runLine)
			}
		}
		builder.WriteString("\n")
	}

	runtimeImage := strings.TrimSpace(plan.RuntimeImage)
	if runtimeImage == "" {
		runtimeImage = "nginx:alpine"
	}
	fmt.Fprintf(&builder, "FROM %s\n\n", runtimeImage)
	builder.WriteString("WORKDIR /usr/share/nginx/html\n\n")

	if strings.TrimSpace(plan.BuilderImage) != "" {
		outputDir := strings.TrimSpace(plan.StaticOutputDir)
		if outputDir == "" || outputDir == "." {
			builder.WriteString("COPY --from=builder /app/ ./\n\n")
		} else {
			fmt.Fprintf(&builder, "COPY --from=builder /app/%s/ ./\n\n", strings.TrimPrefix(outputDir, "/"))
		}
	} else {
		builder.WriteString("COPY . .\n\n")
	}

	exposePort := strings.TrimSpace(plan.ExposePort)
	if exposePort == "" {
		exposePort = "8080"
	}

	builder.WriteString("RUN rm -f /etc/nginx/conf.d/default.conf && mkdir -p /etc/nginx/templates && cat <<'EOF' > /etc/nginx/templates/default.conf.template\n")
	builder.WriteString("server {\n")
	builder.WriteString("  listen 80;\n")
	if exposePort != "80" {
		fmt.Fprintf(&builder, "  listen %s;\n", exposePort)
	}
	builder.WriteString("  server_name _;\n")
	builder.WriteString("  root /usr/share/nginx/html;\n")
	builder.WriteString("  index index.html;\n")
	builder.WriteString("  location / {\n")
	builder.WriteString("    try_files $uri $uri/ /index.html;\n")
	builder.WriteString("  }\n")
	builder.WriteString("}\n")
	builder.WriteString("EOF\n")

	fmt.Fprintf(&builder, "\nENV PORT=%s\n\n", exposePort)
	builder.WriteString("EXPOSE 80\n")
	if exposePort != "80" {
		fmt.Fprintf(&builder, "EXPOSE %s\n", exposePort)
	}
	builder.WriteString("\n")
	builder.WriteString("CMD [\"nginx\", \"-g\", \"daemon off;\"]\n")
	return builder.String()
}

func renderArgLines(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&builder, "ARG %s\n", key)
	}
	builder.WriteString("\n")
	return builder.String()
}

func renderRunLine(command string, secretBuildKeys []string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}

	if len(secretBuildKeys) == 0 {
		return fmt.Sprintf("RUN %s\n", command)
	}

	mountFlags := make([]string, 0, len(secretBuildKeys))
	exports := make([]string, 0, len(secretBuildKeys))
	for _, key := range secretBuildKeys {
		mountFlags = append(mountFlags, fmt.Sprintf("--mount=type=secret,id=%s", key))
		exports = append(exports, fmt.Sprintf("export %s=\"$(cat /run/secrets/%s)\";", key, key))
	}

	payload := "set -e; " + strings.Join(exports, " ") + " " + command
	return fmt.Sprintf("RUN %s sh -c '%s'\n", strings.Join(mountFlags, " "), escapeSingleQuotes(payload))
}

type cacheMount struct {
	Target string
	ID     string
}

func renderRunLineWithCaches(command string, caches []cacheMount, secretBuildKeys []string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}

	mountFlags := make([]string, 0, len(secretBuildKeys)+len(caches))
	for _, cache := range caches {
		target := strings.TrimSpace(cache.Target)
		if target == "" {
			continue
		}
		flag := fmt.Sprintf("--mount=type=cache,target=%s", target)
		if id := strings.TrimSpace(cache.ID); id != "" {
			flag += ",id=" + id
		}
		mountFlags = append(mountFlags, flag)
	}

	if len(secretBuildKeys) == 0 {
		if len(mountFlags) == 0 {
			return fmt.Sprintf("RUN %s\n", command)
		}
		return fmt.Sprintf("RUN %s %s\n", strings.Join(mountFlags, " "), command)
	}

	exports := make([]string, 0, len(secretBuildKeys))
	for _, key := range secretBuildKeys {
		mountFlags = append(mountFlags, fmt.Sprintf("--mount=type=secret,id=%s", key))
		exports = append(exports, fmt.Sprintf("export %s=\"$(cat /run/secrets/%s)\";", key, key))
	}

	payload := "set -e; " + strings.Join(exports, " ") + " " + command
	return fmt.Sprintf("RUN %s sh -c '%s'\n", strings.Join(mountFlags, " "), escapeSingleQuotes(payload))
}

func buildCacheMounts(plan buildPlan) []cacheMount {
	if strings.TrimSpace(plan.BuildCommand) == "" {
		return nil
	}

	framework := strings.ToLower(strings.TrimSpace(plan.Framework))
	switch framework {
	case "next":
		return []cacheMount{
			cacheMountFor(plan, ".next/cache", cacheIDSuffix(framework, "next-cache")),
		}
	case "nuxt":
		return []cacheMount{
			cacheMountFor(plan, ".nuxt", cacheIDSuffix(framework, "nuxt")),
			cacheMountFor(plan, "node_modules/.cache/nuxt", cacheIDSuffix(framework, "node-cache")),
		}
	case "vite":
		return []cacheMount{
			cacheMountFor(plan, "node_modules/.vite", cacheIDSuffix(framework, "vite")),
			cacheMountFor(plan, "node_modules/.cache/vite", cacheIDSuffix(framework, "node-cache")),
		}
	case "astro":
		return []cacheMount{
			cacheMountFor(plan, "node_modules/.astro", cacheIDSuffix(framework, "astro")),
			cacheMountFor(plan, "node_modules/.cache/astro", cacheIDSuffix(framework, "node-cache")),
		}
	case "angular":
		return []cacheMount{
			cacheMountFor(plan, ".angular/cache", cacheIDSuffix(framework, "angular-cache")),
		}
	case "cra":
		return []cacheMount{
			cacheMountFor(plan, "node_modules/.cache", cacheIDSuffix(framework, "node-cache")),
		}
	case "sveltekit":
		return []cacheMount{
			cacheMountFor(plan, ".svelte-kit", cacheIDSuffix(framework, "svelte-kit")),
			cacheMountFor(plan, "node_modules/.vite", cacheIDSuffix(framework, "vite")),
		}
	case "remix":
		return []cacheMount{
			cacheMountFor(plan, "node_modules/.cache/remix", cacheIDSuffix(framework, "remix-cache")),
		}
	default:
		return nil
	}
}

func cacheMountFor(plan buildPlan, relPath, idSuffix string) cacheMount {
	base := "/app"
	target := joinContainerPath(joinContainerPath(base, plan.appWorkDir), relPath)
	return cacheMount{
		Target: target,
		ID:     "${HBF_CACHE_ID}" + idSuffix,
	}
}

func cacheIDSuffix(parts ...string) string {
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		part = normalizeCacheIDComponent(part)
		if part != "" {
			normalized = append(normalized, part)
		}
	}
	if len(normalized) == 0 {
		return ""
	}
	return "-" + strings.Join(normalized, "-")
}

func normalizeCacheIDComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	out := strings.Trim(builder.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return out
}

func splitSetupCommands(plan buildPlan) ([]string, []string) {
	if len(plan.SetupCommands) == 0 {
		return nil, nil
	}

	pre := make([]string, 0, len(plan.SetupCommands))
	post := make([]string, 0, len(plan.SetupCommands))
	for _, command := range plan.SetupCommands {
		canonical := stripTrustedCommandPrefixes(command)
		if trustedNextSharpPattern.MatchString(strings.TrimSpace(canonical)) {
			pre = append(pre, command)
			continue
		}
		post = append(post, command)
	}
	return pre, post
}

func renderCmdLine(command, initCommand string) string {
	command = strings.TrimSpace(command)
	initCommand = strings.TrimSpace(initCommand)
	if command == "" && initCommand == "" {
		return ""
	}

	parts := make([]string, 0, 2)
	if initCommand != "" {
		parts = append(parts, initCommand)
	}
	if command != "" {
		if useExecForCommand(command) {
			parts = append(parts, "exec "+command)
		} else {
			parts = append(parts, command)
		}
	}
	return fmt.Sprintf("CMD %s\n", strings.Join(parts, "; "))
}

func useExecForCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if strings.HasPrefix(command, "cd ") {
		return false
	}
	return !strings.ContainsAny(command, "&;|<>")
}

func renderEnvLines(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var builder strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&builder, "ENV %s=%s\n", key, values[key])
	}
	return builder.String()
}

func renderAptInstallLine(packages []string) string {
	packages = normalizeKeys(packages)
	if len(packages) == 0 {
		return ""
	}
	return fmt.Sprintf("RUN apt-get update && apt-get install -y --no-install-recommends %s && rm -rf /var/lib/apt/lists/*\n", strings.Join(packages, " "))
}

func escapeSingleQuotes(value string) string {
	return strings.ReplaceAll(value, "'", "'\"'\"'")
}

func renderPHPDocrootSetup(docroot string) string {
	if strings.TrimSpace(docroot) == "" {
		return ""
	}
	docroot = strings.TrimSpace(docroot)
	target := "/app"
	if docroot != "" && docroot != "." {
		target = "/app/" + strings.TrimPrefix(docroot, "/")
	}
	return fmt.Sprintf("RUN sed -ri -e 's!/var/www/html!%s!g' /etc/apache2/sites-available/*.conf /etc/apache2/apache2.conf /etc/apache2/conf-available/*.conf\n", target)
}

func renderPHPFPMNginxTemplate(docroot string) string {
	docroot = strings.TrimSpace(docroot)
	target := "/app"
	if docroot != "" && docroot != "." {
		target = "/app/" + strings.TrimPrefix(docroot, "/")
	}

	var builder strings.Builder
	builder.WriteString("RUN mkdir -p /etc/nginx/templates && cat <<'EOF' > /etc/nginx/templates/hubfly-default.conf.template\n")
	builder.WriteString("server {\n")
	builder.WriteString("  listen __PORT__;\n")
	builder.WriteString("  server_name _;\n")
	fmt.Fprintf(&builder, "  root %s;\n", target)
	builder.WriteString("  index index.php index.html;\n")
	builder.WriteString("  location / {\n")
	builder.WriteString("    try_files $uri $uri/ /index.php?$query_string;\n")
	builder.WriteString("  }\n")
	builder.WriteString("  location ~ \\.php$ {\n")
	builder.WriteString("    include snippets/fastcgi-php.conf;\n")
	builder.WriteString("    fastcgi_pass 127.0.0.1:9000;\n")
	builder.WriteString("    fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;\n")
	builder.WriteString("  }\n")
	builder.WriteString("  location ~ /\\.ht {\n")
	builder.WriteString("    deny all;\n")
	builder.WriteString("  }\n")
	builder.WriteString("}\n")
	builder.WriteString("EOF\n")
	return builder.String()
}

func normalizeKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}

	sort.Strings(out)
	return out
}

func containsKey(keys []string, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" || len(keys) == 0 {
		return false
	}
	for _, key := range keys {
		if strings.TrimSpace(key) == needle {
			return true
		}
	}
	return false
}
