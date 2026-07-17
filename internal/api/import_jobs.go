package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/young1lin/cc-otel/internal/dbmerge"
)

type importState string

const (
	importInspecting importState = "inspecting"
	importReady      importState = "ready"
	importImporting  importState = "importing"
	importSucceeded  importState = "succeeded"
	importFailed     importState = "failed"
)

var (
	errImportBusy       = errors.New("a database import is already active")
	errImportNotFound   = errors.New("database import job not found")
	errInvalidJobState  = errors.New("database import job is not ready")
	errImportInProgress = errors.New("database import is in progress")
)

type ImportFileStatus struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type ImportJobError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Table     string `json:"table,omitempty"`
	RowNumber int64  `json:"row_number,omitempty"`
}

type ImportJobStatus struct {
	JobID     string              `json:"job_id"`
	State     importState         `json:"state"`
	Phase     dbmerge.Phase       `json:"phase"`
	CreatedAt int64               `json:"created_at"`
	UpdatedAt int64               `json:"updated_at"`
	ExpiresAt int64               `json:"expires_at,omitempty"`
	Retryable bool                `json:"retryable"`
	File      ImportFileStatus    `json:"file"`
	Progress  dbmerge.Progress    `json:"progress"`
	Preview   *dbmerge.Inspection `json:"preview"`
	Result    *dbmerge.Result     `json:"result"`
	Error     *ImportJobError     `json:"error"`
}

type importEngine interface {
	Inspect(context.Context, string, *sql.DB, dbmerge.ProgressFunc) (dbmerge.Inspection, error)
	Merge(context.Context, *sql.DB, string, dbmerge.Options) (dbmerge.Result, error)
}

type defaultImportEngine struct{}

func (defaultImportEngine) Inspect(ctx context.Context, path string, target *sql.DB, progress dbmerge.ProgressFunc) (dbmerge.Inspection, error) {
	return dbmerge.InspectSQLite(ctx, path, target, progress)
}

func (defaultImportEngine) Merge(ctx context.Context, target *sql.DB, path string, options dbmerge.Options) (dbmerge.Result, error) {
	return dbmerge.MergeSQLite(ctx, target, path, options)
}

type importJob struct {
	ImportJobStatus
	path          string
	cancel        context.CancelFunc
	startedImport bool
	active        bool
}

type uploadReservation struct {
	JobID string
	Path  string
}

type importManager struct {
	mu sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
	target *sql.DB
	dir    string
	broker *Broker
	engine importEngine
	job    *importJob

	now          func() time.Time
	maxFileBytes int64
	sweepEvery   time.Duration

	wg        sync.WaitGroup
	closeOnce sync.Once
}

