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
		return "yarn run " + script
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
	_ = metadata
	return nil
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
