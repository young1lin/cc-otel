# clients-model-drilldown — Clients 面板点击展开模型明细

- Date: 2026-11-11 · Status: **approved & implemented**（实施记录 docs/internal/impl/clients-model-drilldown.md）

## Problem
Clients 面板每 client（service.name）一行汇总。需点击展开该 client 下**每个模型**的 token 明细（含占该 client 的百分比、cost），**按总 token 倒序**，再点收起；展开时按**当前范围选择器**懒加载最新数据（All Time 即全部）。

## 决策（A：懒加载专用端点 + 行内展开）
新增 `GET /api/clients/models?service_name=&from=&to=`，点击时 fetch，行下插入明细行。弃选 B（主响应内嵌 models[]——预取语义违背"点击取最新"，且把不常查看的明细摊到每次 SSE 刷新）与 C（弹窗——遮挡父表对比上下文）。

## 关键事实（实施后仍承重）
- **数据源与父查询同族**：`claude_pool_requests` 视图（db.go:438-448，= SELECT * FROM api_requests 全池含 codex，每次 boot DROP+CREATE）上 GROUP BY `COALESCE(NULLIF(model,''),'(unknown)')`，ORDER BY 总 token DESC, model ASC（稳定次序）；cost int64→costToFloat64；CacheHitRate = cache_read / input 侧（GetClientStats 同款）。
- **token 语义（pinned）**：Total = input + cache_read + cache_creation + output；Input 侧 = 前三者和。
- **service_name 匹配**：空串/NULL 折叠到 DefaultServiceName，与父查询一致；handler 缺 service_name → 400，`rejectInvalidDateParams` + from/to 解析镜像 Clients；空数据返回 `[]` 不返回 null；Go ServeMux 精确路径，与 /api/clients 注册顺序无关。
- **前端竞态**：`beginLatestRequest('clients-models:'+name)` 代数保护；fetch 只允许出现在 api.js（loadClientModelsData）。
- **明细列与父表同定义**（FRONTEND_TASTE）：Model | Requests | Input (Uncached) | Cache Read | Cache Create | Output | Total | Share | Cache Hit | Cost；Share = 模型 total / 本次响应 Σtotal（恒 100%），复用父表百分比条；`escapeHtml(model)` 防注入。
- **展开态保持**：模块级 Set 记录已展开 service_name；SSE/切范围重绘 tbody 后自动按新范围重取并保持展开；**每次展开都重新 fetch**（用户要求）。
- a11y：chevron `<button aria-expanded>` + 90° 旋转（120-180ms），键盘 Enter 可用。
- :memory: 库单连接约束：单条顺序查询天然满足。
- 顺带修三处"视图排除 codex"过时文案（index.html 面板提示、panel-clients.js 头注释、handler.go 注释）——与 HS-08 统一视图矛盾。
- 版本 bump：`style.css?v=37`、`app.js?v=81`（intraday-markup 钉版本断言同步）。

## 验证（实测通过，证据见实施记录）
Σtokens/Σrequests/Σcost 明细 == 父行（10 client × All Time/7 天全 MATCH）；浏览器展开/收起/范围跟随/键盘/双主题截图；`go test ./...` 与 web 套件全绿。

## Rollback
纯增量（无 schema/ingestion/pricing 改动）：revert commit 即可；前端缓存由 ?v= 失效，无兼容层负担。
