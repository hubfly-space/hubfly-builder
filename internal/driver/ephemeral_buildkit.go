package driver

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	ephemeralBuildKitImage            = "moby/buildkit:buildx-stable-1"
	ephemeralBuildKitPort             = "1234"
	ephemeralBuildKitConfigPath       = "configs/buildkitd.toml"
	ephemeralBuildKitConfigMountPath  = "/etc/buildkit/buildkitd.toml"
	ephemeralBuildKitLabelKey         = "hubfly.builder.ephemeral"
	ephemeralBuildKitLabelValue       = "true"
	ephemeralBuildKitWorkerNetMode    = "host"
	ephemeralBuildKitHostEntitlement  = "network.host"
	ephemeralBuildKitReadinessTimeout = 30 * time.Second
	ephemeralBuildKitReadinessPoll    = 500 * time.Millisecond
)

type EphemeralBuildKitOpts struct {
	JobID              string
	UserNetwork        string
	Registry           string
	RegistryPlainHTTP  bool
	CacheDir           string
	UseLocalCache      bool
	CPULimit           float64
	MemoryMB           int
	UseSoftLimits      bool
}

type EphemeralBuildKit struct {
	ContainerName string
	Addr          string
	UserNetwork   string
	configCleanup func()
}

func StartEphemeralBuildKit(opts EphemeralBuildKitOpts) (*EphemeralBuildKit, error) {
	jobID := strings.TrimSpace(opts.JobID)
	if jobID == "" {
		return nil, fmt.Errorf("missing job id for ephemeral buildkit")
	}

	userNetwork := strings.TrimSpace(opts.UserNetwork)
	if userNetwork == "" {
		return nil, fmt.Errorf("missing user network for ephemeral buildkit")
	}

	if err := ensureDockerNetworkExists(userNetwork); err != nil {
		return nil, err
	}

	containerName := "hubfly-buildkit-" + sanitizeContainerName(jobID)
	if err := forceRemoveContainer(containerName); err != nil {
		return nil, err
	}

	buildKitConfigPath, cleanupConfig, err := resolveBuildKitConfigPathWithRegistry(opts.Registry, opts.RegistryPlainHTTP)
	if err != nil {
		return nil, err
	}

	cacheDir := strings.TrimSpace(opts.CacheDir)
	if opts.UseLocalCache && cacheDir != "" {
		absCacheDir, absErr := filepath.Abs(cacheDir)
		if absErr != nil {
			return nil, fmt.Errorf("failed to resolve cache dir %q: %w", cacheDir, absErr)
		}
		if err := os.MkdirAll(absCacheDir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create cache dir %q: %w", absCacheDir, err)
		}
		cacheDir = absCacheDir
	}
	_, err = runDockerCommand(buildEphemeralBuildKitRunArgs(opts, containerName, buildKitConfigPath, cacheDir)...)
	if err != nil {
		if cleanupConfig != nil {
			cleanupConfig()
		}
		return nil, fmt.Errorf("failed to start ephemeral buildkit container %q: %w", containerName, err)
	}

	session := &EphemeralBuildKit{
		ContainerName: containerName,
		UserNetwork:   userNetwork,
		configCleanup: cleanupConfig,
	}

	cleanupOnFailure := true
	defer func() {
		if cleanupOnFailure {
			_ = session.Stop()
		}
	}()

	addr, err := resolveBuildKitAddr(containerName, userNetwork)
	if err != nil {
		return nil, err
	}
	session.Addr = addr

	if err := waitForBuildKitReady(addr); err != nil {
		return nil, err
	}

	cleanupOnFailure = false
	return session, nil
}

