# clients-model-drilldown — 实施记录

- Date: 2026-11-11 · Spec: docs/internal/design/clients-model-drilldown.md（approved）
- Dev: bin/cc-otel-dev.yaml（14317/14318/18899），~25 万行真实数据

## Tasks（全部完成）
repository（ClientModelSummary + GetClientModelStats + TestGetClientModelStatsDrilldown）→ handler（ClientModels + /api/clients/models + TestClientModelsEndpoint）→ api.js loadClientModelsData → panel-clients.js（展开/收起/竞态保护/重绘保持展开）→ style.css 作用域样式 + v=37/v=81 bump + 三处过时文案修正 → panel-clients.test.mjs 4 例 → build + 重启 dev + curl 对账 + 浏览器验证。

## 偏差
无行为偏离。两处增量：① `detailTableHtml` 加 export 供测试直断言（列序/转义/百分比）；② 抽出 `shareBar()` 供父行与明细行共用同一百分比条标记。Spec 的"stale 响应不覆盖新展开"用例由 beginLatestRequest 既有代数机制 + collapse-再展开 fresh-fetch 计数断言覆盖，未单独构造延迟 resolve 场景。

## 实施期事故（值得记）
1. **Scan 列序错位（单测拦截）**：新 SQL SELECT 序 input, cache_read, cache_creation, output；初版 Scan 沿用父查询的 input, output, cache_read, cache_creation——TotalTokens 加法交换律碰巧正确，分列全错（cache hit 0.4 被算成 0.25 暴露）。教训：新查询的 Scan 永远对照**本条 SQL** 的 SELECT 序。
2. **浏览器"表空"假故障**：全范围 /api/clients 的 GROUP BY 在 dev 库要 ~2.2s，验证脚本只等 1.2-1.5s 就断言 rows=0；且脚本末尾 Browser.close 打断在途查询，服务端日志留下 `clients error: sqlite3: interrupted`。放宽等待后全部通过——脚本太急，不是功能缺陷（DP-12 变体：stale-script 结论无效，且关浏览器会打断在途查询制造假 500）。
3. gofmt 漂移（codex_handler_test.go 等 7 个文件）为存量问题，不在本 diff，未触碰。

## 证据
- `go test ./...` 全绿；web 套件 144/144（含新 4 例 + v=37/v=81 钉版本断言）。
- 对账（18899）：10 client × All Time + 7 天，Σtotal_tokens / Σrequest_count / Σcost_usd 与父行全等（14 组 MATCH）；claude-code top3 倒序 claude-opus-5 (6.9B) > opus-5-5 (6.06B) > glm-5.2 (5.27B)；缺 service_name → 400、非法 from → 400、未知 client → `{"data":[]}`。
- 浏览器（无头 Edge CDP）：All Time rows=10；展开 claude-code → aria=true、34 模型、首行 claude-opus-5、topShare 20.7%；收起归零；展开态切 7 Days → 保持展开、模型 34→2（glm-5.3 居首）；chevron 可聚焦、Enter 激活同 click；主题 light↔dark 正常。截图 bin/tmp/clients-drill-{dark,7day,light}-expanded.png。

## Follow-ups（超出范围，留档）
1. 全范围 /api/clients（及新端点）在 dev 库 ~2.2s，与 repository 注释"几十毫秒"不符：GetClientStats 的 COALESCE 折叠使 idx_requests_service_time 不可 sargable；可评估默认 client 走 sargable 分支或 daily_model_agg 预聚合（需 retention 语义评估）。
2. SSE 刷新重绘后展开行会闪一次 "Loading models…"（重取是特性，闪烁是观感）——如在意可先渲染旧数据再后台刷新。
