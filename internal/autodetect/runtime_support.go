package autodetect

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"hubfly-builder/internal/allowlist"
)

var (
	portFlagPattern         = regexp.MustCompile(`(?:--port(?:=|\s+)|\s-p(?:=|\s+))(\d{2,5})`)
	portEnvAssignPattern    = regexp.MustCompile(`(?:^|\s)PORT=(\d{2,5})(?:\s|$)`)
	portDefaultExprPattern  = regexp.MustCompile(`\$\{PORT:?-?(\d{2,5})\}`)
	bindPortPattern         = regexp.MustCompile(`0\.0\.0\.0:(\d{2,5})`)
	trustedCdPrefixPattern  = regexp.MustCompile(`^cd '[^']+' &&\s*`)
	trustedCorepackPattern  = regexp.MustCompile(`^corepack prepare [A-Za-z0-9._-]+@[A-Za-z0-9._-]+ --activate$`)
	trustedNpmGlobalPattern = regexp.MustCompile(`^npm install -g npm@[A-Za-z0-9._-]+$`)
	trustedPrismaGenPattern = regexp.MustCompile(`^(npx|bunx) prisma generate$`)
	trustedPrismaMigPattern = regexp.MustCompile(`^if \[ "\$\{PRISMA_RUN_MIGRATIONS:-0\}" = "1" \]; then (npx|bunx) prisma migrate deploy; fi$`)
	trustedPlaywrightNode   = regexp.MustCompile(`^(npx|bunx) playwright install chromium$`)
	trustedPlaywrightPy     = regexp.MustCompile(`^python -m playwright install chromium$`)
	trustedNextStartPattern = regexp.MustCompile(`^\.\/node_modules\/\.bin\/next start --hostname 0\.0\.0\.0 --port \$\{PORT:-\d{2,5}\}$`)
	trustedNextSharpPattern = regexp.MustCompile(`^(npm install|pnpm add|yarn add|bun add) sharp$`)
	trustedNestStartPattern = regexp.MustCompile(`^HOST=0\.0\.0\.0 PORT=\$\{PORT:-\d{2,5}\} node dist/main\.js$`)
	trustedNuxtStartPattern = regexp.MustCompile(`^HOST=0\.0\.0\.0 PORT=\$\{PORT:-\d{2,5}\} node \.output/server/index\.mjs$`)
	trustedAstroStartPattern = regexp.MustCompile(`^HOST=0\.0\.0\.0 PORT=\$\{PORT:-\d{2,5}\} node \./dist/server/entry\.mjs$`)
	trustedRemixStartPattern = regexp.MustCompile(`^HOST=0\.0\.0\.0 PORT=\$\{PORT:-\d{2,5}\} node \./build/server/index\.js$`)
	trustedAngularSSRPattern = regexp.MustCompile(`^if \[ -f '[^']+' \]; then HOST=0\.0\.0\.0 PORT=\$\{PORT:-\d{2,5}\} node '[^']+'; elif \[ -f '[^']+' \]; then HOST=0\.0\.0\.0 PORT=\$\{PORT:-\d{2,5}\} node '[^']+'; elif \[ -f '[^']+' \]; then HOST=0\.0\.0\.0 PORT=\$\{PORT:-\d{2,5}\} node '[^']+'; else HOST=0\.0\.0\.0 PORT=\$\{PORT:-\d{2,5}\} node '[^']+'; fi$`)
	trustedNodeFileRunPattern = regexp.MustCompile(`^node (server|app|main)\.js$`)
	trustedNodeDistRunPattern = regexp.MustCompile(`^node (dist/server|dist/main|build/server|build/index|build/handler)\.js$`)
	trustedBunFileRunPattern  = regexp.MustCompile(`^bun (server|app)\.(ts|js)$`)
	trustedRustSelectPattern  = regexp.MustCompile(`^set -e; .*cp "\$[^"]+" /app/app.*$`)
	trustedPHPIniPattern    = regexp.MustCompile(`^if \[ -f "\$PHP_INI_DIR/php\.ini-production" \]; then cp "\$PHP_INI_DIR/php\.ini-production" "\$PHP_INI_DIR/php\.ini"; fi$`)
	trustedPHPExtPattern    = regexp.MustCompile(`^docker-php-ext-install(?: [a-z0-9_]+)+$`)
	trustedPHPExtEnable     = regexp.MustCompile(`^docker-php-ext-enable(?: [a-z0-9_]+)+$`)
	trustedPHPPeclPattern   = regexp.MustCompile(`^printf "\\n" \| pecl install [a-z0-9_-]+$`)
	trustedPHPGDPattern     = regexp.MustCompile(`^docker-php-ext-configure gd --with-freetype --with-jpeg$`)
	trustedApacheModule     = regexp.MustCompile(`^a2enmod rewrite$`)
	trustedApachePort       = regexp.MustCompile(`^PORT="\$\{PORT:-\d{2,5}\}"; sed -ri -e ".+" /etc/apache2/ports\.conf; sed -ri -e ".+" /etc/apache2/sites-available/000-default\.conf$`)
	trustedPHPFPMInit       = regexp.MustCompile(`^PORT="\$\{PORT:-\d{2,5}\}"; sed "s/__PORT__/\$\{PORT\}/g" /etc/nginx/templates/hubfly-default\.conf\.template > /etc/nginx/sites-available/default$`)
	trustedPHPFPMRun        = regexp.MustCompile(`^php-fpm -D && exec nginx -g 'daemon off;'$`)
)

