package autodetect

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"hubfly-builder/internal/allowlist"
)

type composerJSON struct {
	Require    map[string]string      `json:"require"`
	RequireDev map[string]string      `json:"require-dev"`
	Scripts    map[string]interface{} `json:"scripts"`
	Config     *struct {
		Platform map[string]interface{} `json:"platform"`
	} `json:"config,omitempty"`
}

type phpExtensionRule struct {
	aptPackages     []string
	configure       string
	install         []string
	peclInstall     []string
	enable          []string
	validationIssue string
}

var phpExtensionRules = map[string]phpExtensionRule{
	"bcmath": {
		install: []string{"bcmath"},
	},
	"exif": {
		install: []string{"exif"},
	},
	"gd": {
		aptPackages: []string{"libfreetype6-dev", "libjpeg62-turbo-dev", "libpng-dev"},
		configure:   "docker-php-ext-configure gd --with-freetype --with-jpeg",
		install:     []string{"gd"},
	},
	"intl": {
		aptPackages: []string{"libicu-dev"},
		install:     []string{"intl"},
	},
	"mbstring": {
		aptPackages: []string{"libonig-dev"},
		install:     []string{"mbstring"},
	},
	"mysqli": {
		install: []string{"mysqli"},
	},
	"opcache": {
		install: []string{"opcache"},
	},
	"pcntl": {
		install: []string{"pcntl"},
	},
	"pdo_mysql": {
		install: []string{"pdo_mysql"},
	},
	"pdo_pgsql": {
		aptPackages: []string{"libpq-dev"},
		install:     []string{"pdo_pgsql"},
	},
	"pgsql": {
		aptPackages: []string{"libpq-dev"},
		install:     []string{"pgsql"},
	},
	"soap": {
		install: []string{"soap"},
	},
	"sockets": {
		install: []string{"sockets"},
	},
	"zip": {
		aptPackages: []string{"libzip-dev"},
		install:     []string{"zip"},
	},
	"imagick": {
		aptPackages: []string{"$PHPIZE_DEPS", "imagemagick", "libmagickwand-dev"},
		peclInstall: []string{"imagick"},
		enable:      []string{"imagick"},
	},
	"redis": {
		aptPackages: []string{"$PHPIZE_DEPS"},
		peclInstall: []string{"redis"},
		enable:      []string{"redis"},
	},
	"apcu": {
		aptPackages: []string{"$PHPIZE_DEPS"},
		peclInstall: []string{"apcu"},
		enable:      []string{"apcu"},
	},
}

func detectPHPBuildPlan(appDir, appPath, version string, allowed *allowlist.AllowedCommands) (buildPlan, error) {
	install, build, run := detectPHPCommands(appPath, allowed)
	plan, err := defaultBuildPlan("php", version, install, build, run)
	if err != nil {
		return buildPlan{}, err
	}

	plan.BuildContextDir = appDir
	plan.AppDir = appDir
	if err := applyPHPPlanDefaults(appPath, &plan); err != nil {
		return buildPlan{}, err
	}
	return plan, nil
}

func detectPHPCommands(repoPath string, allowed *allowlist.AllowedCommands) (string, string, string) {
	installCandidates := phpInstallCandidates(repoPath)
	buildCandidates := phpBuildCandidates(repoPath)
	runCandidates := phpRunCandidates(repoPath)

	return pickFirstAllowed(installCandidates, allowed.Prebuild),
		pickFirstAllowed(buildCandidates, allowed.Build),
		pickFirstAllowed(runCandidates, allowed.Run)
}

func phpInstallCandidates(repoPath string) []string {
	if repoPath != "" && fileExists(filepath.Join(repoPath, "composer.json")) {
		return []string{
			"COMPOSER_ALLOW_SUPERUSER=1 composer install --no-dev --prefer-dist --optimize-autoloader --no-interaction",
			"COMPOSER_ALLOW_SUPERUSER=1 composer install",
			"composer install --no-dev --prefer-dist --optimize-autoloader --no-interaction",
			"composer install",
		}
	}
	return nil
}

