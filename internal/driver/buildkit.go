package driver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	buildNetworkModeHost        = "host"
	buildNetworkHostEntitlement = "network.host"
)

type BuildKit struct {
	// buildkitd address, e.g., "tcp://172.18.0.5:1234"
	// For this project, this should come from an ephemeral per-job buildkitd session.
	Addr string
}

func NewBuildKit(addr string) *BuildKit {
	return &BuildKit{Addr: addr}
}

type BuildSecret struct {
	ID  string
	Src string
}

type BuildOpts struct {
	ContextPath    string
	DockerfilePath string
	ImageTag       string
	ExportPath     string
	BuildArgs      map[string]string
	Secrets        []BuildSecret
	CacheBackend   string
	CacheDir       string
	CacheKeys      []string
	CacheRef       string
	CacheRefs      []string
}

func (bk *BuildKit) BuildCommand(opts BuildOpts) *exec.Cmd {
	// Example: buildctl --addr <addr> build --frontend dockerfile.v0 --local context=. --local dockerfile=. --output type=docker,name=my-image,dest=/tmp/image.tar
	args := []string{
		"--addr", bk.Addr,
		"build",
		"--allow", buildNetworkHostEntitlement,
		"--progress=plain",
		"--frontend", "dockerfile.v0",
		"--local", fmt.Sprintf("context=%s", opts.ContextPath),
		"--local", fmt.Sprintf("dockerfile=%s", opts.DockerfilePath),
		"--opt", "force-network-mode=" + buildNetworkModeHost,
	}

	for _, key := range sortedMapKeys(opts.BuildArgs) {
		args = append(args, "--opt", fmt.Sprintf("build-arg:%s=%s", key, opts.BuildArgs[key]))
	}

	for _, secret := range sortedSecrets(opts.Secrets) {
		args = append(args, "--secret", fmt.Sprintf("id=%s,src=%s", secret.ID, secret.Src))
	}

	cacheBackend := strings.ToLower(strings.TrimSpace(opts.CacheBackend))
	if cacheBackend == "" {
		cacheBackend = "registry"
	}

	switch cacheBackend {
	case "local":
		cacheDir := strings.TrimSpace(opts.CacheDir)
		if cacheDir != "" {
			for _, key := range normalizedCacheKeys(opts.CacheKeys) {
				cachePath := filepath.Join(cacheDir, key)
				if localCacheReady(cachePath) {
					args = append(args, "--import-cache", fmt.Sprintf("type=local,src=%s", cachePath))
				}
				args = append(args, "--export-cache", fmt.Sprintf("type=local,dest=%s,mode=max", cachePath))
			}
		}
	default:
		for _, cacheRef := range normalizedCacheRefs(opts.CacheRefs, opts.CacheRef) {
			args = append(args, "--import-cache", fmt.Sprintf("type=registry,ref=%s", cacheRef))
			args = append(args, "--export-cache", fmt.Sprintf("type=registry,ref=%s,mode=max", cacheRef))
		}
	}

	args = append(args, "--output", fmt.Sprintf("type=docker,name=%s,dest=%s", opts.ImageTag, opts.ExportPath))
	return exec.Command("buildctl", args...)
}

func normalizedCacheRefs(cacheRefs []string, legacyRef string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(cacheRefs)+1)

	addRef := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return
		}
		if _, ok := seen[ref]; ok {
			return
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}

	for _, ref := range cacheRefs {
		addRef(ref)
	}
	addRef(legacyRef)
	return out
}

func localCacheReady(cachePath string) bool {
	info, err := os.Stat(filepath.Join(cachePath, "index.json"))
	if err != nil || info.IsDir() {
		return false
	}
	return true
}

func sortedMapKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSecrets(secrets []BuildSecret) []BuildSecret {
	if len(secrets) == 0 {
		return nil
	}
	out := make([]BuildSecret, len(secrets))
	copy(out, secrets)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID == out[j].ID {
			return out[i].Src < out[j].Src
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func normalizedCacheKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		key = filepath.Clean(key)
		if key == "." || key == ".." || strings.HasPrefix(key, ".."+string(filepath.Separator)) {
			continue
		}
		key = strings.TrimPrefix(key, string(filepath.Separator))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}
