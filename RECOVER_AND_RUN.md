# CC-OTEL 本地复原与验证（按本次修复后的步骤）

这份文档的目标是：你把整个目录拷回家里电脑后，**按步骤做就能 100% 复原**我这次做过的事情，并用指定端口与 `bin/` 下的 DB 运行。

> 约定：本文所有命令均为 **Windows PowerShell**（不使用 `&&`）。

---

## 0. 固定要求（和你一致）

- **不管理 remote**：本机只做 `git init`，不 `git remote add`。
- **`bin/` 必须保留**：`bin/cc-otel.exe`、`bin/cc-otel.db`、`bin/cc-otel.yaml` 等都保留。
- **端口“加 1”**（避免冲突）：本文使用
  - Web：`8899`
  - OTLP gRPC：`4317`
- **DB 使用 `bin/` 下的 DB**：`d:\goProject\cc-otel\bin\cc-otel.db`

---

## 1. 从 GitHub 获取最新源码（不需要 remote）

1) 下载 zip：

```powershell
$zip = Join-Path $env:TEMP "cc-otel-main.zip"
Invoke-WebRequest -Uri "https://github.com/young1lin/cc-otel/archive/refs/heads/main.zip" -OutFile $zip
```

2) 解压到临时目录：

```powershell
$extract = Join-Path $env:TEMP "cc-otel-upstream"
Remove-Item $extract -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Path $extract | Out-Null
Expand-Archive -Path $zip -DestinationPath $extract -Force
```

3) **备份本地 `bin/`**（非常重要）：

```powershell
$backup = Join-Path $env:TEMP ("cc-otel-bin-backup-" + (Get-Date -Format "yyyyMMdd-HHmmss"))
New-Item -ItemType Directory -Path $backup | Out-Null
Copy-Item -Path "d:\goProject\cc-otel\bin" -Destination $backup -Recurse -Force
$backup
```

4) 用上游覆盖工作区（除了 `bin/`）：

```powershell
$root = "d:\goProject\cc-otel"
$src  = Join-Path $extract "cc-otel-main"

# 删除除 bin 外所有内容
Get-ChildItem -LiteralPath $root -Force |
  Where-Object { $_.Name -ne "bin" } |
  ForEach-Object {
    if ($_.PSIsContainer) { Remove-Item -LiteralPath $_.FullName -Recurse -Force }
    else { Remove-Item -LiteralPath $_.FullName -Force }
  }

# 拷贝上游源码进工作区
Copy-Item -Path (Join-Path $src "*") -Destination $root -Recurse -Force
```

5) 清理旧 `.git` + 重新初始化（不配置 remote）：

```powershell
Set-Location "d:\goProject\cc-otel"

if (Test-Path ".git") {
  Remove-Item ".git" -Recurse -Force -ErrorAction SilentlyContinue
}

git init
git remote -v   # 应该为空
```

> 说明：如果 `.git\cursor\...` 因被占用导致删不掉，可以退而求其次：清空 `.git` 目录内除 `cursor` 外的所有内容，然后 `git init`。核心目标是：**无 remote + 重新初始化**。

---

## 2. 启动（DB/端口都指向 `bin/`）

> 这一步会让 Web UI 从源码目录加载（不用重新编译），DB 仍然用 `bin/` 下的 DB。

```powershell
$env:CC_OTEL_DB_PATH    = "d:\goProject\cc-otel\bin\cc-otel.db"
$env:CC_OTEL_WEB_PORT   = "8899"
$env:CC_OTEL_OTEL_PORT  = "4317"
$env:CC_OTEL_STATIC_DIR = "d:\goProject\cc-otel\internal\web\static"

Start-Process -FilePath "d:\goProject\cc-otel\bin\cc-otel.exe" -ArgumentList "serve" -WorkingDirectory "d:\goProject\cc-otel\bin"
```

验证服务端口：

```powershell
Invoke-WebRequest -UseBasicParsing -Uri "http://localhost:8899/api/status" -TimeoutSec 3 | Select-Object -ExpandProperty Content
```

打开页面：

- `http://localhost:8899/`

---

## 3. 本次修复点：代码改动（你要“具体代码写进去”）

下面这些片段来自当前工作区文件，属于“你要的可复原关键改动”。

### 3.1 解决“日期/星期错位”：不要用 `toISOString()` 生成日字符串

文件：`internal/web/static/app.js`

- **Today 下拉**改为本地 `toYMD()`：

```js
function buildDayDropdown() {
  const now = new Date();
  const fmt = d => toYMD(d);
  // ...
}
```

- **范围计算**改为本地 `toYMD()`：

```js
function rangeToFromTo(range) {
  if (range === 'custom' || range === 'single-day') return { from: customFrom, to: customTo };
  const now = new Date();
  const fmt = d => toYMD(d);
  const today = fmt(now);
  switch (range) {
    case 'week':  { const f = new Date(now); f.setDate(f.getDate() - 6);  return { from: fmt(f), to: today }; }
    case 'month': { const f = new Date(now); f.setDate(f.getDate() - 29); return { from: fmt(f), to: today }; }
    case 'all':   return { from: '1970-01-01', to: today };
    default:      return { from: today, to: today };
  }
}
```

### 3.2 跨天自动刷新（第二天不手动刷新也能更新 Today）

文件：`internal/web/static/app.js`

