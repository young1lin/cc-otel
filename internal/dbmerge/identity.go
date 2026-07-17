package dbmerge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"strings"
)

type Identity struct {
	Columns []string
	Values  []any
	Digest  string
}

func IdentityFor(row Row) (Identity, error) {
	spec, ok := LookupSpec(row.Table)
	if !ok {
		return Identity{}, fmt.Errorf("unknown import table %q", row.Table)
	}
	var columns []string
	switch spec.Kind {
	case KindClaudeRequest:
		if requestID, _ := row.Values["request_id"].(string); requestID != "" {
			columns = []string{"request_id"}
		} else {
			columns = cols("timestamp session_id prompt_id event_sequence model input_tokens output_tokens duration_ms")
		}
	case KindCodexRequest:
		columns = cols("timestamp session_id model input_tokens output_tokens duration_ms")
	case KindPendingTTFT:
		if requestID, _ := row.Values["request_id"].(string); requestID != "" {
			columns = []string{"request_id"}
		} else {
			columns = cols("session_id model span_end_unix ttft_ms")
		}
	default:
		columns = append([]string(nil), spec.Columns...)
	}

	values := make([]any, len(columns))
	for i, column := range columns {
		values[i] = normalizeValue(row.Values[column])
	}
	digest, err := canonicalDigest(row.Table, columns, values)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Columns: columns, Values: values, Digest: digest}, nil
}

func LedgerID(row Row) (string, error) {
	identity, err := IdentityFor(row)
	return identity.Digest, err
}

func normalizeValue(value any) any {
	switch value := value.(type) {
	case int:
		return int64(value)
	case []byte:
		return string(value)
	case float64:
		if !math.IsInf(value, 0) && !math.IsNaN(value) && math.Trunc(value) == value && value >= math.MinInt64 && value <= math.MaxInt64 {
			return int64(value)
		}
	}
	return value
}

func canonicalDigest(table string, columns []string, values []any) (string, error) {
	digest := sha256.New()
	writeDigestBytes(digest, []byte(table))
	for index, column := range columns {
		writeDigestBytes(digest, []byte(column))
		if err := writeDigestValue(digest, values[index]); err != nil {
			return "", fmt.Errorf("identity %s.%s: %w", table, column, err)
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeDigestValue(digest hash.Hash, value any) error {
	var tag byte
	var data []byte
	switch value := value.(type) {
	case nil:
		tag = 'n'
	case int:
		tag = 'i'
		data = make([]byte, 8)
		binary.BigEndian.PutUint64(data, uint64(int64(value)))
	case int64:
		tag = 'i'
		data = make([]byte, 8)
		binary.BigEndian.PutUint64(data, uint64(value))
	case float64:
		tag = 'f'
		data = make([]byte, 8)
		binary.BigEndian.PutUint64(data, math.Float64bits(value))
	case string:
		tag = 's'
		data = []byte(value)
	case []byte:
		tag = 'b'
		data = value
	case bool:
		tag = 't'
		if value {
			data = []byte{1}
		} else {
			data = []byte{0}
		}
	default:
		return fmt.Errorf("unsupported value type %T", value)
	}
	_, _ = digest.Write([]byte{tag})
	writeDigestBytes(digest, data)
	return nil
}

func writeDigestBytes(digest hash.Hash, data []byte) {
	length := make([]byte, 8)
	binary.BigEndian.PutUint64(length, uint64(len(data)))
	_, _ = digest.Write(length)
	_, _ = digest.Write(data)
}

func RecordExists(ctx context.Context, q queryer, row Row) (bool, error) {
	identity, err := IdentityFor(row)
	if err != nil {
		return false, err
	}
	clauses := make([]string, len(identity.Columns))
	for i, column := range identity.Columns {
		clauses[i] = quoteIdent(column) + " IS ?"
	}
	sqlText := fmt.Sprintf(
		"SELECT 1 FROM %s WHERE %s LIMIT 1",
		quoteIdent(row.Table),
		strings.Join(clauses, " AND "),
	)
	var one int
	err = q.QueryRowContext(ctx, sqlText, identity.Values...).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
