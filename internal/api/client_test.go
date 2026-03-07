package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hubfly-builder/internal/storage"
)

func TestReportResultIncludesExposePort(t *testing.T) {
	payloadCh := make(chan ReportPayload, 1)
	errCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			errCh <- err
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload ReportPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			errCh <- err
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		payloadCh <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	job := &storage.BuildJob{
		ID:        "job-1",
		ProjectID: "project-1",
		UserID:    "user-1",
		ImageTag:  "registry.example.com/user-1/project-1:latest",
		LogPath:   "log/build-job-1.log",
		StartedAt: sql.NullTime{Time: time.Now().Add(-10 * time.Second), Valid: true},
		BuildConfig: storage.BuildConfig{
			Runtime:    "static",
			ExposePort: "8080",
		},
	}

	if err := client.ReportResult(job, "success", ""); err != nil {
		t.Fatalf("ReportResult returned error: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("callback handler failed: %v", err)
	case payload := <-payloadCh:
		if payload.ExposePort != "8080" {
			t.Fatalf("expected exposePort 8080 in callback payload, got %q", payload.ExposePort)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback payload")
	}
}

func TestReportResultOmitsExposePortForNonStaticRuntime(t *testing.T) {
	payloadCh := make(chan ReportPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload ReportPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		payloadCh <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	job := &storage.BuildJob{
		ID:        "job-2",
		ProjectID: "project-2",
		UserID:    "user-2",
		ImageTag:  "registry.example.com/user-2/project-2:latest",
		LogPath:   "log/build-job-2.log",
		StartedAt: sql.NullTime{Time: time.Now().Add(-10 * time.Second), Valid: true},
		BuildConfig: storage.BuildConfig{
			Runtime:    "node",
			ExposePort: "3000",
		},
	}

	if err := client.ReportResult(job, "success", ""); err != nil {
		t.Fatalf("ReportResult returned error: %v", err)
	}

	select {
	case payload := <-payloadCh:
		if payload.ExposePort != "" {
			t.Fatalf("expected exposePort to be omitted for non-static runtime, got %q", payload.ExposePort)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback payload")
	}
}
