package executor

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"hubfly-builder/internal/allowlist"
	"hubfly-builder/internal/api"
	"hubfly-builder/internal/autodetect"
	"hubfly-builder/internal/driver"
	"hubfly-builder/internal/envplan"
	"hubfly-builder/internal/logs"
	"hubfly-builder/internal/storage"
)

var ErrBuildFailed = errors.New("build failed")

type Worker struct {
	job        *storage.BuildJob
	storage    *storage.Storage
	logManager *logs.LogManager
	allowlist  *allowlist.AllowedCommands
	apiClient  *api.Client
	registry   string
	logFile    *os.File
	logWriter  io.Writer
	workDir    string
}

func NewWorker(job *storage.BuildJob, storage *storage.Storage, logManager *logs.LogManager, allowlist *allowlist.AllowedCommands, apiClient *api.Client, registry string) *Worker {
	return &Worker{
		job:        job,
		storage:    storage,
		logManager: logManager,
		allowlist:  allowlist,
		apiClient:  apiClient,
		registry:   registry,
	}
}

func (w *Worker) Run() error {
	log.Printf("Starting build for job %s", w.job.ID)
	w.job.BuildConfig.NormalizePhaseAliases()
	w.job.StartedAt = sql.NullTime{Time: time.Now(), Valid: true}

	logPath, logFile, err := w.logManager.CreateLogFile(w.job.ID)
	if err != nil {
		log.Printf("ERROR: could not create log file for job %s: %v", w.job.ID, err)
		return w.failJob("failed to create log file")
	}
	w.job.LogPath = logPath
	w.logFile = logFile
	defer w.logFile.Close()
	w.logWriter = io.MultiWriter(os.Stdout, w.logFile)

	if err := w.storage.UpdateJobLogPath(w.job.ID, logPath); err != nil {
		w.log("ERROR: could not update log path: %v", err)
		return w.failJob("internal server error")
	}

	if err := w.storage.UpdateJobStatus(w.job.ID, "building"); err != nil {
		w.log("ERROR: could not update status to 'building': %v", err)
		return w.failJob("internal server error")
	}

	w.workDir, err = os.MkdirTemp("", fmt.Sprintf("hubfly-builder-ws-%s-", w.job.ID))
	if err != nil {
		w.log("ERROR: could not create workspace: %v", err)
		return w.failJob("internal server error")
	}
	defer os.RemoveAll(w.workDir)
	w.log("Created workspace: %s", w.workDir)

	cloneCmd := exec.Command("git", "clone", w.job.SourceInfo.GitRepository, w.workDir)
	if err := w.executeCommand(cloneCmd); err != nil {
		w.log("ERROR: failed to clone repository: %v", err)
		return w.failJob("failed to clone repository")
	}

	if w.job.SourceInfo.Ref != "" {
		w.log("Checking out ref: %s", w.job.SourceInfo.Ref)
		checkoutRefCmd := exec.Command("git", "-C", w.workDir, "checkout", w.job.SourceInfo.Ref)
		if err := w.executeCommand(checkoutRefCmd); err != nil {
			w.log("ERROR: failed to checkout ref %s: %v", w.job.SourceInfo.Ref, err)
			return w.failJob("failed to checkout ref")
		}
	}

	if w.job.SourceInfo.CommitSha != "" {
		w.log("Checking out commit SHA: %s", w.job.SourceInfo.CommitSha)
		checkoutShaCmd := exec.Command("git", "-C", w.workDir, "checkout", w.job.SourceInfo.CommitSha)
		if err := w.executeCommand(checkoutShaCmd); err != nil {
			w.log("ERROR: failed to checkout commit %s: %v", w.job.SourceInfo.CommitSha, err)
			return w.failJob("failed to checkout commit")
		}
	}

	w.log("Repository cloned and checked out successfully.")

	appDir, appPath, err := resolveWorkspacePath(w.workDir, w.job.SourceInfo.WorkingDir)
	if err != nil {
		w.log("ERROR: invalid working directory %q: %v", w.job.SourceInfo.WorkingDir, err)
		return w.failJob("invalid working directory")
	}
	if appDir != "." {
		w.log("Using working directory: %s", appDir)
	}

	buildContextDir := appDir
	buildContext := appPath
	dockerfilePath, dockerfileContextDir := detectDockerfileLayout(w.workDir, appDir)
	hasExistingDockerfile := dockerfilePath != ""
	if dockerfilePath != "" {
		buildContextDir = dockerfileContextDir
		buildContext, err = resolveBuildContextPath(w.workDir, buildContextDir)
		if err != nil {
			w.log("ERROR: invalid build context %q: %v", buildContextDir, err)
			return w.failJob("invalid build context")
		}
	}

	var plannedConfig autodetect.BuildConfig
	if dockerfilePath == "" {
		switch {
		case w.job.BuildConfig.IsAutoBuild:
			plannedConfig, err = autodetect.AutoDetectBuildConfigWithOptions(autodetect.AutoDetectOptions{
				RepoRoot:   w.workDir,
				WorkingDir: appDir,
			}, w.allowlist)
			if err != nil {
				w.log("ERROR: failed to auto-detect build config: %v", err)
				return w.failJob("failed to auto-detect build config")
			}
		case hasStructuredBuildStrategy(w.job.BuildConfig):
			plannedConfig, err = autodetect.FinalizeBuildConfigWithOptions(autodetect.AutoDetectOptions{
				RepoRoot:   w.workDir,
				WorkingDir: appDir,
			}, toAutodetectBuildConfig(w.job.BuildConfig), w.allowlist)
			if err != nil {
				w.log("ERROR: failed to finalize submitted build config: %v", err)
				return w.failJob("failed to finalize submitted build config")
			}
		}
	}
	if !hasExistingDockerfile && plannedConfig.BuildContextDir != "" {
		buildContextDir = plannedConfig.BuildContextDir
		buildContext, err = resolveBuildContextPath(w.workDir, buildContextDir)
		if err != nil {
			w.log("ERROR: invalid detected build context %q: %v", buildContextDir, err)
			return w.failJob("invalid detected build context")
		}
	}

	if len(w.job.BuildConfig.Env) == 0 && len(w.job.Env) > 0 {
		w.job.BuildConfig.Env = copyStringMap(w.job.Env)
	}

	envResult := envplan.ResolveForPaths([]string{buildContext, appPath}, w.job.BuildConfig.Env, w.job.BuildConfig.EnvOverrides)
	w.job.BuildConfig.ResolvedEnvPlan = envResult.Entries
	w.job.BuildConfig.ValidationWarnings = mergeWarnings(w.job.BuildConfig.ValidationWarnings, envResult.Warnings)
	w.logResolvedEnvPlan(envResult.Entries)
	for _, warning := range envResult.Warnings {
		w.log("Env warning: %s", warning)
	}
	if len(w.job.BuildConfig.Env) > 0 || len(w.job.BuildConfig.ResolvedEnvPlan) > 0 || len(w.job.BuildConfig.ValidationWarnings) > 0 {
		if err := w.storage.UpdateJobBuildConfig(w.job.ID, &w.job.BuildConfig); err != nil {
			w.log("WARNING: could not persist resolved env plan: %v", err)
		}
	}

	buildSecrets, cleanupSecrets, err := w.prepareBuildSecrets(envResult.BuildSecrets)
	if err != nil {
		w.log("ERROR: could not prepare build secrets: %v", err)
		return w.failJob("failed to prepare build secrets")
	}
	defer cleanupSecrets()

	requestedNetwork := strings.TrimSpace(w.job.BuildConfig.Network)
	if requestedNetwork == "" {
		w.log("ERROR: no user network provided")
		return w.failJob("no user network provided")
	}

	w.log("Starting ephemeral BuildKit daemon for network: %s", requestedNetwork)
	ephemeralSession, startErr := driver.StartEphemeralBuildKit(driver.EphemeralBuildKitOpts{
		JobID:       w.job.ID,
		UserNetwork: requestedNetwork,
	})
	if startErr != nil {
		w.log("ERROR: failed to start ephemeral BuildKit daemon: %v", startErr)
		return w.failJob("failed to start ephemeral BuildKit daemon")
	}
	defer func() {
		if stopErr := ephemeralSession.Stop(); stopErr != nil {
			w.log("WARNING: failed to clean up ephemeral BuildKit daemon %s: %v", ephemeralSession.ContainerName, stopErr)
		}
	}()
	w.log("Ephemeral BuildKit ready: container=%s userNetwork=%s workerNet=host runNet=host addr=%s", ephemeralSession.ContainerName, ephemeralSession.UserNetwork, ephemeralSession.Addr)
	activeBuildKit := driver.NewBuildKit(ephemeralSession.Addr)

	if hasExistingDockerfile {
		w.log("Dockerfile found in context, starting BuildKit build...")

		audit := autodetect.AuditDockerfileWithOptions(autodetect.AutoDetectOptions{
			RepoRoot:   w.workDir,
			WorkingDir: appDir,
		}, dockerfilePath)
		for _, warning := range audit.Warnings {
			w.log("Dockerfile audit warning: %s", warning)
		}
		if len(audit.Errors) > 0 {
			for _, auditErr := range audit.Errors {
				w.log("Dockerfile audit error: %s", auditErr)
			}
			return w.failJob(strings.Join(audit.Errors, "; "))
		}
		w.job.BuildConfig.BuildContextDir = buildContextDir
		w.job.BuildConfig.AppDir = appDir
		w.job.BuildConfig.ValidationWarnings = mergeWarnings(w.job.BuildConfig.ValidationWarnings, audit.Warnings)
		if content, readErr := os.ReadFile(dockerfilePath); readErr == nil {
			w.job.BuildConfig.DockerfileContent = content
		}
		if err := w.storage.UpdateJobBuildConfig(w.job.ID, &w.job.BuildConfig); err != nil {
			w.log("WARNING: could not persist Dockerfile audit metadata: %v", err)
		}

		if hasStructuredBuildStrategy(w.job.BuildConfig) {
			w.log("WARNING: submitted install/setup/build/run phases are ignored because a Dockerfile was provided. Keep custom lifecycle steps in the Dockerfile itself.")
		}

		imageTag := w.generateImageTag()
		w.log("Image tag: %s", imageTag)

		opts := driver.BuildOpts{
			ContextPath:    buildContext,
			DockerfilePath: buildContext,
			ImageTag:       imageTag,
			BuildArgs:      envResult.BuildArgs,
			Secrets:        buildSecrets,
		}
		buildCmd := activeBuildKit.BuildCommand(opts)
		if err := w.executeCommand(buildCmd); err != nil {
			w.log("ERROR: BuildKit build failed: %v", err)
			return w.failJob("BuildKit build failed")
		}
		w.log("BuildKit build and push successful.")
		w.job.ImageTag = imageTag
		if err := w.storage.UpdateJobImageTag(w.job.ID, imageTag); err != nil {
			w.log("ERROR: could not update image tag: %v", err)
			// Don't fail the build for this, just log it.
		}
	} else {
		w.log("No Dockerfile found in context, attempting to auto-detect and generate...")
		if !w.job.BuildConfig.IsAutoBuild && !hasStructuredBuildStrategy(w.job.BuildConfig) {
			w.log("ERROR: Auto-build is not enabled for this job.")
			return w.failJob("No build strategy found (e.g., Dockerfile missing and auto-build disabled)")
		}

		// Detect config and generate Dockerfile content.
		var detectedConfig autodetect.BuildConfig
		if w.job.BuildConfig.IsAutoBuild {
			detectedConfig, err = autodetect.AutoDetectBuildConfigWithEnvOptions(autodetect.AutoDetectOptions{
				RepoRoot:   w.workDir,
				WorkingDir: appDir,
			}, w.allowlist, envResult.BuildArgKeys(), envResult.BuildSecretKeys())
			if err != nil {
				w.log("ERROR: failed to auto-detect build config: %v", err)
				return w.failJob("failed to auto-detect build config")
			}
		} else {
			detectedConfig, err = autodetect.FinalizeBuildConfigWithEnvOptions(autodetect.AutoDetectOptions{
				RepoRoot:   w.workDir,
				WorkingDir: appDir,
			}, toAutodetectBuildConfig(w.job.BuildConfig), w.allowlist, envResult.BuildArgKeys(), envResult.BuildSecretKeys())
			if err != nil {
				w.log("ERROR: failed to finalize submitted build config: %v", err)
				return w.failJob("failed to finalize submitted build config")
			}
		}

		buildContextDir = detectedConfig.BuildContextDir
		buildContext, err = resolveBuildContextPath(w.workDir, buildContextDir)
		if err != nil {
			w.log("ERROR: invalid detected build context %q: %v", buildContextDir, err)
			return w.failJob("invalid detected build context")
		}
		dockerfilePath = filepath.Join(buildContext, "Dockerfile")

		w.log("Auto-detected runtime: %s, version: %s", detectedConfig.Runtime, detectedConfig.Version)
		if detectedConfig.Framework != "" {
			w.log("Auto-detected framework: %s", detectedConfig.Framework)
		}
		if detectedConfig.InstallCommand != "" {
			w.log("Resolved install command: %s", detectedConfig.InstallCommand)
		}
		for _, command := range detectedConfig.SetupCommands {
			w.log("Resolved setup command: %s", command)
		}
		for _, command := range detectedConfig.PostBuildCommands {
			w.log("Resolved post-build command: %s", command)
		}
		for _, warning := range detectedConfig.ValidationWarnings {
			w.log("Resolved warning: %s", warning)
		}

		applyDetectedBuildConfig(&w.job.BuildConfig, detectedConfig)
		w.job.BuildConfig.ValidationWarnings = mergeWarnings(w.job.BuildConfig.ValidationWarnings, detectedConfig.ValidationWarnings)
		w.job.BuildConfig.DockerfileContent = detectedConfig.DockerfileContent
		if err := w.storage.UpdateJobBuildConfig(w.job.ID, &w.job.BuildConfig); err != nil {
			w.log("WARNING: could not persist generated Dockerfile metadata: %v", err)
		}

		// Write the generated Dockerfile.
		if err := os.WriteFile(dockerfilePath, detectedConfig.DockerfileContent, 0644); err != nil {
			w.log("ERROR: failed to write generated Dockerfile: %v", err)
			return w.failJob("failed to write generated Dockerfile")
		}

		w.log("Dockerfile generated successfully, starting BuildKit build...")
		imageTag := w.generateImageTag()
		w.log("Image tag: %s", imageTag)

		opts := driver.BuildOpts{
			ContextPath:    buildContext,
			DockerfilePath: buildContext,
			ImageTag:       imageTag,
			BuildArgs:      envResult.BuildArgs,
			Secrets:        buildSecrets,
		}
		buildCmd := activeBuildKit.BuildCommand(opts)
		if err := w.executeCommand(buildCmd); err != nil {
			w.log("ERROR: BuildKit build failed: %v", err)
			return w.failJob("BuildKit build failed")
		}
		w.log("BuildKit build and push successful.")
		w.job.ImageTag = imageTag
		if err := w.storage.UpdateJobImageTag(w.job.ID, imageTag); err != nil {
			w.log("ERROR: could not update image tag: %v", err)
		}
	}

	return w.succeedJob()
}