func phpBuildCandidates(repoPath string) []string {
	metadata := loadComposerJSON(repoPath)
	framework, _, hasWebEntrypoint := detectPHPFrameworkAndDocroot(repoPath, metadata)
	if !hasWebEntrypoint {
		return nil
	}

	switch framework {
	case "laravel":
		return []string{"php artisan optimize"}
	case "symfony":
		return []string{"php bin/console cache:clear --env=prod --no-debug"}
	default:
		if metadata != nil {
			return append(composerBuildScriptCandidates(metadata), "composer dump-autoload --optimize")
		}
		return nil
	}
}

func composerBuildScriptCandidates(metadata *composerJSON) []string {
	if metadata == nil || len(metadata.Scripts) == 0 {
		return nil
	}

	var candidates []string
	for _, script := range []string{"build", "compile", "assets", "production"} {
		if _, ok := metadata.Scripts[script]; ok {
			candidates = append(candidates, "composer run-script "+script)
		}
	}
	return candidates
}

func phpRunCandidates(repoPath string) []string {
	_, _, hasWebEntrypoint := detectPHPFrameworkAndDocroot(repoPath, loadComposerJSON(repoPath))
	if hasWebEntrypoint {
		return []string{"apache2-foreground"}
	}

	for _, fileName := range []string{"index.php", "app.php", "server.php", "main.php", "worker.php"} {
		if repoPath != "" && fileExists(filepath.Join(repoPath, fileName)) {
			return []string{"php " + fileName}
		}
	}
	if repoPath != "" && fileExists(filepath.Join(repoPath, "artisan")) {
		return []string{"php artisan queue:work"}
	}
	if repoPath != "" && fileExists(filepath.Join(repoPath, "bin", "console")) {
		return []string{"php bin/console messenger:consume async"}
	}
	return nil
}

func loadComposerJSON(repoPath string) *composerJSON {
	if repoPath == "" {
		return nil
	}

	data, err := os.ReadFile(filepath.Join(repoPath, "composer.json"))
	if err != nil {
		return nil
	}

	var parsed composerJSON
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	return &parsed
}

func detectPHPFrameworkAndDocroot(appPath string, metadata *composerJSON) (string, string, bool) {
	switch {
	case fileExists(filepath.Join(appPath, "artisan")) || composerRequires(metadata, "laravel/framework"):
		if fileExists(filepath.Join(appPath, "public", "index.php")) {
			return "laravel", "public", true
		}
		return "laravel", "public", false
	case fileExists(filepath.Join(appPath, "bin", "console")) || composerRequires(metadata, "symfony/framework-bundle", "symfony/runtime", "symfony/symfony"):
		if fileExists(filepath.Join(appPath, "public", "index.php")) {
			return "symfony", "public", true
		}
		if fileExists(filepath.Join(appPath, "web", "index.php")) {
			return "symfony", "web", true
		}
		return "symfony", "public", false
	case composerRequires(metadata, "slim/slim", "mezzio/mezzio", "laminas/laminas-mvc"):
		return phpComposerFrameworkDocroot(appPath, "slim", "public", ".")
	case fileExists(filepath.Join(appPath, "spark")) || composerRequires(metadata, "codeigniter4/framework"):
		return phpComposerFrameworkDocroot(appPath, "codeigniter", "public", ".")
	case fileExists(filepath.Join(appPath, "bin", "cake")) || composerRequires(metadata, "cakephp/cakephp"):
		return phpComposerFrameworkDocroot(appPath, "cakephp", "webroot", ".")
	case composerRequires(metadata, "yiisoft/yii2"):
		return phpComposerFrameworkDocroot(appPath, "yii", "web", ".")
	case composerRequires(metadata, "drupal/core", "drupal/core-recommended"):
		return phpComposerFrameworkDocroot(appPath, "drupal", "web", ".")
	case composerRequires(metadata, "magento/product-community-edition", "magento/project-community-edition"):
		return phpComposerFrameworkDocroot(appPath, "magento", "pub", ".")
	case fileExists(filepath.Join(appPath, "wp-config.php")):
		if fileExists(filepath.Join(appPath, "index.php")) {
			return "wordpress", ".", true
		}
		return "wordpress", ".", false
	case fileExists(filepath.Join(appPath, "public", "index.php")):
		return "php-web", "public", true
	case fileExists(filepath.Join(appPath, "web", "index.php")):
		return "php-web", "web", true
	case fileExists(filepath.Join(appPath, "index.php")):
		return "php-web", ".", true
	default:
		return "php", "", false
	}
}

