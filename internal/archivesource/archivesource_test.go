package archivesource

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name, body, link string
	typ              byte
}

func build(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: e.typ, Linkname: e.link}
		if e.typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if e.typ == tar.TypeDir {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestFetchVerifiesDigestAndExtracts(t *testing.T) {
	archive := build(t, []entry{
		{name: "./", typ: tar.TypeDir},
		{name: "./package.json", body: `{"name":"app"}`, typ: tar.TypeReg},
		{name: "./src/index.js", body: "console.log(1)", typ: tar.TypeReg},
		{name: "./link", link: "src/index.js", typ: tar.TypeSymlink},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }))
	defer srv.Close()

	dest := t.TempDir()
	if err := Fetch(t.Context(), srv.URL+"/b/k.tar.gz", digest(archive), dest); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dest, "src/index.js")); string(data) != "console.log(1)" {
		t.Fatalf("extracted %q", data)
	}
	if err := Fetch(t.Context(), srv.URL+"/b/k.tar.gz", strings.Repeat("0", 64), t.TempDir()); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("digest mismatch not detected: %v", err)
	}
}

func TestExtractRejectsEscapes(t *testing.T) {
	for name, entries := range map[string][]entry{
		"parent traversal": {{name: "../evil", body: "x", typ: tar.TypeReg}},
		"absolute path":    {{name: "/etc/evil", body: "x", typ: tar.TypeReg}},
		"symlink escape":   {{name: "link", link: "../../etc/passwd", typ: tar.TypeSymlink}},
		"absolute symlink": {{name: "link", link: "/etc/passwd", typ: tar.TypeSymlink}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Extract(bytes.NewReader(build(t, entries)), t.TempDir()); err == nil {
				t.Fatal("escape accepted")
			}
		})
	}
}

func TestValidate(t *testing.T) {
	allowed := []string{"http://127.0.0.1:10023/"}
	good := strings.Repeat("a", 64)
	if err := Validate("http://127.0.0.1:10023/ai-builder-snapshots/ws/1.tar.gz?X-Amz-Signature=x", good, allowed); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"http://169.254.169.254/latest", "file:///etc/passwd", "http://127.0.0.1:10023.evil.com/x", "http://user:pw@127.0.0.1:10023/x"} {
		if Validate(bad, good, allowed) == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if Validate("http://127.0.0.1:10023/x", "nothex", allowed) == nil {
		t.Error("accepted bad digest")
	}
}
