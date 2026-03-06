package autodetect

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type DockerfileAuditResult struct {
	Warnings []string
	Errors   []string
}

func AuditDockerfileWithOptions(opts AutoDetectOptions, dockerfilePath string) DockerfileAuditResult {
	repoRoot := strings.TrimSpace(opts.RepoRoot)
	if repoRoot == "" {
		return DockerfileAuditResult{Errors: []string{"repository root is required"}}
	}

	appDir, err := normalizeRelativeDir(opts.WorkingDir)
	if err != nil {
		return DockerfileAuditResult{Errors: []string{err.Error()}}
	}

	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return DockerfileAuditResult{Errors: []string{"failed to read Dockerfile"}}
	}

	appPath := repoRoot
	if appDir != "." {
		appPath = filepath.Join(repoRoot, filepath.FromSlash(appDir))
	}

	runtime, version := DetectRuntime(appPath)
	result := DockerfileAuditResult{}
	lower := strings.ToLower(string(data))

	for _, marker := range []string{
		"cmd npm run dev",
		"cmd yarn dev",
		"cmd pnpm dev",
		"cmd pnpm run dev",
		"cmd bun run dev",
		"cmd next dev",
		"cmd nuxt dev",
		"cmd vite dev",
		"entrypoint npm run dev",
		"entrypoint yarn dev",
		"entrypoint pnpm dev",
		"entrypoint bun run dev",
		"entrypoint next dev",
		"entrypoint nuxt dev",
		"entrypoint vite dev",
		"cmd php -s ",
		"entrypoint php -s ",
	} {
		if strings.Contains(lower, marker) {
			result.Errors = append(result.Errors, "Dockerfile uses a development server command for container startup")
			break
		}
	}

	if runtime != "node" && runtime != "bun" {
		if runtime == "php" {
			metadata := loadComposerJSON(appPath)
			_, _, hasWebEntrypoint := detectPHPFrameworkAndDocroot(appPath, metadata)
			if strings.Contains(lower, "php -s ") {
				result.Errors = append(result.Errors, "Dockerfile uses php -S, which is a development server and not a production runtime")
			}
			if fileExists(filepath.Join(appPath, "composer.json")) && !strings.Contains(lower, "composer install") {
				result.Warnings = append(result.Warnings, "PHP project detected but Dockerfile does not appear to run composer install")
			}
			for _, extension := range detectPHPRequiredExtensions(metadata) {
				rule, ok := phpExtensionRules[extension]
				if !ok {
					continue
				}
				if len(rule.peclInstall) > 0 {
					if !strings.Contains(lower, "pecl install "+extension) {
						result.Warnings = append(result.Warnings, "PHP project requires ext-"+extension+" but Dockerfile does not appear to install it via PECL")
					}
					if !strings.Contains(lower, "docker-php-ext-enable "+extension) && !strings.Contains(lower, "docker-php-ext-enable "+strings.Join(rule.enable, " ")) {
						result.Warnings = append(result.Warnings, "PHP project requires ext-"+extension+" but Dockerfile does not appear to enable it")
					}
				}
			}
			if hasWebEntrypoint && strings.Contains(lower, "php-fpm") && !strings.Contains(lower, "nginx") && !strings.Contains(lower, "apache2-foreground") {
				result.Warnings = append(result.Warnings, "Dockerfile appears to run php-fpm for a web app without starting an HTTP server")
			}
		}
		if runtime == "python" {
			requiredPackages := detectPythonSystemPackages(appPath)
			if len(requiredPackages) > 0 && !strings.Contains(lower, "apt-get install") && !strings.Contains(lower, "apk add") && !strings.Contains(lower, "yum install") && !strings.Contains(lower, "dnf install") {
				result.Warnings = append(result.Warnings, "Python dependencies suggest additional system packages, but the Dockerfile does not appear to install any")
			}
			if len(detectPythonSetupCommands(appPath)) > 0 && !strings.Contains(lower, "python -m playwright install") {
				result.Warnings = append(result.Warnings, "Python Playwright dependency detected but Dockerfile does not appear to install browser binaries")
			}
		}
		result.Warnings = uniqueSortedStrings(result.Warnings)
		result.Errors = uniqueSortedStrings(result.Errors)
		return result
	}

	ctx := newJSProjectContext(repoRoot, appDir, appPath, runtime, version)
	framework := detectJSFramework(ctx)

	if framework == "vite" && strings.Contains(lower, "vite preview") {
		if !strings.Contains(lower, "--host 0.0.0.0") && !strings.Contains(lower, "--host=0.0.0.0") {
			result.Errors = append(result.Errors, "Dockerfile runs Vite preview without binding to 0.0.0.0")
		}
		if !strings.Contains(lower, "--port") && !strings.Contains(lower, "${port") {
			result.Warnings = append(result.Warnings, "Dockerfile runs Vite preview without explicit PORT wiring")
		}
	}
	if framework == "vite" && (strings.Contains(lower, "cmd npm run preview") || strings.Contains(lower, "cmd yarn preview") || strings.Contains(lower, "cmd pnpm preview") || strings.Contains(lower, "cmd bun run preview")) {
		result.Errors = append(result.Errors, "Dockerfile starts a Vite preview server instead of a production static server")
	}

	pmName, pmVersion := parsePackageManagerSpec(ctx.PackageManagerSpec)
	if pmVersion != "" && (pmName == "pnpm" || pmName == "yarn") && !strings.Contains(lower, "corepack") {
		result.Warnings = append(result.Warnings, "Dockerfile does not enable Corepack for the declared package manager version")
	}

	if detectJavaScriptPrisma(ctx.AppPath, ctx.BuildContextPath, ctx.AppMetadata) && !strings.Contains(lower, "prisma generate") && !hasScriptBodyContaining(ctx.AppMetadata, "prisma generate") {
		result.Warnings = append(result.Warnings, "Prisma detected but Dockerfile does not appear to generate the Prisma client")
	}
	if framework == "next" && !hasAnyPackage(ctx.AppMetadata, "sharp") && !hasAnyPackage(ctx.RootMetadata, "sharp") && !strings.Contains(lower, "install sharp") && !strings.Contains(lower, "add sharp") {
		result.Warnings = append(result.Warnings, "Next.js app does not declare sharp, so production image optimization may use more memory unless the Dockerfile installs it")
	}

	if strings.Contains(lower, "node:") && strings.Contains(lower, "alpine") && len(detectJavaScriptSystemPackages(ctx.AppMetadata)) > 0 {
		result.Warnings = append(result.Warnings, "Dockerfile uses an Alpine Node image for dependencies that typically require Debian-based runtime libraries")
	}

	if detectJavaScriptPlaywright(ctx.AppMetadata) && !strings.Contains(lower, "playwright install") && !strings.Contains(lower, "mcr.microsoft.com/playwright") {
		result.Warnings = append(result.Warnings, "Playwright detected but Dockerfile does not appear to install browser binaries")
	}

	result.Warnings = uniqueSortedStrings(result.Warnings)
	result.Errors = uniqueSortedStrings(result.Errors)
	return result
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
