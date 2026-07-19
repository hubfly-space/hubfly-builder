package main

import (
	"os"
	"path/filepath"
	"testing"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"HUBCELL_BASE_URL",
		"HUBCELL_CLI_PATH",
		"CALLBACK_URL",
		"SERVER_ADDR",
		"DATA_DIR",
		"LOG_DIR",
		"MAX_CONCURRENT_BUILDS",
		"LOG_RETENTION_DAYS",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadEnvConfigCreatesDefaultExplicitConfig(t *testing.T) {
	clearConfigEnv(t)
	configPath := filepath.Join(t.TempDir(), "hubfly-builder", "config.json")
	t.Setenv("HUBFLY_BUILDER_CONFIG", configPath)

	config := loadEnvConfig()

	if config.HubcellCLIPath != defaultHubcellCLIPath {
		t.Fatalf("expected default hubcell path %q, got %q", defaultHubcellCLIPath, config.HubcellCLIPath)
	}
	if config.DataDir != defaultGlobalDataDir {
		t.Fatalf("expected global data dir %q, got %q", defaultGlobalDataDir, config.DataDir)
	}
	if config.LogDir != defaultGlobalLogDir {
		t.Fatalf("expected global log dir %q, got %q", defaultGlobalLogDir, config.LogDir)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected default config file to be created: %v", err)
	}
}

func TestLoadEnvConfigEnvironmentOverridesFile(t *testing.T) {
	clearConfigEnv(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{
  "CALLBACK_URL": "https://file.example/callback",
  "SERVER_ADDR": ":12000",
  "MAX_CONCURRENT_BUILDS": 2
}
`), 0o640); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	t.Setenv("HUBFLY_BUILDER_CONFIG", configPath)
	t.Setenv("CALLBACK_URL", "https://env.example/callback")
	t.Setenv("MAX_CONCURRENT_BUILDS", "5")

	config := loadEnvConfig()

	if config.CallbackURL != "https://env.example/callback" {
		t.Fatalf("expected env callback override, got %q", config.CallbackURL)
	}
	if config.ServerAddr != ":12000" {
		t.Fatalf("expected server addr from file, got %q", config.ServerAddr)
	}
	if config.MaxConcurrentBuilds != 5 {
		t.Fatalf("expected env max concurrent override, got %d", config.MaxConcurrentBuilds)
	}
}