func (w *Worker) failJob(reason string) error {
	log.Printf("Failing job %s: %s", w.job.ID, reason)
	if err := w.storage.UpdateJobStatus(w.job.ID, "failed"); err != nil {
		log.Printf("ERROR: could not update job status to 'failed' for job %s: %v", w.job.ID, err)
	}
	if err := w.apiClient.ReportResult(w.job, "failed", reason); err != nil {
		log.Printf("ERROR: could not report result to backend for job %s: %v", w.job.ID, err)
	}
	return fmt.Errorf("%w: %s", ErrBuildFailed, reason)
}

func (w *Worker) succeedJob() error {
	log.Printf("Succeeding job %s", w.job.ID)
	if err := w.storage.UpdateJobStatus(w.job.ID, "success"); err != nil {
		log.Printf("ERROR: could not update status to 'success' for job %s: %v", w.job.ID, err)
		return err
	}
	if err := w.apiClient.ReportResult(w.job, "success", ""); err != nil {
		log.Printf("ERROR: could not report result to backend for job %s: %v", w.job.ID, err)
		return err
	}
	return nil
}

func (w *Worker) log(format string, args ...interface{}) {
	logLine := fmt.Sprintf(format, args...)
	fmt.Fprintf(w.logWriter, "[%s] %s\n", time.Now().UTC().Format(time.RFC3339), logLine)
}

