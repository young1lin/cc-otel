package receiver

import (
	"context"
	"testing"

	"github.com/young1lin/cc-otel/internal/api"
	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commontpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// The DSH wire contract these tests pin down, read off
// @deepseek-ai/dsh-session-telemetry-otel's source: `event.type` lives in the
// record ATTRIBUTES (the body is only the session event's `data`), `event.seq`
// arrives as an int, and the body is a structured KvlistValue rather than a
// JSON string. Getting any of those wrong silently ingests nothing.

const dshSecond = int64(1_000_000_000)

func kvValue(pairs ...*commontpb.KeyValue) *commontpb.AnyValue {
	return &commontpb.AnyValue{Value: &commontpb.AnyValue_KvlistValue{
		KvlistValue: &commontpb.KeyValueList{Values: pairs},
	}}
}

func kvPair(key string, value *commontpb.AnyValue) *commontpb.KeyValue {
	return &commontpb.KeyValue{Key: key, Value: value}
}

// kvStr / kvInt are the package's existing OTLP attribute builders (opencode.go).
func kvNested(key string, p ...*commontpb.KeyValue) *commontpb.KeyValue {
	return kvPair(key, kvValue(p...))
}

// dshRecord builds one ledger record the way the coordinator's identityOf()
// plus the OTel JS SDK would put it on the wire.
func dshRecord(seq int64, eventType string, nanos int64, body *commontpb.AnyValue) *logspb.LogRecord {
	return &logspb.LogRecord{
		TimeUnixNano: uint64(nanos),
		Body:         body,
		Attributes: []*commontpb.KeyValue{
			strAttr("session.id", "sess-dsh"),
			strAttr("event.type", eventType),
			intAttr("event.seq", seq), // a number on the wire, not a string
			strAttr("session.cwd", "/repo"),
		},
	}
}

func dshResource() *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		strAttr("service.name", "deepseek-harness"),
		strAttr("service.version", "0.4.2"),
		strAttr("user.id", "anon-user-1"),
	}}
}

func sendDSHLogs(t *testing.T, rcv *Receiver, scope string, records ...*logspb.LogRecord) {
	t.Helper()
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: dshResource(),
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope:      &commontpb.InstrumentationScope{Name: scope},
				LogRecords: records,
			}},
		}},
	}
	if _, err := rcv.logs.Export(context.Background(), req); err != nil {
		t.Fatalf("logs Export: %v", err)
	}
}

func newDSHTestReceiver(t *testing.T) (*Receiver, *db.Repository) {
	t.Helper()
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	repo := db.NewRepository(d)
	return New(repo, &fakeResolver{}, api.NewBroker(), nil), repo
}

// One model call's worth of ledger records: the step opens, streams, assembles
// a message carrying usage, and closes.
func dshStepRecords(base int64) []*logspb.LogRecord {
	return []*logspb.LogRecord{
		dshRecord(0, "step/start", base, kvValue(kvInt("turn", 1), kvInt("step", 1))),
		dshRecord(1, "assistant/chunk", base+dshSecond,
			kvValue(kvInt("turn", 1), kvInt("step", 1))),
		dshRecord(2, "assistant/message", base+3*dshSecond, kvValue(
			kvInt("turn", 1),
			kvInt("step", 1),
			kvNested("message", kvNested("source",
				kvStr("kind", "model"),
				kvStr("provider", "deepseek"),
				kvStr("model", "deepseek-chat"),
			)),
			kvNested("usage",
				kvInt("inputTokens", 1200),
				kvInt("outputTokens", 340),
				kvInt("cacheReadTokens", 8600),
				kvInt("cacheWriteTokens", 450),
			),
		)),
		dshRecord(3, "step/end", base+3*dshSecond+dshSecond/10,
			kvValue(kvInt("turn", 1), kvInt("step", 1))),
	}
}

// A nonconforming exporter that omits event.seq must not collapse a whole
// session onto one row ("dsh-<session>-0") — the unique index on request_id
// would then silently drop every row after the first.
func TestDSHMissingSeq_StillStoresEveryRow(t *testing.T) {
	rcv, repo := newDSHTestReceiver(t)
	base := int64(1_700_000_000) * dshSecond

	noSeq := func(nanos int64) *logspb.LogRecord {
		return &logspb.LogRecord{
			TimeUnixNano: uint64(nanos),
			Body: kvValue(
				kvInt("turn", 1), kvInt("step", 1),
				kvNested("message", kvNested("source", kvStr("model", "deepseek-chat"))),
				kvNested("usage", kvInt("inputTokens", 10), kvInt("outputTokens", 5)),
			),
			Attributes: []*commontpb.KeyValue{
				strAttr("session.id", "sess-noseq"),
				strAttr("event.type", "assistant/message"),
			},
		}
	}
	sendDSHLogs(t, rcv, dshScopeName, noSeq(base), noSeq(base+dshSecond))
	// A replay of the same batch must collapse onto the same rows.
	sendDSHLogs(t, rcv, dshScopeName, noSeq(base), noSeq(base+dshSecond))

	var rows int
	if err := repo.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM api_requests WHERE session_id = 'sess-noseq'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Errorf("api_requests rows = %d, want 2 — missing event.seq collapsed or dropped rows", rows)
	}
}

