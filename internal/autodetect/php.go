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

type composerJSON struct {
	Require map[string]string `json:"require"`
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
			"composer install --no-dev --prefer-dist --optimize-autoloader --no-interaction",
			"composer install",
		}
	}
	return nil
}

func phpBuildCandidates(repoPath string) []string {
	framework, _, hasWebEntrypoint := detectPHPFrameworkAndDocroot(repoPath, loadComposerJSON(repoPath))
	if !hasWebEntrypoint {
		return nil
	}

	switch framework {
	case "laravel":
		return []string{"php artisan optimize"}
	case "symfony":
		return []string{"php bin/console cache:clear --env=prod --no-debug"}
	default:
		return nil
	}
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

func composerRequires(metadata *composerJSON, packages ...string) bool {
	if metadata == nil || len(metadata.Require) == 0 {
		return false
	}
	for _, pkg := range packages {
		if _, ok := metadata.Require[pkg]; ok {
			return true
		}
	}
	return false
}

func detectPHPRequiredExtensions(metadata *composerJSON) []string {
	if metadata == nil || len(metadata.Require) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	var extensions []string
	for key := range metadata.Require {
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

	sort.Strings(extensions)
	return extensions
}

func applyPHPPlanDefaults(appPath string, plan *buildPlan) error {
	metadata := loadComposerJSON(appPath)
	framework, docroot, hasWebEntrypoint := detectPHPFrameworkAndDocroot(appPath, metadata)
	if strings.TrimSpace(plan.Framework) == "" {
		plan.Framework = framework
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
	plan.RuntimeEnv = map[string]string{
		"APP_ENV": "production",
	}
	plan.DocumentRoot = ""
	plan.RuntimeInitCommand = strings.TrimSpace(plan.RuntimeInitCommand)
	if plan.RuntimeFlavor == "apache" {
		plan.DocumentRoot = docroot
		if strings.TrimSpace(plan.RunCommand) == "" || strings.Contains(strings.ToLower(plan.RunCommand), "php-fpm") || strings.Contains(strings.ToLower(plan.RunCommand), "nginx") {
			plan.RunCommand = "apache2-foreground"
		}
		plan.RuntimeInitCommand = defaultString(plan.RuntimeInitCommand, detectPHPRuntimeInitCommand(plan.ExposePort))
		plan.RuntimeEnv["PORT"] = plan.ExposePort
	} else if plan.RuntimeFlavor == "fpm" {
		plan.DocumentRoot = docroot
		if strings.TrimSpace(plan.RunCommand) == "" || strings.TrimSpace(plan.RunCommand) == "apache2-foreground" {
			plan.RunCommand = detectPHPFPMRunCommand()
		}
		plan.RuntimeInitCommand = defaultString(plan.RuntimeInitCommand, detectPHPFPMRuntimeInitCommand(plan.ExposePort))
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
	if plan.RuntimeFlavor == "fpm" {
		plan.AptPackages = appendUniqueString(plan.AptPackages, "nginx")
	}
	plan.BootstrapCommands = mergeUniqueCommands(plan.BootstrapCommands, detectPHPBootstrapCommands(metadata, plan.RuntimeFlavor))
	plan.ValidationWarnings = mergeUniqueCommands(plan.ValidationWarnings, detectPHPValidationWarnings(metadata))

	if hasWebEntrypoint && plan.RuntimeFlavor == "cli" {
		plan.ValidationWarnings = appendUniqueString(plan.ValidationWarnings, "php app has a web entrypoint; submitted run command overrides the default web runtime")
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
	case hasPHPNginxHint(appPath):
		return "fpm"
	default:
		return "apache"
	}
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
		return "php:" + version + "-fpm"
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
	return fmt.Sprintf("PORT=\"${PORT:-%s}\"; sed -ri -e \"s/Listen [0-9]+/Listen ${PORT}/\" /etc/apache2/ports.conf; sed -ri -e \"s!<VirtualHost \\\\*:[0-9]+>!<VirtualHost *:${PORT}>!g\" /etc/apache2/sites-available/000-default.conf", port)
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
