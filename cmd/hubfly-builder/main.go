package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"hubfly-builder/internal/allowlist"
	"hubfly-builder/internal/api"
	"hubfly-builder/internal/executor"
	"hubfly-builder/internal/logs"
	"hubfly-builder/internal/offline"
	"hubfly-builder/internal/server"
	"hubfly-builder/internal/storage"
	"hubfly-builder/internal/uploadserver"
)

const (
	defaultGlobalConfigPath = "/etc/hubfly-builder/config.json"
	localConfigPath         = "configs/env.json"
	defaultHubcellBaseURL   = "http://127.0.0.1:10012"
	defaultHubcellCLIPath   = "/usr/local/bin/hubcell"
	defaultCallbackURL      = "https://hubfly.space/api/builds/callback"
	defaultServerAddr       = ":10008"
	defaultUploadAddr       = ":10011"
	defaultDataDir          = "./data"
	defaultLogDir           = "./log"
	defaultGlobalDataDir    = "/var/lib/hubfly-builder"
	defaultGlobalLogDir     = "/var/log/hubfly-builder"
	defaultConcurrentBuilds = 3
	defaultLogRetentionDays = 7
	defaultUpdateLockfile   = "/run/hubfly-builder-update.lock"
)

var version = "dev"

type EnvConfig struct {
	HubcellBaseURL      string `json:"HUBCELL_BASE_URL"`
	HubcellCLIPath      string `json:"HUBCELL_CLI_PATH"`
	CallbackURL         string `json:"CALLBACK_URL"`
	ServerAddr          string `json:"SERVER_ADDR"`
	UploadAddr          string `json:"UPLOAD_ADDR"`
	DataDir             string `json:"DATA_DIR"`
	LogDir              string `json:"LOG_DIR"`
	MaxConcurrentBuilds int    `json:"MAX_CONCURRENT_BUILDS"`
	LogRetentionDays    int    `json:"LOG_RETENTION_DAYS"`
	UpdateLockfile      string `json:"UPDATE_LOCKFILE"`
	APIToken            string `json:"API_TOKEN"`
	CallbackSecret      string `json:"CALLBACK_SECRET"`
}

func defaultEnvConfig() EnvConfig {
	return EnvConfig{
		HubcellBaseURL:      defaultHubcellBaseURL,
		HubcellCLIPath:      defaultHubcellCLIPath,
		CallbackURL:         defaultCallbackURL,
		ServerAddr:          defaultServerAddr,
		UploadAddr:          defaultUploadAddr,
		DataDir:             defaultDataDir,
		LogDir:              defaultLogDir,
		MaxConcurrentBuilds: defaultConcurrentBuilds,
		LogRetentionDays:    defaultLogRetentionDays,
		UpdateLockfile:      "./hubfly-builder-update.lock",
	}
}

func defaultGlobalEnvConfig() EnvConfig {
	config := defaultEnvConfig()
	config.DataDir = defaultGlobalDataDir
	config.LogDir = defaultGlobalLogDir
	config.UpdateLockfile = defaultUpdateLockfile
	return config
}

func loadEnvConfig() EnvConfig {
	config := defaultEnvConfig()
	configPath, explicitConfigPath := os.LookupEnv("HUBFLY_BUILDER_CONFIG")
	if configPath == "" {
		configPath = defaultGlobalConfigPath
	}

	loadedGlobal := false
	if _, err := os.Stat(configPath); err == nil {
		if loaded, err := readEnvConfig(configPath); err != nil {
			log.Printf("WARN: could not load config %s: %v", configPath, err)
		} else {
			mergeEnvConfig(&config, loaded)
			loadedGlobal = true
			log.Printf("Loaded config %s", configPath)
		}
	} else if os.IsNotExist(err) {
		if err := writeDefaultEnvConfig(configPath, defaultGlobalEnvConfig()); err != nil {
			log.Printf("WARN: could not create default config %s: %v", configPath, err)
		} else if loaded, err := readEnvConfig(configPath); err != nil {
			log.Printf("WARN: could not load created config %s: %v", configPath, err)
		} else {
			mergeEnvConfig(&config, loaded)
			loadedGlobal = true
			log.Printf("Created default config %s", configPath)
		}
	} else if err != nil {
		log.Printf("WARN: could not stat config %s: %v", configPath, err)
	}

	if !loadedGlobal && !explicitConfigPath {
		if loaded, err := readEnvConfig(localConfigPath); err == nil {
			mergeEnvConfig(&config, loaded)
			log.Printf("Loaded local fallback config %s", localConfigPath)
		} else if !os.IsNotExist(err) {
			log.Printf("WARN: could not load local fallback config %s: %v", localConfigPath, err)
		}
	}

	applyEnvironmentOverrides(&config)
	return config
}

func readEnvConfig(filename string) (EnvConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return EnvConfig{}, err
	}

	var config EnvConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return EnvConfig{}, err
	}
	return config, nil
}

func writeDefaultEnvConfig(filename string, config EnvConfig) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filename, data, 0o640)
}

func mergeEnvConfig(dst *EnvConfig, src EnvConfig) {
	if src.HubcellBaseURL != "" {
		dst.HubcellBaseURL = src.HubcellBaseURL
	}
	if src.HubcellCLIPath != "" {
		dst.HubcellCLIPath = src.HubcellCLIPath
	}
	if src.CallbackURL != "" {
		dst.CallbackURL = src.CallbackURL
	}
	if src.ServerAddr != "" {
		dst.ServerAddr = src.ServerAddr
	}
	if src.UploadAddr != "" {
		dst.UploadAddr = src.UploadAddr
	}
	if src.DataDir != "" {
		dst.DataDir = src.DataDir
	}
	if src.LogDir != "" {
		dst.LogDir = src.LogDir
	}
	if src.MaxConcurrentBuilds > 0 {
		dst.MaxConcurrentBuilds = src.MaxConcurrentBuilds
	}
	if src.LogRetentionDays > 0 {
		dst.LogRetentionDays = src.LogRetentionDays
	}
	if src.UpdateLockfile != "" {
		dst.UpdateLockfile = src.UpdateLockfile
	}
	if src.APIToken != "" {
		dst.APIToken = src.APIToken
	}
	if src.CallbackSecret != "" {
		dst.CallbackSecret = src.CallbackSecret
	}
}