func resolveBuildKitConfigPath() (string, error) {
	_, err := os.Stat(ephemeralBuildKitConfigPath)
	if err == nil {
		absPath, absErr := filepath.Abs(ephemeralBuildKitConfigPath)
		if absErr != nil {
			return "", fmt.Errorf("failed to resolve BuildKit config path %q: %w", ephemeralBuildKitConfigPath, absErr)
		}
		return absPath, nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", fmt.Errorf("failed to access BuildKit config %q: %w", ephemeralBuildKitConfigPath, err)
}

func resolveBuildKitConfigPathWithRegistry(registry string, plainHTTP bool) (string, func(), error) {
	basePath, err := resolveBuildKitConfigPath()
	if err != nil {
		return "", nil, err
	}
	if !plainHTTP {
		return basePath, nil, nil
	}

	host := normalizeRegistryHost(registry)
	if host == "" {
		return basePath, nil, nil
	}

	var content string
	if basePath != "" {
		data, readErr := os.ReadFile(basePath)
		if readErr != nil {
			return "", nil, fmt.Errorf("failed to read BuildKit config %q: %w", basePath, readErr)
		}
		content = string(data)
	}

	registryBlock := fmt.Sprintf("[registry.%q]\n  http = true\n  insecure = true\n", host)
	if !strings.Contains(content, fmt.Sprintf("[registry.%q]", host)) {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += registryBlock
	}

	tmpFile, err := os.CreateTemp("", "hubfly-buildkitd-*.toml")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create BuildKit config: %w", err)
	}
	if _, err := tmpFile.WriteString(content); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return "", nil, fmt.Errorf("failed to write BuildKit config: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", nil, fmt.Errorf("failed to close BuildKit config: %w", err)
	}

	cleanup := func() {
		_ = os.Remove(tmpFile.Name())
	}
	return tmpFile.Name(), cleanup, nil
}

func normalizeRegistryHost(registry string) string {
	registry = strings.TrimSpace(registry)
	if registry == "" {
		return ""
	}
	registry = strings.TrimPrefix(registry, "http://")
	registry = strings.TrimPrefix(registry, "https://")
	registry = strings.TrimSuffix(registry, "/")
	if idx := strings.Index(registry, "/"); idx >= 0 {
		registry = registry[:idx]
	}
	return strings.TrimSpace(registry)
}

func buildEphemeralBuildKitRunArgs(opts EphemeralBuildKitOpts, containerName, configPath string, cacheDir string) []string {
	args := []string{
		"run", "-d", "--rm",
		"--name", containerName,
		"--privileged",
		"--label", ephemeralBuildKitLabelKey + "=" + ephemeralBuildKitLabelValue,
		"--network", opts.UserNetwork,
	}
	if opts.UseLocalCache && strings.TrimSpace(cacheDir) != "" {
		args = append(args, "-v", cacheDir+":"+cacheDir)
	}
	if strings.TrimSpace(configPath) != "" {
		args = append(args, "-v", configPath+":"+ephemeralBuildKitConfigMountPath+":ro")
	}

	args = appendResourceLimits(args, opts)
	args = append(args, ephemeralBuildKitImage)
	if strings.TrimSpace(configPath) != "" {
		args = append(args, "--config", ephemeralBuildKitConfigMountPath)
	}
	args = append(
		args,
		"--addr", "tcp://0.0.0.0:"+ephemeralBuildKitPort,
		"--oci-worker-net="+ephemeralBuildKitWorkerNetMode,
		"--allow-insecure-entitlement="+ephemeralBuildKitHostEntitlement,
	)
	return args
}

func appendResourceLimits(args []string, opts EphemeralBuildKitOpts) []string {
	cpu := opts.CPULimit
	mem := opts.MemoryMB
	if cpu <= 0 && mem <= 0 {
		return args
	}

	if opts.UseSoftLimits {
		if cpu > 0 {
			shares := int(math.Round(cpu * 1024))
			if shares < 2 {
				shares = 2
			}
			args = append(args, "--cpu-shares", strconv.Itoa(shares))
		}
		if mem > 0 {
			args = append(args, "--memory-reservation", fmt.Sprintf("%dm", mem))
		}
		return args
	}

	if cpu > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(cpu, 'f', -1, 64))
	}
	if mem > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", mem))
	}
	return args
}