func newImportManager(
	parent context.Context,
	target *sql.DB,
	dbPath string,
	broker *Broker,
	engine importEngine,
) (*importManager, error) {
	if parent == nil {
		parent = context.Background()
	}
	if engine == nil {
		engine = defaultImportEngine{}
	}
	dir := filepath.Join(filepath.Dir(dbPath), ".imports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	ctx, cancel := context.WithCancel(parent)
	m := &importManager{
		ctx: ctx, cancel: cancel, target: target, dir: dir,
		broker: broker, engine: engine, now: time.Now,
		maxFileBytes: 2 << 30, sweepEvery: 15 * time.Minute,
	}
	if err := m.sweep(true); err != nil {
		cancel()
		return nil, err
	}
	go m.sweepLoop()
	return m, nil
}

func (m *importManager) reserveUpload(originalName string) (uploadReservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil && (m.job.State == importInspecting || m.job.State == importReady || m.job.State == importImporting) {
		return uploadReservation{}, errImportBusy
	}
	if m.job != nil {
		removeImportFiles(m.job.path)
		m.job = nil
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return uploadReservation{}, err
	}
	jobID := hex.EncodeToString(bytes)
	path := filepath.Join(m.dir, jobID+".db.upload")
	now := m.now().Unix()
	m.job = &importJob{
		ImportJobStatus: ImportJobStatus{
			JobID: jobID, State: importInspecting, Phase: dbmerge.PhaseUploading,
			CreatedAt: now, UpdatedAt: now,
			File:     ImportFileStatus{Name: filepath.Base(originalName)},
			Progress: dbmerge.Progress{Phase: dbmerge.PhaseUploading},
		},
		path: path,
	}
	return uploadReservation{JobID: jobID, Path: path}, nil
}

func (m *importManager) completeUpload(jobID string, size int64, sha256 string) error {
	m.mu.Lock()
	if m.job == nil || m.job.JobID != jobID {
		m.mu.Unlock()
		return errImportNotFound
	}
	if m.job.State != importInspecting || m.job.Phase != dbmerge.PhaseUploading {
		m.mu.Unlock()
		return errInvalidJobState
	}
	ctx, cancel := context.WithCancel(m.ctx)
	job := m.job
	job.File.SizeBytes = size
	job.File.SHA256 = sha256
	job.Phase = dbmerge.PhaseInspecting
	job.Progress = dbmerge.Progress{Phase: dbmerge.PhaseInspecting}
	job.UpdatedAt = m.now().Unix()
	job.cancel = cancel
	job.active = true
	path := job.path
	m.wg.Add(1)
	m.mu.Unlock()

	go m.runInspection(ctx, jobID, path)
	return nil
}

func (m *importManager) runInspection(ctx context.Context, jobID, path string) {
	defer m.wg.Done()
	preview, err := m.engine.Inspect(ctx, path, m.target, m.progressCallback(jobID))
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil || m.job.JobID != jobID {
		return
	}
	job := m.job
	job.active = false
	job.cancel = nil
	if ctx.Err() != nil {
		return
	}
	now := m.now()
	job.UpdatedAt = now.Unix()
	if err != nil {
		job.State = importFailed
		job.Retryable = false
		job.ExpiresAt = now.Add(time.Hour).Unix()
		job.Error = publicImportError(err, dbmerge.ErrInspection)
		return
	}
	job.State = importReady
	job.Phase = dbmerge.PhasePreview
	job.Progress.Phase = dbmerge.PhasePreview
	job.Progress.ProcessedRows = preview.SourceRows
	job.Progress.TotalRows = preview.SourceRows
	job.Progress.Percent = 100
	job.Preview = cloneInspection(&preview)
	job.ExpiresAt = now.Add(24 * time.Hour).Unix()
}

func (m *importManager) progressCallback(jobID string) dbmerge.ProgressFunc {
	return func(progress dbmerge.Progress) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.job == nil || m.job.JobID != jobID {
			return
		}
		m.job.Progress = progress
		m.job.Phase = progress.Phase
		m.job.UpdatedAt = m.now().Unix()
	}
}

func (m *importManager) start(jobID string) (*ImportJobStatus, error) {
	m.mu.Lock()
	if m.job == nil || m.job.JobID != jobID {
		m.mu.Unlock()
		return nil, errImportNotFound
	}
	job := m.job
	if job.State != importReady && !(job.State == importFailed && job.Retryable) {
		m.mu.Unlock()
		return nil, errInvalidJobState
	}
	ctx, cancel := context.WithCancel(m.ctx)
	job.State = importImporting
	job.Phase = dbmerge.PhaseImporting
	job.Progress = dbmerge.Progress{Phase: dbmerge.PhaseImporting}
	job.UpdatedAt = m.now().Unix()
	job.ExpiresAt = 0
	job.Retryable = false
	job.Error = nil
	job.Result = nil
	job.cancel = cancel
	job.active = true
	job.startedImport = true
	path := job.path
	sha := job.File.SHA256
	var total int64
	if job.Preview != nil {
		total = job.Preview.SourceRows
	}
	status := job.snapshot()
	m.wg.Add(1)
	m.mu.Unlock()

	go m.runMerge(ctx, jobID, path, sha, total)
	return status, nil
}

func (m *importManager) runMerge(ctx context.Context, jobID, path, sha string, total int64) {
	defer m.wg.Done()
	result, err := m.engine.Merge(ctx, m.target, path, dbmerge.Options{
		BatchSize: dbmerge.MaxBatchSize,
		SourceID:  "upload:" + sha,
		Location:  time.Local,
		TotalRows: total,
		Progress:  m.progressCallback(jobID),
	})
	notify := result.InsertedRows > 0
	m.mu.Lock()
	if m.job == nil || m.job.JobID != jobID {
		m.mu.Unlock()
		return
	}
	job := m.job
	job.active = false
	job.cancel = nil
	if ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	now := m.now()
	job.UpdatedAt = now.Unix()
	job.Result = cloneResult(&result)
	if err != nil {
		job.State = importFailed
		job.Retryable = true
		job.ExpiresAt = now.Add(time.Hour).Unix()
		job.Error = publicImportError(err, dbmerge.ErrImport)
	} else {
		removeImportFiles(job.path)
		job.State = importSucceeded
		job.Retryable = false
		job.ExpiresAt = 0
		job.Error = nil
	}
	m.mu.Unlock()
	if notify && m.broker != nil {
		m.broker.NotifySource("all")
	}
}

