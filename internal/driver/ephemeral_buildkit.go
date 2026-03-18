package driver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	JobID       string
	UserNetwork string
}

type EphemeralBuildKit struct {
	ContainerName string
	Addr          string
	UserNetwork   string
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

	buildKitConfigPath, err := resolveBuildKitConfigPath()
	if err != nil {
		return nil, err
	}

	_, err = runDockerCommand(buildEphemeralBuildKitRunArgs(containerName, userNetwork, buildKitConfigPath)...)
	if err != nil {
		return nil, fmt.Errorf("failed to start ephemeral buildkit container %q: %w", containerName, err)
	}

	session := &EphemeralBuildKit{
		ContainerName: containerName,
		UserNetwork:   userNetwork,
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

func buildEphemeralBuildKitRunArgs(containerName, userNetwork, configPath string) []string {
	args := []string{
		"run", "-d", "--rm",
		"--name", containerName,
		"--privileged",
		"--label", ephemeralBuildKitLabelKey + "=" + ephemeralBuildKitLabelValue,
		"--network", userNetwork,
	}
	if strings.TrimSpace(configPath) != "" {
		args = append(args, "-v", configPath+":"+ephemeralBuildKitConfigMountPath+":ro")
	}
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

func (s *EphemeralBuildKit) Stop() error {
	if s == nil || s.ContainerName == "" {
		return nil
	}

	output, err := runDockerCommand("rm", "-f", s.ContainerName)
	if err != nil && !isNoSuchContainerError(output) {
		return fmt.Errorf("failed to remove container %q: %w", s.ContainerName, err)
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
