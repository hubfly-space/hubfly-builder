package sourceguard

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var (
	scpLikeGitURLPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._~/-]+(?:\.git)?$`)
	hexObjectPattern     = regexp.MustCompile(`^[A-Fa-f0-9]{40}$|^[A-Fa-f0-9]{64}$`)
)

// NormalizeGitRepository validates repository URLs before passing them to git.
// Git accepts many local path forms, but this builder only supports remote URLs.
func NormalizeGitRepository(raw string) (string, error) {
	repository := strings.TrimSpace(raw)
	if repository == "" {
		return "", fmt.Errorf("git repository is required")
	}
	if strings.HasPrefix(repository, "-") {
		return "", fmt.Errorf("git repository cannot start with an option prefix")
	}

	if strings.Contains(repository, "://") {
		parsed, err := url.Parse(repository)
		if err != nil {
			return "", fmt.Errorf("git repository URL is invalid")
		}
		switch parsed.Scheme {
		case "https", "http", "ssh":
		default:
			return "", fmt.Errorf("git repository URL scheme is not supported")
		}
		if parsed.Host == "" {
			return "", fmt.Errorf("git repository URL host is required")
		}
		if hasParentPathSegment(parsed.EscapedPath()) {
			return "", fmt.Errorf("git repository URL path is invalid")
		}
		return repository, nil
	}

	if scpLikeGitURLPattern.MatchString(repository) {
		_, repoPath, _ := strings.Cut(repository, ":")
		if hasParentPathSegment(repoPath) {
			return "", fmt.Errorf("git repository URL path is invalid")
		}
		return repository, nil
	}

	return "", fmt.Errorf("git repository must be an http, https, ssh, or scp-like remote URL")
}

func NormalizeGitRef(raw string) (string, error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return "", nil
	}
	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("git ref cannot start with an option prefix")
	}
	if strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.Contains(ref, "//") {
		return "", fmt.Errorf("git ref is invalid")
	}
	if strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, ".lock") {
		return "", fmt.Errorf("git ref is invalid")
	}
	for _, r := range ref {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", fmt.Errorf("git ref cannot contain whitespace or control characters")
		}
		switch r {
		case '\\', '~', '^', ':', '?', '*', '[':
			return "", fmt.Errorf("git ref contains an unsupported character")
		}
	}
	return ref, nil
}

func NormalizeCommitSHA(raw string) (string, error) {
	commitSHA := strings.TrimSpace(raw)
	if commitSHA == "" {
		return "", nil
	}
	if !hexObjectPattern.MatchString(commitSHA) {
		return "", fmt.Errorf("commitSha must be a full 40 or 64 character hex object ID")
	}
	return commitSHA, nil
}

func hasParentPathSegment(rawPath string) bool {
	if rawPath == "" {
		return false
	}
	decodedPath, err := url.PathUnescape(rawPath)
	if err != nil {
		decodedPath = rawPath
	}
	for _, segment := range strings.Split(decodedPath, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}
