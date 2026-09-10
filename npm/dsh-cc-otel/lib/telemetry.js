/**
 * Telemetry adapter: one `llm/stream` waterfall listener that reports every
 * settled provider-call attempt to cc-otel as an OTLP/HTTP JSON log record,
 * using Claude Code's `api_request` vocabulary — the shape cc-otel's generic
 * ingestion path already stores (tokens, duration_ms, ttft_ms), so no
 * receiver-side changes are needed.
 *
 * Why a plugin at all: dsh 0.1.5 removed the FULL telemetry mode, so the
 * harness's own exporter uploads nothing for ordinary conversation (only an
 * explicit feedback action exports once). This adapter restores continuous,
 * per-call reporting without touching the native telemetry row — it is a
 * transparent listener, not an interceptor, and coexists with any other
 * `llm/stream` listener in the same host.
 *
 * Red line (same as everything in this package): reporting must never break
 * a model call. Every step is fenced — observing a chunk, building the
 * record, queueing, flushing. Failures log (rate-limited to once) and drop;
 * there is no retry loop and no durable queue. The wire contract:
 *
 *   POST <url>  Content-Type: application/json
 *   resource attr service.name = "deepseek-harness-plugin"
 *     (NOT "deepseek-harness": that exact value routes records into
 *      cc-otel's dsh session-ledger parser, which expects the native
 *      telemetry shape and would drop these; it must also never contain
 *      "codex", which routes to the Codex tables)
 *   record attrs: event.name=api_request, session.id, model, request_id,
 *     input/output/cache_read/cache_creation tokens (int64 as JSON strings),
 *     duration_ms, ttft_ms, error.type on failed attempts
 */

import { randomUUID } from 'node:crypto'

/** Default cc-otel OTLP/HTTP logs endpoint (full path — the exporter protocol needs /v1/logs). */
export const DEFAULT_OTLP_URL = 'http://localhost:4318/v1/logs'

/** Resource identity cc-otel stores as the Source column. */
const SERVICE_NAME = 'deepseek-harness-plugin'

/** Instrumentation scope naming this adapter on the wire. */
const SCOPE_NAME = 'young1lin.dsh-cc-otel'

const FLUSH_INTERVAL_MS = 3000
const FLUSH_TIMEOUT_MS = 3000
/** Flush early once this many records are waiting, so a burst is not held back by the timer. */
const FLUSH_BATCH_THRESHOLD = 16
/** Hard bound on queued records — beyond this the oldest are dropped, never the model call delayed. */
const MAX_QUEUED = 512

const str = (key, value) => ({ key, value: { stringValue: String(value) } })
// OTLP/JSON encodes int64 as a JSON string; protojson accepts numbers too,
// but the string form is the canonical one every SDK emits.
const int = (key, value) => ({ key, value: { intValue: String(Math.trunc(value)) } })

/**
 * Map one settled attempt onto an OTLP log record.
 * @param {{
 *   id: string, sessionId: string, model: string, provider: string,
 *   purpose?: string, startedAt: number, firstChunkAt?: number, endedAt: number,
 *   status: 'ok' | 'error' | 'aborted', errorType?: string,
 *   usage?: { inputTokens?: number, outputTokens?: number, cacheReadTokens?: number, cacheWriteTokens?: number },
 * }} attempt
 */
export function attemptToLogRecord(attempt) {
  const usage = attempt.usage ?? {}
  const attributes = [
    str('event.name', 'api_request'),
    str('session.id', attempt.sessionId),
    str('model', attempt.model),
    str('request_id', attempt.id),
    int('input_tokens', usage.inputTokens ?? 0),
    int('output_tokens', usage.outputTokens ?? 0),
    int('cache_read_tokens', usage.cacheReadTokens ?? 0),
    int('cache_creation_tokens', usage.cacheWriteTokens ?? 0),
    int('duration_ms', Math.max(0, attempt.endedAt - attempt.startedAt)),
  ]
  if (attempt.firstChunkAt !== undefined) {
    attributes.push(int('ttft_ms', Math.max(0, attempt.firstChunkAt - attempt.startedAt)))
  }
  if (attempt.purpose !== undefined) {
    // Auxiliary calls (compaction, session-title) are real spend; label them.
    attributes.push(str('purpose', attempt.purpose))
  }
  if (attempt.status !== 'ok' && attempt.errorType !== undefined) {
    attributes.push(str('error.type', attempt.errorType))
  }
  return {
    timeUnixNano: String(BigInt(attempt.startedAt) * 1000000n),
    severityNumber: attempt.status === 'ok' ? 9 : 17,
    attributes,
  }
}

/**
 * Assemble the OTLP/JSON ExportLogsServiceRequest for a batch of attempts.
 * @returns {object} JSON-serializable request body.
 */
