package offline

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hubfly-builder/internal/allowlist"
	"hubfly-builder/internal/autodetect"
)

type configEnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
	Scope  string `json:"scope,omitempty"`
}

type configBuild struct {
	Mode               string   `json:"mode,omitempty"`
	WorkingDir         string   `json:"workingDir,omitempty"`
	ContextDir         string   `json:"contextDir,omitempty"`
	Runtime            string   `json:"runtime,omitempty"`
	Framework          string   `json:"framework,omitempty"`
	Version            string   `json:"version,omitempty"`
	InstallCommand     string   `json:"installCommand,omitempty"`
	SetupCommands      []string `json:"setupCommands,omitempty"`
	BuildCommand       string   `json:"buildCommand,omitempty"`
	PostBuildCommands  []string `json:"postBuildCommands,omitempty"`
	RunCommand         string   `json:"runCommand,omitempty"`
	RuntimeInitCommand string   `json:"runtimeInitCommand,omitempty"`
	ExposePort         string   `json:"exposePort,omitempty"`
}

type configFile struct {
	Version int            `json:"version,omitempty"`
	Build   configBuild    `json:"build,omitempty"`
	Env     []configEnvVar `json:"env,omitempty"`
}

type inspectBuildConfig struct {
	IsAutoBuild        bool     `json:"isAutoBuild"`
	Runtime            string   `json:"runtime"`
	Framework          string   `json:"framework,omitempty"`
	Version            string   `json:"version,omitempty"`
	InstallCommand     string   `json:"installCommand,omitempty"`
	SetupCommands      []string `json:"setupCommands,omitempty"`
	BuildCommand       string   `json:"buildCommand,omitempty"`
	PostBuildCommands  []string `json:"postBuildCommands,omitempty"`
	RunCommand         string   `json:"runCommand,omitempty"`
	RuntimeInitCommand string   `json:"runtimeInitCommand,omitempty"`
	ExposePort         string   `json:"exposePort,omitempty"`
	BuildContextDir    string   `json:"buildContextDir,omitempty"`
	AppDir             string   `json:"appDir,omitempty"`
	ValidationWarnings []string `json:"validationWarnings,omitempty"`
}

type inspectOutput struct {
	BuildConfig     inspectBuildConfig `json:"buildConfig"`
	Dockerfile      string             `json:"dockerfile"`
	BuildArgKeys    []string           `json:"buildArgKeys,omitempty"`
	BuildSecretKeys []string           `json:"buildSecretKeys,omitempty"`
}

func Run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: hubfly-builder offline inspect [--path <project>] [--config <hubfly.build.json>]")
	}

	switch args[0] {
	case "inspect":
		return runInspect(args[1:])
	default:
		return fmt.Errorf("unknown offline command: %s", args[0])
	}
}

func runInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectPath := fs.String("path", ".", "project directory")
	configPath := fs.String("config", "hubfly.build.json", "build config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectRoot, err := filepath.Abs(strings.TrimSpace(*projectPath))
	if err != nil {
		return err
	}

	cfg, err := loadConfig(projectRoot, strings.TrimSpace(*configPath))
	if err != nil {
		return err
	}

	output, err := inspect(projectRoot, cfg)
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func inspect(projectRoot string, cfg configFile) (inspectOutput, error) {
	buildArgKeys, buildSecretKeys := resolveBuildEnvKeys(cfg.Env)
	allowed := allowlist.DefaultAllowedCommands()
	opts := autodetect.AutoDetectOptions{
		RepoRoot:   projectRoot,
		WorkingDir: normalizeDirOrDefault(cfg.Build.WorkingDir, "."),
	}

	var buildCfg autodetect.BuildConfig
	var err error
	mode := strings.ToLower(strings.TrimSpace(cfg.Build.Mode))
	switch mode {
	case "", "auto":
		buildCfg, err = autodetect.AutoDetectBuildConfigWithEnvOptions(
			opts,
			allowed,
			buildArgKeys,
			buildSecretKeys,
		)
	case "manual":
		buildCfg, err = autodetect.FinalizeBuildConfigWithEnvOptions(
			opts,
			autodetect.BuildConfig{
				IsAutoBuild:        false,
				Runtime:            strings.TrimSpace(cfg.Build.Runtime),
				Framework:          strings.TrimSpace(cfg.Build.Framework),
				Version:            strings.TrimSpace(cfg.Build.Version),
				InstallCommand:     strings.TrimSpace(cfg.Build.InstallCommand),
				SetupCommands:      cloneStringSlice(cfg.Build.SetupCommands),
				BuildCommand:       strings.TrimSpace(cfg.Build.BuildCommand),
				PostBuildCommands:  cloneStringSlice(cfg.Build.PostBuildCommands),
				RunCommand:         strings.TrimSpace(cfg.Build.RunCommand),
				RuntimeInitCommand: strings.TrimSpace(cfg.Build.RuntimeInitCommand),
				ExposePort:         strings.TrimSpace(cfg.Build.ExposePort),
				BuildContextDir:    normalizeDirOrDefault(cfg.Build.ContextDir, "."),
				AppDir:             normalizeDirOrDefault(cfg.Build.WorkingDir, "."),
			},
			allowed,
			buildArgKeys,
			buildSecretKeys,
		)
	default:
		return inspectOutput{}, fmt.Errorf("unsupported build mode %q", cfg.Build.Mode)
	}
	if err != nil {
		return inspectOutput{}, err
	}

	output := inspectOutput{
		BuildConfig: inspectBuildConfig{
			IsAutoBuild:        buildCfg.IsAutoBuild,
			Runtime:            buildCfg.Runtime,
			Framework:          buildCfg.Framework,
			Version:            buildCfg.Version,
			InstallCommand:     buildCfg.InstallCommand,
			SetupCommands:      cloneStringSlice(buildCfg.SetupCommands),
			BuildCommand:       buildCfg.BuildCommand,
			PostBuildCommands:  cloneStringSlice(buildCfg.PostBuildCommands),
			RunCommand:         buildCfg.RunCommand,
			RuntimeInitCommand: buildCfg.RuntimeInitCommand,
			ExposePort:         buildCfg.ExposePort,
			BuildContextDir:    buildCfg.BuildContextDir,
			AppDir:             buildCfg.AppDir,
			ValidationWarnings: cloneStringSlice(buildCfg.ValidationWarnings),
		},
		Dockerfile:      string(buildCfg.DockerfileContent),
		BuildArgKeys:    buildArgKeys,
		BuildSecretKeys: buildSecretKeys,
	}

	return output, nil
}

func loadConfig(projectRoot, configPath string) (configFile, error) {
	var cfg configFile
	if strings.TrimSpace(configPath) == "" {
		return cfg, nil
	}

	resolvedPath := configPath
	if !filepath.IsAbs(resolvedPath) {
		resolvedPath = filepath.Join(projectRoot, resolvedPath)
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", resolvedPath, err)
	}
	return cfg, nil
}

func resolveBuildEnvKeys(values []configEnvVar) ([]string, []string) {
	buildArgs := make([]string, 0)
	secrets := make([]string, 0)
	seenArgs := make(map[string]struct{})
	seenSecrets := make(map[string]struct{})

	for _, entry := range values {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		scope := strings.ToLower(strings.TrimSpace(entry.Scope))
		if scope == "" {
			scope = "runtime"
		}
		if scope != "build" && scope != "both" {
			continue
		}

		if entry.Secret {
			if _, ok := seenSecrets[name]; ok {
				continue
			}
			seenSecrets[name] = struct{}{}
			secrets = append(secrets, name)
			continue
		}

		if _, ok := seenArgs[name]; ok {
			continue
		}
		seenArgs[name] = struct{}{}
		buildArgs = append(buildArgs, name)
	}

	return buildArgs, secrets
}

func normalizeDirOrDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}
