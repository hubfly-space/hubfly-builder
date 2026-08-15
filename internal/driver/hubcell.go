package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type HubcellBuildOpts struct {
	HubcellPath       string
	WorkDir           string
	ContextPath       string
	ImageTag          string
	Envs              []string
	Network           string
	MemoryBytes       int64
	CPUPeriod         int64
	CPUQuota          int64
	RootfsInitialSize string
}

func HubcellBuildCommand(opts HubcellBuildOpts) *exec.Cmd {
	return HubcellBuildCommandContext(context.Background(), opts)
}

func HubcellBuildCommandContext(ctx context.Context, opts HubcellBuildOpts) *exec.Cmd {
	hubcellPath := ResolveHubcellCLIPath(opts.HubcellPath)
	args := []string{hubcellPath, "build", "--verbose"}

	args = append(args, "-t", opts.ImageTag)

	for _, envEntry := range opts.Envs {
		envEntry = strings.TrimSpace(envEntry)
		if envEntry == "" {
			continue
		}
		args = append(args, "-e", envEntry)
	}

	if network := strings.TrimSpace(opts.Network); network != "" {
		args = append(args, "--network", network)
	}
	if opts.MemoryBytes > 0 {
		args = append(args, "-m", strconv.FormatInt(opts.MemoryBytes, 10))
	}
	if opts.CPUPeriod > 0 {
		args = append(args, "--cpu-period", strconv.FormatInt(opts.CPUPeriod, 10))
	}
	if opts.CPUQuota > 0 {
		args = append(args, "--cpu-quota", strconv.FormatInt(opts.CPUQuota, 10))
	}
	if size := strings.TrimSpace(opts.RootfsInitialSize); size != "" {
		args = append(args, "--rootfs-initial-size", size)
	}

	args = append(args, opts.ContextPath)
	cmd := exec.CommandContext(ctx, "sudo", args...)
	if workDir := strings.TrimSpace(opts.WorkDir); workDir != "" {
		cmd.Dir = workDir
	}
	return cmd
}

func ResolveHubcellCLIPath(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "hubcell"
	}

	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return filepath.Join(path, "hubcell")
	}
	if strings.HasSuffix(path, string(filepath.Separator)) {
		return filepath.Join(strings.TrimRight(path, string(filepath.Separator)), "hubcell")
	}
	return path
}

func ValidateHubcellBuildOpts(opts HubcellBuildOpts) error {
	if strings.TrimSpace(opts.ImageTag) == "" {
		return fmt.Errorf("image tag is required")
	}
	if strings.TrimSpace(opts.ContextPath) == "" {
		return fmt.Errorf("build context path is required")
	}
	return nil
}