func phpComposerFrameworkDocroot(appPath, framework string, candidates ...string) (string, string, bool) {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		indexPath := filepath.Join(appPath, "index.php")
		if candidate != "." {
			indexPath = filepath.Join(appPath, filepath.FromSlash(candidate), "index.php")
		}
		if fileExists(indexPath) {
			return framework, candidate, true
		}
	}
	if len(candidates) > 0 {
		return framework, strings.TrimSpace(candidates[0]), false
	}
	return framework, "", false
}

func composerRequires(metadata *composerJSON, packages ...string) bool {
	if metadata == nil || (len(metadata.Require) == 0 && len(metadata.RequireDev) == 0) {
		return false
	}
	for _, pkg := range packages {
		if _, ok := metadata.Require[pkg]; ok {
			return true
		}
		if _, ok := metadata.RequireDev[pkg]; ok {
			return true
		}
	}
	return false
}

func detectPHPRequiredExtensions(metadata *composerJSON) []string {
	if metadata == nil || (len(metadata.Require) == 0 && (metadata.Config == nil || len(metadata.Config.Platform) == 0)) {
		return nil
	}

	seen := make(map[string]struct{})
	var extensions []string
	for _, values := range []map[string]string{metadata.Require, composerPlatformExtensions(metadata)} {
		for key := range values {
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "ext-") {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(key, "ext-")))
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			extensions = append(extensions, name)
		}
	}

	sort.Strings(extensions)
	return extensions
}

func composerPlatformExtensions(metadata *composerJSON) map[string]string {
	if metadata == nil || metadata.Config == nil {
		return nil
	}
	extensions := make(map[string]string)
	for key, version := range metadata.Config.Platform {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "ext-") {
			continue
		}
		if composerPlatformValueDisabled(version) {
			continue
		}
		extensions[key] = composerPlatformValueString(version)
	}
	return extensions
}

func composerPlatformValueString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	default:
		return ""
	}
}

func composerPlatformValueDisabled(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return !typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "false")
	default:
		return false
	}
}

