package driver

import (
	"strings"
	"testing"
)

func TestBuildEphemeralBuildKitRunArgsWithConfig(t *testing.T) {
	args := buildEphemeralBuildKitRunArgs("hubfly-buildkit-job1", "user-net", "/tmp/buildkitd.toml")
	got := strings.Join(args, " ")

	for _, want := range []string{
		"run -d --rm",
		"--name hubfly-buildkit-job1",
		"--network user-net",
		"-v /tmp/buildkitd.toml:/etc/buildkit/buildkitd.toml:ro",
		"moby/buildkit:buildx-stable-1",
		"--config /etc/buildkit/buildkitd.toml",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in docker args, got %q", want, got)
		}
	}
}

func TestBuildEphemeralBuildKitRunArgsWithoutConfig(t *testing.T) {
	args := buildEphemeralBuildKitRunArgs("hubfly-buildkit-job1", "user-net", "")
	got := strings.Join(args, " ")

	if strings.Contains(got, "/etc/buildkit/buildkitd.toml") {
		t.Fatalf("did not expect buildkit config mount/flag when no config path is provided: %q", got)
	}
}