func (w *Worker) executeCommand(cmd *exec.Cmd) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		w.streamPipe(stdout)
	}()

	go func() {
		defer wg.Done()
		w.streamPipe(stderr)
	}()

	w.log("Executing: %s", sanitizeCommandForLog(cmd))
	if err := cmd.Start(); err != nil {
		return err
	}

	err = cmd.Wait()
	wg.Wait()
	return err
}

func (w *Worker) streamPipe(pipe io.Reader) {
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		w.log("%s", scanner.Text())
	}
}

func (w *Worker) generateImageTag() string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	shortSha := w.job.SourceInfo.CommitSha
	if len(shortSha) > 12 {
		shortSha = shortSha[:12]
	}
	sanitizedUserID := sanitize(w.job.UserID)
	sanitizedProjectID := sanitize(w.job.ProjectID)
	return fmt.Sprintf("%s/%s/%s:%s-b%s-v%s", w.registry, sanitizedUserID, sanitizedProjectID, shortSha, w.job.ID, ts)
}

func (w *Worker) logResolvedEnvPlan(entries []storage.ResolvedEnvVar) {
	if len(entries) == 0 {
		w.log("Env auto-resolution: no env variables provided")
		return
	}

	for _, entry := range entries {
		w.log("Env auto-resolution: key=%s scope=%s secret=%t reason=%s", entry.Key, entry.Scope, entry.Secret, entry.Reason)
	}
}