export function exportRequest(attempts) {
  return {
    resourceLogs: [{
      resource: { attributes: [str('service.name', SERVICE_NAME)] },
      scopeLogs: [{
        scope: { name: SCOPE_NAME },
        logRecords: attempts.map(attemptToLogRecord),
      }],
    }],
  }
}

/**
 * Create the batching sink: attempts land here fire-and-forget, one OTLP
 * request carries whatever accumulated. A failed POST drops the batch —
 * cc-otel's own session-log backfill (tools/dsh_ingest) is the safety net,
 * and a telemetry helper must never grow a retry loop of its own.
 */
function createSink(url, logger) {
  /** @type {object[]} */
  let queue = []
  let flushing = false
  let warned = false
  const timer = setInterval(() => { void flush() }, FLUSH_INTERVAL_MS)
  timer.unref?.()

  async function flush() {
    if (flushing || queue.length === 0) return
    const batch = queue
    queue = []
    flushing = true
    try {
      const response = await fetch(url, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(exportRequest(batch)),
        signal: AbortSignal.timeout(FLUSH_TIMEOUT_MS),
      })
      // cc-otel never answers 2xx for data it did not store, so a 2xx here
      // means the batch is durably in; anything else is a drop-and-log.
      if (!response.ok && !warned) {
        warned = true
        logger.warn('dsh-cc-otel: telemetry export rejected (HTTP ' + response.status + ') — dropping this batch; further drops stay silent')
      }
    } catch (error) {
      if (!warned) {
        warned = true
        logger.warn('dsh-cc-otel: telemetry export failed (' + String(error) + ') — dropping this batch; further failures stay silent')
      }
    } finally {
      flushing = false
    }
  }

  return {
    push(attempt) {
      // Usage-less successful rows are counting noise (a cancelled/failed
      // attempt that never reported); failed ones still surface as errors.
      if (attempt.usage === undefined && attempt.status === 'ok') return
      if (queue.length >= MAX_QUEUED) queue.shift()
      queue.push(attempt)
      if (queue.length >= FLUSH_BATCH_THRESHOLD) void flush()
    },
    dispose() {
      clearInterval(timer)
      void flush()
    },
  }
}

/**
 * Wrap one attempt's stream: observe chunks through to the consumer
 * unchanged, then hand a minimal summary to the sink.
 * @returns {AsyncIterable<object>} the source stream, unchanged.
 */
async function* reportAttempt(options, source, sink) {
  let attempt
  try {
    const sessionId = typeof options.sessionId === 'string' && options.sessionId.length > 0 ? options.sessionId : '_'
    attempt = {
      id: 'dshp-' + randomUUID(),
      sessionId,
      provider: options.provider,
      model: options.model,
      ...options.purpose === undefined ? {} : { purpose: options.purpose },
      startedAt: Date.now(),
      firstChunkAt: undefined,
      endedAt: undefined,
      status: 'ok',
      errorType: undefined,
      usage: undefined,
    }
  } catch {
    // Nothing to report without identity — pass the stream through as-is.
    yield* source
    return
  }

  try {
    for await (const chunk of source) {
      try {
        if (attempt.firstChunkAt === undefined) attempt.firstChunkAt = Date.now()
        if (chunk.type === 'usage') {
          attempt.usage = chunk.usage
        } else if (chunk.type === 'finish') {
          const reason = chunk.reason
          if (reason.kind === 'error' || reason.kind === 'aborted') {
            attempt.status = reason.kind
            attempt.errorType = (reason.failure && reason.failure.code) || reason.kind
          }
          attempt.endedAt = Date.now()
        }
      } catch {
        // One unreadable chunk costs that chunk's fidelity, never the call.
      }
      yield chunk
    }
    if (attempt.endedAt === undefined) attempt.endedAt = Date.now()
  } finally {
    // Teardown before a finish chunk (consumer cancellation) settles aborted.
    if (attempt.endedAt === undefined) {
      attempt.endedAt = Date.now()
      attempt.status = 'aborted'
      attempt.errorType = attempt.errorType ?? 'STREAM_CLOSED'
    }
    try {
      sink.push(attempt)
    } catch {
      // Red line: reporting must never break the model call.
    }
  }
}

/**
 * Install the telemetry listener. Registered `global` so attempts from every
 * session scope (subagents included) are reported.
 * @param {{ on: Function, effect?: Function }} ctx Cordis context.
 * @param {{ url: string, logger: { info: Function, warn: Function } }} options
 * @returns {() => void} disposer
 */
export function installTelemetry(ctx, { url, logger }) {
  const sink = createSink(url, logger)
  const disposeListener = ctx.on('llm/stream', (request, next) => {
    const source = next()
    return reportAttempt(request, source, sink)
  }, { global: true })
  logger.info('dsh-cc-otel: telemetry adapter active — reporting every provider call to ' + url)
  return () => {
    disposeListener()
    sink.dispose()
  }
}
