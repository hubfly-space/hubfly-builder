package autodetect

import (
	"path/filepath"
	"sort"
	"strings"
)

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

	for _, fileName := range []string{"server.js", "app.js", "main.js", "dist/server.js", "build/server.js", "build/index.js", "build/handler.js"} {
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