func inferExposePort(defaultPort string, sources ...string) string {
	for _, source := range sources {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		for _, pattern := range []*regexp.Regexp{
			portEnvAssignPattern,
			portFlagPattern,
			portDefaultExprPattern,
			bindPortPattern,
		} {
			matches := pattern.FindStringSubmatch(source)
			if len(matches) == 2 && strings.TrimSpace(matches[1]) != "" {
				return matches[1]
			}
		}
	}
	return defaultPort
}

func detectPythonBuildPlan(appDir, appPath, version string, allowed *allowlist.AllowedCommands) (buildPlan, error) {
	prebuild, build, run := detectPythonCommands(appPath, allowed)
	plan, err := defaultBuildPlan("python", version, prebuild, build, run)
	if err != nil {
		return buildPlan{}, err
	}

	plan.BuildContextDir = appDir
	plan.AppDir = appDir
	plan.Framework = detectPythonFramework(appPath)
	plan.ExposePort = inferExposePort(plan.ExposePort, run)
	plan.AptPackages = detectPythonSystemPackages(appPath)
	plan.SetupCommands = detectPythonSetupCommands(appPath)
	if shouldUseSimpleFastAPIDockerfile(plan) {
		plan.BuilderImage = "python:" + version + "-alpine"
		plan.RuntimeImage = plan.BuilderImage
	}
	return plan, nil
}

func detectPythonFramework(appPath string) string {
	if detectFastAPIRunCommand(appPath) != "" {
		return "fastapi"
	}
	if detectGunicornRunCommand(appPath) != "" {
		return "flask"
	}
	return ""
}

func shouldUseSimpleFastAPIDockerfile(plan buildPlan) bool {
	if strings.TrimSpace(strings.ToLower(plan.Framework)) != "fastapi" {
		return false
	}
	if strings.TrimSpace(plan.InstallCommand) != "pip install -r requirements.txt" {
		return false
	}
	return len(plan.AptPackages) == 0 && len(plan.SetupCommands) == 0 && strings.TrimSpace(plan.BuildCommand) == ""
}

func shouldUseSimpleFlaskDockerfile(plan buildPlan) bool {
	if strings.TrimSpace(strings.ToLower(plan.Framework)) != "flask" {
		return false
	}
	if strings.TrimSpace(plan.InstallCommand) != "pip install -r requirements.txt" {
		return false
	}
	run := strings.TrimSpace(plan.RunCommand)
	if !strings.HasPrefix(run, "gunicorn ") {
		return false
	}
	return len(plan.AptPackages) == 0 && len(plan.SetupCommands) == 0 && strings.TrimSpace(plan.BuildCommand) == ""
}

