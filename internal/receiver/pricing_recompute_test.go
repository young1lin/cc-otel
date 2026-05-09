package receiver

import (
	"context"
	"testing"

	"github.com/young1lin/cc-otel/internal/api"
	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
	"github.com/young1lin/cc-otel/internal/pricing"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commontpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// stubPricer returns a fixed cost regardless of model — handy for asserting
// "the receiver hooked the pricer in" without depending on the seed table
// staying numerically stable across LiteLLM regenerations.
type stubPricer struct {
	cost           float64
	calls          int
	last           string
	lastInput      int64
	lastOutput     int64
	lastCacheRead  int64
	lastCacheWrite int64
}

func (p *stubPricer) Calc(_ context.Context, model string, input, output, cacheRead, cacheWrite int64) float64 {
	p.calls++
	p.last = model
	p.lastInput = input
	p.lastOutput = output
	p.lastCacheRead = cacheRead
	p.lastCacheWrite = cacheWrite
	return p.cost
}

func newPricingTestReceiver(t *testing.T, pricer Pricer) (*Receiver, *db.Repository) {
	t.Helper()
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	repo := db.NewRepository(d)
	rcv := New(repo, &fakeResolver{}, api.NewBroker(), pricer)
	return rcv, repo
}

func sendLogs(t *testing.T, rcv *Receiver, res *resourcepb.Resource, lr *logspb.LogRecord) {
	t.Helper()
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: res,
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{lr},
			}},
		}},
	}
	if _, err := rcv.logs.Export(context.Background(), req); err != nil {
		t.Fatalf("Export: %v", err)
	}
}

// TestReceiver_ClaudeNotRecomputed verifies the load-bearing rule: Claude
// model events keep the upstream-reported cost_usd verbatim (Anthropic owns
// the canonical price table), even with a pricer wired in.
func TestReceiver_ClaudeNotRecomputed(t *testing.T) {
	pricer := &stubPricer{cost: 999} // would corrupt the row if applied
	rcv, repo := newPricingTestReceiver(t, pricer)

	sendLogs(t, rcv, nil, &logspb.LogRecord{
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "api_request"),
			strAttr("model", "claude-sonnet-4-5"),
			strAttr("session.id", "sess-claude"),
			strAttr("request_id", "req-claude"),
			intAttr("input_tokens", 1000),
			intAttr("output_tokens", 500),
			dblAttr("cost_usd", 0.0123),
		},
	})

	var cost int64
	repo.DB().QueryRowContext(context.Background(),
		`SELECT cost_usd FROM api_requests ORDER BY id DESC LIMIT 1`).Scan(&cost)
	// cost_usd is stored as int64 0.00001-USD units (costScale=1e5). 0.0123 USD == 1230.
	if cost != 1230 {
		t.Errorf("Claude row should keep upstream cost (1230 units = 0.0123 USD), got %d", cost)
	}
	// Pricer must not have been consulted for a Claude model.
	if pricer.calls != 0 {
		t.Errorf("pricer should not run for Claude models; calls=%d last=%s", pricer.calls, pricer.last)
	}
}

// TestReceiver_GLMRecomputed verifies non-Claude models go through the
// pricer. GLM via the Anthropic-compatible reverse proxy reports an
// Anthropic-priced cost_usd; the receiver must overwrite it.
func TestReceiver_GLMRecomputed(t *testing.T) {
	pricer := &stubPricer{cost: 0.42}
	rcv, repo := newPricingTestReceiver(t, pricer)

	sendLogs(t, rcv, nil, &logspb.LogRecord{
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "api_request"),
			strAttr("model", "glm-4.6"),
			strAttr("session.id", "sess-glm"),
			strAttr("request_id", "req-glm"),
			intAttr("input_tokens", 1000),
			intAttr("output_tokens", 500),
			dblAttr("cost_usd", 99), // bogus Anthropic-priced upstream value
		},
	})

	if pricer.calls != 1 {
		t.Errorf("pricer should run exactly once for GLM, got %d", pricer.calls)
	}

	var cost int64
	repo.DB().QueryRowContext(context.Background(),
		`SELECT cost_usd FROM api_requests ORDER BY id DESC LIMIT 1`).Scan(&cost)
	// 0.42 USD * 1e5 = 42000 units
	if cost != 42000 {
		t.Errorf("GLM row should be recomputed to 0.42 USD (42000 units), got %d", cost)
	}
}

