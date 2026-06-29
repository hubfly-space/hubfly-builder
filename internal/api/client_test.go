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

func TestReportResultSendsBuildInspection(t *testing.T) {
	payloadCh := make(chan runtimeBuildInspection, 1)
	errCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			errCh <- err
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload runtimeBuildInspection
		if err := json.Unmarshal(body, &payload); err != nil {
			errCh <- err
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		payloadCh <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	job := &storage.BuildJob{
		ID:        "job-1",
		ProjectID: "project-1",
		UserID:    "user-1",
		ImageTag:  "hubcell.local/user-1/project-1:abc123-b-build_1-v20260210T123000Z",
		LogPath:   "log/build-job-1.log",
		StartedAt: sql.NullTime{Time: time.Now().Add(-10 * time.Second), Valid: true},
		SourceInfo: storage.SourceInfo{
			GitRepository: "https://github.com/owner/repo.git",
			CommitSha:     "abc123def456",
			Ref:           "main",
		},
	}

	if err := client.ReportResult(job, "success", ""); err != nil {
		t.Fatalf("ReportResult returned error: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("callback handler failed: %v", err)
	case payload := <-payloadCh:
		if payload.BuildID != "job-1" {
			t.Fatalf("expected buildId job-1, got %q", payload.BuildID)
		}
		if payload.Status != "success" {
			t.Fatalf("expected status success, got %q", payload.Status)
		}
		if payload.ImageTag == "" {
			t.Fatal("expected imageTag to be set")
		}
		if payload.CommitSha != "abc123def456" {
			t.Fatalf("expected commitSha abc123def456, got %q", payload.CommitSha)
		}
		if payload.Ref != "main" {
			t.Fatalf("expected ref main, got %q", payload.Ref)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback payload")
	}
}

func TestReportResultMapsCanceledStatus(t *testing.T) {
	payloadCh := make(chan runtimeBuildInspection, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload runtimeBuildInspection
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		payloadCh <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	job := &storage.BuildJob{
		ID:        "job-2",
		ProjectID: "project-2",
		UserID:    "user-2",
		ImageTag:  "hubcell.local/user-2/project-2:latest",
		LogPath:   "log/build-job-2.log",
		StartedAt: sql.NullTime{Time: time.Now().Add(-10 * time.Second), Valid: true},
	}

	if err := client.ReportResult(job, "canceled", "user cancelled"); err != nil {
		t.Fatalf("ReportResult returned error: %v", err)
	}

	select {
	case payload := <-payloadCh:
		if payload.Status != "cancelled" {
			t.Fatalf("expected mapped status 'cancelled', got %q", payload.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback payload")
	}
}

func TestReportResultIncludesAuthHeaders(t *testing.T) {
	headerCh := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerCh <- r.Header
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-secret-key-that-is-at-least-32-bytes-lon")
	job := &storage.BuildJob{
		ID:        "job-3",
		ProjectID: "project-3",
		UserID:    "user-3",
		StartedAt: sql.NullTime{Time: time.Now(), Valid: true},
	}

	if err := client.ReportResult(job, "success", ""); err != nil {
		t.Fatalf("ReportResult returned error: %v", err)
	}

	select {
	case headers := <-headerCh:
		if headers.Get("x-hubfly-event-id") == "" {
			t.Fatal("expected x-hubfly-event-id header")
		}
		if headers.Get("x-hubfly-timestamp") == "" {
			t.Fatal("expected x-hubfly-timestamp header")
		}
		if headers.Get("x-hubfly-signature") == "" {
			t.Fatal("expected x-hubfly-signature header")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback")
	}
}