func detectElixirBuildPlan(appDir, appPath, version string, allowed *allowlist.AllowedCommands) (buildPlan, error) {
	prebuild, build, run := detectElixirCommands(appPath, allowed)
	plan, err := defaultBuildPlan("elixir", version, prebuild, build, run)
	if err != nil {
		return buildPlan{}, err
	}

	plan.BuildContextDir = appDir
	plan.AppDir = appDir
	if isPhoenixProject(appPath) {
		plan.Framework = "phoenix"
	}
	if isDistilleryProject(appPath) {
		plan.SetupCommands = mergeUniqueCommands(plan.SetupCommands, []string{
			"mix local.rebar --force",
			"MIX_ENV=prod mix deps.get",
			"MIX_ENV=prod mix compile",
			"MIX_ENV=prod mix phx.digest",
		})
	}
	plan.ExposePort = inferExposePort(defaultExposePort(plan.Runtime), run)
	plan.AptPackages = []string{"build-essential", "git"}
	if plan.RuntimeEnv == nil {
		plan.RuntimeEnv = map[string]string{}
	}
	if plan.Framework == "phoenix" {
		plan.RuntimeEnv["PHX_SERVER"] = "true"
	}
	if strings.TrimSpace(plan.ExposePort) != "" {
		plan.RuntimeEnv["PORT"] = plan.ExposePort
	}
	return plan, nil
}

func detectPythonSetupCommands(appPath string) []string {
	deps := detectPythonDependencies(appPath)
	if _, ok := deps["playwright"]; ok {
		return []string{"python -m playwright install chromium"}
	}
	return nil
}

func detectPythonSystemPackages(appPath string) []string {
	deps := detectPythonDependencies(appPath)
	if len(deps) == 0 {
		return nil
	}

	var packages []string
	add := func(values ...string) {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			found := false
			for _, existing := range packages {
				if existing == value {
					found = true
					break
				}
			}
			if !found {
				packages = append(packages, value)
			}
		}
	}

	if hasPythonDependency(deps, "playwright") {
		add(
			"ca-certificates",
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
	if hasPythonDependency(deps, "psycopg2", "psycopg", "psycopg2-binary") {
		add("gcc", "libpq-dev")
	}
	if hasPythonDependency(deps, "mysqlclient") {
		add("default-libmysqlclient-dev", "gcc", "pkg-config")
	}
	if hasPythonDependency(deps, "pillow") {
		add("libfreetype6-dev", "libjpeg62-turbo-dev", "zlib1g-dev")
	}
	if hasPythonDependency(deps, "lxml") {
		add("libxml2-dev", "libxslt1-dev")
	}
	if hasPythonDependency(deps, "cryptography", "pyopenssl") {
		add("libssl-dev")
	}

	sort.Strings(packages)
	return packages
}

func detectPythonDependencies(appPath string) map[string]struct{} {
	deps := make(map[string]struct{})

	requirementsPath := filepath.Join(appPath, "requirements.txt")
	if data, err := os.ReadFile(requirementsPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
			if line == "" || strings.HasPrefix(line, "-") {
				continue
			}
			name := normalizePythonDependencyName(line)
			if name != "" {
				deps[name] = struct{}{}
			}
		}
	}

	for _, fileName := range []string{"pyproject.toml", "setup.py"} {
		path := filepath.Join(appPath, fileName)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lower := strings.ToLower(string(data))
		for _, name := range []string{"playwright", "psycopg2", "psycopg", "psycopg2-binary", "mysqlclient", "pillow", "lxml", "cryptography", "pyopenssl"} {
			if strings.Contains(lower, name) {
				deps[name] = struct{}{}
			}
		}
	}

	return deps
}

func normalizePythonDependencyName(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}

	cutMarkers := []string{"==", ">=", "<=", "~=", "!=", ">", "<", "[", ";", " ", "\t"}
	for _, marker := range cutMarkers {
		if idx := strings.Index(line, marker); idx >= 0 {
			line = line[:idx]
		}
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return strings.Trim(line, "\"'")
}

