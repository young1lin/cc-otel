package dbmerge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type JSONLSource struct {
	path string
}

func NewJSONLSource(path string) *JSONLSource {
	return &JSONLSource{path: path}
}

type legacyRecord struct {
	UUID  string         `json:"uuid"`
	Table string         `json:"table"`
	Ts    int64          `json:"ts"`
	Row   map[string]any `json:"row"`
}

func (s *JSONLSource) Scan(ctx context.Context, yield func(Row) error) error {
	file, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1024*1024)
	var lineNumber int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) != 0 {
			lineNumber++
			var record legacyRecord
			if err := json.Unmarshal(line, &record); err != nil {
				return fmt.Errorf("decode line %d: %w", lineNumber, err)
			}
			if _, ignored := ignoredColumns(record.Table); ignored {
				continue
			}
			spec, ok := LookupSpec(record.Table)
			if !ok {
				return fmt.Errorf("line %d: unknown table %q", lineNumber, record.Table)
			}
			for key, value := range record.Row {
				record.Row[key] = normalizeValue(value)
			}
			if record.Table == "pending_ttft_spans" {
				if _, ok := record.Row["request_id"]; !ok {
					record.Row["request_id"] = ""
				}
			}
			if record.Table == "codex_api_requests" {
				_, hasTotal := record.Row["total_tokens"]
				tool, hasTool := record.Row["tool_tokens"]
				if !hasTotal && hasTool {
					record.Row["total_tokens"] = normalizeValue(tool)
				}
			}
			for _, column := range spec.Columns {
				if _, ok := record.Row[column]; !ok {
					return fmt.Errorf("line %d: %s missing %s", lineNumber, record.Table, column)
				}
			}
			if err := yield(Row{Table: record.Table, Values: record.Row}); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