func applyPHPPlanDefaults(appPath string, plan *buildPlan) error {
	metadata := loadComposerJSON(appPath)
	framework, docroot, hasWebEntrypoint := detectPHPFrameworkAndDocroot(appPath, metadata)
	if strings.TrimSpace(plan.Framework) == "" {
		plan.Framework = framework
	}

	laravelInit := ""
	if framework == "laravel" && hasWebEntrypoint {
		laravelInit = laravelRuntimeInitCommand()
	}

	if strings.TrimSpace(plan.InstallCommand) == "" {
		plan.InstallCommand = pickFirstNonEmpty(phpInstallCandidates(appPath))
	}
	if strings.TrimSpace(plan.BuildCommand) == "" {
		plan.BuildCommand = pickFirstNonEmpty(phpBuildCandidates(appPath))
	}
	if strings.TrimSpace(plan.RunCommand) == "" {
		plan.RunCommand = pickFirstNonEmpty(phpRunCandidates(appPath))
	}

	if strings.TrimSpace(plan.RunCommand) == "" {
		return fmt.Errorf("no production run command detected for php application")
	}

	if strings.TrimSpace(plan.ExposePort) == "" {
		plan.ExposePort = "8080"
	}

	plan.RuntimeFlavor = detectPHPRuntimeFlavor(appPath, metadata, plan.Framework, plan.RunCommand)
	plan.BuilderImage = selectPHPBaseImage(plan.Version, plan.RuntimeFlavor)
	plan.PHPIniPath = phpSourcePathInContext(plan.BuildContextDir, plan.AppDir, detectPHPIniPath(appPath))
	appEnv := "production"
	if strings.HasPrefix(framework, "symfony") {
		appEnv = "prod"
	}
	plan.RuntimeEnv = map[string]string{
		"APP_ENV": appEnv,
	}
	plan.DocumentRoot = ""
	plan.RuntimeInitCommand = strings.TrimSpace(plan.RuntimeInitCommand)
	if plan.RuntimeFlavor == "apache" {
		plan.DocumentRoot = docroot
		if strings.TrimSpace(plan.RunCommand) == "" || strings.Contains(strings.ToLower(plan.RunCommand), "php-fpm") || strings.Contains(strings.ToLower(plan.RunCommand), "nginx") {
			plan.RunCommand = "apache2-foreground"
		}
		plan.RuntimeEnv["PORT"] = plan.ExposePort
	} else if plan.RuntimeFlavor == "fpm" {
		plan.DocumentRoot = docroot
		if strings.TrimSpace(plan.RunCommand) == "" || strings.TrimSpace(plan.RunCommand) == "apache2-foreground" {
			plan.RunCommand = detectPHPFPMRunCommand()
		}
		plan.RuntimeEnv["PORT"] = plan.ExposePort
	} else {
		if inferredPort := inferExposePort("", plan.RunCommand); inferredPort != "" {
			plan.ExposePort = inferredPort
			plan.RuntimeEnv["PORT"] = inferredPort
		} else {
			plan.ExposePort = ""
		}
	}
	plan.AptPackages = detectPHPAptPackages(metadata)
	if hasWebEntrypoint {
		nodeBootstrap, nodeSetup, nodePackages := detectPHPNodeIntegration(appPath)
		if len(nodePackages) > 0 {
			for _, pkg := range nodePackages {
				plan.AptPackages = appendUniqueString(plan.AptPackages, pkg)
			}
		}
		plan.BootstrapCommands = mergeUniqueCommands(plan.BootstrapCommands, nodeBootstrap)
		plan.SetupCommands = mergeUniqueCommands(plan.SetupCommands, nodeSetup)
	}
	plan.BootstrapCommands = mergeUniqueCommands(plan.BootstrapCommands, detectPHPBootstrapCommands(metadata, plan.RuntimeFlavor))
	plan.ValidationWarnings = mergeUniqueCommands(plan.ValidationWarnings, detectPHPValidationWarnings(metadata))

	if hasWebEntrypoint && plan.RuntimeFlavor == "cli" {
		plan.ValidationWarnings = appendUniqueString(plan.ValidationWarnings, "php app has a web entrypoint; submitted run command overrides the default web runtime")
	}

	if plan.RuntimeFlavor == "apache" {
		plan.RuntimeInitCommand = joinRuntimeInitCommands(laravelInit, plan.RuntimeInitCommand, detectPHPRuntimeInitCommand(plan.ExposePort))
	} else if plan.RuntimeFlavor == "fpm" {
		plan.RuntimeInitCommand = joinRuntimeInitCommands(laravelInit, plan.RuntimeInitCommand, detectPHPFPMRuntimeInitCommand(plan.ExposePort))
	} else if laravelInit != "" && strings.TrimSpace(plan.RuntimeInitCommand) == "" {
		plan.RuntimeInitCommand = laravelInit
	}

	return nil
}

func detectPHPRuntimeFlavor(appPath string, metadata *composerJSON, framework, runCommand string) string {
	_, _, hasWebEntrypoint := detectPHPFrameworkAndDocroot(appPath, metadata)
	if !hasWebEntrypoint {
		return "cli"
	}

	lowerFramework := strings.ToLower(strings.TrimSpace(framework))
	lowerRun := strings.ToLower(strings.TrimSpace(runCommand))
	switch {
	case strings.Contains(lowerFramework, "fpm"), strings.Contains(lowerFramework, "nginx"):
		return "fpm"
	case strings.Contains(lowerRun, "php-fpm"), strings.Contains(lowerRun, "nginx"):
		return "fpm"
	case hasPHPHtaccess(appPath):
		return "apache"
	case hasPHPNginxHint(appPath):
		return "fpm"
	default:
		return "apache"
	}
}

