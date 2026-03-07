package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"time"

	"hubfly-builder/internal/storage"
)

type Client struct {
	httpClient  *http.Client
	callbackURL string
}

func NewClient(callbackURL string) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		callbackURL: callbackURL,
	}
}

type ReportPayload struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"projectId"`
	UserID          string    `json:"userId"`
	Status          string    `json:"status"`
	ImageTag        string    `json:"imageTag,omitempty"`
	ExposePort      string    `json:"exposePort,omitempty"`
	StartedAt       time.Time `json:"startedAt"`
	FinishedAt      time.Time `json:"finishedAt"`
	DurationSeconds float64   `json:"durationSeconds"`
	LogPath         string    `json:"logPath"`
	Error           string    `json:"error,omitempty"`
	ResolvedEnvPlan []storage.ResolvedEnvVar `json:"resolvedEnvPlan,omitempty"`
	RuntimeEnvKeys  []string                 `json:"runtimeEnvKeys,omitempty"`
}

func (c *Client) ReportResult(job *storage.BuildJob, status, errorMsg string) error {
	if c.callbackURL == "" {
		return nil // No callback URL configured
	}

	payload := ReportPayload{
		ID:         job.ID,
		ProjectID:  job.ProjectID,
		UserID:     job.UserID,
		Status:     status,
		ImageTag:   job.ImageTag,
		ExposePort: callbackExposePort(job.BuildConfig),
		LogPath:    job.LogPath,
		Error:      errorMsg,
		ResolvedEnvPlan: job.BuildConfig.ResolvedEnvPlan,
		RuntimeEnvKeys:  runtimeEnvKeys(job.BuildConfig.ResolvedEnvPlan),
	}
	if !job.StartedAt.Time.IsZero() {
		payload.StartedAt = job.StartedAt.Time
		payload.FinishedAt = time.Now()
		payload.DurationSeconds = payload.FinishedAt.Sub(payload.StartedAt).Seconds()
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	log.Printf("Callback payload for job %s: %s", job.ID, string(body))

	const maxRetries = 5
	const baseDelay = 2 * time.Second

	var lastErr error

	for i := 0; i <= maxRetries; i++ {
		if i > 0 {
			// Exponential backoff: 2s, 4s, 8s, 16s, 32s
			backoff := float64(baseDelay) * math.Pow(2, float64(i-1))
			// Add jitter: +/- 20%
			jitter := (rand.Float64() * 0.4) - 0.2
			sleepDuration := time.Duration(backoff * (1 + jitter))
			log.Printf("Retrying callback for job %s in %v (attempt %d/%d)", job.ID, sleepDuration, i, maxRetries)
			time.Sleep(sleepDuration)
		}

		req, err := http.NewRequest("POST", c.callbackURL, bytes.NewBuffer(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

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
