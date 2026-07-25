package sourceguard

import "testing"

func TestNormalizeGitRepositoryAcceptsRemoteURLs(t *testing.T) {
	valid := []string{
		"https://github.com/hubfly-space/hub-files.git",
		"http://github.com/hubfly-space/hub-files.git",
		"ssh://git@github.com/hubfly-space/hub-files.git",
		"git@github.com:hubfly-space/hub-files.git",
	}

	for _, value := range valid {
		t.Run(value, func(t *testing.T) {
			got, err := NormalizeGitRepository(value)
			if err != nil {
				t.Fatalf("expected valid repository, got error: %v", err)
			}
			if got != value {
				t.Fatalf("expected %q, got %q", value, got)
			}
		})
	}
}

func TestNormalizeGitRepositoryRejectsUnsafeInputs(t *testing.T) {
	invalid := []string{
		"",
		"--upload-pack=touch /tmp/pwn",
		"file:///tmp/repo",
		"/tmp/repo",
		"https://",
		"https://github.com/hubfly-space/../private.git",
		"https://github.com/hubfly-space/%2e%2e/private.git",
		"git@github.com:hubfly-space/../private.git",
	}

	for _, value := range invalid {
		t.Run(value, func(t *testing.T) {
			if got, err := NormalizeGitRepository(value); err == nil {
				t.Fatalf("expected invalid repository, got %q", got)
			}
		})
	}
}

func TestNormalizeGitRef(t *testing.T) {
	valid := []string{"main", "feature/test", "refs/heads/main", "release-2026.07"}
	for _, value := range valid {
		t.Run("valid "+value, func(t *testing.T) {
			got, err := NormalizeGitRef(value)
			if err != nil {
				t.Fatalf("expected valid ref, got error: %v", err)
			}
			if got != value {
				t.Fatalf("expected %q, got %q", value, got)
			}
		})
	}

	invalid := []string{"--work-tree=/tmp", "main other", "../main", "main..other", "main.lock", "refs/heads/main/", "main:next", "main~1"}
	for _, value := range invalid {
		t.Run("invalid "+value, func(t *testing.T) {
			if got, err := NormalizeGitRef(value); err == nil {
				t.Fatalf("expected invalid ref, got %q", got)
			}
		})
	}
}

func TestNormalizeCommitSHA(t *testing.T) {
	valid40 := "0123456789abcdef0123456789abcdef01234567"
	valid64 := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, value := range []string{"", valid40, valid64} {
		t.Run("valid "+value, func(t *testing.T) {
			if _, err := NormalizeCommitSHA(value); err != nil {
				t.Fatalf("expected valid commit SHA, got error: %v", err)
			}
		})
	}

	for _, value := range []string{"abc123", "--detach", "0123456789abcdef0123456789abcdef0123456g"} {
		t.Run("invalid "+value, func(t *testing.T) {
			if got, err := NormalizeCommitSHA(value); err == nil {
				t.Fatalf("expected invalid commit SHA, got %q", got)
			}
		})
	}
}