func hasPHPHtaccess(appPath string) bool {
	_, docroot, hasWebEntrypoint := detectPHPFrameworkAndDocroot(appPath, loadComposerJSON(appPath))
	for _, rel := range []string{
		filepath.ToSlash(filepath.Join(docroot, ".htaccess")),
		".htaccess",
		"public/.htaccess",
		"web/.htaccess",
		"webroot/.htaccess",
		"pub/.htaccess",
	} {
		rel = strings.Trim(rel, "/")
		if rel == "" || rel == "." {
			continue
		}
		if fileExists(filepath.Join(appPath, filepath.FromSlash(rel))) {
			return true
		}
	}
	return !hasWebEntrypoint && fileExists(filepath.Join(appPath, ".htaccess"))
}

func hasPHPNginxHint(appPath string) bool {
	for _, fileName := range []string{
		"nginx.conf",
		".nginx/default.conf",
		"nginx/default.conf",
		"docker/nginx.conf",
		"docker/nginx/default.conf",
		"deploy/nginx.conf",
		"ops/nginx.conf",
	} {
		if fileExists(filepath.Join(appPath, filepath.FromSlash(fileName))) {
			return true
		}
	}
	return false
}

func detectPHPIniPath(appPath string) string {
	for _, rel := range []string{
		"php.ini",
		".php.ini",
		"docker/php.ini",
		"docker/php/php.ini",
		"config/php.ini",
		".docker/php.ini",
		"deploy/php.ini",
		"ops/php.ini",
	} {
		if fileExists(filepath.Join(appPath, filepath.FromSlash(rel))) {
			return rel
		}
	}
	return ""
}

func phpSourcePathInContext(buildContextDir, appDir, rel string) string {
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	if rel == "" {
		return ""
	}
	appWorkDir := containerRelativeDir(buildContextDir, appDir)
	if appWorkDir == "" || appWorkDir == "." {
		return rel
	}
	return path.Join(appWorkDir, rel)
}

func selectPHPBaseImage(version, runtimeFlavor string) string {
	version = strings.TrimSpace(version)
	switch version {
	case "", "8":
		version = "8.3"
	}
	switch strings.TrimSpace(runtimeFlavor) {
	case "apache":
		return "php:" + version + "-apache"
	case "fpm":
		return "php:" + version + "-fpm-trixie"
	default:
		return "php:" + version + "-cli"
	}
}

func detectPHPAptPackages(metadata *composerJSON) []string {
	packages := []string{"git", "unzip"}
	for _, extension := range detectPHPRequiredExtensions(metadata) {
		rule, ok := phpExtensionRules[extension]
		if !ok {
			continue
		}
		for _, pkg := range rule.aptPackages {
			packages = appendUniqueString(packages, pkg)
		}
	}
	return normalizeKeys(packages)
}

func detectPHPBootstrapCommands(metadata *composerJSON, runtimeFlavor string) []string {
	commands := []string{
		"if [ -f \"$PHP_INI_DIR/php.ini-production\" ]; then cp \"$PHP_INI_DIR/php.ini-production\" \"$PHP_INI_DIR/php.ini\"; fi",
	}
	if runtimeFlavor == "apache" {
		commands = append(commands, "a2enmod rewrite")
	}

	installExtensions := []string{"opcache"}
	var peclExtensions []string
	var enabledExtensions []string
	for _, extension := range detectPHPRequiredExtensions(metadata) {
		rule, ok := phpExtensionRules[extension]
		if !ok {
			continue
		}
		if strings.TrimSpace(rule.configure) != "" {
			commands = appendUniqueString(commands, rule.configure)
		}
		for _, install := range rule.install {
			installExtensions = appendUniqueString(installExtensions, install)
		}
		for _, install := range rule.peclInstall {
			peclExtensions = appendUniqueString(peclExtensions, install)
		}
		for _, enable := range rule.enable {
			enabledExtensions = appendUniqueString(enabledExtensions, enable)
		}
	}

	if len(installExtensions) > 0 {
		sort.Strings(installExtensions)
		commands = append(commands, "docker-php-ext-install "+strings.Join(installExtensions, " "))
	}
	if len(peclExtensions) > 0 {
		sort.Strings(peclExtensions)
		for _, extension := range peclExtensions {
			commands = append(commands, "printf \"\\n\" | pecl install "+extension)
		}
	}
	if len(enabledExtensions) > 0 {
		sort.Strings(enabledExtensions)
		commands = append(commands, "docker-php-ext-enable "+strings.Join(enabledExtensions, " "))
	}
	return commands
}

