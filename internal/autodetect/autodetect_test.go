package autodetect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hubfly-builder/internal/allowlist"
)

func touchFile(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("failed to create %s: %v", name, err)
	}
}

func writePackageJSON(t *testing.T, dir string, scripts map[string]string, packageManager string) {
	t.Helper()

	writePackageJSONWithFields(t, dir, scripts, packageManager, nil, nil, nil)
}

func writePackageJSONWithFields(t *testing.T, dir string, scripts map[string]string, packageManager string, dependencies, devDependencies map[string]string, workspaces interface{}) {
	t.Helper()

	payload := map[string]interface{}{
		"name": "sample-app",
	}
	if len(scripts) > 0 {
		payload["scripts"] = scripts
	}
	if packageManager != "" {
		payload["packageManager"] = packageManager
	}
	if len(dependencies) > 0 {
		payload["dependencies"] = dependencies
	}
	if len(devDependencies) > 0 {
		payload["devDependencies"] = devDependencies
	}
	if workspaces != nil {
		payload["workspaces"] = workspaces
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), data, 0o644); err != nil {
		t.Fatalf("failed to write package.json: %v", err)
	}
}

func writeComposerJSON(t *testing.T, dir string, require map[string]string) {
	t.Helper()

	payload := map[string]interface{}{
		"name": "sample/php-app",
	}
	if len(require) > 0 {
		payload["require"] = require
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal composer.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), data, 0o644); err != nil {
		t.Fatalf("failed to write composer.json: %v", err)
	}
}

func javaAllowedCommands() *allowlist.AllowedCommands {
	return &allowlist.AllowedCommands{
		Prebuild: []string{
			"mvn clean",
			"./mvnw clean",
			"gradle dependencies",
			"./gradlew dependencies",
		},
		Build: []string{
			"mvn install -DskipTests",
			"./mvnw install -DskipTests",
			"gradle build -x test",
			"./gradlew build -x test",
		},
		Run: []string{
			"java -jar target/*.jar",
			"java -jar build/libs/*.jar",
		},
	}
}

func nodeAllowedCommands() *allowlist.AllowedCommands {
	return &allowlist.AllowedCommands{
		Prebuild: []string{
			"npm ci",
			"npm install",
			"yarn install",
			"pnpm install",
		},
		Build: []string{
			"npm run build",
			"npm run build:*",
			"yarn build",
			"yarn run build",
			"yarn run build:*",
			"yarn build:*",
			"pnpm run build",
			"pnpm run build:*",
			"pnpm build",
			"pnpm build:*",
		},
		Run: []string{
			"npm start",
			"npm run start",
			"npm run *",
			"npm run serve",
			"npm run preview",
			"npm run dev",
			"yarn start",
			"yarn run start",
			"yarn run *",
			"yarn serve",
			"yarn preview",
			"yarn dev",
			"yarn run serve",
			"yarn run preview",
			"yarn run dev",
			"pnpm start",
			"pnpm run start",
			"pnpm run *",
			"pnpm run serve",
			"pnpm run preview",
			"pnpm run dev",
			"pnpm serve",
			"pnpm preview",
			"pnpm dev",
			"node server.js",
		},
	}
}

func pythonAllowedCommands() *allowlist.AllowedCommands {
	return &allowlist.AllowedCommands{
		Prebuild: []string{
			"pip install -r requirements.txt",
			"pip install pipenv && pipenv install --system --deploy",
			"pip install .",
		},
		Build: []string{
			"python setup.py build",
		},
		Run: []string{
			"python main.py",
			"python app.py",
			"python server.py",
			"python run.py",
			"python manage.py",
			"python manage.py runserver 0.0.0.0:${PORT:-8000}",
			"python *.py",
			"python -m *",
			"uvicorn *:* --host 0.0.0.0 --port ${PORT:-8000}",
			"uvicorn *:app --host 0.0.0.0 --port ${PORT:-8000}",
			"uvicorn *:application --host 0.0.0.0 --port ${PORT:-8000}",
			"gunicorn *:* --bind 0.0.0.0:${PORT:-8000}",
			"gunicorn *:app --bind 0.0.0.0:${PORT:-8000}",
			"gunicorn *:application --bind 0.0.0.0:${PORT:-8000}",
			"flask run --host=0.0.0.0 --port=${PORT:-8000}",
		},
	}
}

func goAllowedCommands() *allowlist.AllowedCommands {
	return &allowlist.AllowedCommands{
		Prebuild: []string{
			"go work sync",
			"go mod download",
		},
		Build: []string{
			"go build -o app .",
			"go build -o app ./cmd/*",
			"go build -o app ./*",
			"go build ./...",
		},
		Run: []string{
			"./app",
			"go run .",
			"go run ./cmd/*",
			"go run ./*",
			"go run main.go",
		},
	}
}

func phpAllowedCommands() *allowlist.AllowedCommands {
	return &allowlist.AllowedCommands{
		Prebuild: []string{
			"composer install",
			"composer install --no-dev --prefer-dist --optimize-autoloader --no-interaction",
		},
		Build: []string{
			"composer dump-autoload --optimize",
			"php artisan optimize",
			"php bin/console cache:clear --env=prod --no-debug",
		},
		Run: []string{
			"apache2-foreground",
			"php-fpm -D && exec nginx -g 'daemon off;'",
			"php *.php",
			"php artisan queue:work",
			"php bin/console messenger:consume async",
		},
	}
}

