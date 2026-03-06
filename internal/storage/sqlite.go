package storage

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Storage struct {
	db *sql.DB
}

func NewStorage(dbPath string) (*Storage, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		return nil, err
	}

	if err := createTables(db); err != nil {
		return nil, err
	}

	return &Storage{db: db}, nil
}

func createTables(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS build_jobs (
			id TEXT PRIMARY KEY,
			project_id TEXT,
			user_id TEXT,
			source_type TEXT,
			source_info TEXT,
			build_config TEXT,
			status TEXT,
			image_tag TEXT,
			started_at DATETIME NULL,
			finished_at DATETIME NULL,
			exit_code INT NULL,
			retry_count INT DEFAULT 0,
			log_path TEXT,
			last_checkpoint TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME
		)
	`)
	return err
}

type SourceInfo struct {
	GitRepository string `json:"gitRepository"`
	CommitSha     string `json:"commitSha"`
	Ref           string `json:"ref"`
	WorkingDir    string `json:"workingDir"` // Subdirectory within the repo
}

func (a *SourceInfo) Value() (driver.Value, error) {
	return json.Marshal(a)
}

func (a *SourceInfo) Scan(value interface{}) error {
	b, ok := value.([]byte)
	if !ok {
		s, ok := value.(string)
		if !ok {
			return errors.New("type assertion to []byte or string failed")
		}
		b = []byte(s)
	}
	return json.Unmarshal(b, &a)
}

type ResourceLimits struct {
	CPU      int `json:"cpu"`
	MemoryMB int `json:"memoryMB"`
}

type EnvOverride struct {
	Scope  string `json:"scope,omitempty"`  // build, runtime, both
	Secret *bool  `json:"secret,omitempty"` // nil means auto-detect
}

type ResolvedEnvVar struct {
	Key    string `json:"key"`
	Scope  string `json:"scope"` // build, runtime, both
	Secret bool   `json:"secret"`
	Reason string `json:"reason,omitempty"`
}

type BuildConfig struct {
	IsAutoBuild        bool                   `json:"isAutoBuild"`
	Runtime            string                 `json:"runtime"`
	Framework          string                 `json:"framework,omitempty"`
	Version            string                 `json:"version"`
	InstallCommand     string                 `json:"installCommand,omitempty"`
	PrebuildCommand    string                 `json:"prebuildCommand"`
	SetupCommands      []string               `json:"setupCommands,omitempty"`
	BuildCommand       string                 `json:"buildCommand"`
	PostBuildCommands  []string               `json:"postBuildCommands,omitempty"`
	RunCommand         string                 `json:"runCommand"`
	RuntimeInitCommand string                 `json:"runtimeInitCommand,omitempty"`
	ExposePort         string                 `json:"exposePort,omitempty"`
	BuildContextDir    string                 `json:"buildContextDir,omitempty"`
	AppDir             string                 `json:"appDir,omitempty"`
	ValidationWarnings []string               `json:"validationWarnings,omitempty"`
	Network            string                 `json:"network,omitempty"`
	TimeoutSeconds     int                    `json:"timeoutSeconds"`
	ResourceLimits     ResourceLimits         `json:"resourceLimits"`
	Env                map[string]string      `json:"env,omitempty"`
	EnvOverrides       map[string]EnvOverride `json:"envOverrides,omitempty"`
	ResolvedEnvPlan    []ResolvedEnvVar       `json:"resolvedEnvPlan,omitempty"`
	DockerfileContent  []byte                 `json:"dockerfileContent,omitempty"`
}

func (a *BuildConfig) Value() (driver.Value, error) {
	return json.Marshal(a)
}

func (a *BuildConfig) Scan(value interface{}) error {
	b, ok := value.([]byte)
	if !ok {
		s, ok := value.(string)
		if !ok {
			return errors.New("type assertion to []byte or string failed")
		}
		b = []byte(s)
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	a.NormalizePhaseAliases()
	return nil
}

func (a *BuildConfig) NormalizePhaseAliases() {
	if strings.TrimSpace(a.InstallCommand) == "" {
		a.InstallCommand = strings.TrimSpace(a.PrebuildCommand)
	}
	if strings.TrimSpace(a.PrebuildCommand) == "" {
		a.PrebuildCommand = strings.TrimSpace(a.InstallCommand)
	}
}

type BuildJob struct {
	ID             string            `json:"id"`
	ProjectID      string            `json:"projectId"`
	UserID         string            `json:"userId"`
	SourceType     string            `json:"sourceType"`
	SourceInfo     SourceInfo        `json:"sourceInfo"`
	Env            map[string]string `json:"env,omitempty"` // Backward-compatible top-level env input.
	BuildConfig    BuildConfig       `json:"buildConfig"`
	Status         string            `json:"status"`
	ImageTag       string            `json:"imageTag"`
	StartedAt      sql.NullTime      `json:"startedAt"`
	FinishedAt     sql.NullTime      `json:"finishedAt"`
	ExitCode       sql.NullInt64     `json:"exitCode"`
	RetryCount     int               `json:"retryCount"`
	LogPath        string            `json:"logPath"`
	LastCheckpoint string            `json:"lastCheckpoint"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

func (s *Storage) CreateJob(job *BuildJob) error {
	job.BuildConfig.NormalizePhaseAliases()
	job.CreatedAt = time.Now()
	job.UpdatedAt = time.Now()
	job.Status = "pending"

	_, err := s.db.Exec(`
		INSERT INTO build_jobs (id, project_id, user_id, source_type, source_info, build_config, status, image_tag, started_at, finished_at, exit_code, retry_count, log_path, last_checkpoint, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, job.ID, job.ProjectID, job.UserID, job.SourceType, &job.SourceInfo, &job.BuildConfig, job.Status, job.ImageTag, job.StartedAt, job.FinishedAt, job.ExitCode, job.RetryCount, job.LogPath, job.LastCheckpoint, job.CreatedAt, job.UpdatedAt)

	return err
}

func (s *Storage) GetJob(id string) (*BuildJob, error) {
	job := &BuildJob{}

	err := s.db.QueryRow(`
		SELECT id, project_id, user_id, source_type, source_info, build_config, status, image_tag, started_at, finished_at, exit_code, retry_count, log_path, last_checkpoint, created_at, updated_at
		FROM build_jobs WHERE id = ?
	`, id).Scan(&job.ID, &job.ProjectID, &job.UserID, &job.SourceType, &job.SourceInfo, &job.BuildConfig, &job.Status, &job.ImageTag, &job.StartedAt, &job.FinishedAt, &job.ExitCode, &job.RetryCount, &job.LogPath, &job.LastCheckpoint, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return nil, err
	}
	job.Env = cloneStringMap(job.BuildConfig.Env)

	return job, nil
}

func (s *Storage) GetPendingJob() (*BuildJob, error) {
	job := &BuildJob{}

	err := s.db.QueryRow(`
		SELECT id, project_id, user_id, source_type, source_info, build_config, status, image_tag, started_at, finished_at, exit_code, retry_count, log_path, last_checkpoint, created_at, updated_at
		FROM build_jobs WHERE status = 'pending' ORDER BY created_at ASC LIMIT 1
	`).Scan(&job.ID, &job.ProjectID, &job.UserID, &job.SourceType, &job.SourceInfo, &job.BuildConfig, &job.Status, &job.ImageTag, &job.StartedAt, &job.FinishedAt, &job.ExitCode, &job.RetryCount, &job.LogPath, &job.LastCheckpoint, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return nil, err
	}
	job.Env = cloneStringMap(job.BuildConfig.Env)

	return job, nil
}

func (s *Storage) UpdateJobStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE build_jobs SET status = ?, updated_at = ? WHERE id = ?`, status, time.Now(), id)
	return err
}

func (s *Storage) UpdateJobLogPath(id, logPath string) error {
	_, err := s.db.Exec(`UPDATE build_jobs SET log_path = ?, updated_at = ? WHERE id = ?`, logPath, time.Now(), id)
	return err
}

func (s *Storage) UpdateJobImageTag(id, imageTag string) error {
	_, err := s.db.Exec(`UPDATE build_jobs SET image_tag = ?, updated_at = ? WHERE id = ?`, imageTag, time.Now(), id)
	return err
}

func (s *Storage) UpdateJobBuildConfig(id string, buildConfig *BuildConfig) error {
	buildConfig.NormalizePhaseAliases()
	_, err := s.db.Exec(`UPDATE build_jobs SET build_config = ?, updated_at = ? WHERE id = ?`, buildConfig, time.Now(), id)
	return err
}

func (s *Storage) IncrementJobRetryCount(id string) error {
	_, err := s.db.Exec(`UPDATE build_jobs SET retry_count = retry_count + 1, updated_at = ? WHERE id = ?`, time.Now(), id)
	return err
}

func (s *Storage) ResetInProgressJobs() error {
	_, err := s.db.Exec(`UPDATE build_jobs SET status = 'pending' WHERE status = 'claimed' OR status = 'building'`)
	return err
}

func (s *Storage) ResetDatabase() error {
	_, err := s.db.Exec(`DELETE FROM build_jobs`)
	return err
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}