// An assistant/message with usage becomes an api_request whose token columns
// keep DSH's disjoint split, with timing recovered from the step lifecycle.
func TestDSHAssistantMessage_BecomesAPIRequest(t *testing.T) {
	rcv, repo := newDSHTestReceiver(t)
	base := int64(1_700_000_000) * dshSecond

	sendDSHLogs(t, rcv, dshScopeName, dshStepRecords(base)...)

	var model, service, version, user, requestID string
	var in, out, cr, cc, dur, ttft int64
	err := repo.DB().QueryRowContext(context.Background(), `
		SELECT model, service_name, service_version, user_id, request_id,
		       input_tokens, output_tokens, cache_read_tokens,
		       cache_creation_tokens, duration_ms, ttft_ms
		  FROM api_requests ORDER BY id DESC LIMIT 1`).
		Scan(&model, &service, &version, &user, &requestID,
			&in, &out, &cr, &cc, &dur, &ttft)
	if err != nil {
		t.Fatalf("no api_request row was created: %v", err)
	}

	if model != "deepseek-chat" {
		t.Errorf("model = %q, want deepseek-chat", model)
	}
	if service != "deepseek-harness" {
		t.Errorf("service_name = %q, want deepseek-harness", service)
	}
	if version != "0.4.2" || user != "anon-user-1" {
		t.Errorf("resource identity = (%q, %q)", version, user)
	}
	// inputTokens is the uncached remainder, exactly as stored.
	if in != 1200 || out != 340 || cr != 8600 || cc != 450 {
		t.Errorf("tokens = (in %d, out %d, cacheRead %d, cacheCreate %d)", in, out, cr, cc)
	}
	// step/start → assistant/message is 3s; first chunk lands 1s in.
	if dur != 3000 {
		t.Errorf("duration_ms = %d, want 3000", dur)
	}
	if ttft != 1000 {
		t.Errorf("ttft_ms = %d, want 1000", ttft)
	}
	if requestID != "dsh-sess-dsh-2" {
		t.Errorf("request_id = %q, want dsh-sess-dsh-2 (session + event.seq)", requestID)
	}
}

// The exporter retries whole batches; the ledger's (session, seq) identity has
// to collapse a replay onto the same row.
func TestDSHExport_IsIdempotent(t *testing.T) {
	rcv, repo := newDSHTestReceiver(t)
	base := int64(1_700_000_000) * dshSecond

	sendDSHLogs(t, rcv, dshScopeName, dshStepRecords(base)...)
	sendDSHLogs(t, rcv, dshScopeName, dshStepRecords(base)...)

	var rows int
	if err := repo.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM api_requests`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("api_requests rows = %d after a replayed batch, want 1", rows)
	}
}

// FULL mode ships the whole session ledger — prompts, tool results, file
// contents. Only usage-bearing records may be persisted.
func TestDSHNonUsageRecords_AreNotStored(t *testing.T) {
	rcv, repo := newDSHTestReceiver(t)
	base := int64(1_700_000_000) * dshSecond

	sendDSHLogs(t, rcv, dshScopeName,
		dshRecord(0, "turn/start", base, kvValue(kvInt("turn", 1))),
		dshRecord(1, "user/message", base+dshSecond, kvValue(
			kvInt("turn", 1),
			kvNested("message", kvNested("source", kvStr("kind", "user"))),
		)),
		// An assembled message whose adapter reported no usage.
		dshRecord(2, "assistant/message", base+2*dshSecond, kvValue(
			kvInt("turn", 1), kvInt("step", 1),
			kvNested("message", kvNested("source",
				kvStr("kind", "model"), kvStr("model", "deepseek-chat"))),
		)),
		dshRecord(3, "tool/result", base+3*dshSecond, kvValue(kvStr("output", "secret file body"))),
	)
	// An ops record: no ledger identity at all.
	sendDSHLogs(t, rcv, dshScopeName+"/ops",
		&logspb.LogRecord{
			TimeUnixNano: uint64(base + 4*dshSecond),
			Body:         kvValue(),
			Attributes: []*commontpb.KeyValue{
				strAttr("telemetry.op", "shutdown"),
				strAttr("session.id", "sess-dsh"),
			},
		})

	for _, table := range []string{"api_requests", "events"} {
		var rows int
		if err := repo.DB().QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM `+table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Errorf("%s rows = %d, want 0 — non-usage ledger records must not be persisted", table, rows)
		}
	}
}