func (w *Worker) prepareBuildSecrets(secretValues map[string]string) ([]driver.BuildSecret, func(), error) {
	if len(secretValues) == 0 {
		return nil, func() {}, nil
	}

	secretDir, err := os.MkdirTemp("", fmt.Sprintf("hubfly-builder-secrets-%s-", w.job.ID))
	if err != nil {
		return nil, nil, err
	}

	keys := make([]string, 0, len(secretValues))
	for key := range secretValues {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	secrets := make([]driver.BuildSecret, 0, len(keys))
	for idx, key := range keys {
		secretPath := filepath.Join(secretDir, fmt.Sprintf("%03d_%s", idx, sanitizeSecretFilename(key)))
		if err := os.WriteFile(secretPath, []byte(secretValues[key]), 0600); err != nil {
			_ = os.RemoveAll(secretDir)
			return nil, nil, err
		}
		secrets = append(secrets, driver.BuildSecret{ID: key, Src: secretPath})
	}

	cleanup := func() {
		_ = os.RemoveAll(secretDir)
	}

	return secrets, cleanup, nil
}

func sanitizeCommandForLog(cmd *exec.Cmd) string {
	if len(cmd.Args) == 0 {
		return cmd.String()
	}

	sanitized := make([]string, 0, len(cmd.Args))
	for _, arg := range cmd.Args {
		sanitized = append(sanitized, redactBuildArg(arg))
	}
	return strings.Join(sanitized, " ")
}

func redactBuildArg(arg string) string {
	idx := strings.Index(arg, "build-arg:")
	if idx == -1 {
		return arg
	}

	start := idx + len("build-arg:")
	eq := strings.Index(arg[start:], "=")
	if eq == -1 {
		return arg
	}

	eq += start
	return arg[:eq+1] + "<redacted>"
}

func sanitizeSecretFilename(value string) string {
	var builder strings.Builder
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z':
			builder.WriteRune(ch)
		case ch >= 'A' && ch <= 'Z':
			builder.WriteRune(ch)
		case ch >= '0' && ch <= '9':
			builder.WriteRune(ch)
		case ch == '-', ch == '_', ch == '.':
			builder.WriteRune(ch)
		default:
			builder.WriteByte('_')
		}
	}

	if builder.Len() == 0 {
		return "secret"
	}
	return builder.String()
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func sanitize(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", "-"))
}

