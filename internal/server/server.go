package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gorilla/mux"
	"hubfly-builder/internal/allowlist"
	"hubfly-builder/internal/autodetect"
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
}

func NewServer(storage *storage.Storage, logManager *logs.LogManager, manager *executor.Manager, allowlist *allowlist.AllowedCommands) *Server {
	return &Server{
		storage:    storage,
		logManager: logManager,
		manager:    manager,
		allowlist:  allowlist,
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

	return http.ListenAndServe(addr, r)
}

func (s *Server) CreateJobHandler(w http.ResponseWriter, r *http.Request) {
	var job storage.BuildJob
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("Incoming %s %s payload: %s", r.Method, r.URL.Path, string(body))
	if err := json.Unmarshal(body, &job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	job.BuildConfig.NormalizePhaseAliases()
	if strings.TrimSpace(job.BuildConfig.Network) == "" {
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
			http.Error(w, "failed to create temp dir for autodetect", http.StatusInternalServerError)
			return
		}
		defer os.RemoveAll(tempDir)

		cloneCmd := exec.Command("git", "clone", job.SourceInfo.GitRepository, tempDir)
		if err := cloneCmd.Run(); err != nil {
			http.Error(w, "failed to clone repository for autodetect", http.StatusBadRequest)
			return
		}

		if job.SourceInfo.Ref != "" {
			if err := exec.Command("git", "-C", tempDir, "checkout", job.SourceInfo.Ref).Run(); err != nil {
				http.Error(w, fmt.Sprintf("failed to checkout ref %s", job.SourceInfo.Ref), http.StatusBadRequest)
				return
			}
		}
		if job.SourceInfo.CommitSha != "" {
			if err := exec.Command("git", "-C", tempDir, "checkout", job.SourceInfo.CommitSha).Run(); err != nil {
				http.Error(w, fmt.Sprintf("failed to checkout commit %s", job.SourceInfo.CommitSha), http.StatusBadRequest)
				return
			}
		}

		appDir, inspectDir, err := resolveWorkingDirectory(tempDir, job.SourceInfo.WorkingDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		dockerfilePath, buildContextDir := detectDockerfileLayout(tempDir, appDir)
		if dockerfilePath != "" {
			audit := autodetect.AuditDockerfileWithOptions(autodetect.AutoDetectOptions{
				RepoRoot:   tempDir,
				WorkingDir: appDir,
			}, dockerfilePath)
			if len(audit.Errors) > 0 {
				http.Error(w, strings.Join(audit.Errors, "; "), http.StatusBadRequest)
				return
			}

			dockerfileContent, readErr := os.ReadFile(dockerfilePath)
			if readErr != nil {
				http.Error(w, "failed to read Dockerfile", http.StatusInternalServerError)
				return
			}

			runtime, version := autodetect.DetectRuntime(inspectDir)
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
				DockerfileContent:  dockerfileContent,
			}
		} else {
			detectedConfig, err := autodetect.AutoDetectBuildConfigWithOptions(autodetect.AutoDetectOptions{
				RepoRoot:   tempDir,
				WorkingDir: appDir,
			}, s.allowlist)
			if err != nil {
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
				DockerfileContent:  detectedConfig.DockerfileContent,
			}
		}

		buildContextPath, err := resolveBuildContextPath(tempDir, job.BuildConfig.BuildContextDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		envResult := envplan.ResolveForPaths([]string{buildContextPath, inspectDir}, job.BuildConfig.Env, job.BuildConfig.EnvOverrides)
		job.BuildConfig.ResolvedEnvPlan = envResult.Entries
		job.BuildConfig.ValidationWarnings = mergeWarnings(job.BuildConfig.ValidationWarnings, envResult.Warnings)
	}

	if err := s.storage.CreateJob(&job); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Signal the manager that a new job is available
	s.manager.SignalNewJob()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(job)
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
			return appDockerfile, appDir
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
	fmt.Fprintln(w, "OK")
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
