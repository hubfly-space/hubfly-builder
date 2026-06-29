package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	mathrand "math/rand"
	"net/http"
	"sort"
	"strings"
	"time"

	"hubfly-builder/internal/storage"
)

type Client struct {
	httpClient     *http.Client
	callbackURL    string
	callbackSecret string
}

func NewClient(callbackURL, callbackSecret string) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		callbackURL:    callbackURL,
		callbackSecret: callbackSecret,
	}
}

type runtimeBuildInspection struct {
	BuildID       string  `json:"buildId"`
	Status        string  `json:"status"`
	ImageTag      string  `json:"imageTag,omitempty"`
	CommitSha     string  `json:"commitSha,omitempty"`
	Ref           string  `json:"ref,omitempty"`
	StartedAt     string  `json:"startedAt,omitempty"`
	FinishedAt    string  `json:"finishedAt,omitempty"`
	DurationMs    *int64  `json:"durationMs,omitempty"`
	Error         string  `json:"error,omitempty"`
	ExitCode      *int64  `json:"exitCode,omitempty"`
	ArtifactURL   string  `json:"artifactUrl,omitempty"`
	BuilderVersion string `json:"builderVersion,omitempty"`
}

var builderStatusToGateway = map[string]string{
	"pending":  "pending",
	"claimed":  "building",
	"building": "building",
	"success":  "success",
	"failed":   "failed",
	"canceled": "cancelled",
}

func mapStatus(status string) string {
	if mapped, ok := builderStatusToGateway[status]; ok {
		return mapped
	}
	return status
}

func generateEventID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("evt_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("evt_%s", hex.EncodeToString(bytes))
}

func signPayload(secret, timestamp, eventID string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	signedPayload := fmt.Sprintf("%s.%s.%s", timestamp, eventID, string(body))
	mac.Write([]byte(signedPayload))
	return fmt.Sprintf("v1=%s", hex.EncodeToString(mac.Sum(nil)))
}

func nullTimeToRFC3339(nt sql.NullTime) string {
	if nt.Valid {
		return nt.Time.Format(time.RFC3339)
	}
	return ""
}

func (c *Client) ReportResult(job *storage.BuildJob, status, errorMsg string) error {
	if c.callbackURL == "" {
		return nil // No callback URL configured
	}

	status = mapStatus(status)

	inspection := runtimeBuildInspection{
		BuildID:  job.ID,
		Status:   status,
		ImageTag: job.ImageTag,
		CommitSha: job.SourceInfo.CommitSha,
		Ref:      job.SourceInfo.Ref,
		Error:    errorMsg,
	}
	if !job.StartedAt.Time.IsZero() {
		inspection.StartedAt = job.StartedAt.Time.Format(time.RFC3339)
		inspection.FinishedAt = time.Now().Format(time.RFC3339)
		started := job.StartedAt.Time
		finished := time.Now()
		d := finished.Sub(started).Milliseconds()
		inspection.DurationMs = &d
	}
	if job.ExitCode.Valid {
		e := job.ExitCode.Int64
		inspection.ExitCode = &e
	}

	body, err := json.Marshal(inspection)
	if err != nil {
		return err
	}
	log.Printf("Callback payload for job %s: %s", job.ID, string(body))

	const maxRetries = 5
	const baseDelay = 2 * time.Second

	var lastErr error

	for i := 0; i <= maxRetries; i++ {
		if i > 0 {
			backoff := float64(baseDelay) * math.Pow(2, float64(i-1))
			jitter := (mathrand.Float64() * 0.4) - 0.2
			sleepDuration := time.Duration(backoff * (1 + jitter))
			log.Printf("Retrying callback for job %s in %v (attempt %d/%d)", job.ID, sleepDuration, i, maxRetries)
			time.Sleep(sleepDuration)
		}

		req, err := http.NewRequest("POST", c.callbackURL, bytes.NewBuffer(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		if c.callbackSecret != "" {
			eventID := generateEventID()
			timestamp := fmt.Sprintf("%d", time.Now().Unix())
			signature := signPayload(c.callbackSecret, timestamp, eventID, body)

			req.Header.Set("x-hubfly-event-id", eventID)
			req.Header.Set("x-hubfly-timestamp", timestamp)
			req.Header.Set("x-hubfly-signature", signature)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("WARN: callback request failed for job %s: %v", job.ID, err)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Body.Close()
			return nil
		}

		lastErr = fmt.Errorf("backend returned non-2xx status: %d", resp.StatusCode)
		log.Printf("WARN: callback request returned error for job %s: %v", job.ID, lastErr)
		resp.Body.Close()
	}

	return fmt.Errorf("failed to report result after %d attempts: %w", maxRetries, lastErr)
}

func runtimeEnvKeys(plan []storage.ResolvedEnvVar) []string {
	keys := make([]string, 0)
	for _, entry := range plan {
		if entry.Scope == "runtime" || entry.Scope == "both" {
			keys = append(keys, entry.Key)
		}
	}
	sort.Strings(keys)
	return keys
}

func callbackExposePort(cfg storage.BuildConfig) string {
	if !strings.EqualFold(strings.TrimSpace(cfg.Runtime), "static") {
		return ""
	}
	port := strings.TrimSpace(cfg.ExposePort)
	if port == "" {
		return "8080"
	}
	return port
}
