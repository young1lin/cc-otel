package dbmerge

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"
)

type aggKey struct {
	date  string
	model string
}

type claudeDelta struct {
	input         int64
	output        int64
	cacheRead     int64
	cacheCreation int64
	cost          int64
	requests      int64
}

type codexDelta struct {
	claudeDelta
	reasoning int64
	total     int64
}

func addAggregateDelta(location *time.Location, row Row, claude map[aggKey]*claudeDelta, codex map[aggKey]*codexDelta) {
	if row.Table != "api_requests" && row.Table != "codex_api_requests" {
		return
	}
	if location == nil {
		location = time.Local
	}
	timestamp := int64Value(row.Values["timestamp"])
	key := aggKey{
		date:  time.Unix(timestamp, 0).In(location).Format("2006-01-02"),
		model: stringValue(row.Values["model"]),
	}
	if row.Table == "api_requests" {
		delta := claude[key]
		if delta == nil {
			delta = &claudeDelta{}
			claude[key] = delta
		}
		addCommonDelta(delta, row)
		return
	}
	delta := codex[key]
	if delta == nil {
		delta = &codexDelta{}
		codex[key] = delta
	}
	addCommonDelta(&delta.claudeDelta, row)
	delta.reasoning += int64Value(row.Values["reasoning_tokens"])
	delta.total += int64Value(row.Values["total_tokens"])
}

func addCommonDelta(delta *claudeDelta, row Row) {
	delta.input += int64Value(row.Values["input_tokens"])
	delta.output += int64Value(row.Values["output_tokens"])
	delta.cacheRead += int64Value(row.Values["cache_read_tokens"])
	delta.cacheCreation += int64Value(row.Values["cache_creation_tokens"])
	delta.cost += int64Value(row.Values["cost_usd"])
	delta.requests++
}

func applyAggregateDeltas(ctx context.Context, conn *sql.Conn, claude map[aggKey]*claudeDelta, codex map[aggKey]*codexDelta) error {
	for _, key := range sortedAggKeys(claude) {
		delta := claude[key]
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO daily_model_agg
				(date, model, input_tokens, output_tokens, cache_read_tokens,
				 cache_creation_tokens, cost_usd, request_count)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(date, model) DO UPDATE SET
				input_tokens = input_tokens + excluded.input_tokens,
				output_tokens = output_tokens + excluded.output_tokens,
				cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
				cache_creation_tokens = cache_creation_tokens + excluded.cache_creation_tokens,
				cost_usd = cost_usd + excluded.cost_usd,
				request_count = request_count + excluded.request_count
		`, key.date, key.model, delta.input, delta.output, delta.cacheRead, delta.cacheCreation, delta.cost, delta.requests); err != nil {
			return err
		}
	}
	for _, key := range sortedAggKeys(codex) {
		delta := codex[key]
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO codex_daily_model_agg
				(date, model, input_tokens, output_tokens, cache_read_tokens,
				 cache_creation_tokens, reasoning_tokens, total_tokens, cost_usd,
				 request_count)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(date, model) DO UPDATE SET
				input_tokens = input_tokens + excluded.input_tokens,
				output_tokens = output_tokens + excluded.output_tokens,
				cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
				cache_creation_tokens = cache_creation_tokens + excluded.cache_creation_tokens,
				reasoning_tokens = reasoning_tokens + excluded.reasoning_tokens,
				total_tokens = total_tokens + excluded.total_tokens,
				cost_usd = cost_usd + excluded.cost_usd,
				request_count = request_count + excluded.request_count
		`, key.date, key.model, delta.input, delta.output, delta.cacheRead, delta.cacheCreation,
			delta.reasoning, delta.total, delta.cost, delta.requests); err != nil {
			return err
		}
	}
	return nil
}

func sortedAggKeys[T any](values map[aggKey]T) []aggKey {
	keys := make([]aggKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].date == keys[j].date {
			return keys[i].model < keys[j].model
		}
		return keys[i].date < keys[j].date
	})
	return keys
}

func int64Value(value any) int64 {
	switch value := normalizeValue(value).(type) {
	case int64:
		return value
	case float64:
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			return int64(value)
		}
	}
	return 0
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
