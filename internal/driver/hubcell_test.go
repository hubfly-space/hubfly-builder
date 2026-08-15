package driver

import (
	"strings"
	"testing"
)

func TestHubcellBuildCommandUsesSudoAndResourceFlags(t *testing.T) {
	hubcellDir := t.TempDir()
	cmd := HubcellBuildCommand(HubcellBuildOpts{
		HubcellPath:       hubcellDir,
		WorkDir:           "/tmp/repo",
		ContextPath:       "/tmp/context",
		ImageTag:          "hubcell.local/user/project:tag",
		Envs:              []string{`APP_ENV="production"`, `DATABASE_URL="postgres://db/app"`},
		Network:           "project-net",
		MemoryBytes:       4294967296,
		CPUPeriod:         100000,
		CPUQuota:          200000,
		RootfsInitialSize: "10g",
	})

	got := strings.Join(cmd.Args, " ")
	if cmd.Dir != "/tmp/repo" {
		t.Fatalf("expected command dir to be set, got %q", cmd.Dir)
	}
	for _, want := range []string{
		"sudo " + hubcellDir + "/hubcell build",
		"--verbose",
		"-t hubcell.local/user/project:tag",
		`-e APP_ENV="production"`,
		`-e DATABASE_URL="postgres://db/app"`,
		"--network project-net",
		"-m 4294967296",
		"--cpu-period 100000",
		"--cpu-quota 200000",
		"--rootfs-initial-size 10g",
		"/tmp/context",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in command, got %q", want, got)
		}
	}

	for _, forbidden := range []string{"--cap-add", "--allow-unsafe-build-isolation", "KILL", "NET_BIND_SERVICE", "SETFCAP", "SYS_CHROOT", "SYS_ADMIN", "rootfs-virtual-size", "rootfs-grow-step", " /tmp/context/Dockerfile"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("did not expect %q in command: %q", forbidden, got)
		}
	}
}
