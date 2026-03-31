package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hubfly-builder/internal/allowlist"
	"hubfly-builder/internal/api"
	"hubfly-builder/internal/driver"
	"hubfly-builder/internal/executor"
	"hubfly-builder/internal/logs"
	"hubfly-builder/internal/server"
	"hubfly-builder/internal/storage"
)

const maxConcurrentBuilds = 3
const logRetentionDays = 7

const (
	defaultBuildKitAddr = "docker-container://buildkitd"
	defaultBuildKitHost = "docker-container://buildkitd"
	defaultCallbackURL  = "https://hubfly.space/api/builds/callback"
	defaultRegistryURL  = "127.0.0.1:10009"
	defaultCacheBackend = "local"
	defaultCacheDir     = "data/buildkit-cache"
	defaultServerAddr   = ":10008"
)

var version = "dev"

type EnvConfig struct {
	BuildKitAddr string `json:"BUILDKIT_ADDR"`
	BuildKitHost string `json:"BUILDKIT_HOST"`
	RegistryURL  string `json:"REGISTRY_URL"`
	CallbackURL  string `json:"CALLBACK_URL"`
	CacheBackend string `json:"BUILDKIT_CACHE_BACKEND"`
	CacheDir     string `json:"BUILDKIT_CACHE_DIR"`
}

func applyDefaultEnvConfig() {
	setEnvIfEmpty("BUILDKIT_ADDR", defaultBuildKitAddr)
	setEnvIfEmpty("BUILDKIT_HOST", defaultBuildKitHost)
	setEnvIfEmpty("REGISTRY_URL", defaultRegistryURL)
	setEnvIfEmpty("BUILDKIT_CACHE_BACKEND", defaultCacheBackend)
	setEnvIfEmpty("BUILDKIT_CACHE_DIR", defaultCacheDir)
	setEnvIfEmpty("CALLBACK_URL", defaultCallbackURL)
}

func loadOptionalEnvConfig() {
	filename := "configs/env.json"
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		log.Printf("Optional config %s not found; using default environment values", filename)
		return
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		log.Printf("WARN: could not read %s: %v", filename, err)
		return
	}

	var config EnvConfig
	if err := json.Unmarshal(data, &config); err != nil {
		log.Printf("WARN: could not parse %s: %v", filename, err)
		return
	}

	// Only override defaults when the optional config provides a value.
	if config.BuildKitAddr != "" {
		os.Setenv("BUILDKIT_ADDR", config.BuildKitAddr)
	}
	if config.BuildKitHost != "" {
		os.Setenv("BUILDKIT_HOST", config.BuildKitHost)
	}
	if config.RegistryURL != "" {
		os.Setenv("REGISTRY_URL", config.RegistryURL)
	}
	if config.CacheBackend != "" {
		os.Setenv("BUILDKIT_CACHE_BACKEND", config.CacheBackend)
	}
	if config.CacheDir != "" {
		os.Setenv("BUILDKIT_CACHE_DIR", config.CacheDir)
	}
	if config.CallbackURL != "" {
		os.Setenv("CALLBACK_URL", config.CallbackURL)
	}
}

func setEnvIfEmpty(key, value string) {
	if os.Getenv(key) == "" {
		os.Setenv(key, value)
	}
}

func ensureBuildKitCacheDir() {
	if strings.ToLower(strings.TrimSpace(os.Getenv("BUILDKIT_CACHE_BACKEND"))) != "local" {
		return
	}

	cacheDir := strings.TrimSpace(os.Getenv("BUILDKIT_CACHE_DIR"))
	if cacheDir == "" {
		return
	}

	absCacheDir, err := filepath.Abs(cacheDir)
	if err != nil {
		log.Printf("WARN: could not resolve BUILDKIT_CACHE_DIR %q: %v", cacheDir, err)
		return
	}
	if err := os.MkdirAll(absCacheDir, 0o755); err != nil {
		log.Printf("WARN: could not create BUILDKIT_CACHE_DIR %q: %v", absCacheDir, err)
		return
	}
	os.Setenv("BUILDKIT_CACHE_DIR", absCacheDir)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		_, _ = io.WriteString(os.Stdout, version+"\n")
		return
	}

	applyDefaultEnvConfig()
	loadOptionalEnvConfig()
	ensureBuildKitCacheDir()

	registry := os.Getenv("REGISTRY_URL")
	callbackURL := os.Getenv("CALLBACK_URL") // e.g., "http://localhost:3000/api/builds/callback"
	allowedCommands := allowlist.DefaultAllowedCommands()

	if err := os.MkdirAll("data", 0o755); err != nil {
		log.Fatalf("could not create data directory: %s\n", err)
	}

	storage, err := storage.NewStorage("./data/hubfly-builder.sqlite")
	if err != nil {
		log.Fatalf("could not create storage: %s\n", err)
	}

	if err := storage.ResetInProgressJobs(); err != nil {
		log.Fatalf("could not reset in-progress jobs: %s\n", err)
	}

	logManager, err := logs.NewLogManager("./log")
	if err != nil {
		log.Fatalf("could not create log manager: %s\n", err)
	}

	systemLogPath, systemLogFile, err := logManager.CreateSystemLogFile()
	if err != nil {
		log.Fatalf("could not create system log file: %s\n", err)
	}
	defer systemLogFile.Close()
	log.SetOutput(io.MultiWriter(os.Stdout, systemLogFile))
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.Printf("System log file: %s", systemLogPath)
	log.Printf(
		"Env: BUILDKIT_ADDR=%q BUILDKIT_HOST=%q REGISTRY_URL=%q CALLBACK_URL=%q",
		os.Getenv("BUILDKIT_ADDR"),
		os.Getenv("BUILDKIT_HOST"),
		os.Getenv("REGISTRY_URL"),
		os.Getenv("CALLBACK_URL"),
	)
	log.Printf("Effective: REGISTRY_URL=%q CALLBACK_URL=%q", registry, callbackURL)
	if err := driver.CleanupOrphanedEphemeralBuildKits(); err != nil {
		log.Printf("WARN: could not cleanup stale ephemeral BuildKit containers: %v", err)
	}

	// Start log cleanup routine
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			<-ticker.C
			if err := logManager.Cleanup(logRetentionDays * 24 * time.Hour); err != nil {
				log.Printf("ERROR: log cleanup failed: %v", err)
			}
		}
	}()

	apiClient := api.NewClient(callbackURL)

	manager := executor.NewManager(storage, logManager, allowedCommands, apiClient, registry, maxConcurrentBuilds)
	go manager.Start()

	server := server.NewServer(storage, logManager, manager, allowedCommands)

	log.Printf("Server listening on %s", defaultServerAddr)
	if err := server.Start(defaultServerAddr); err != nil {
		log.Fatalf("could not start server: %s\n", err)
	}
}
