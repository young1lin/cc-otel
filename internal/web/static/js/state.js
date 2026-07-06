// Central mutable state shared across modules during the ESM refactor.
// Property access only — no rebinding (ES Modules forbid reassigning imported bindings).
export const state = {
    source: 'claude',
    currentRange: 'today',
    customFrom: '',
    customTo: '',
    isDark: true,
    chartMetric: 'tokens',
    chartGranularity: 'day',
    selectedDayDate: '',
    customRangeFlatpickr: null,
    mainChart: null,
    hourlyChart: null,
    rateChart: null,
    rateMethod: 'weighted',
    rateTokens: 'out',
    rateBucket: 5,
    rateSpan: null,
    rateLegendFocus: null,
    breakdownChart: null,
    breakdownLast: null,
    insightsModal: null,
    insightsData: null,
    insightsReqId: 0,
    sseRefreshTimer: null,
    sseReconnectTimer: null,
    sseBackoff: 1000,
    sseConnected: false,
    statusTimer: null,
    dailyDetailView: 'day',
    durationStatsRows: [],
    durationSort: { key: 'avg_duration_ms', dir: 'desc' },
    lastSeenTodayYMD: null,
};

export const paging = {
    daily:    { page: 1, pageSize: 20, total: 0 },
    sessions: { page: 1, pageSize: 20, total: 0 },
    requests: { page: 1, pageSize: 20, total: 0 },
};

export const SSE_BACKOFF_MAX = 30000;