// TestReceiver_PricerZero_KeepsUpstream guards against the pricer returning
// 0 (model not in table) silently zeroing out a row. We only overwrite when
// the recompute produces a positive value.
func TestReceiver_PricerZero_KeepsUpstream(t *testing.T) {
	pricer := &stubPricer{cost: 0}
	rcv, repo := newPricingTestReceiver(t, pricer)

	sendLogs(t, rcv, nil, &logspb.LogRecord{
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "api_request"),
			strAttr("model", "totally-unknown-model"),
			strAttr("request_id", "req-unknown"),
			intAttr("input_tokens", 100),
			intAttr("output_tokens", 50),
			dblAttr("cost_usd", 0.05),
		},
	})

	var cost int64
	repo.DB().QueryRowContext(context.Background(),
		`SELECT cost_usd FROM api_requests ORDER BY id DESC LIMIT 1`).Scan(&cost)
	// Upstream 0.05 USD * 1e5 = 5000 units should survive.
	if cost != 5000 {
		t.Errorf("unknown model with cost=0 from pricer should keep upstream 5000 units, got %d", cost)
	}
}

// TestReceiver_NilPricer_NoOp verifies legacy code paths (pricer omitted)
// still trust the upstream cost — nothing crashes, nothing rewrites.
func TestReceiver_NilPricer_NoOp(t *testing.T) {
	rcv, repo := newPricingTestReceiver(t, nil)

	sendLogs(t, rcv, nil, &logspb.LogRecord{
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "api_request"),
			strAttr("model", "glm-4.6"),
			strAttr("request_id", "req-nilp"),
			intAttr("input_tokens", 100),
			intAttr("output_tokens", 50),
			dblAttr("cost_usd", 0.07),
		},
	})

	var cost int64
	repo.DB().QueryRowContext(context.Background(),
		`SELECT cost_usd FROM api_requests ORDER BY id DESC LIMIT 1`).Scan(&cost)
	if cost != 7000 {
		t.Errorf("nil pricer should leave cost untouched (7000 units = 0.07 USD), got %d", cost)
	}
}

// TestReceiver_CodexRecomputedOnSSECompleted verifies the Codex path: the
// initial codex.api_request row carries no tokens/cost, and the
// codex.sse_event(response.completed) update must populate cost via the
// pricer along with token counts.
func TestReceiver_CodexRecomputedOnSSECompleted(t *testing.T) {
	pricer := &stubPricer{cost: 0.077}
	rcv, repo := newPricingTestReceiver(t, pricer)
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		strAttr("service.name", "codex-cli"),
	}}

	// 1) codex.api_request — row inserted with zero tokens, cost should still be 0
	sendLogs(t, rcv, res, &logspb.LogRecord{
		TimeUnixNano: uint64(1700000000) * 1e9,
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "codex.api_request"),
			strAttr("conversation.id", "conv-codex-1"),
			strAttr("model", "gpt-5-codex"),
			intAttr("duration_ms", 1234),
		},
	})
	var preCost int64
	repo.DB().QueryRowContext(context.Background(),
		`SELECT cost_usd FROM codex_api_requests`).Scan(&preCost)
	if preCost != 0 {
		t.Errorf("after codex.api_request only, cost should still be 0, got %d", preCost)
	}
	if pricer.calls != 0 {
		t.Errorf("pricer should not run on api_request alone, calls=%d", pricer.calls)
	}

	// 2) codex.sse_event response.completed — cost recomputed and stored
	sendLogs(t, rcv, res, &logspb.LogRecord{
		TimeUnixNano: uint64(1700000005) * 1e9,
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "codex.sse_event"),
			strAttr("event.kind", "response.completed"),
			strAttr("conversation.id", "conv-codex-1"),
			strAttr("model", "gpt-5-codex"),
			intAttr("input_token_count", 10000),
			intAttr("output_token_count", 2000),
			intAttr("cached_token_count", 500),
		},
	})

	if pricer.calls != 1 {
		t.Errorf("pricer should run once on response.completed, got %d", pricer.calls)
	}

	var costAfter, inputAfter int64
	repo.DB().QueryRowContext(context.Background(),
		`SELECT cost_usd, input_tokens FROM codex_api_requests`).Scan(&costAfter, &inputAfter)
	if inputAfter != 10000 {
		t.Errorf("expected tokens to land on the existing row, got input=%d", inputAfter)
	}
	// 0.077 USD * 1e5 = 7700 units
	if costAfter != 7700 {
		t.Errorf("expected cost 7700 units after recompute, got %d", costAfter)
	}

	// And the daily aggregate should also carry the recomputed cost.
	var aggCost int64
	repo.DB().QueryRowContext(context.Background(),
		`SELECT cost_usd FROM codex_daily_model_agg WHERE model = 'gpt-5-codex'`).Scan(&aggCost)
	if aggCost != 7700 {
		t.Errorf("codex_daily_model_agg cost should also be 7700 units, got %d", aggCost)
	}
}

