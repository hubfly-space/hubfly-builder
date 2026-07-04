package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"hubfly-builder/internal/allowlist"
	"hubfly-builder/internal/autodetect"
	"hubfly-builder/internal/dockerfileparams"
	"hubfly-builder/internal/envplan"
	"hubfly-builder/internal/executor"
	"hubfly-builder/internal/logs"
	"hubfly-builder/internal/storage"
)

type Server struct {
	storage    *storage.Storage
	logManager *logs.LogManager
	manager    *executor.Manager
	allowlist  *allowlist.AllowedCommands
	callbackURL   string
	callbackSecret string
}

var credentialURLPattern = regexp.MustCompile(`https?://[^@\s]+@`)

func NewServer(storage *storage.Storage, logManager *logs.LogManager, manager *executor.Manager, allowlist *allowlist.AllowedCommands, callbackURL string, callbackSecret string) *Server {
	return &Server{
		storage:    storage,
		logManager: logManager,
		manager:    manager,
		allowlist:  allowlist,
		callbackURL:   callbackURL,
		callbackSecret: callbackSecret,
	}
}

func (s *Server) Start(addr string) error {
	r := mux.NewRouter()
	r.HandleFunc("/api/v1/jobs", s.CreateJobHandler).Methods("POST")
	r.HandleFunc("/api/v1/jobs/{id}", s.GetJobHandler).Methods("GET")
	r.HandleFunc("/api/v1/jobs/{id}/logs", s.GetJobLogsHandler).Methods("GET")
	r.HandleFunc("/dev/running-builds", s.GetRunningBuildsHandler).Methods("GET")
	r.HandleFunc("/dev/reset-db", s.ResetDatabaseHandler).Methods("POST")
	r.HandleFunc("/healthz", HealthCheckHandler).Methods("GET")

	r.HandleFunc("/v1/regions/{regionId}/builds", s.CreateBuildV1Handler).Methods("POST")
	r.HandleFunc("/v1/regions/{regionId}/builds/{buildId}", s.GetBuildV1Handler).Methods("GET")
	r.HandleFunc("/v1/regions/{regionId}/builds/{buildId}/logs", s.GetBuildLogsV1Handler).Methods("GET")
	r.HandleFunc("/v1/regions/{regionId}/builds/{buildId}/cancel", s.CancelBuildV1Handler).Methods("POST")

	return http.ListenAndServe(addr, r)
}