func (s *EphemeralBuildKit) Stop() error {
	if s == nil || s.ContainerName == "" {
		return nil
	}

	output, err := runDockerCommand("rm", "-f", s.ContainerName)
	if err != nil && !isNoSuchContainerError(output) {
		return fmt.Errorf("failed to remove container %q: %w", s.ContainerName, err)
	}
	if s.configCleanup != nil {
		s.configCleanup()
	}
	return nil
}

func CleanupOrphanedEphemeralBuildKits() error {
	output, err := runDockerCommand("ps", "-aq", "--filter", "label="+ephemeralBuildKitLabelKey+"="+ephemeralBuildKitLabelValue)
	if err != nil {
		return err
	}

	ids := splitLines(output)
	for _, id := range ids {
		removeOut, removeErr := runDockerCommand("rm", "-f", id)
		if removeErr != nil && !isNoSuchContainerError(removeOut) {
			return fmt.Errorf("failed to remove stale buildkit container %q: %w", id, removeErr)
		}
	}

	return nil
}

func resolveBuildKitAddr(containerName, network string) (string, error) {
	ip, err := inspectContainerIPAddress(containerName, network)
	if err != nil {
		return "", err
	}
	if ip == "" {
		return "", fmt.Errorf("container %q has no IP on network %q", containerName, network)
	}
	return "tcp://" + ip + ":" + ephemeralBuildKitPort, nil
}

func inspectContainerIPAddress(containerName, network string) (string, error) {
	format := fmt.Sprintf(`{{with index .NetworkSettings.Networks %q}}{{.IPAddress}}{{end}}`, network)
	output, err := runDockerCommand("inspect", "--format", format, containerName)
	if err != nil {
		return "", fmt.Errorf("failed to inspect IP for container %q on network %q: %w", containerName, network, err)
	}
	return strings.TrimSpace(output), nil
}

func ensureDockerNetworkExists(name string) error {
	_, err := runDockerCommand("network", "inspect", name)
	if err != nil {
		return fmt.Errorf("docker network %q not found or inaccessible: %w", name, err)
	}
	return nil
}

func waitForBuildKitReady(addr string) error {
	deadline := time.Now().Add(ephemeralBuildKitReadinessTimeout)
	var lastErr error

	for time.Now().Before(deadline) {
		cmd := exec.Command("buildctl", "--addr", addr, "debug", "workers")
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(ephemeralBuildKitReadinessPoll)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("timed out waiting for buildkit readiness")
	}
	return fmt.Errorf("buildkit daemon at %s is not ready: %w", addr, lastErr)
}

func forceRemoveContainer(name string) error {
	output, err := runDockerCommand("rm", "-f", name)
	if err != nil && !isNoSuchContainerError(output) {
		return fmt.Errorf("failed to remove existing container %q: %w", name, err)
	}
	return nil
}

func isNoSuchContainerError(output string) bool {
	text := strings.ToLower(output)
	return strings.Contains(text, "no such container") || strings.Contains(text, "no such object")
}

func splitLines(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	parts := strings.Split(value, "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func runDockerCommand(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		if trimmed == "" {
			return "", fmt.Errorf("docker %s failed: %w", strings.Join(args, " "), err)
		}
		return trimmed, fmt.Errorf("docker %s failed: %w: %s", strings.Join(args, " "), err, trimmed)
	}
	return trimmed, nil
}

func sanitizeContainerName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "job"
	}

	var builder strings.Builder
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z':
			builder.WriteRune(ch)
		case ch >= '0' && ch <= '9':
			builder.WriteRune(ch)
		case ch == '.', ch == '-', ch == '_':
			builder.WriteRune(ch)
		default:
			builder.WriteByte('-')
		}
	}

	result := strings.Trim(builder.String(), "-_.")
	if result == "" {
		return "job"
	}

	if len(result) > 48 {
		result = result[:48]
	}

	return result
}
