package dbmerge

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	MaxBatchSize         = 10_000
	MaxBatchPayloadBytes = int64(128 << 20)
)

type Phase string

const (
	PhaseUploading  Phase = "uploading"
	PhaseInspecting Phase = "inspecting"
	PhasePreview    Phase = "preview"
	PhaseImporting  Phase = "importing"
	PhaseVerifying  Phase = "verifying"
)

type Row struct {
	Table  string
	Values map[string]any
}

type RowSource interface {
	Scan(context.Context, func(Row) error) error
}

type ProgressFunc func(Progress)

type Progress struct {
	Phase         Phase   `json:"phase"`
	CurrentTable  string  `json:"current_table,omitempty"`
	ProcessedRows int64   `json:"processed_rows"`
	TotalRows     int64   `json:"total_rows"`
	InsertedRows  int64   `json:"inserted_rows"`
	DuplicateRows int64   `json:"duplicate_rows"`
	Percent       float64 `json:"percent"`
}

type TableStats struct {
	Name          string `json:"name"`
	SourceRows    int64  `json:"source_rows"`
	NewRows       int64  `json:"new_rows"`
	DuplicateRows int64  `json:"duplicate_rows"`
	InsertedRows  int64  `json:"inserted_rows,omitempty"`
}

type Inspection struct {
	SourceRows    int64        `json:"source_rows"`
	NewRows       int64        `json:"new_rows"`
	DuplicateRows int64        `json:"duplicate_rows"`
	Tables        []TableStats `json:"tables"`
	IgnoredTables []string     `json:"ignored_tables"`
	Warnings      []string     `json:"warnings"`
}

type Result struct {
	ScannedRows        int64        `json:"scanned_rows"`
	InsertedRows       int64        `json:"inserted_rows"`
	DuplicateRows      int64        `json:"duplicate_rows"`
	VerifiedIdentities int64        `json:"verified_identities"`
	StartedAt          int64        `json:"started_at"`
	FinishedAt         int64        `json:"finished_at"`
	Tables             []TableStats `json:"tables"`
}

type TimeWindow struct {
	FromUnix int64
	ToUnix   int64
}

type Options struct {
	BatchSize    int
	SourceID     string
	Location     *time.Location
	TotalRows    int64
	Progress     ProgressFunc
	Window       *TimeWindow
	retryDelays  []time.Duration
	beforeCommit func(batch int) error
	batchBytes   int64
	metrics      func(batchMetrics)
}

type batchMetrics struct {
	LogicalRows         int64
	TransportStatements int
	TargetStatements    int
	WriterDuration      time.Duration
}

type ErrorCode string

const (
	ErrInvalidSQLite     ErrorCode = "invalid_sqlite"
	ErrIntegrityCheck    ErrorCode = "integrity_check_failed"
	ErrUnsupportedSchema ErrorCode = "unsupported_schema"
	ErrInspection        ErrorCode = "inspection_failed"
	ErrImport            ErrorCode = "import_failed"
	ErrVerification      ErrorCode = "verification_failed"
)

type MergeError struct {
	Code  ErrorCode
	Table string
	Row   int64
	Err   error
}

func (e *MergeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Table != "" {
		return fmt.Sprintf("%s: table %s row %d: %v", e.Code, e.Table, e.Row, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e *MergeError) Unwrap() error { return e.Err }

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