func detectPHPValidationWarnings(metadata *composerJSON) []string {
	var warnings []string
	for _, extension := range detectPHPRequiredExtensions(metadata) {
		rule, ok := phpExtensionRules[extension]
		if !ok {
			warnings = append(warnings, "composer requires ext-"+extension+"; generated PHP image does not install it automatically")
			continue
		}
		if strings.TrimSpace(rule.validationIssue) != "" {
			warnings = append(warnings, rule.validationIssue)
		}
	}
	sort.Strings(warnings)
	return warnings
}

func detectPHPRuntimeInitCommand(port string) string {
	port = strings.TrimSpace(port)
	if port == "" {
		port = "8080"
	}
	return fmt.Sprintf("PORT=\"${PORT:-%s}\"; sed -ri -e \"s/^Listen .*/Listen 0.0.0.0:${PORT}/\" /etc/apache2/ports.conf; sed -ri -e \"s!<VirtualHost [^>]+>!<VirtualHost 0.0.0.0:${PORT}>!g\" /etc/apache2/sites-available/000-default.conf", port)
}

func detectPHPFPMRuntimeInitCommand(port string) string {
	port = strings.TrimSpace(port)
	if port == "" {
		port = "8080"
	}
	return fmt.Sprintf("PORT=\"${PORT:-%s}\"; sed \"s/__PORT__/${PORT}/g\" /etc/nginx/templates/hubfly-default.conf.template > /etc/nginx/sites-available/default", port)
}

func detectPHPFPMRunCommand() string {
	return "php-fpm -D && exec nginx -g 'daemon off;'"
}

func detectPHPNodeIntegration(appPath string) ([]string, []string, []string) {
	if appPath == "" || !fileExists(filepath.Join(appPath, "package.json")) {
		return nil, nil, nil
	}

	metadata := loadNodePackageJSON(appPath)
	packageManager := detectNodePackageManager(appPath, metadata)
	scripts := map[string]string{}
	if metadata != nil && metadata.Scripts != nil {
		scripts = metadata.Scripts
	}

	buildCandidates := nodeBuildCandidates(packageManager, scripts)
	buildCommand := pickFirstNonEmpty(buildCandidates)
	if buildCommand == "" {
		return nil, nil, nil
	}

	installCommand := pickFirstNonEmpty(nodePrebuildCandidates(appPath, packageManager))
	bootstrap := detectPHPNodeBootstrapCommands(packageManager, metadata)
	setup := []string{}
	if strings.TrimSpace(installCommand) != "" {
		setup = append(setup, installCommand)
	}
	setup = append(setup, buildCommand)

	return bootstrap, setup, []string{"nodejs", "npm"}
}

func detectPHPNodeBootstrapCommands(packageManager string, metadata *nodePackageJSON) []string {
	spec := ""
	if metadata != nil {
		spec = strings.TrimSpace(metadata.PackageManager)
	}
	name, version := parsePackageManagerSpec(spec)
	if name == "" {
		name = packageManager
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

func laravelRuntimeInitCommand() string {
	command := `if [ -d storage ]; then chmod -R 775 storage; fi; if [ -d bootstrap/cache ]; then chmod -R 775 bootstrap/cache; fi; if [ -f artisan ]; then if [ "${LARAVEL_STORAGE_LINK:-1}" = "1" ] && [ ! -e public/storage ]; then php artisan storage:link; fi; if [ "${LARAVEL_RUN_MIGRATIONS:-0}" = "1" ]; then php artisan migrate --force; fi; if [ "${LARAVEL_OPTIMIZE_ON_START:-0}" = "1" ]; then php artisan optimize; fi; fi`
	return strings.TrimSpace(command)
}

func joinRuntimeInitCommands(commands ...string) string {
	parts := make([]string, 0, len(commands))
	for _, command := range commands {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		parts = append(parts, command)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}