func resolveWorkspacePath(repoRoot, workingDir string) (string, string, error) {
	trimmed := strings.TrimSpace(workingDir)
	if trimmed == "" || trimmed == "." {
		return ".", repoRoot, nil
	}

	cleaned := filepath.Clean(trimmed)
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("working directory must stay within the repository root")
	}

	return filepath.ToSlash(cleaned), filepath.Join(repoRoot, cleaned), nil
}

func resolveBuildContextPath(repoRoot, buildContextDir string) (string, error) {
	buildContextDir = strings.TrimSpace(buildContextDir)
	if buildContextDir == "" || buildContextDir == "." {
		return repoRoot, nil
	}

	cleaned := filepath.Clean(buildContextDir)
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("build context must stay within the repository root")
	}

	return filepath.Join(repoRoot, cleaned), nil
}

func detectDockerfileLayout(repoRoot, appDir string) (string, string) {
	if appDir != "." {
		appDockerfile := filepath.Join(repoRoot, filepath.FromSlash(appDir), "Dockerfile")
		if info, err := os.Stat(appDockerfile); err == nil && info.Mode().IsRegular() {
			return appDockerfile, appDir
		}
	}

	rootDockerfile := filepath.Join(repoRoot, "Dockerfile")
	if info, err := os.Stat(rootDockerfile); err == nil && info.Mode().IsRegular() {
		return rootDockerfile, "."
	}

	return "", ""
}