func applyEnvironmentOverrides(config *EnvConfig) {
	if value := os.Getenv("HUBCELL_BASE_URL"); value != "" {
		config.HubcellBaseURL = value
	}
	if value := os.Getenv("HUBCELL_CLI_PATH"); value != "" {
		config.HubcellCLIPath = value
	}
	if value := os.Getenv("CALLBACK_URL"); value != "" {
		config.CallbackURL = value
	}
	if value := os.Getenv("SERVER_ADDR"); value != "" {
		config.ServerAddr = value
	}
	if value := os.Getenv("UPLOAD_ADDR"); value != "" {
		config.UploadAddr = value
	}
	if value := os.Getenv("DATA_DIR"); value != "" {
		config.DataDir = value
	}
	if value := os.Getenv("LOG_DIR"); value != "" {
		config.LogDir = value
	}
	if value := os.Getenv("MAX_CONCURRENT_BUILDS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			config.MaxConcurrentBuilds = parsed
		} else {
			log.Printf("WARN: ignoring invalid MAX_CONCURRENT_BUILDS=%q", value)
		}
	}
	if value := os.Getenv("LOG_RETENTION_DAYS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			config.LogRetentionDays = parsed
		} else {
			log.Printf("WARN: ignoring invalid LOG_RETENTION_DAYS=%q", value)
		}
	}
	if value := os.Getenv("UPDATE_LOCKFILE"); value != "" {
		config.UpdateLockfile = value
	}
	if value := os.Getenv("API_TOKEN"); value != "" {
		config.APIToken = value
	}
	if value := os.Getenv("CALLBACK_SECRET"); value != "" {
		config.CallbackSecret = value
	}
}

func applyEnvConfig(config EnvConfig) {
	os.Setenv("HUBCELL_BASE_URL", config.HubcellBaseURL)
	os.Setenv("HUBCELL_CLI_PATH", config.HubcellCLIPath)
	os.Setenv("CALLBACK_URL", config.CallbackURL)
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			_, _ = io.WriteString(os.Stdout, version+"\n")
			return
		case "offline":
			if err := offline.Run(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}

	config := loadEnvConfig()
	applyEnvConfig(config)

	callbackURL := config.CallbackURL // e.g., "http://localhost:3000/api/builds/callback"
	allowedCommands := allowlist.DefaultAllowedCommands()

	if err := os.MkdirAll(config.DataDir, 0o755); err != nil {
		log.Fatalf("could not create data directory: %s\n", err)
	}

	storage, err := storage.NewStorage(filepath.Join(config.DataDir, "hubfly-builder.sqlite"))
	if err != nil {
		log.Fatalf("could not create storage: %s\n", err)
	}

	if err := storage.ResetInProgressJobs(); err != nil {
		log.Fatalf("could not reset in-progress jobs: %s\n", err)
	}

	logManager, err := logs.NewLogManager(config.LogDir)
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
	apiTokenDisplay := "not set"
	if config.APIToken != "" {
		apiTokenDisplay = "configured"
	}
	callbackSecretDisplay := "not set"
	if config.CallbackSecret != "" {
		callbackSecretDisplay = "configured"
	}
	log.Printf(
		"Config: HUBCELL_BASE_URL=%q HUBCELL_CLI_PATH=%q CALLBACK_URL=%q SERVER_ADDR=%q UPLOAD_ADDR=%q DATA_DIR=%q LOG_DIR=%q MAX_CONCURRENT_BUILDS=%d LOG_RETENTION_DAYS=%d UPDATE_LOCKFILE=%s API_TOKEN=%s CALLBACK_SECRET=%s",
		config.HubcellBaseURL,
		config.HubcellCLIPath,
		config.CallbackURL,
		config.ServerAddr,
		config.UploadAddr,
		config.DataDir,
		config.LogDir,
		config.MaxConcurrentBuilds,
		config.LogRetentionDays,
		config.UpdateLockfile,
		apiTokenDisplay,
		callbackSecretDisplay,
	)
	log.Printf("Effective: CALLBACK_URL=%q", callbackURL)

	// Start log cleanup routine
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			<-ticker.C
			if err := logManager.Cleanup(time.Duration(config.LogRetentionDays) * 24 * time.Hour); err != nil {
				log.Printf("ERROR: log cleanup failed: %v", err)
			}
		}
	}()

	apiClient := api.NewClient(callbackURL, config.CallbackSecret)
	manager := executor.NewManager(storage, logManager, allowedCommands, apiClient, config.MaxConcurrentBuilds, config.UpdateLockfile)
	go manager.Start()

	uploadServer := uploadserver.NewServer(callbackURL, config.CallbackSecret)
	go func() {
		log.Printf("Image upload server listening on %s", config.UploadAddr)
		if err := uploadServer.Start(config.UploadAddr); err != nil {
			log.Fatalf("could not start image upload server: %s\n", err)
		}
	}()

	server := server.NewServer(storage, logManager, manager, allowedCommands, config.APIToken, callbackURL, config.CallbackSecret)

	log.Printf("Server listening on %s", config.ServerAddr)
	if err := server.Start(config.ServerAddr); err != nil {
		log.Fatalf("could not start server: %s\n", err)
	}
}