// TestReceiver_CodexFallbackInsertCarriesCost covers the path where SSE
// completion arrives without a matching pending api_request row — the
// fallback INSERT must still carry the recomputed cost.
func TestReceiver_CodexFallbackInsertCarriesCost(t *testing.T) {
	pricer := &stubPricer{cost: 0.0125}
	rcv, repo := newPricingTestReceiver(t, pricer)
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		strAttr("service.name", "codex-cli"),
	}}

	// Note: no preceding codex.api_request — fall through to InsertCodexAPIRequest.
	sendLogs(t, rcv, res, &logspb.LogRecord{
		TimeUnixNano: uint64(1700000000) * 1e9,
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "codex.sse_event"),
			strAttr("event.kind", "response.completed"),
			strAttr("conversation.id", "conv-codex-fb"),
			strAttr("model", "gpt-5-codex"),
			intAttr("input_token_count", 100),
			intAttr("output_token_count", 50),
		},
	})

	var rowCount int
	repo.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM codex_api_requests`).Scan(&rowCount)
	if rowCount != 1 {
		t.Fatalf("expected fallback INSERT to create exactly 1 row, got %d", rowCount)
	}

	var cost int64
	repo.DB().QueryRowContext(context.Background(),
		`SELECT cost_usd FROM codex_api_requests`).Scan(&cost)
	// 0.0125 USD * 1e5 = 1250 units
	if cost != 1250 {
		t.Errorf("fallback row cost should be 1250 units, got %d", cost)
	}
}

// TestReceiver_CodexCacheNotDoubleCounted verifies the receiver subtracts
// cached_token_count from input_token_count before pricing — OpenAI reports
// input as the TOTAL including cached, while pricing.Calc expects uncached
// only. Without the subtraction, cache tokens get billed twice (once at
// full input rate, once at cache rate).
func TestReceiver_CodexCacheNotDoubleCounted(t *testing.T) {
	pricer := &stubPricer{cost: 0.01}
	rcv, _ := newPricingTestReceiver(t, pricer)
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		strAttr("service.name", "codex-cli"),
	}}

	sendLogs(t, rcv, res, &logspb.LogRecord{
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "codex.sse_event"),
			strAttr("event.kind", "response.completed"),
			strAttr("conversation.id", "conv-cache"),
			strAttr("model", "gpt-5-codex"),
			intAttr("input_token_count", 10000), // total input incl. cached
			intAttr("output_token_count", 200),
			intAttr("cached_token_count", 9000), // 9000 of those were cached
		},
	})

	if pricer.lastInput != 1000 {
		t.Errorf("expected uncached input passed to pricer = 10000-9000 = 1000, got %d", pricer.lastInput)
	}
	if pricer.lastCacheRead != 9000 {
		t.Errorf("expected cache_read = 9000 passed unchanged, got %d", pricer.lastCacheRead)
	}
	if pricer.lastOutput != 200 {
		t.Errorf("expected output = 200 passed unchanged, got %d", pricer.lastOutput)
	}
}

// TestReceiver_CodexCacheGreaterThanInput_ClampsToZero defends against weird
// upstream payloads where cached_token_count > input_token_count (shouldn't
// happen but malformed clients do exist) — uncached must clamp to 0, never
// go negative.
func TestReceiver_CodexCacheGreaterThanInput_ClampsToZero(t *testing.T) {
	pricer := &stubPricer{cost: 0.01}
	rcv, _ := newPricingTestReceiver(t, pricer)
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		strAttr("service.name", "codex-cli"),
	}}

	sendLogs(t, rcv, res, &logspb.LogRecord{
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "codex.sse_event"),
			strAttr("event.kind", "response.completed"),
			strAttr("conversation.id", "conv-bad"),
			strAttr("model", "gpt-5-codex"),
			intAttr("input_token_count", 100),
			intAttr("output_token_count", 10),
			intAttr("cached_token_count", 9999), // bogus: cache > input
		},
	})

	if pricer.lastInput != 0 {
		t.Errorf("expected uncached input clamped to 0, got %d", pricer.lastInput)
	}
}

// TestReceiver_RealRegistry_HitsSeed exercises the real pricing.Registry
// against the embedded seed: a known model (gpt-5-codex) should produce
// non-zero cost end-to-end through the receiver.
func TestReceiver_RealRegistry_HitsSeed(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	repo := db.NewRepository(d)

	reg, err := pricing.NewRegistry(context.Background(), d, &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	rcv := New(repo, &fakeResolver{}, api.NewBroker(), reg)
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		strAttr("service.name", "codex-cli"),
	}}

	sendLogs(t, rcv, res, &logspb.LogRecord{
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "codex.sse_event"),
			strAttr("event.kind", "response.completed"),
			strAttr("conversation.id", "conv-real"),
			strAttr("model", "gpt-5-codex"),
			intAttr("input_token_count", 1_000_000),
			intAttr("output_token_count", 1_000_000),
		},
	})

	var cost int64
	repo.DB().QueryRowContext(context.Background(),
		`SELECT cost_usd FROM codex_api_requests`).Scan(&cost)
	if cost <= 0 {
		t.Errorf("expected positive cost from real seed for gpt-5-codex, got %d micro-USD", cost)
	}
}