```js
let lastSeenTodayYMD = null;
function startDayRolloverWatcher() {
  lastSeenTodayYMD = toYMD(new Date());
  setInterval(() => {
    const nowYMD = toYMD(new Date());
    if (nowYMD === lastSeenTodayYMD) return;
    lastSeenTodayYMD = nowYMD;

    try { customRangeFlatpickr?.set?.('maxDate', 'today'); } catch {}
    try { buildDayDropdown(); } catch {}

    const isTodayView =
      currentRange === 'today' ||
      (currentRange === 'single-day' && !selectedDayDate);
    if (isTodayView) {
      resetPages();
      loadAll();
    }
  }, 45 * 1000);
}
```

启动时调用：

```js
startDayRolloverWatcher();
```

### 3.3 过去 7 天 / 区间的 Avg/day + Insights（active days 口径 + 详情弹窗）

文件：`internal/web/static/app.js`

- 当 `from != to` 时，展示 Avg/day 摘要条，并支持打开 Insights 详情弹窗：
  - **Avg/day**：总 / active days（默认只展示这一行摘要）
  - **Details 弹窗**：支持按 `Tokens/Cost/Reqs` 维度切换、按模型筛选，并查看 **每天排名(rank)** 与 **占比(share)** 等详情

#### 3.3.1 核心逻辑（节选）：多日范围才显示 Avg/day

```js
if (from && to && from !== to) {
  loadInsights(from, to);
} else {
  setInsightsVisible(false);
}
```

```js
const res = await fetch(`/api/daily?from=${from}&to=${to}&page=1&page_size=2000&granularity=day`);
const rows = (json.data || json) || [];

const byDate = new Map();   // active days
const byModel = new Map();  // model aggregates

for (const r of rows) {
  byDate.set(r.date, true);
  // tokensTotal = input_side + output
}

const activeDays = byDate.size || 1;
// Avg = total / activeDays
```

#### 3.3.2 Avg/day Bar：默认只显示“日均 + active days”

页面（KPI 下方）默认只展示一行摘要：

- `Avg/day tokens 63.0M · active days 5`

对应（运行时插入）的 bar 结构：

```js
bar.innerHTML = `
  <button type="button" class="insights-main" id="insights-toggle">
    <span class="insights-k">Avg/day</span>
    <span class="insights-v mono" id="ins-avg-summary">—</span>
    <span class="insights-tail mono" id="ins-days">—</span>
  </button>
  <button type="button" class="insights-details-btn" id="insights-details-btn">Details</button>
`;
```

#### 3.3.3 Insights 详情弹窗（Modal）：占比/每天排名/按模型筛选

点击 `Details`（或点击 Avg/day 区域）会打开弹窗。弹窗使用 `.modal-backdrop` + `.modal`，并复用现有的 `openPopover/closePopover`（和 Status/Cost modal 同一套交互：遮罩/ESC/× 关闭）。

打开弹窗：

```js
function openInsightsModal() {
  ensureInsightsModal();
  openPopover(insightsModal, null); // centered
  if (insightsData) renderInsightsDetails();
}
```

弹窗里提供两个下拉：

- `Tokens | Cost | Reqs`（metric 维度）
- `All models | <model>`（按模型筛选）

并展示两张表：

- **Daily ranking**：每天 Top1、所选模型 rank/value/share
- **Model share (Top 10)**：区间内 Top10 模型占比

关键数据结构（前端聚合后缓存到 `insightsData`，供筛选/渲染复用）：

```js
insightsData = {
  activeDates: [...byDate.keys()].sort(),
  totals: { tokens: totalTokens, cost: totalCost, reqs: totalReqs },
  byModel,      // model => { tokens, cost, reqs }
  byDayModel,   // date => Map(model => { tokens, cost, reqs })
  dayTotals,    // date => { tokens, cost, reqs }
  models,
  dates,
};
```

#### 3.3.4 文案歧义修正：Top model 是“区间总量”不是日均

弹窗的 Top model 卡片明确标注为：

- `Top model (range total, current metric)`
- `total=... · share=...`

### 3.4 关键：你提到的“日期选择器显示 15 号像周六”——其实是 CSS 让网格和表头错位

根因：曾经把 `.flatpickr-day` 固定成 `max-width: 32px`，导致日期格子缩在左边，和 `Sun/Mon/.../Sat` 表头不对齐，于是视觉上“15 号在周六列”。  

修复：把日期格子与表头都改成 **7 等分宽度**（自适应容器宽度）。

文件：`internal/web/static/style.css`

```css
.flatpickr-day {
  height: 32px !important;
  width: calc(100% / 7) !important;
  max-width: calc(100% / 7) !important;
  flex-basis: calc(100% / 7) !important;
}

span.flatpickr-weekday {
  width: calc(100% / 7) !important;
  max-width: calc(100% / 7) !important;
  flex-basis: calc(100% / 7) !important;
}
```

---

## 4. 你问：这个“百分比”是不是错误？是不是应该自适应？

- **不是错误**：日历是 7 列，固定 `1/7` 的宽度就是正确做法。
- **而且是自适应**：`calc(100% / 7)` 会随着弹窗宽度变化自动缩放，比写死 `32px` 更自适应。
- 真正的问题就是：之前写死 `32px` 破坏了网格对齐，才出现“日期落在错误星期列”的视觉错位。

---

## 5. 常见坑：浏览器缓存导致你看不到最新修复

如果你在家里复原后仍看到旧样式/旧脚本：\n
- 用 **Ctrl+F5** 强刷\n
- 或者临时给 `style.css`/`app.js` 加 querystring（cache bust）。\n

---

## 6. 数字格式：支持 B（十亿）

当输入超过十亿（1e9）时，UI 会显示 `B` 单位，例如：`1.33B`。

文件：`internal/web/static/app.js`

```js
function fmtNum(n) {
  if (n == null || isNaN(n)) return '0';
  if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(n);
}
```