// A body that arrived as a JSON string (a proxy that stringifies structured
// bodies) must map identically to the SDK's native KvlistValue encoding.
func TestDSHBody_AcceptsJSONString(t *testing.T) {
	rcv, repo := newDSHTestReceiver(t)
	base := int64(1_700_000_000) * dshSecond

	sendDSHLogs(t, rcv, dshScopeName,
		dshRecord(7, "assistant/message", base, &commontpb.AnyValue{
			Value: &commontpb.AnyValue_StringValue{StringValue: `{
				"turn": 1, "step": 1,
				"message": {"source": {"kind": "model", "model": "deepseek-reasoner"}},
				"usage": {"inputTokens": 900, "outputTokens": 1500}
			}`},
		}))

	var model string
	var in, out int64
	err := repo.DB().QueryRowContext(context.Background(),
		`SELECT model, input_tokens, output_tokens FROM api_requests ORDER BY id DESC LIMIT 1`).
		Scan(&model, &in, &out)
	if err != nil {
		t.Fatalf("no api_request row was created: %v", err)
	}
	if model != "deepseek-reasoner" || in != 900 || out != 1500 {
		t.Errorf("row = (%q, %d, %d)", model, in, out)
	}
}

// The DSH branch must not swallow other clients' exports.
func TestResourceLogsIsDSH_OnlyMatchesDSH(t *testing.T) {
	claude := &logspb.ResourceLogs{
		Resource: &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
			strAttr("service.name", "claude-code"),
		}},
		ScopeLogs: []*logspb.ScopeLogs{{
			Scope: &commontpb.InstrumentationScope{Name: "com.anthropic.claude_code"},
		}},
	}
	if resourceLogsIsDSH(claude) {
		t.Error("a Claude Code resource was routed to the DSH path")
	}

	dsh := &logspb.ResourceLogs{
		ScopeLogs: []*logspb.ScopeLogs{{
			// The ops channel suffixes the ledger scope name.
			Scope: &commontpb.InstrumentationScope{Name: dshScopeName + "/ops"},
		}},
	}
	if !resourceLogsIsDSH(dsh) {
		t.Error("a DSH resource was not routed to the DSH path")
	}
}

// A collector in front of cc-otel batches several clients into one export
// request. DSH routing is decided per ResourceLogs, so the Claude Code half of
// such a batch must still land in api_requests — not be silently dropped by the
// DSH handler, which only understands DSH event types.
func TestMixedBatch_StoresBothResources(t *testing.T) {
	rcv, repo := newDSHTestReceiver(t)
	base := int64(1_700_000_000) * dshSecond

	dshMessage := dshRecord(2, "assistant/message", base, kvValue(
		kvInt("turn", 1),
		kvInt("step", 1),
		kvNested("usage",
			kvInt("inputTokens", 1200),
			kvInt("outputTokens", 340),
		),
	))
	claudeRecord := &logspb.LogRecord{
		TimeUnixNano: uint64(base),
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "api_request"),
			strAttr("model", "claude-opus-4-6"),
			strAttr("session.id", "sess-claude"),
			strAttr("request_id", "req-mixed"),
			intAttr("input_tokens", 100),
			intAttr("output_tokens", 50),
		},
	}
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{
			{
				Resource: dshResource(),
				ScopeLogs: []*logspb.ScopeLogs{{
					Scope:      &commontpb.InstrumentationScope{Name: dshScopeName},
					LogRecords: []*logspb.LogRecord{dshMessage},
				}},
			},
			{
				Resource: &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
					strAttr("service.name", "claude-code"),
				}},
				ScopeLogs: []*logspb.ScopeLogs{{
					LogRecords: []*logspb.LogRecord{claudeRecord},
				}},
			},
		},
	}
	if _, err := rcv.logs.Export(context.Background(), req); err != nil {
		t.Fatalf("logs Export: %v", err)
	}

	var dshRows, claudeRows int
	if err := repo.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM api_requests WHERE service_name = 'deepseek-harness'`).Scan(&dshRows); err != nil {
		t.Fatal(err)
	}
	if err := repo.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM api_requests WHERE service_name = 'claude-code'`).Scan(&claudeRows); err != nil {
		t.Fatal(err)
	}
	if dshRows != 1 || claudeRows != 1 {
		t.Errorf("mixed batch stored (dsh %d, claude %d), want (1, 1) — the non-DSH half was dropped", dshRows, claudeRows)
	}
}