func (m *importManager) abortUpload(jobID string) error {
	m.mu.Lock()
	if m.job == nil || m.job.JobID != jobID {
		m.mu.Unlock()
		return errImportNotFound
	}
	job := m.job
	if job.State == importImporting {
		m.mu.Unlock()
		return errImportInProgress
	}
	cancel := job.cancel
	path := job.path
	active := job.active
	m.job = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if active {
		m.wg.Wait()
	}
	removeImportFiles(path)
	return nil
}

func (m *importManager) delete(jobID string) error {
	return m.abortUpload(jobID)
}

func (m *importManager) status(jobID string) (*ImportJobStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil || (jobID != "" && m.job.JobID != jobID) {
		return nil, false
	}
	return m.job.snapshot(), true
}

func (j *importJob) snapshot() *ImportJobStatus {
	if j == nil {
		return nil
	}
	out := j.ImportJobStatus
	out.Preview = cloneInspection(j.Preview)
	out.Result = cloneResult(j.Result)
	if j.Error != nil {
		errorCopy := *j.Error
		out.Error = &errorCopy
	}
	return &out
}

func cloneInspection(value *dbmerge.Inspection) *dbmerge.Inspection {
	if value == nil {
		return nil
	}
	out := *value
	out.Tables = append([]dbmerge.TableStats(nil), value.Tables...)
	out.IgnoredTables = append([]string(nil), value.IgnoredTables...)
	out.Warnings = append([]string(nil), value.Warnings...)
	return &out
}

func cloneResult(value *dbmerge.Result) *dbmerge.Result {
	if value == nil {
		return nil
	}
	out := *value
	out.Tables = append([]dbmerge.TableStats(nil), value.Tables...)
	return &out
}

func publicImportError(err error, fallback dbmerge.ErrorCode) *ImportJobError {
	code := fallback
	var mergeErr *dbmerge.MergeError
	if errors.As(err, &mergeErr) {
		code = mergeErr.Code
	}
	message := map[dbmerge.ErrorCode]string{
		dbmerge.ErrInvalidSQLite:     "The selected file is not a valid SQLite database.",
		dbmerge.ErrIntegrityCheck:    "The selected database failed its integrity check.",
		dbmerge.ErrUnsupportedSchema: "The selected database schema is not supported.",
		dbmerge.ErrInspection:        "Database inspection failed.",
		dbmerge.ErrImport:            "Database import failed.",
		dbmerge.ErrVerification:      "Imported data could not be verified.",
	}[code]
	if message == "" {
		message = "Database import failed."
	}
	out := &ImportJobError{Code: string(code), Message: message}
	if mergeErr != nil {
		out.Table = mergeErr.Table
		out.RowNumber = mergeErr.Row
	}
	return out
}

func (m *importManager) sweep(startup bool) error {
	now := m.now()
	var expiredPath string
	m.mu.Lock()
	currentPath := ""
	if m.job != nil {
		currentPath = m.job.path
		if !m.job.active {
			expired := (m.job.State == importFailed && now.Unix() >= m.job.UpdatedAt+int64(time.Hour/time.Second)) ||
				(m.job.State == importReady && now.Unix() >= m.job.UpdatedAt+int64((24*time.Hour)/time.Second))
			if expired {
				expiredPath = m.job.path
				m.job = nil
				currentPath = ""
			}
		}
	}
	m.mu.Unlock()
	if expiredPath != "" {
		removeImportFiles(expiredPath)
	}

	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	cutoff := now.Add(-24 * time.Hour)
	for _, entry := range entries {
		path := filepath.Join(m.dir, entry.Name())
		if path == currentPath || !knownImportOrphan(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	_ = startup
	return nil
}

func (m *importManager) sweepLoop() {
	ticker := time.NewTicker(m.sweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			_ = m.sweep(false)
		}
	}
}

func (m *importManager) close() {
	m.closeOnce.Do(func() {
		m.cancel()
		m.mu.Lock()
		if m.job != nil && m.job.cancel != nil {
			m.job.cancel()
		}
		m.mu.Unlock()
		m.wg.Wait()
	})
}

func knownImportOrphan(name string) bool {
	if strings.HasSuffix(name, ".db.upload") {
		return true
	}
	return strings.HasPrefix(name, ".inspect-") &&
		(strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".db-wal") || strings.HasSuffix(name, ".db-shm"))
}

func removeImportFiles(path string) {
	if path == "" {
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
}