func hasPythonDependency(deps map[string]struct{}, names ...string) bool {
	for _, name := range names {
		if _, ok := deps[strings.ToLower(strings.TrimSpace(name))]; ok {
			return true
		}
	}
	return false
}

func validateBuildPlanCommands(plan buildPlan, allowed *allowlist.AllowedCommands) error {
	check := func(stage, command string, allowedCommands []string) error {
		command = strings.TrimSpace(command)
		if command == "" {
			return nil
		}
		canonical := stripTrustedCommandPrefixes(command)
		if allowlist.IsCommandAllowed(command, allowedCommands) || allowlist.IsCommandAllowed(canonical, allowedCommands) || isTrustedGeneratedCommand(command) {
			return nil
		}
		return fmt.Errorf("%s command is not allowed: %s", stage, command)
	}

	if allowed != nil {
		if err := check("install", plan.InstallCommand, allowed.Prebuild); err != nil {
			return err
		}
		for _, command := range plan.SetupCommands {
			if err := checkMultiple("setup", command, allowed.Prebuild, allowed.Build); err != nil {
				return err
			}
		}
		if err := check("build", plan.BuildCommand, allowed.Build); err != nil {
			return err
		}
		for _, command := range plan.PostBuildCommands {
			if err := checkMultiple("post-build", command, allowed.Build); err != nil {
				return err
			}
		}
		if err := check("run", plan.RunCommand, allowed.Run); err != nil {
			return err
		}
		if err := checkMultiple("runtime init", plan.RuntimeInitCommand, allowed.Run, allowed.Build, allowed.Prebuild); err != nil {
			return err
		}
	}

	for _, command := range plan.BootstrapCommands {
		if !isTrustedGeneratedCommand(command) {
			return fmt.Errorf("autodetected setup command is not trusted: %s", command)
		}
	}
	return nil
}

func checkMultiple(stage, command string, allowedSets ...[]string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}

	canonical := stripTrustedCommandPrefixes(command)
	for _, allowed := range allowedSets {
		if allowlist.IsCommandAllowed(command, allowed) || allowlist.IsCommandAllowed(canonical, allowed) {
			return nil
		}
	}
	if isTrustedGeneratedCommand(command) {
		return nil
	}
	return fmt.Errorf("%s command is not allowed: %s", stage, command)
}

func isTrustedGeneratedCommand(command string) bool {
	command = stripTrustedCommandPrefixes(command)
	if command == "" {
		return false
	}

	switch command {
	case "corepack enable",
		"pnpm install --frozen-lockfile",
		"yarn install --frozen-lockfile",
		"yarn install --immutable",
		"bun install --frozen-lockfile",
		"python -m playwright install chromium",
		javaSelectJarCommand(),
		laravelRuntimeInitCommand():
		return true
	}

	for _, pattern := range []*regexp.Regexp{
		trustedCorepackPattern,
		trustedNpmGlobalPattern,
		trustedPrismaGenPattern,
		trustedPrismaMigPattern,
		trustedPlaywrightNode,
		trustedPlaywrightPy,
		trustedNextStartPattern,
		trustedNextSharpPattern,
		trustedNestStartPattern,
		trustedNuxtStartPattern,
		trustedAstroStartPattern,
		trustedRemixStartPattern,
		trustedAngularSSRPattern,
		trustedNodeFileRunPattern,
		trustedNodeDistRunPattern,
		trustedBunFileRunPattern,
		trustedRustSelectPattern,
		trustedPHPIniPattern,
		trustedPHPExtPattern,
		trustedPHPExtEnable,
		trustedPHPPeclPattern,
		trustedPHPGDPattern,
		trustedApacheModule,
		trustedApachePort,
		trustedPHPFPMInit,
		trustedPHPFPMRun,
	} {
		if pattern.MatchString(command) {
			return true
		}
	}

	return false
}

func stripTrustedCommandPrefixes(command string) string {
	command = strings.TrimSpace(command)
	for strings.HasPrefix(command, "cd '") {
		trimmed := trustedCdPrefixPattern.ReplaceAllString(command, "")
		if trimmed == command {
			break
		}
		command = strings.TrimSpace(trimmed)
	}
	return command
}