func mergeWarnings(primary []string, extras []string) []string {
	if len(primary) == 0 && len(extras) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(primary)+len(extras))
	merged := make([]string, 0, len(primary)+len(extras))
	for _, value := range append(append([]string{}, primary...), extras...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		merged = append(merged, value)
	}
	return merged
}

func hasStructuredBuildStrategy(cfg storage.BuildConfig) bool {
	return autodetect.HasStructuredBuildPhases(toAutodetectBuildConfig(cfg))
}

func toAutodetectBuildConfig(cfg storage.BuildConfig) autodetect.BuildConfig {
	cfg.NormalizePhaseAliases()
	return autodetect.BuildConfig{
		IsAutoBuild:        cfg.IsAutoBuild,
		Runtime:            cfg.Runtime,
		Framework:          cfg.Framework,
		Version:            cfg.Version,
		InstallCommand:     cfg.InstallCommand,
		PrebuildCommand:    cfg.PrebuildCommand,
		SetupCommands:      cloneStringSlice(cfg.SetupCommands),
		BuildCommand:       cfg.BuildCommand,
		PostBuildCommands:  cloneStringSlice(cfg.PostBuildCommands),
		RunCommand:         cfg.RunCommand,
		RuntimeInitCommand: cfg.RuntimeInitCommand,
		ExposePort:         cfg.ExposePort,
		BuildContextDir:    cfg.BuildContextDir,
		AppDir:             cfg.AppDir,
		ValidationWarnings: cloneStringSlice(cfg.ValidationWarnings),
		DockerfileContent:  cfg.DockerfileContent,
	}
}

func applyDetectedBuildConfig(dst *storage.BuildConfig, src autodetect.BuildConfig) {
	dst.Runtime = src.Runtime
	dst.Framework = src.Framework
	dst.Version = src.Version
	dst.InstallCommand = src.InstallCommand
	dst.PrebuildCommand = src.PrebuildCommand
	dst.SetupCommands = cloneStringSlice(src.SetupCommands)
	dst.BuildCommand = src.BuildCommand
	dst.PostBuildCommands = cloneStringSlice(src.PostBuildCommands)
	dst.RunCommand = src.RunCommand
	dst.RuntimeInitCommand = src.RuntimeInitCommand
	dst.ExposePort = src.ExposePort
	dst.BuildContextDir = src.BuildContextDir
	dst.AppDir = src.AppDir
	dst.DockerfileContent = src.DockerfileContent
	dst.NormalizePhaseAliases()
}