func TestAutoDetectBuildConfigJavaMaven(t *testing.T) {
	repo := t.TempDir()
	touchFile(t, repo, "pom.xml")

	cfg, err := AutoDetectBuildConfig(repo, javaAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.Runtime != "java" {
		t.Fatalf("expected runtime java, got %q", cfg.Runtime)
	}
	if cfg.PrebuildCommand != "mvn clean" {
		t.Fatalf("expected maven prebuild command, got %q", cfg.PrebuildCommand)
	}
	if cfg.BuildCommand != "mvn install -DskipTests" {
		t.Fatalf("expected maven build command, got %q", cfg.BuildCommand)
	}
	if cfg.RunCommand != "java -jar target/*.jar" {
		t.Fatalf("expected maven run command, got %q", cfg.RunCommand)
	}
	dockerfile := string(cfg.DockerfileContent)
	if !strings.Contains(dockerfile, "FROM maven:3.9-eclipse-temurin-17") {
		t.Fatalf("expected maven base image in Dockerfile, got:\n%s", dockerfile)
	}
}

func TestAutoDetectBuildConfigJavaGradleWrapper(t *testing.T) {
	repo := t.TempDir()
	touchFile(t, repo, "build.gradle")
	touchFile(t, repo, "gradlew")

	cfg, err := AutoDetectBuildConfig(repo, javaAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.PrebuildCommand != "./gradlew dependencies" {
		t.Fatalf("expected gradle wrapper prebuild command, got %q", cfg.PrebuildCommand)
	}
	if cfg.BuildCommand != "./gradlew build -x test" {
		t.Fatalf("expected gradle wrapper build command, got %q", cfg.BuildCommand)
	}
	if cfg.RunCommand != "java -jar build/libs/*.jar" {
		t.Fatalf("expected gradle run command, got %q", cfg.RunCommand)
	}
	dockerfile := string(cfg.DockerfileContent)
	if !strings.Contains(dockerfile, "FROM gradle:8-jdk17") {
		t.Fatalf("expected gradle base image in Dockerfile, got:\n%s", dockerfile)
	}
}

func TestGenerateDockerfileJavaFallbackBase(t *testing.T) {
	content, err := GenerateDockerfile("java", "21", "", "", "java -jar app.jar")
	if err != nil {
		t.Fatalf("GenerateDockerfile returned error: %v", err)
	}

	if !strings.Contains(string(content), "FROM eclipse-temurin:21-jdk") {
		t.Fatalf("expected temurin base image, got:\n%s", string(content))
	}
}

func TestAutoDetectBuildConfigNodeUsesNpmCIAndScripts(t *testing.T) {
	repo := t.TempDir()
	writePackageJSON(t, repo, map[string]string{
		"build": "webpack",
		"start": "node dist/server.js",
	}, "")
	touchFile(t, repo, "package-lock.json")

	cfg, err := AutoDetectBuildConfig(repo, nodeAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.Runtime != "node" {
		t.Fatalf("expected runtime node, got %q", cfg.Runtime)
	}
	if cfg.PrebuildCommand != "npm ci" {
		t.Fatalf("expected npm ci prebuild command, got %q", cfg.PrebuildCommand)
	}
	if cfg.BuildCommand != "npm run build" {
		t.Fatalf("expected npm run build command, got %q", cfg.BuildCommand)
	}
	if cfg.RunCommand != "npm start" {
		t.Fatalf("expected npm start command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigNodePnpmServeNoBuild(t *testing.T) {
	repo := t.TempDir()
	writePackageJSON(t, repo, map[string]string{
		"serve": "node server.js",
	}, "pnpm@9.0.0")

	cfg, err := AutoDetectBuildConfig(repo, nodeAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.PrebuildCommand != "pnpm install" {
		t.Fatalf("expected pnpm install prebuild command, got %q", cfg.PrebuildCommand)
	}
	if cfg.BuildCommand != "" {
		t.Fatalf("expected empty build command, got %q", cfg.BuildCommand)
	}
	if cfg.RunCommand != "pnpm run serve" {
		t.Fatalf("expected pnpm run serve command, got %q", cfg.RunCommand)
	}
	dockerfile := string(cfg.DockerfileContent)
	if strings.Contains(dockerfile, "RUN pnpm run build") {
		t.Fatalf("did not expect build RUN line in Dockerfile:\n%s", dockerfile)
	}
}

func TestAutoDetectBuildConfigNodeFallbackToServerJS(t *testing.T) {
	repo := t.TempDir()
	writePackageJSON(t, repo, nil, "")
	touchFile(t, repo, "server.js")

	cfg, err := AutoDetectBuildConfig(repo, nodeAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.PrebuildCommand != "npm install" && cfg.PrebuildCommand != "npm ci" {
		t.Fatalf("expected npm install or npm ci prebuild command, got %q", cfg.PrebuildCommand)
	}
	if cfg.BuildCommand != "" {
		t.Fatalf("expected empty build command, got %q", cfg.BuildCommand)
	}
	if cfg.RunCommand != "node server.js" {
		t.Fatalf("expected node server.js command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigNodeWithoutProductionRunCommandFails(t *testing.T) {
	repo := t.TempDir()
	writePackageJSON(t, repo, map[string]string{
		"dev": "vite dev",
	}, "")

	if _, err := AutoDetectBuildConfig(repo, nodeAllowedCommands()); err == nil {
		t.Fatalf("expected AutoDetectBuildConfig to fail without a production run command")
	}
}

func TestAutoDetectBuildConfigNodeCustomStartScript(t *testing.T) {
	repo := t.TempDir()
	writePackageJSON(t, repo, map[string]string{
		"start:prod": "node dist/server.js",
	}, "")
	touchFile(t, repo, "package-lock.json")

	cfg, err := AutoDetectBuildConfig(repo, nodeAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.PrebuildCommand != "npm ci" {
		t.Fatalf("expected npm ci prebuild command, got %q", cfg.PrebuildCommand)
	}
	if cfg.BuildCommand != "" {
		t.Fatalf("expected empty build command, got %q", cfg.BuildCommand)
	}
	if cfg.RunCommand != "npm run start:prod" {
		t.Fatalf("expected npm run start:prod command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigViteUsesStaticRuntime(t *testing.T) {
	repo := t.TempDir()
	writePackageJSONWithFields(t, repo, map[string]string{
		"build":   "vite build",
		"preview": "vite preview",
	}, "", map[string]string{
		"react": "18.0.0",
	}, map[string]string{
		"vite": "5.0.0",
	}, nil)
	touchFile(t, repo, "package-lock.json")
	touchFile(t, repo, "vite.config.ts")

	cfg, err := AutoDetectBuildConfig(repo, nodeAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.Runtime != "static" {
		t.Fatalf("expected static runtime, got %q", cfg.Runtime)
	}
	if cfg.Framework != "vite" {
		t.Fatalf("expected vite framework, got %q", cfg.Framework)
	}
	if cfg.BuildCommand != "npm run build" {
		t.Fatalf("expected npm run build command, got %q", cfg.BuildCommand)
	}
	if cfg.RunCommand != "" {
		t.Fatalf("expected empty run command for static runtime, got %q", cfg.RunCommand)
	}
	dockerfile := string(cfg.DockerfileContent)
	if !strings.Contains(dockerfile, "FROM node:22-bookworm-slim AS builder") {
		t.Fatalf("expected builder stage in Dockerfile, got:\n%s", dockerfile)
	}
	if !strings.Contains(dockerfile, "FROM nginx:alpine") {
		t.Fatalf("expected nginx runtime stage in Dockerfile, got:\n%s", dockerfile)
	}
	if !strings.Contains(dockerfile, "COPY --from=builder /app/dist/ ./") {
		t.Fatalf("expected dist copy in Dockerfile, got:\n%s", dockerfile)
	}
	if !strings.Contains(dockerfile, "listen ${PORT};") {
		t.Fatalf("expected dynamic PORT nginx template, got:\n%s", dockerfile)
	}
}

func TestAutoDetectBuildConfigNodePrismaAddsGenerateAndRuntimeInit(t *testing.T) {
	repo := t.TempDir()
	writePackageJSONWithFields(t, repo, map[string]string{
		"build": "tsc -p .",
		"start": "node dist/server.js",
	}, "", map[string]string{
		"@prisma/client": "5.0.0",
	}, map[string]string{
		"prisma": "5.0.0",
	}, nil)
	touchFile(t, repo, "package-lock.json")
	if err := os.MkdirAll(filepath.Join(repo, "prisma"), 0o755); err != nil {
		t.Fatalf("failed to create prisma dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "prisma", "schema.prisma"), []byte("datasource db { provider = \"sqlite\" url = \"file:dev.db\" }\n"), 0o644); err != nil {
		t.Fatalf("failed to write schema.prisma: %v", err)
	}

	cfg, err := AutoDetectBuildConfig(repo, nodeAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	foundGenerate := false
	for _, command := range cfg.SetupCommands {
		if command == "npx prisma generate" {
			foundGenerate = true
			break
		}
	}
	if !foundGenerate {
		t.Fatalf("expected prisma generate setup command, got %#v", cfg.SetupCommands)
	}
	if !strings.Contains(cfg.RuntimeInitCommand, "PRISMA_RUN_MIGRATIONS") {
		t.Fatalf("expected Prisma runtime init command, got %q", cfg.RuntimeInitCommand)
	}
	dockerfile := string(cfg.DockerfileContent)
	if !strings.Contains(dockerfile, "RUN npx prisma generate") {
		t.Fatalf("expected prisma generate RUN line, got:\n%s", dockerfile)
	}
	if !strings.Contains(dockerfile, "PRISMA_RUN_MIGRATIONS") {
		t.Fatalf("expected Prisma runtime init in Dockerfile, got:\n%s", dockerfile)
	}
}

func TestAutoDetectBuildConfigNextInfersPortFromScript(t *testing.T) {
	repo := t.TempDir()
	writePackageJSONWithFields(t, repo, map[string]string{
		"build": "next build",
		"start": "next start --port 4100",
	}, "", map[string]string{
		"next":  "15.0.0",
		"react": "19.0.0",
	}, nil, nil)
	touchFile(t, repo, "package-lock.json")

	cfg, err := AutoDetectBuildConfig(repo, nodeAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.ExposePort != "4100" {
		t.Fatalf("expected inferred expose port 4100, got %q", cfg.ExposePort)
	}
	if !strings.Contains(cfg.RunCommand, "${PORT:-4100}") {
		t.Fatalf("expected run command to use inferred port, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigNextInstallsSharpWhenMissing(t *testing.T) {
	repo := t.TempDir()
	writePackageJSONWithFields(t, repo, map[string]string{
		"build": "next build",
		"start": "next start",
	}, "", map[string]string{
		"next":      "15.0.0",
		"react":     "19.0.0",
		"react-dom": "19.0.0",
	}, nil, nil)
	touchFile(t, repo, "package-lock.json")

	cfg, err := AutoDetectBuildConfig(repo, nodeAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	foundSharp := false
	for _, command := range cfg.SetupCommands {
		if command == "npm install sharp" {
			foundSharp = true
			break
		}
	}
	if !foundSharp {
		t.Fatalf("expected sharp install setup command, got %#v", cfg.SetupCommands)
	}
	foundWarning := false
	for _, warning := range cfg.ValidationWarnings {
		if warning == "Next.js app does not declare sharp; builder will install it for production image optimization" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected Next.js sharp warning, got %#v", cfg.ValidationWarnings)
	}
	dockerfile := string(cfg.DockerfileContent)
	if !strings.Contains(dockerfile, "RUN npm install sharp") {
		t.Fatalf("expected Dockerfile to install sharp, got:\n%s", dockerfile)
	}
	if !strings.Contains(dockerfile, "apt-get install -y --no-install-recommends") {
		t.Fatalf("expected Dockerfile to install native packages, got:\n%s", dockerfile)
	}
}

func TestAutoDetectBuildConfigWorkspaceUsesRootBuildContext(t *testing.T) {
	repo := t.TempDir()
	writePackageJSONWithFields(t, repo, nil, "pnpm@9.0.0", nil, nil, []string{"apps/*"})
	touchFile(t, repo, "pnpm-lock.yaml")
	touchFile(t, repo, "pnpm-workspace.yaml")

	appDir := filepath.Join(repo, "apps", "web")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("failed to create app dir: %v", err)
	}
	writePackageJSONWithFields(t, appDir, map[string]string{
		"build":   "vite build",
		"preview": "vite preview",
	}, "", map[string]string{
		"react": "18.0.0",
	}, map[string]string{
		"vite": "5.0.0",
	}, nil)
	touchFile(t, appDir, "vite.config.ts")

	cfg, err := AutoDetectBuildConfigWithOptions(AutoDetectOptions{
		RepoRoot:   repo,
		WorkingDir: "apps/web",
	}, nodeAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfigWithOptions returned error: %v", err)
	}

	if cfg.BuildContextDir != "." {
		t.Fatalf("expected root build context, got %q", cfg.BuildContextDir)
	}
	if cfg.AppDir != "apps/web" {
		t.Fatalf("expected app dir apps/web, got %q", cfg.AppDir)
	}
	if cfg.PrebuildCommand != "pnpm install --frozen-lockfile" {
		t.Fatalf("expected frozen pnpm install, got %q", cfg.PrebuildCommand)
	}
	if cfg.BuildCommand != "cd 'apps/web' && pnpm run build" {
		t.Fatalf("expected build command to run in app dir, got %q", cfg.BuildCommand)
	}
	dockerfile := string(cfg.DockerfileContent)
	if !strings.Contains(dockerfile, "RUN corepack enable") {
		t.Fatalf("expected corepack enable in Dockerfile, got:\n%s", dockerfile)
	}
	if !strings.Contains(dockerfile, "RUN corepack prepare pnpm@9.0.0 --activate") {
		t.Fatalf("expected corepack prepare in Dockerfile, got:\n%s", dockerfile)
	}
	if !strings.Contains(dockerfile, "COPY --from=builder /app/apps/web/dist/ ./") {
		t.Fatalf("expected workspace dist copy in Dockerfile, got:\n%s", dockerfile)
	}
}

func TestAutoDetectBuildConfigPythonDjango(t *testing.T) {
	repo := t.TempDir()
	touchFile(t, repo, "requirements.txt")
	touchFile(t, repo, "manage.py")

	cfg, err := AutoDetectBuildConfig(repo, pythonAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.Runtime != "python" {
		t.Fatalf("expected runtime python, got %q", cfg.Runtime)
	}
	if cfg.PrebuildCommand != "pip install -r requirements.txt" {
		t.Fatalf("expected pip install from requirements, got %q", cfg.PrebuildCommand)
	}
	if cfg.RunCommand != "python manage.py runserver 0.0.0.0:${PORT:-8000}" {
		t.Fatalf("expected django runserver command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigPythonFastAPI(t *testing.T) {
	repo := t.TempDir()
	mainPy := `from fastapi import FastAPI

api = FastAPI()
`
	if err := os.WriteFile(filepath.Join(repo, "main.py"), []byte(mainPy), 0o644); err != nil {
		t.Fatalf("failed to write main.py: %v", err)
	}
	touchFile(t, repo, "pyproject.toml")

	cfg, err := AutoDetectBuildConfig(repo, pythonAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.PrebuildCommand != "pip install ." {
		t.Fatalf("expected pip install . prebuild, got %q", cfg.PrebuildCommand)
	}
	if cfg.RunCommand != "uvicorn main:api --host 0.0.0.0 --port ${PORT:-8000}" {
		t.Fatalf("expected uvicorn fastapi command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigPythonModuleEntrypoint(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "myapp"), 0o755); err != nil {
		t.Fatalf("failed to create package dir: %v", err)
	}
	touchFile(t, filepath.Join(repo, "myapp"), "__main__.py")
	touchFile(t, repo, "pyproject.toml")

	cfg, err := AutoDetectBuildConfig(repo, pythonAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.RunCommand != "python -m myapp" {
		t.Fatalf("expected python -m module command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigPythonPipfile(t *testing.T) {
	repo := t.TempDir()
	touchFile(t, repo, "Pipfile")
	touchFile(t, repo, "app.py")

	cfg, err := AutoDetectBuildConfig(repo, pythonAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.PrebuildCommand != "pip install pipenv && pipenv install --system --deploy" {
		t.Fatalf("expected pipenv prebuild command, got %q", cfg.PrebuildCommand)
	}
	if cfg.RunCommand != "python app.py" {
		t.Fatalf("expected python app.py run command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigPythonWSGI(t *testing.T) {
	repo := t.TempDir()
	wsgiPy := `application = object()
`
	if err := os.WriteFile(filepath.Join(repo, "wsgi.py"), []byte(wsgiPy), 0o644); err != nil {
		t.Fatalf("failed to write wsgi.py: %v", err)
	}
	touchFile(t, repo, "pyproject.toml")

	cfg, err := AutoDetectBuildConfig(repo, pythonAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.RunCommand != "gunicorn wsgi:application --bind 0.0.0.0:${PORT:-8000}" {
		t.Fatalf("expected gunicorn wsgi command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigPythonASGI(t *testing.T) {
	repo := t.TempDir()
	asgiPy := `application = object()
`
	if err := os.WriteFile(filepath.Join(repo, "asgi.py"), []byte(asgiPy), 0o644); err != nil {
		t.Fatalf("failed to write asgi.py: %v", err)
	}
	touchFile(t, repo, "pyproject.toml")

	cfg, err := AutoDetectBuildConfig(repo, pythonAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.RunCommand != "uvicorn asgi:application --host 0.0.0.0 --port ${PORT:-8000}" {
		t.Fatalf("expected uvicorn asgi command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigPythonPlaywrightAddsSystemDeps(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "requirements.txt"), []byte("playwright==1.44.0\n"), 0o644); err != nil {
		t.Fatalf("failed to write requirements.txt: %v", err)
	}
	touchFile(t, repo, "app.py")

	cfg, err := AutoDetectBuildConfig(repo, pythonAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	found := false
	for _, command := range cfg.SetupCommands {
		if command == "python -m playwright install chromium" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected playwright browser install command, got %#v", cfg.SetupCommands)
	}

	dockerfile := string(cfg.DockerfileContent)
	if !strings.Contains(dockerfile, "apt-get install -y --no-install-recommends") {
		t.Fatalf("expected apt install line in Dockerfile, got:\n%s", dockerfile)
	}
	if !strings.Contains(dockerfile, "RUN python -m playwright install chromium") {
		t.Fatalf("expected playwright install RUN line, got:\n%s", dockerfile)
	}
}

func TestAutoDetectBuildConfigGoRootMain(t *testing.T) {
	repo := t.TempDir()
	touchFile(t, repo, "go.mod")
	mainGo := `package main

func main() {}
`
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatalf("failed to write main.go: %v", err)
	}

	cfg, err := AutoDetectBuildConfig(repo, goAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.Runtime != "go" {
		t.Fatalf("expected runtime go, got %q", cfg.Runtime)
	}
	if cfg.PrebuildCommand != "go mod download" {
		t.Fatalf("expected go mod download prebuild command, got %q", cfg.PrebuildCommand)
	}
	if cfg.BuildCommand != "go build -o app ." {
		t.Fatalf("expected go build root command, got %q", cfg.BuildCommand)
	}
	if cfg.RunCommand != "./app" {
		t.Fatalf("expected go binary run command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigGoCmdEntrypoint(t *testing.T) {
	repo := t.TempDir()
	touchFile(t, repo, "go.mod")
	if err := os.MkdirAll(filepath.Join(repo, "cmd", "api"), 0o755); err != nil {
		t.Fatalf("failed to create cmd/api: %v", err)
	}
	mainGo := `package main

func main() {}
`
	if err := os.WriteFile(filepath.Join(repo, "cmd", "api", "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatalf("failed to write cmd/api/main.go: %v", err)
	}

	cfg, err := AutoDetectBuildConfig(repo, goAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.BuildCommand != "go build -o app ./cmd/api" {
		t.Fatalf("expected go build cmd command, got %q", cfg.BuildCommand)
	}
	if cfg.RunCommand != "./app" {
		t.Fatalf("expected go binary run command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigGoTopLevelEntrypoint(t *testing.T) {
	repo := t.TempDir()
	touchFile(t, repo, "go.mod")
	if err := os.MkdirAll(filepath.Join(repo, "server"), 0o755); err != nil {
		t.Fatalf("failed to create server dir: %v", err)
	}
	mainGo := `package main

func main() {}
`
	if err := os.WriteFile(filepath.Join(repo, "server", "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatalf("failed to write server/main.go: %v", err)
	}

	cfg, err := AutoDetectBuildConfig(repo, goAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.BuildCommand != "go build -o app ./server" {
		t.Fatalf("expected go build top-level command, got %q", cfg.BuildCommand)
	}
	if cfg.RunCommand != "./app" {
		t.Fatalf("expected go binary run command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigGoNestedEntrypoint(t *testing.T) {
	repo := t.TempDir()
	touchFile(t, repo, "go.mod")
	if err := os.MkdirAll(filepath.Join(repo, "services", "api"), 0o755); err != nil {
		t.Fatalf("failed to create services/api dir: %v", err)
	}
	mainGo := `package main

func main() {}
`
	if err := os.WriteFile(filepath.Join(repo, "services", "api", "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatalf("failed to write services/api/main.go: %v", err)
	}

	cfg, err := AutoDetectBuildConfig(repo, goAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.BuildCommand != "go build -o app ./services/api" {
		t.Fatalf("expected go build nested command, got %q", cfg.BuildCommand)
	}
	if cfg.RunCommand != "./app" {
		t.Fatalf("expected go binary run command, got %q", cfg.RunCommand)
	}
}

func TestAutoDetectBuildConfigGoWorkSyncPreferred(t *testing.T) {
	repo := t.TempDir()
	touchFile(t, repo, "go.mod")
	touchFile(t, repo, "go.work")
	mainGo := `package main

func main() {}
`
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatalf("failed to write main.go: %v", err)
	}

	cfg, err := AutoDetectBuildConfig(repo, goAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.PrebuildCommand != "go work sync" {
		t.Fatalf("expected go work sync prebuild command, got %q", cfg.PrebuildCommand)
	}
}

func TestAutoDetectBuildConfigPHPLaravelUsesApacheRuntime(t *testing.T) {
	repo := t.TempDir()
	writeComposerJSON(t, repo, map[string]string{
		"laravel/framework": "^11.0",
		"ext-intl":          "*",
		"ext-pdo_mysql":     "*",
	})
	touchFile(t, repo, "composer.lock")
	touchFile(t, repo, "artisan")
	if err := os.MkdirAll(filepath.Join(repo, "public"), 0o755); err != nil {
		t.Fatalf("failed to create public dir: %v", err)
	}
	touchFile(t, filepath.Join(repo, "public"), "index.php")

	cfg, err := AutoDetectBuildConfig(repo, phpAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.Runtime != "php" {
		t.Fatalf("expected runtime php, got %q", cfg.Runtime)
	}
	if cfg.Framework != "laravel" {
		t.Fatalf("expected laravel framework, got %q", cfg.Framework)
	}
	if cfg.PrebuildCommand != "composer install --no-dev --prefer-dist --optimize-autoloader --no-interaction" {
		t.Fatalf("expected composer install command, got %q", cfg.PrebuildCommand)
	}
	if cfg.BuildCommand != "php artisan optimize" {
		t.Fatalf("expected laravel optimize command, got %q", cfg.BuildCommand)
	}
	if cfg.RunCommand != "apache2-foreground" {
		t.Fatalf("expected apache runtime command, got %q", cfg.RunCommand)
	}
	if cfg.ExposePort != "8080" {
		t.Fatalf("expected php expose port 8080, got %q", cfg.ExposePort)
	}

	dockerfile := string(cfg.DockerfileContent)
	for _, snippet := range []string{
		"FROM php:8.3-apache",
		"COPY --from=composer:2 /usr/bin/composer /usr/local/bin/composer",
		"RUN a2enmod rewrite",
		"RUN docker-php-ext-install intl opcache pdo_mysql",
		"s!/var/www/html!/app/public!g",
		"exec apache2-foreground",
	} {
		if !strings.Contains(dockerfile, snippet) {
			t.Fatalf("expected Dockerfile to contain %q, got:\n%s", snippet, dockerfile)
		}
	}
}

func TestAutoDetectBuildConfigPHPSymfonyBuildCommand(t *testing.T) {
	repo := t.TempDir()
	writeComposerJSON(t, repo, map[string]string{
		"symfony/framework-bundle": "^7.0",
		"ext-intl":                 "*",
		"ext-zip":                  "*",
	})
	touchFile(t, repo, "composer.lock")
	if err := os.MkdirAll(filepath.Join(repo, "bin"), 0o755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}
	touchFile(t, filepath.Join(repo, "bin"), "console")
	if err := os.MkdirAll(filepath.Join(repo, "public"), 0o755); err != nil {
		t.Fatalf("failed to create public dir: %v", err)
	}
	touchFile(t, filepath.Join(repo, "public"), "index.php")

	cfg, err := AutoDetectBuildConfig(repo, phpAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.Framework != "symfony" {
		t.Fatalf("expected symfony framework, got %q", cfg.Framework)
	}
	if cfg.BuildCommand != "php bin/console cache:clear --env=prod --no-debug" {
		t.Fatalf("expected symfony build command, got %q", cfg.BuildCommand)
	}
	dockerfile := string(cfg.DockerfileContent)
	if !strings.Contains(dockerfile, "RUN docker-php-ext-install intl opcache zip") {
		t.Fatalf("expected php extensions in Dockerfile, got:\n%s", dockerfile)
	}
}

func TestAutoDetectBuildConfigPHPCLIWorkerUsesCLIImage(t *testing.T) {
	repo := t.TempDir()
	writeComposerJSON(t, repo, map[string]string{
		"symfony/console": "^7.0",
	})
	touchFile(t, repo, "composer.lock")
	touchFile(t, repo, "worker.php")

	cfg, err := AutoDetectBuildConfig(repo, phpAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.RunCommand != "php worker.php" {
		t.Fatalf("expected php worker runtime command, got %q", cfg.RunCommand)
	}
	if cfg.ExposePort != "" {
		t.Fatalf("expected no exposed port for php cli worker, got %q", cfg.ExposePort)
	}
	dockerfile := string(cfg.DockerfileContent)
	if !strings.Contains(dockerfile, "FROM php:8.3-cli") {
		t.Fatalf("expected cli image in Dockerfile, got:\n%s", dockerfile)
	}
	if strings.Contains(dockerfile, "a2enmod rewrite") {
		t.Fatalf("did not expect apache module enable in cli Dockerfile:\n%s", dockerfile)
	}
}

func TestAutoDetectBuildConfigPHPFPMNginxModeAndPECL(t *testing.T) {
	repo := t.TempDir()
	writeComposerJSON(t, repo, map[string]string{
		"laravel/framework": "^11.0",
		"ext-imagick":       "*",
		"ext-redis":         "*",
	})
	touchFile(t, repo, "composer.lock")
	touchFile(t, repo, "artisan")
	touchFile(t, repo, "nginx.conf")
	if err := os.MkdirAll(filepath.Join(repo, "public"), 0o755); err != nil {
		t.Fatalf("failed to create public dir: %v", err)
	}
	touchFile(t, filepath.Join(repo, "public"), "index.php")

	cfg, err := AutoDetectBuildConfig(repo, phpAllowedCommands())
	if err != nil {
		t.Fatalf("AutoDetectBuildConfig returned error: %v", err)
	}

	if cfg.RunCommand != "php-fpm -D && exec nginx -g 'daemon off;'" {
		t.Fatalf("expected php-fpm nginx run command, got %q", cfg.RunCommand)
	}
	if cfg.ExposePort != "8080" {
		t.Fatalf("expected fpm expose port 8080, got %q", cfg.ExposePort)
	}
	dockerfile := string(cfg.DockerfileContent)
	for _, snippet := range []string{
		"FROM php:8.3-fpm",
		"apt-get install -y --no-install-recommends $PHPIZE_DEPS git imagemagick libmagickwand-dev nginx unzip",
		"RUN printf \"\\n\" | pecl install imagick",
		"RUN printf \"\\n\" | pecl install redis",
		"RUN docker-php-ext-enable imagick redis",
		"/etc/nginx/templates/hubfly-default.conf.template",
		"fastcgi_pass 127.0.0.1:9000;",
	} {
		if !strings.Contains(dockerfile, snippet) {
			t.Fatalf("expected Dockerfile to contain %q, got:\n%s", snippet, dockerfile)
		}
	}
}

func TestFinalizeBuildConfigWithOptionsSupportsManualPHPFPMMode(t *testing.T) {
	repo := t.TempDir()
	writeComposerJSON(t, repo, map[string]string{
		"symfony/framework-bundle": "^7.0",
	})
	touchFile(t, repo, "composer.lock")
	if err := os.MkdirAll(filepath.Join(repo, "public"), 0o755); err != nil {
		t.Fatalf("failed to create public dir: %v", err)
	}
	touchFile(t, filepath.Join(repo, "public"), "index.php")

	cfg, err := FinalizeBuildConfigWithOptions(AutoDetectOptions{RepoRoot: repo}, BuildConfig{
		Runtime:        "php",
		Framework:      "symfony-nginx",
		InstallCommand: "composer install --no-dev --prefer-dist --optimize-autoloader --no-interaction",
		RunCommand:     "php-fpm -D && exec nginx -g 'daemon off;'",
		ExposePort:     "9090",
	}, phpAllowedCommands())
	if err != nil {
		t.Fatalf("FinalizeBuildConfigWithOptions returned error: %v", err)
	}

	if cfg.ExposePort != "9090" {
		t.Fatalf("expected submitted expose port 9090, got %q", cfg.ExposePort)
	}
	if !strings.Contains(string(cfg.DockerfileContent), "FROM php:8.3-fpm") {
		t.Fatalf("expected fpm image in Dockerfile, got:\n%s", string(cfg.DockerfileContent))
	}
	if !strings.Contains(cfg.RuntimeInitCommand, "${PORT:-9090}") {
		t.Fatalf("expected fpm runtime init to honor custom port, got %q", cfg.RuntimeInitCommand)
	}
}

func TestAuditDockerfileWarnsWhenPHPPecLExtensionsOrServerAreMissing(t *testing.T) {
	repo := t.TempDir()
	writeComposerJSON(t, repo, map[string]string{
		"laravel/framework": "^11.0",
		"ext-imagick":       "*",
	})
	if err := os.MkdirAll(filepath.Join(repo, "public"), 0o755); err != nil {
		t.Fatalf("failed to create public dir: %v", err)
	}
	touchFile(t, filepath.Join(repo, "public"), "index.php")
	dockerfile := `FROM php:8.3-fpm
COPY . .
RUN composer install
CMD ["php-fpm"]
`
	if err := os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatalf("failed to write Dockerfile: %v", err)
	}

	audit := AuditDockerfileWithOptions(AutoDetectOptions{RepoRoot: repo}, filepath.Join(repo, "Dockerfile"))
	for _, expected := range []string{
		"Dockerfile appears to run php-fpm for a web app without starting an HTTP server",
		"PHP project requires ext-imagick but Dockerfile does not appear to enable it",
		"PHP project requires ext-imagick but Dockerfile does not appear to install it via PECL",
	} {
		found := false
		for _, warning := range audit.Warnings {
			if warning == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected warning %q, got %#v", expected, audit.Warnings)
		}
	}
}

func TestAuditDockerfileRejectsVitePreviewWithoutHost(t *testing.T) {
	repo := t.TempDir()
	writePackageJSONWithFields(t, repo, map[string]string{
		"build":   "vite build",
		"preview": "vite preview",
	}, "", map[string]string{
		"react": "18.0.0",
	}, map[string]string{
		"vite": "5.0.0",
	}, nil)
	touchFile(t, repo, "vite.config.ts")
	dockerfile := `FROM node:22-bookworm-slim
CMD npm run preview
`
	if err := os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatalf("failed to write Dockerfile: %v", err)
	}

	audit := AuditDockerfileWithOptions(AutoDetectOptions{RepoRoot: repo}, filepath.Join(repo, "Dockerfile"))
	if len(audit.Errors) == 0 {
		t.Fatalf("expected audit errors, got %#v", audit)
	}
}

func TestAuditDockerfileWarnsWhenPythonPlaywrightInstallMissing(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "requirements.txt"), []byte("playwright==1.44.0\n"), 0o644); err != nil {
		t.Fatalf("failed to write requirements.txt: %v", err)
	}
	touchFile(t, repo, "app.py")
	dockerfile := `FROM python:3.11-slim
COPY . .
CMD ["python", "app.py"]
`
	if err := os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatalf("failed to write Dockerfile: %v", err)
	}

	audit := AuditDockerfileWithOptions(AutoDetectOptions{RepoRoot: repo}, filepath.Join(repo, "Dockerfile"))
	if len(audit.Warnings) == 0 {
		t.Fatalf("expected audit warnings, got %#v", audit)
	}
}

func TestAuditDockerfileWarnsWhenNextSharpMissing(t *testing.T) {
	repo := t.TempDir()
	writePackageJSONWithFields(t, repo, map[string]string{
		"build": "next build",
		"start": "next start",
	}, "", map[string]string{
		"next":      "15.0.0",
		"react":     "19.0.0",
		"react-dom": "19.0.0",
	}, nil, nil)
	dockerfile := `FROM node:22-bookworm-slim
WORKDIR /app
COPY . .
RUN npm ci
RUN npm run build
CMD ["npm", "start"]
`
	if err := os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatalf("failed to write Dockerfile: %v", err)
	}

	audit := AuditDockerfileWithOptions(AutoDetectOptions{RepoRoot: repo}, filepath.Join(repo, "Dockerfile"))
	foundWarning := false
	for _, warning := range audit.Warnings {
		if warning == "Next.js app does not declare sharp, so production image optimization may use more memory unless the Dockerfile installs it" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected Next.js sharp warning, got %#v", audit.Warnings)
	}
}

func TestFinalizeBuildConfigWithOptionsSupportsStructuredNodePhases(t *testing.T) {
	repo := t.TempDir()
	writePackageJSONWithFields(t, repo, map[string]string{
		"build":       "webpack",
		"build:check": "node scripts/check-build.js",
		"start":       "node dist/server.js",
	}, "", map[string]string{
		"@prisma/client": "5.0.0",
	}, map[string]string{
		"prisma": "5.0.0",
	}, nil)
	touchFile(t, repo, "package-lock.json")

	cfg, err := FinalizeBuildConfigWithOptions(AutoDetectOptions{RepoRoot: repo}, BuildConfig{
		Runtime:           "node",
		PrebuildCommand:   "npm ci",
		SetupCommands:     []string{"npx prisma generate"},
		BuildCommand:      "npm run build",
		PostBuildCommands: []string{"npm run build:check"},
		RunCommand:        "npm start",
	}, nodeAllowedCommands())
	if err != nil {
		t.Fatalf("FinalizeBuildConfigWithOptions returned error: %v", err)
	}

	if cfg.InstallCommand != "npm ci" {
		t.Fatalf("expected install command alias to resolve to npm ci, got %q", cfg.InstallCommand)
	}
	if cfg.PrebuildCommand != "npm ci" {
		t.Fatalf("expected legacy prebuild alias to remain npm ci, got %q", cfg.PrebuildCommand)
	}
	if len(cfg.PostBuildCommands) != 1 || cfg.PostBuildCommands[0] != "npm run build:check" {
		t.Fatalf("expected post-build command, got %#v", cfg.PostBuildCommands)
	}

	dockerfile := string(cfg.DockerfileContent)
	for _, snippet := range []string{
		"RUN npm ci",
		"RUN npx prisma generate",
		"RUN npm run build",
		"RUN npm run build:check",
		"exec npm start",
	} {
		if !strings.Contains(dockerfile, snippet) {
			t.Fatalf("expected Dockerfile to contain %q, got:\n%s", snippet, dockerfile)
		}
	}
}

func TestFinalizeBuildConfigWithOptionsForcesStaticFrontendRuntime(t *testing.T) {
	repo := t.TempDir()
	writePackageJSONWithFields(t, repo, map[string]string{
		"build":   "vite build",
		"preview": "vite preview",
	}, "", map[string]string{
		"react": "18.0.0",
	}, map[string]string{
		"vite": "5.0.0",
	}, nil)
	touchFile(t, repo, "package-lock.json")
	touchFile(t, repo, "vite.config.ts")

	cfg, err := FinalizeBuildConfigWithOptions(AutoDetectOptions{RepoRoot: repo}, BuildConfig{
		Runtime:           "static",
		InstallCommand:    "npm ci",
		BuildCommand:      "npm run build",
		RunCommand:        "npm run preview",
		PostBuildCommands: []string{"npm run build"},
	}, nodeAllowedCommands())
	if err != nil {
		t.Fatalf("FinalizeBuildConfigWithOptions returned error: %v", err)
	}

	if cfg.Runtime != "static" {
		t.Fatalf("expected static runtime, got %q", cfg.Runtime)
	}
	if cfg.RunCommand != "" {
		t.Fatalf("expected run command to be cleared for static runtime, got %q", cfg.RunCommand)
	}
	if len(cfg.ValidationWarnings) == 0 || !strings.Contains(cfg.ValidationWarnings[0], "ignoring submitted run command") {
		t.Fatalf("expected static runtime warning, got %#v", cfg.ValidationWarnings)
	}

	dockerfile := string(cfg.DockerfileContent)
	if !strings.Contains(dockerfile, "FROM nginx:alpine") {
		t.Fatalf("expected nginx runtime in Dockerfile, got:\n%s", dockerfile)
	}
	if strings.Contains(dockerfile, "npm run preview") {
		t.Fatalf("did not expect preview command in static Dockerfile:\n%s", dockerfile)
	}
}
