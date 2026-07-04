package uploadserver

import (
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	callbackURL    string
	callbackSecret string
	httpClient     *http.Client
}

type callbackPayload struct {
	BuildID     string `json:"buildId"`
	Status      string `json:"status"`
	StartedAt   string `json:"startedAt,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
	ImageTag    string `json:"imageTag,omitempty"`
	Error       string `json:"error,omitempty"`
	UploadToken string `json:"uploadToken,omitempty"`
}

type uploadSessionManifest struct {
	BuildID         string `json:"buildId"`
	SourceImage     string `json:"sourceImage"`
	UploadToken     string `json:"uploadToken"`
	ContentEncoding string `json:"contentEncoding"`
	TotalSize       int64  `json:"totalSize"`
	ChunkSize       int64  `json:"chunkSize"`
}

type createUploadSessionRequest struct {
	ContentEncoding string `json:"contentEncoding"`
	TotalSize       int64  `json:"totalSize"`
	ChunkSize       int64  `json:"chunkSize"`
}

type uploadSessionResponse struct {
	BuildID        string `json:"buildId"`
	TotalSize      int64  `json:"totalSize"`
	ChunkSize      int64  `json:"chunkSize"`
	UploadedChunks []int  `json:"uploadedChunks"`
}

func NewServer(callbackURL string, callbackSecret string) *Server {
	return &Server{
		callbackURL:    strings.TrimSpace(callbackURL),
		callbackSecret: strings.TrimSpace(callbackSecret),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/image-upload/sessions", s.handleUploadSessions)
	mux.HandleFunc("/api/v1/image-upload/sessions/", s.handleUploadSession)
	mux.HandleFunc("/api/v1/image-upload", s.handleImageUpload)
	mux.HandleFunc("/healthz", s.handleHealth)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func (s *Server) handleUploadSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	buildID, sourceImage, uploadToken, ok := readUploadHeaders(w, r)
	if !ok {
		return
	}

	var payload createUploadSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid session payload", http.StatusBadRequest)
		return
	}
	if payload.ChunkSize <= 0 {
		payload.ChunkSize = 8 * 1024 * 1024
	}
	if payload.TotalSize <= 0 {
		http.Error(w, "totalSize must be greater than 0", http.StatusBadRequest)
		return
	}

	manifest := uploadSessionManifest{
		BuildID:         buildID,
		SourceImage:     sourceImage,
		UploadToken:     uploadToken,
		ContentEncoding: strings.TrimSpace(payload.ContentEncoding),
		TotalSize:       payload.TotalSize,
		ChunkSize:       payload.ChunkSize,
	}

	if err := s.ensureUploadSession(manifest); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	uploadedChunks, err := s.listUploadedChunks(buildID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(uploadSessionResponse{
		BuildID:        buildID,
		TotalSize:      manifest.TotalSize,
		ChunkSize:      manifest.ChunkSize,
		UploadedChunks: uploadedChunks,
	})
}

func (s *Server) handleUploadSession(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/image-upload/sessions/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}

	buildID := parts[0]
	switch {
	case len(parts) == 2 && parts[1] == "complete" && r.Method == http.MethodPost:
		s.handleCompleteUploadSession(w, r, buildID)
		return
	case len(parts) == 3 && parts[1] == "chunks" && r.Method == http.MethodPut:
		index, err := strconv.Atoi(parts[2])
		if err != nil || index < 0 {
			http.Error(w, "invalid chunk index", http.StatusBadRequest)
			return
		}
		s.handleUploadChunk(w, r, buildID, index)
		return
	default:
		http.NotFound(w, r)
		return
	}
}

func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request, buildID string, chunkIndex int) {
	_, sourceImage, uploadToken, ok := readUploadHeaders(w, r)
	if !ok {
		return
	}

	manifest, err := s.loadUploadSession(buildID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if manifest.UploadToken != uploadToken {
		http.Error(w, "invalid upload token", http.StatusForbidden)
		return
	}
	if manifest.SourceImage != sourceImage {
		http.Error(w, "invalid source image", http.StatusForbidden)
		return
	}

	if err := os.MkdirAll(s.uploadChunksDir(buildID), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	chunkPath := s.uploadChunkPath(buildID, chunkIndex)
	tempPath := chunkPath + ".partial"
	file, err := os.Create(tempPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if _, err := io.Copy(file, r.Body); err != nil {
		_ = file.Close()
		_ = os.Remove(tempPath)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tempPath, chunkPath); err != nil {
		_ = os.Remove(tempPath)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleCompleteUploadSession(w http.ResponseWriter, r *http.Request, buildID string) {
	startedAt := time.Now().UTC()
	_, sourceImage, uploadToken, ok := readUploadHeaders(w, r)
	if !ok {
		return
	}

	manifest, err := s.loadUploadSession(buildID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if manifest.UploadToken != uploadToken {
		http.Error(w, "invalid upload token", http.StatusForbidden)
		return
	}
	if manifest.SourceImage != sourceImage {
		http.Error(w, "invalid source image", http.StatusForbidden)
		return
	}

	if err := s.reportCallback(callbackPayload{
		BuildID:     buildID,
		Status:      "building",
		StartedAt:   startedAt.Format(time.RFC3339),
		UploadToken: uploadToken,
	}); err != nil {
		log.Printf("WARN: failed to report building status for %s: %v", buildID, err)
	}

	archivePath, cleanup, err := s.assembleUploadArchive(manifest)
	if err != nil {
		s.failUpload(w, buildID, uploadToken, startedAt, err)
		return
	}
	defer cleanup()

	loadPath, loadCleanup, err := prepareDockerLoadArchive(archivePath, manifest.ContentEncoding)
	if err != nil {
		s.failUpload(w, buildID, uploadToken, startedAt, err)
		return
	}
	defer loadCleanup()

	if output, err := runCommand("docker", "load", "-i", loadPath); err != nil {
		s.failUpload(w, buildID, uploadToken, startedAt, fmt.Errorf("docker load failed: %s", sanitizeOutput(output, err)))
		return
	}

	if output, err := runCommand("docker", "image", "inspect", manifest.SourceImage); err != nil {
		s.failUpload(w, buildID, uploadToken, startedAt, fmt.Errorf("loaded image %q not found: %s", manifest.SourceImage, sanitizeOutput(output, err)))
		return
	}

	imageTag := s.generateImageTag(buildID)
	if output, err := runCommand("docker", "tag", manifest.SourceImage, imageTag); err != nil {
		s.failUpload(w, buildID, uploadToken, startedAt, fmt.Errorf("docker tag failed: %s", sanitizeOutput(output, err)))
		return
	}

	if output, err := runCommand("docker", "push", imageTag); err != nil {
		s.failUpload(w, buildID, uploadToken, startedAt, fmt.Errorf("docker push failed: %s", sanitizeOutput(output, err)))
		return
	}

	defer func() {
		if manifest.SourceImage != imageTag {
			_, _ = runCommand("docker", "image", "rm", "-f", manifest.SourceImage)
		}
		_ = os.RemoveAll(s.uploadSessionDir(buildID))
	}()

	finishedAt := time.Now().UTC()
	if err := s.reportCallback(callbackPayload{
		BuildID:     buildID,
		Status:      "success",
		StartedAt:   startedAt.Format(time.RFC3339),
		FinishedAt:  finishedAt.Format(time.RFC3339),
		DurationMs:  finishedAt.Sub(startedAt).Milliseconds(),
		ImageTag:    imageTag,
		UploadToken: uploadToken,
	}); err != nil {
		http.Error(w, fmt.Sprintf("callback failed: %v", err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":  true,
		"buildId":  buildID,
		"imageTag": imageTag,
	})
}

func (s *Server) handleImageUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	startedAt := time.Now().UTC()
	buildID, sourceImage, uploadToken, ok := readUploadHeaders(w, r)
	if !ok {
		return
	}

	if err := s.reportCallback(callbackPayload{
		BuildID:     buildID,
		Status:      "building",
		StartedAt:   startedAt.Format(time.RFC3339),
		UploadToken: uploadToken,
	}); err != nil {
		log.Printf("WARN: failed to report building status for %s: %v", buildID, err)
	}

	archivePath, cleanup, err := writeRequestBodyToTemp(r.Body, buildID)
	if err != nil {
		s.failUpload(w, buildID, uploadToken, startedAt, fmt.Errorf("write upload archive: %w", err))
		return
	}
	defer cleanup()

	if output, err := runCommand("docker", "load", "-i", archivePath); err != nil {
		s.failUpload(w, buildID, uploadToken, startedAt, fmt.Errorf("docker load failed: %s", sanitizeOutput(output, err)))
		return
	}

	if output, err := runCommand("docker", "image", "inspect", sourceImage); err != nil {
		s.failUpload(w, buildID, uploadToken, startedAt, fmt.Errorf("loaded image %q not found: %s", sourceImage, sanitizeOutput(output, err)))
		return
	}

	imageTag := s.generateImageTag(buildID)
	if output, err := runCommand("docker", "tag", sourceImage, imageTag); err != nil {
		s.failUpload(w, buildID, uploadToken, startedAt, fmt.Errorf("docker tag failed: %s", sanitizeOutput(output, err)))
		return
	}

	if output, err := runCommand("docker", "push", imageTag); err != nil {
		s.failUpload(w, buildID, uploadToken, startedAt, fmt.Errorf("docker push failed: %s", sanitizeOutput(output, err)))
		return
	}

	defer func() {
		if sourceImage != imageTag {
			_, _ = runCommand("docker", "image", "rm", "-f", sourceImage)
		}
	}()

	finishedAt := time.Now().UTC()
	if err := s.reportCallback(callbackPayload{
		BuildID:     buildID,
		Status:      "success",
		StartedAt:   startedAt.Format(time.RFC3339),
		FinishedAt:  finishedAt.Format(time.RFC3339),
		DurationMs:  finishedAt.Sub(startedAt).Milliseconds(),
		ImageTag:    imageTag,
		UploadToken: uploadToken,
	}); err != nil {
		http.Error(w, fmt.Sprintf("callback failed: %v", err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":  true,
		"buildId":  buildID,
		"imageTag": imageTag,
	})
}

func readUploadHeaders(w http.ResponseWriter, r *http.Request) (string, string, string, bool) {
	buildID := strings.TrimSpace(r.Header.Get("X-Hubfly-Build-Id"))
	sourceImage := strings.TrimSpace(r.Header.Get("X-Hubfly-Source-Image"))
	uploadToken := strings.TrimSpace(r.Header.Get("X-Hubfly-Upload-Token"))
	if buildID == "" || sourceImage == "" || uploadToken == "" {
		http.Error(w, "missing upload headers", http.StatusBadRequest)
		return "", "", "", false
	}
	return buildID, sourceImage, uploadToken, true
}

func (s *Server) ensureUploadSession(manifest uploadSessionManifest) error {
	if err := os.MkdirAll(s.uploadChunksDir(manifest.BuildID), 0o755); err != nil {
		return err
	}

	existing, err := s.loadUploadSession(manifest.BuildID)
	if err == nil {
		if existing.UploadToken != manifest.UploadToken {
			return fmt.Errorf("upload token mismatch for build %s", manifest.BuildID)
		}
		if existing.SourceImage != manifest.SourceImage {
			return fmt.Errorf("source image mismatch for build %s", manifest.BuildID)
		}
		if existing.TotalSize != manifest.TotalSize {
			return fmt.Errorf("upload size mismatch for build %s", manifest.BuildID)
		}
		if existing.ChunkSize != manifest.ChunkSize {
			return fmt.Errorf("upload chunk size mismatch for build %s", manifest.BuildID)
		}
		return nil
	}

	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(s.uploadManifestPath(manifest.BuildID), payload, 0o600)
}

func (s *Server) loadUploadSession(buildID string) (uploadSessionManifest, error) {
	var manifest uploadSessionManifest
	data, err := os.ReadFile(s.uploadManifestPath(buildID))
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return uploadSessionManifest{}, err
	}
	return manifest, nil
}

func (s *Server) uploadRootDir() string {
	return filepath.Join(os.TempDir(), "hubfly-upload-sessions")
}

func (s *Server) uploadSessionDir(buildID string) string {
	return filepath.Join(s.uploadRootDir(), sanitizeImageComponent(buildID))
}

func (s *Server) uploadChunksDir(buildID string) string {
	return filepath.Join(s.uploadSessionDir(buildID), "chunks")
}

func (s *Server) uploadManifestPath(buildID string) string {
	return filepath.Join(s.uploadSessionDir(buildID), "manifest.json")
}

func (s *Server) uploadChunkPath(buildID string, chunkIndex int) string {
	return filepath.Join(s.uploadChunksDir(buildID), fmt.Sprintf("%06d.part", chunkIndex))
}

func (s *Server) listUploadedChunks(buildID string) ([]int, error) {
	entries, err := os.ReadDir(s.uploadChunksDir(buildID))
	if err != nil {
		if os.IsNotExist(err) {
			return []int{}, nil
		}
		return nil, err
	}

	indexes := make([]int, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		indexText := strings.TrimSuffix(entry.Name(), ".part")
		index, err := strconv.Atoi(indexText)
		if err != nil || index < 0 {
			continue
		}
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes, nil
}

func (s *Server) assembleUploadArchive(manifest uploadSessionManifest) (string, func(), error) {
	uploadedChunks, err := s.listUploadedChunks(manifest.BuildID)
	if err != nil {
		return "", nil, err
	}
	if len(uploadedChunks) == 0 {
		return "", nil, fmt.Errorf("no uploaded chunks found for build %s", manifest.BuildID)
	}

	expectedChunks := totalChunkCount(manifest.TotalSize, manifest.ChunkSize)
	if expectedChunks == 0 {
		return "", nil, fmt.Errorf("invalid upload manifest for build %s", manifest.BuildID)
	}
	if len(uploadedChunks) != expectedChunks {
		return "", nil, fmt.Errorf("upload is incomplete for build %s", manifest.BuildID)
	}

	for expected := 0; expected < expectedChunks; expected++ {
		if uploadedChunks[expected] != expected {
			return "", nil, fmt.Errorf("missing chunk %d for build %s", expected, manifest.BuildID)
		}
	}

	tmpDir, err := os.MkdirTemp("", "hubfly-upload-complete-"+sanitizeImageComponent(manifest.BuildID)+"-")
	if err != nil {
		return "", nil, err
	}

	archivePath := filepath.Join(tmpDir, "image.upload")
	out, err := os.Create(archivePath)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", nil, err
	}

	var written int64
	for _, index := range uploadedChunks {
		chunkPath := s.uploadChunkPath(manifest.BuildID, index)
		file, err := os.Open(chunkPath)
		if err != nil {
			_ = out.Close()
			_ = os.RemoveAll(tmpDir)
			return "", nil, err
		}
		n, err := io.Copy(out, file)
		_ = file.Close()
		if err != nil {
			_ = out.Close()
			_ = os.RemoveAll(tmpDir)
			return "", nil, err
		}
		written += n
	}
	if err := out.Close(); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", nil, err
	}
	if written != manifest.TotalSize {
		_ = os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("assembled upload size mismatch for build %s", manifest.BuildID)
	}

	return archivePath, func() { _ = os.RemoveAll(tmpDir) }, nil
}

func prepareDockerLoadArchive(archivePath, contentEncoding string) (string, func(), error) {
	switch strings.TrimSpace(contentEncoding) {
	case "", "docker-save+tar":
		return archivePath, func() {}, nil
	case "docker-save+gzip":
		file, err := os.Open(archivePath)
		if err != nil {
			return "", nil, err
		}
		defer file.Close()

		reader, err := gzip.NewReader(file)
		if err != nil {
			return "", nil, err
		}
		defer reader.Close()

		tmpDir, err := os.MkdirTemp("", "hubfly-upload-tar-*")
		if err != nil {
			return "", nil, err
		}

		tarPath := filepath.Join(tmpDir, "image.tar")
		out, err := os.Create(tarPath)
		if err != nil {
			_ = os.RemoveAll(tmpDir)
			return "", nil, err
		}
		if _, err := io.Copy(out, reader); err != nil {
			_ = out.Close()
			_ = os.RemoveAll(tmpDir)
			return "", nil, err
		}
		if err := out.Close(); err != nil {
			_ = os.RemoveAll(tmpDir)
			return "", nil, err
		}
		return tarPath, func() { _ = os.RemoveAll(tmpDir) }, nil
	default:
		return "", nil, fmt.Errorf("unsupported upload content encoding %q", contentEncoding)
	}
}

func totalChunkCount(totalSize, chunkSize int64) int {
	if totalSize <= 0 || chunkSize <= 0 {
		return 0
	}
	count := totalSize / chunkSize
	if totalSize%chunkSize != 0 {
		count++
	}
	return int(count)
}

func (s *Server) failUpload(
	w http.ResponseWriter,
	buildID, uploadToken string,
	startedAt time.Time,
	err error,
) {
	log.Printf("ERROR: image upload failed for %s: %v", buildID, err)
	finishedAt := time.Now().UTC()
	if callbackErr := s.reportCallback(callbackPayload{
		BuildID:     buildID,
		Status:      "failed",
		StartedAt:   startedAt.Format(time.RFC3339),
		FinishedAt:  finishedAt.Format(time.RFC3339),
		DurationMs:  finishedAt.Sub(startedAt).Milliseconds(),
		Error:       err.Error(),
		UploadToken: uploadToken,
	}); callbackErr != nil {
		log.Printf("WARN: failed to report failed upload callback for %s: %v", buildID, callbackErr)
	}
	http.Error(w, err.Error(), http.StatusBadGateway)
}

func (s *Server) reportCallback(payload callbackPayload) error {
	if s.callbackURL == "" {
		return fmt.Errorf("callback url is not configured")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, s.callbackURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	if s.callbackSecret != "" {
		eventID := generateEventID()
		timestamp := fmt.Sprintf("%d", time.Now().Unix())
		signature := signPayload(s.callbackSecret, timestamp, eventID, body)

		req.Header.Set("x-hubfly-event-id", eventID)
		req.Header.Set("x-hubfly-timestamp", timestamp)
		req.Header.Set("x-hubfly-signature", signature)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("callback status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	return nil
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

func (s *Server) generateImageTag(buildID string) string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	buildID = sanitizeImageComponent(buildID)
	if buildID == "" {
		buildID = "upload"
	}
	return fmt.Sprintf("127.0.0.1:10013/hubfly-cli-upload:%s-%s", buildID, ts)
}

func writeRequestBodyToTemp(body io.ReadCloser, buildID string) (string, func(), error) {
	defer body.Close()

	tmpDir, err := os.MkdirTemp("", "hubfly-upload-"+sanitizeImageComponent(buildID)+"-")
	if err != nil {
		return "", nil, err
	}

	path := filepath.Join(tmpDir, "image.tar")
	file, err := os.Create(path)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", nil, err
	}

	if _, err := io.Copy(file, body); err != nil {
		_ = file.Close()
		_ = os.RemoveAll(tmpDir)
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", nil, err
	}

	return path, func() {
		_ = os.RemoveAll(tmpDir)
	}, nil
}

func runCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func sanitizeOutput(output string, err error) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return err.Error()
	}
	return output
}

func sanitizeImageComponent(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}

	var builder strings.Builder
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z':
			builder.WriteRune(ch)
		case ch >= '0' && ch <= '9':
			builder.WriteRune(ch)
		case ch == '-', ch == '.', ch == '_':
			builder.WriteRune(ch)
		default:
			builder.WriteByte('-')
		}
	}

	return strings.Trim(builder.String(), "-._")
}