func (s *Server) CreateJobHandler(w http.ResponseWriter, r *http.Request) {
	var job storage.BuildJob
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("ERROR: failed to read request body for %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("Incoming %s %s payloadBytes=%d", r.Method, r.URL.Path, len(body))
	if err := json.Unmarshal(body, &job); err != nil {
		log.Printf("ERROR: failed to decode request body for %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	job.BuildConfig.NormalizePhaseAliases()
	log.Printf(
		"CreateJob decoded: id=%s projectId=%s userId=%s sourceType=%s repo=%s ref=%s workingDir=%q autoBuild=%t",
		job.ID,
		job.ProjectID,
		job.UserID,
		job.SourceType,
		sanitizeGitRepositoryURL(job.SourceInfo.GitRepository),
		job.SourceInfo.Ref,
		job.SourceInfo.WorkingDir,
		job.BuildConfig.IsAutoBuild,
	)
	if strings.TrimSpace(job.UserID) == "" {
		log.Printf("ERROR: job %s missing userId", job.ID)
		http.Error(w, "userId is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(job.BuildConfig.Network) == "" {
		log.Printf("ERROR: job %s missing buildConfig.network", job.ID)
		http.Error(w, "no user network provided", http.StatusBadRequest)
		return
	}
	if len(job.BuildConfig.Env) == 0 && len(job.Env) > 0 {
		job.BuildConfig.Env = copyStringMap(job.Env)
	}

	if job.BuildConfig.IsAutoBuild {
		// For auto-build, we need to clone the repo first to inspect it.
		// This is a simplified approach. A more robust solution might involve
		// a separate service to handle repo inspection before creating the job.
		tempDir, err := os.MkdirTemp("", "hubfly-builder-autodetect-")
		if err != nil {
			log.Printf("ERROR: job %s failed to create temp dir for autodetect: %v", job.ID, err)
			http.Error(w, "failed to create temp dir for autodetect", http.StatusInternalServerError)
			return
		}
		defer os.RemoveAll(tempDir)

		cloneCmd := exec.Command("git", "clone", job.SourceInfo.GitRepository, tempDir)
		if output, err := cloneCmd.CombinedOutput(); err != nil {
			log.Printf(
				"ERROR: job %s failed to clone repository repo=%s err=%v output=%s",
				job.ID,
				sanitizeGitRepositoryURL(job.SourceInfo.GitRepository),
				err,
				sanitizeCommandOutput(string(output)),
			)
			http.Error(w, "failed to clone repository for autodetect", http.StatusBadRequest)
			return
		}

		if job.SourceInfo.Ref != "" {
			if output, err := exec.Command("git", "-C", tempDir, "checkout", job.SourceInfo.Ref).CombinedOutput(); err != nil {
				log.Printf(
					"ERROR: job %s failed to checkout ref=%s err=%v output=%s",
					job.ID,
					job.SourceInfo.Ref,
					err,
					sanitizeCommandOutput(string(output)),
				)
				http.Error(w, fmt.Sprintf("failed to checkout ref %s", job.SourceInfo.Ref), http.StatusBadRequest)
				return
			}
		}
		if job.SourceInfo.CommitSha != "" {
			if output, err := exec.Command("git", "-C", tempDir, "checkout", job.SourceInfo.CommitSha).CombinedOutput(); err != nil {
				log.Printf(
					"ERROR: job %s failed to checkout commit=%s err=%v output=%s",
					job.ID,
					job.SourceInfo.CommitSha,
					err,
					sanitizeCommandOutput(string(output)),
				)
				http.Error(w, fmt.Sprintf("failed to checkout commit %s", job.SourceInfo.CommitSha), http.StatusBadRequest)
				return
			}
		}

		appDir, inspectDir, err := resolveWorkingDirectory(tempDir, job.SourceInfo.WorkingDir)
		if err != nil {
			log.Printf("ERROR: job %s invalid working directory %q: %v", job.ID, job.SourceInfo.WorkingDir, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		customDockerfile := job.BuildConfig.CustomDockerfileBytes()
		dockerfilePath, buildContextDir := detectDockerfileLayout(tempDir, appDir)
		if len(customDockerfile) > 0 {
			buildContextDir = appDir
			buildContextPath, err := resolveBuildContextPath(tempDir, buildContextDir)
			if err != nil {
				log.Printf("ERROR: job %s invalid custom Dockerfile build context %q: %v", job.ID, buildContextDir, err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			dockerfilePath = filepath.Join(buildContextPath, "Dockerfile")
			if err := os.WriteFile(dockerfilePath, customDockerfile, 0644); err != nil {
				log.Printf("ERROR: job %s failed to stage custom Dockerfile path=%s: %v", job.ID, dockerfilePath, err)
				http.Error(w, "failed to stage custom Dockerfile", http.StatusBadRequest)
				return
			}

			audit := autodetect.AuditDockerfileWithOptions(autodetect.AutoDetectOptions{
				RepoRoot:   tempDir,
				WorkingDir: appDir,
			}, dockerfilePath)
			if len(audit.Errors) > 0 {
				http.Error(w, strings.Join(audit.Errors, "; "), http.StatusBadRequest)
				return
			}

			runtime, version := autodetect.DetectRuntimeWithContext(tempDir, inspectDir)
			job.BuildConfig = storage.BuildConfig{
				IsAutoBuild:        true,
				Runtime:            runtime,
				Version:            version,
				BuildContextDir:    buildContextDir,
				AppDir:             appDir,
				ValidationWarnings: audit.Warnings,
				Network:            job.BuildConfig.Network,
				TimeoutSeconds:     job.BuildConfig.TimeoutSeconds,
				ResourceLimits:     job.BuildConfig.ResourceLimits,
				Env:                job.BuildConfig.Env,
				EnvOverrides:       job.BuildConfig.EnvOverrides,
				ResolvedEnvPlan:    job.BuildConfig.ResolvedEnvPlan,
				DockerfileArgs:     job.BuildConfig.DockerfileArgs,
				DockerfileEnv:      job.BuildConfig.DockerfileEnv,
				CustomDockerfile:   job.BuildConfig.CustomDockerfile,
				DockerfileContent:  customDockerfile,
			}
		} else if dockerfilePath != "" {
			if requestedContextDir := strings.TrimSpace(job.BuildConfig.BuildContextDir); requestedContextDir != "" {
				buildContextDir, err = normalizeDockerfileBuildContextDir(requestedContextDir, appDir)
				if err != nil {
					log.Printf("ERROR: job %s invalid Dockerfile build context %q for appDir=%q: %v", job.ID, requestedContextDir, appDir, err)
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if _, err := resolveBuildContextPath(tempDir, buildContextDir); err != nil {
					log.Printf("ERROR: job %s invalid Dockerfile build context %q: %v", job.ID, buildContextDir, err)
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			}

			dockerfileContent, stageErr := dockerfileparams.Stage(dockerfilePath, job.BuildConfig.DockerfileArgs, job.BuildConfig.DockerfileEnv)
			if stageErr != nil {
				log.Printf("ERROR: job %s failed to stage Dockerfile request params path=%s: %v", job.ID, dockerfilePath, stageErr)
				http.Error(w, stageErr.Error(), http.StatusBadRequest)
				return
			}

			audit := autodetect.AuditDockerfileWithOptions(autodetect.AutoDetectOptions{
				RepoRoot:   tempDir,
				WorkingDir: appDir,
			}, dockerfilePath)
			if len(audit.Errors) > 0 {
				http.Error(w, strings.Join(audit.Errors, "; "), http.StatusBadRequest)
				return
			}

			runtime, version := autodetect.DetectRuntimeWithContext(tempDir, inspectDir)
			job.BuildConfig = storage.BuildConfig{
				IsAutoBuild:        true,
				Runtime:            runtime,
				Version:            version,
				BuildContextDir:    buildContextDir,
				AppDir:             appDir,
				ValidationWarnings: audit.Warnings,
				Network:            job.BuildConfig.Network,
				TimeoutSeconds:     job.BuildConfig.TimeoutSeconds,
				ResourceLimits:     job.BuildConfig.ResourceLimits,
				Env:                job.BuildConfig.Env,
				EnvOverrides:       job.BuildConfig.EnvOverrides,
				ResolvedEnvPlan:    job.BuildConfig.ResolvedEnvPlan,
				DockerfileArgs:     job.BuildConfig.DockerfileArgs,
				DockerfileEnv:      job.BuildConfig.DockerfileEnv,
				CustomDockerfile:   job.BuildConfig.CustomDockerfile,
				DockerfileContent:  dockerfileContent,
			}
		} else {
			detectedConfig, err := autodetect.AutoDetectBuildConfigWithOptions(autodetect.AutoDetectOptions{
				RepoRoot:   tempDir,
				WorkingDir: appDir,
			}, s.allowlist)
			if err != nil {
				log.Printf(
					"ERROR: job %s autodetect failed repo=%s appDir=%s: %v",
					job.ID,
					sanitizeGitRepositoryURL(job.SourceInfo.GitRepository),
					appDir,
					err,
				)
				http.Error(w, "failed to autodetect build config", http.StatusInternalServerError)
				return
			}
			job.BuildConfig = storage.BuildConfig{
				IsAutoBuild:        detectedConfig.IsAutoBuild,
				Runtime:            detectedConfig.Runtime,
				Framework:          detectedConfig.Framework,
				Version:            detectedConfig.Version,
				InstallCommand:     detectedConfig.InstallCommand,
				PrebuildCommand:    detectedConfig.PrebuildCommand,
				SetupCommands:      detectedConfig.SetupCommands,
				BuildCommand:       detectedConfig.BuildCommand,
				PostBuildCommands:  detectedConfig.PostBuildCommands,
				RunCommand:         detectedConfig.RunCommand,
				RuntimeInitCommand: detectedConfig.RuntimeInitCommand,
				ExposePort:         detectedConfig.ExposePort,
				BuildContextDir:    detectedConfig.BuildContextDir,
				AppDir:             detectedConfig.AppDir,
				ValidationWarnings: detectedConfig.ValidationWarnings,
				Network:            job.BuildConfig.Network,
				TimeoutSeconds:     job.BuildConfig.TimeoutSeconds,
				ResourceLimits:     job.BuildConfig.ResourceLimits,
				Env:                job.BuildConfig.Env,
				EnvOverrides:       job.BuildConfig.EnvOverrides,
				ResolvedEnvPlan:    job.BuildConfig.ResolvedEnvPlan,
				DockerfileArgs:     job.BuildConfig.DockerfileArgs,
				DockerfileEnv:      job.BuildConfig.DockerfileEnv,
				CustomDockerfile:   job.BuildConfig.CustomDockerfile,
				DockerfileContent:  detectedConfig.DockerfileContent,
			}
		}

		buildContextPath, err := resolveBuildContextPath(tempDir, job.BuildConfig.BuildContextDir)
		if err != nil {
			log.Printf("ERROR: job %s invalid build context %q: %v", job.ID, job.BuildConfig.BuildContextDir, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		envResult := envplan.ResolveForPaths([]string{buildContextPath, inspectDir}, job.BuildConfig.Env, job.BuildConfig.EnvOverrides)
		job.BuildConfig.ResolvedEnvPlan = envResult.Entries
		job.BuildConfig.ValidationWarnings = mergeWarnings(job.BuildConfig.ValidationWarnings, envResult.Warnings)
	}

	if err := s.storage.CreateJob(&job); err != nil {
		log.Printf("ERROR: job %s failed to persist in storage: %v", job.ID, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Signal the manager that a new job is available
	s.manager.SignalNewJob()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(job)
}

func sanitizeGitRepositoryURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return sanitizeCommandOutput(raw)
	}
	if parsed.User != nil {
		parsed.User = url.User("REDACTED")
	}
	return parsed.String()
}

func sanitizeCommandOutput(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return credentialURLPattern.ReplaceAllStringFunc(raw, func(match string) string {
		if strings.HasPrefix(match, "https://") {
			return "https://REDACTED@"
		}
		if strings.HasPrefix(match, "http://") {
			return "http://REDACTED@"
		}
		return "REDACTED@"
	})
}

func resolveWorkingDirectory(repoRoot, workingDir string) (string, string, error) {
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

func detectDockerfileLayout(repoRoot, appDir string) (string, string) {
	if appDir != "." {
		appDockerfile := filepath.Join(repoRoot, filepath.FromSlash(appDir), "Dockerfile")
		if info, err := os.Stat(appDockerfile); err == nil && info.Mode().IsRegular() {
			return appDockerfile, "."
		}
	}

	rootDockerfile := filepath.Join(repoRoot, "Dockerfile")
	if info, err := os.Stat(rootDockerfile); err == nil && info.Mode().IsRegular() {
		return rootDockerfile, "."
	}

	return "", ""
}

func resolveBuildContextPath(repoRoot, buildContextDir string) (string, error) {
	trimmed := strings.TrimSpace(buildContextDir)
	if trimmed == "" || trimmed == "." {
		return repoRoot, nil
	}

	cleaned := filepath.Clean(trimmed)
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("build context must stay within the repository root")
	}

	return filepath.Join(repoRoot, cleaned), nil
}

func normalizeDockerfileBuildContextDir(buildContextDir, appDir string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(buildContextDir))
	if cleaned == "" || cleaned == "." {
		cleaned = "."
	}
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("build context must stay within the repository root")
	}

	cleaned = filepath.ToSlash(cleaned)
	appDir = strings.TrimSpace(appDir)
	if appDir == "" {
		appDir = "."
	}
	appDir = filepath.ToSlash(filepath.Clean(appDir))
	if cleaned != "." && appDir != cleaned && !strings.HasPrefix(appDir, cleaned+"/") {
		return "", fmt.Errorf("build context must contain the working directory")
	}

	return cleaned, nil
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

func (s *Server) GetJobHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	job, err := s.storage.GetJob(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJobNotFound(w)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(job)
}

func (s *Server) GetJobLogsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	job, err := s.storage.GetJob(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJobNotFound(w)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if job.LogPath == "" {
		writeBuildLogNotFound(w)
		return
	}

	logs, err := s.logManager.GetLog(job.LogPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeBuildLogNotFound(w)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write(logs)
}

func writeBuildLogNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   "BUILD_LOG_NOT_FOUND",
		"message": "build log not found",
	})
}

func writeJobNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   "JOB_NOT_FOUND",
		"message": "job not found",
	})
}

func (s *Server) GetRunningBuildsHandler(w http.ResponseWriter, r *http.Request) {
	activeIDs := s.manager.GetActiveBuilds()
	runningBuilds := []storage.BuildJob{}

	for _, id := range activeIDs {
		job, err := s.storage.GetJob(id)
		if err != nil {
			// Log the error but don't fail the whole request
			log.Printf("WARN: could not get job details for active job %s: %v", id, err)
			continue
		}
		runningBuilds = append(runningBuilds, *job)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(runningBuilds)
}

func (s *Server) ResetDatabaseHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.storage.ResetDatabase(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "Database reset successful")
}

func HealthCheckHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "healthy")
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

// ─── Gateway-compatible types ──────────────────────────────────────────────────

type gatewaySubmitBuildSource struct {
	Type       string `json:"type"`
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
	CommitSha  string `json:"commitSha,omitempty"`
	WorkingDir string `json:"workingDir,omitempty"`
}

type gatewaySubmitBuildConfig struct {
	IsAutoBuild        bool     `json:"isAutoBuild"`
	Runtime            string   `json:"runtime,omitempty"`
	Framework          string   `json:"framework,omitempty"`
	Version            string   `json:"version,omitempty"`
	InstallCommand     string   `json:"installCommand,omitempty"`
	SetupCommands      []string `json:"setupCommands,omitempty"`
	BuildCommand       string   `json:"buildCommand,omitempty"`
	PostBuildCommands  []string `json:"postBuildCommands,omitempty"`
	RuntimeInitCommand string   `json:"runtimeInitCommand,omitempty"`
	RunCommand         string   `json:"runCommand"`
	DockerfileContent  string   `json:"dockerfileContent,omitempty"`
}

type gatewayEnvironmentVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Scope string `json:"scope"`
}

type gatewayResourceLimits struct {
	CPU      float64 `json:"cpu"`
	MemoryMB int     `json:"memoryMb"`
}

type gatewaySubmitBuildInput struct {
	BuildID            string                   `json:"buildId"`
	ProjectID          string                   `json:"projectId"`
	UserID             string                   `json:"userId"`
	RegionID           string                   `json:"regionId"`
	ProjectNetworkName string                   `json:"projectNetworkName"`
	Source             gatewaySubmitBuildSource `json:"source"`
	Build              gatewaySubmitBuildConfig `json:"build"`
	Environment        []gatewayEnvironmentVar  `json:"environment"`
	TimeoutSeconds     int                      `json:"timeoutSeconds"`
	ResourceLimits     gatewayResourceLimits    `json:"resourceLimits"`
}

type runtimeBuildInspection struct {
	BuildID       string     `json:"buildId"`
	Status        string     `json:"status"`
	ImageTag      *string    `json:"imageTag"`
	CommitSha     *string    `json:"commitSha"`
	Ref           *string    `json:"ref"`
	StartedAt     *time.Time `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
	DurationMs    *int64     `json:"durationMs"`
	Error         *string    `json:"error"`
	ExitCode      *int64     `json:"exitCode"`
	ArtifactURL   *string    `json:"artifactUrl"`
	BuilderVersion *string   `json:"builderVersion"`
}

// ─── Status mapping ────────────────────────────────────────────────────────────

var builderStatusToGateway = map[string]string{
	"pending":  "pending",
	"claimed":  "building",
	"building": "building",
	"success":  "success",
	"failed":   "failed",
	"canceled": "cancelled",
}

func mapStatus(status string) string {
	if mapped, ok := builderStatusToGateway[status]; ok {
		return mapped
	}
	return status
}

// ─── V1 handlers ───────────────────────────────────────────────────────────────

func (s *Server) CreateBuildV1Handler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("ERROR: failed to read request body: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "BAD_REQUEST", "message": err.Error()})
		return
	}

	var input gatewaySubmitBuildInput
	if err := json.Unmarshal(body, &input); err != nil {
		log.Printf("ERROR: failed to decode request body: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "BAD_REQUEST", "message": err.Error()})
		return
	}

	if strings.TrimSpace(input.UserID) == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "BAD_REQUEST", "message": "userId is required"})
		return
	}
	if strings.TrimSpace(input.ProjectNetworkName) == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "BAD_REQUEST", "message": "projectNetworkName is required"})
		return
	}
	if strings.TrimSpace(input.Source.Repository) == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "BAD_REQUEST", "message": "source.repository is required"})
		return
	}

	env := make(map[string]string)
	for _, e := range input.Environment {
		if e.Scope == "build" || e.Scope == "both" {
			env[e.Key] = e.Value
		}
	}

	job := &storage.BuildJob{
		ID:        input.BuildID,
		ProjectID: input.ProjectID,
		UserID:    input.UserID,
		SourceType: "git",
		SourceInfo: storage.SourceInfo{
			GitRepository: input.Source.Repository,
			CommitSha:     input.Source.CommitSha,
			Ref:           input.Source.Ref,
			WorkingDir:    input.Source.WorkingDir,
		},
		BuildConfig: storage.BuildConfig{
			IsAutoBuild:        input.Build.IsAutoBuild,
			Runtime:            input.Build.Runtime,
			Framework:          input.Build.Framework,
			Version:            input.Build.Version,
			InstallCommand:     input.Build.InstallCommand,
			PrebuildCommand:    input.Build.InstallCommand,
			SetupCommands:      input.Build.SetupCommands,
			BuildCommand:       input.Build.BuildCommand,
			PostBuildCommands:  input.Build.PostBuildCommands,
			RunCommand:         input.Build.RunCommand,
			RuntimeInitCommand: input.Build.RuntimeInitCommand,
			Network:            input.ProjectNetworkName,
			TimeoutSeconds:     input.TimeoutSeconds,
			ResourceLimits: storage.ResourceLimits{
				CPU:      input.ResourceLimits.CPU,
				MemoryMB: input.ResourceLimits.MemoryMB,
			},
			Env:               env,
			CustomDockerfile:  input.Build.DockerfileContent,
		},
		Env: env,
	}
	if input.Build.DockerfileContent != "" {
		job.BuildConfig.CustomDockerfile = input.Build.DockerfileContent
	}

	log.Printf(
		"Gateway create build: id=%s projectId=%s userId=%s repo=%s ref=%s network=%s autoBuild=%t",
		job.ID, job.ProjectID, job.UserID,
		sanitizeGitRepositoryURL(job.SourceInfo.GitRepository),
		job.SourceInfo.Ref, job.BuildConfig.Network, job.BuildConfig.IsAutoBuild,
	)

	if err := s.storage.CreateJob(job); err != nil {
		log.Printf("ERROR: failed to persist job %s: %v", job.ID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}

	s.manager.SignalNewJob()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(buildJobToInspection(job))
}

func (s *Server) GetBuildV1Handler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	buildId := vars["buildId"]

	job, err := s.storage.GetJob(buildId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "NOT_FOUND", "message": "build not found"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(buildJobToInspection(job))
}

func (s *Server) GetBuildLogsV1Handler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	buildId := vars["buildId"]

	job, err := s.storage.GetJob(buildId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "NOT_FOUND", "message": "build not found"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}

	if job.LogPath == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "BUILD_LOG_NOT_FOUND", "message": "build log not found"})
		return
	}

	logs, err := s.logManager.GetLog(job.LogPath)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "BUILD_LOG_NOT_FOUND", "message": "build log not found"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write(logs)
}

func (s *Server) CancelBuildV1Handler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	buildId := vars["buildId"]

	job, err := s.storage.GetJob(buildId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "NOT_FOUND", "message": "build not found"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}

	if job.Status != "pending" && job.Status != "claimed" && job.Status != "building" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"error":   "BUILD_STATE_CONFLICT",
			"message": fmt.Sprintf("build cannot be cancelled while %s", job.Status),
		})
		return
	}

	if err := s.storage.UpdateJobStatus(buildId, "canceled"); err != nil {
		log.Printf("ERROR: failed to cancel job %s: %v", buildId, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}

	log.Printf("Gateway cancel build: id=%s", buildId)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// ─── Helpers ───────────────────────────────────────────────────────────────────

func buildJobToInspection(job *storage.BuildJob) *runtimeBuildInspection {
	status := mapStatus(job.Status)

	var imageTag, commitSha, ref *string
	if job.ImageTag != "" {
		imageTag = &job.ImageTag
	}
	if job.SourceInfo.CommitSha != "" {
		commitSha = &job.SourceInfo.CommitSha
	}
	if job.SourceInfo.Ref != "" {
		ref = &job.SourceInfo.Ref
	}

	var startedAt, finishedAt *time.Time
	if job.StartedAt.Valid {
		startedAt = &job.StartedAt.Time
	}
	if job.FinishedAt.Valid {
		finishedAt = &job.FinishedAt.Time
	}

	var durationMs *int64
	if job.StartedAt.Valid && job.FinishedAt.Valid {
		d := job.FinishedAt.Time.Sub(job.StartedAt.Time).Milliseconds()
		durationMs = &d
	}

	var exitCode *int64
	if job.ExitCode.Valid {
		e := job.ExitCode.Int64
		exitCode = &e
	}

	var errorStr *string
	if job.ErrorMessage != "" {
		errorStr = &job.ErrorMessage
	}

	return &runtimeBuildInspection{
		BuildID:       job.ID,
		Status:        status,
		ImageTag:      imageTag,
		CommitSha:     commitSha,
		Ref:           ref,
		StartedAt:     startedAt,
		FinishedAt:    finishedAt,
		DurationMs:    durationMs,
		Error:         errorStr,
		ExitCode:      exitCode,
		ArtifactURL:   nil,
		BuilderVersion: nil,
	}
}

func generateEventID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("evt_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("evt_%s", hex.EncodeToString(bytes))
}

func signPayload(secret, timestamp, eventID string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	signedPayload := fmt.Sprintf("%s.%s.%s", timestamp, eventID, string(body))
	mac.Write([]byte(signedPayload))
	return fmt.Sprintf("v1=%s", hex.EncodeToString(mac.Sum(nil)))
}
