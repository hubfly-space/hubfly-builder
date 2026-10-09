// Package archivesource fetches and safely extracts a tar.gz build source.
// AI Builder deploys use it: the agent uploads a workspace snapshot to
// regional storage and passes a short-lived presigned URL plus its SHA-256.
package archivesource

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// MaxArchiveBytes bounds the compressed download.
	MaxArchiveBytes = 2 << 30
	// MaxExtractedBytes bounds the uncompressed size (zip-bomb guard).
	MaxExtractedBytes = 8 << 30
	// MaxEntries bounds the number of files.
	MaxEntries = 200_000
)

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// AllowedPrefixes reads BUILDER_ARCHIVE_ALLOWED_PREFIXES (comma separated).
// The default allows only the co-located regional storage S3 endpoint, so an
// archive URL can never be used to make the builder fetch arbitrary hosts.
func AllowedPrefixes() []string {
	raw := strings.TrimSpace(os.Getenv("BUILDER_ARCHIVE_ALLOWED_PREFIXES"))
	if raw == "" {
		raw = "http://127.0.0.1:10023/"
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Validate checks the URL against the allowlist and the digest format.
func Validate(rawURL, digest string, allowed []string) error {
	if !sha256Pattern.MatchString(digest) {
		return errors.New("archive sha256 must be 64 lowercase hex characters")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return errors.New("archive URL is invalid")
	}
	if strings.Contains(parsed.EscapedPath(), "..") {
		return errors.New("archive URL path is invalid")
	}
	for _, prefix := range allowed {
		if strings.HasPrefix(rawURL, prefix) {
			return nil
		}
	}
	return errors.New("archive URL host is not allowed")
}

// Fetch downloads the archive, verifies its digest, and extracts it into dest.
func Fetch(ctx context.Context, rawURL, digest, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are not allowed") }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "hubfly-builder-archive-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, MaxArchiveBytes+1))
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	if n > MaxArchiveBytes {
		return errors.New("archive is too large")
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != digest {
		return errors.New("archive digest mismatch")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return Extract(tmp, dest)
}

// Extract unpacks a tar.gz into dest, rejecting absolute paths, parent
// traversal, links that escape dest, and special files.
func Extract(r io.Reader, dest string) error {
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("archive is not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("archive is corrupt: %w", err)
		}
		entries++
		if entries > MaxEntries {
			return errors.New("archive has too many files")
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if name == "." {
			continue
		}
		if filepath.IsAbs(name) || !filepath.IsLocal(name) {
			return fmt.Errorf("archive entry %q escapes the build directory", hdr.Name)
		}
		target := filepath.Join(root, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > MaxExtractedBytes {
				return errors.New("archive expands beyond the size limit")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.CopyN(f, tr, hdr.Size); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			linkTarget := hdr.Linkname
			resolved := linkTarget
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(target), resolved)
			}
			if rel, err := filepath.Rel(root, filepath.Clean(resolved)); err != nil || !filepath.IsLocal(rel) {
				return fmt.Errorf("archive symlink %q points outside the build directory", hdr.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(linkTarget, target); err != nil {
				return err
			}
		default:
			// Hard links, devices, FIFOs: never needed for source builds.
			continue
		}
	}
}
