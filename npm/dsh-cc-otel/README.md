# @young1lin/dsh-cc-otel

[中文](#中文) | [English](#english)

## 中文

把 [cc-otel](https://github.com/young1lin/cc-otel)（本地 OTLP 接收器 + Token 用量仪表盘）接进 [DeepSeek Harness (dsh)](https://github.com/deepseek-ai/deepseek-harness)。

### 安装（任意 profile 通用）

```bash
dsh plugin --profile web add @young1lin/dsh-cc-otel
```

`dsh plugin` 是 pnpm 转发器：它在 profile 目录里执行安装，然后把这个包登记为 profile 的一个 bundle 层。本包做了四件事：

1. **按平台分发二进制**——主包通过 optionalDependencies 声明 6 个平台子包（`@young1lin/cc-otel-{linux,darwin,win32}-{x64,arm64}`），npm/pnpm 只安装与当前 os/cpu 匹配的那一个，没有 install 脚本、不触发 pnpm 的构建脚本白名单。
2. **遥测 adapter（持续上报）**——运行时插件挂一个透明的 `llm/stream` 监听：每次 provider 调用 settle 后，把用量（in/out/cacheRead/cacheWrite）、时长、**首字延迟**、会话、模型组一条 OTLP `api_request` 记录推给本地 cc-otel（`http://localhost:4318/v1/logs`）。实时、逐次、**不含任何提示词/工具输出**；失败即丢、绝不影响模型调用。dsh 0.1.5 移除了 `FULL` 模式后，这就是持续统计的来源。
3. **关闭原生遥测防双计**——bundle 的 cordis.patch.yml 把 dsh-base 的 `session-telemetry-otel` 行钉在 `DISABLED`：原生 FEEDBACK_ONLY 导出（反馈时上传整段会话前缀）会和 adapter 的行重复计数。
4. **守护进程保活**——profile 启动时加载本包的运行时插件：解析平台二进制 → 版本对齐 `~/.cc-otel`（必要时执行 `cc-otel install`，与 Claude Code 插件共享同一份安装）→ 幂等拉起守护进程（`cc-otel start`）。

### 环境变量与配置

| 变量 | 作用 |
|------|------|
| `CC_OTEL_OTLP_URL` | adapter 的上报端点（默认 `http://localhost:4318/v1/logs`，可指向 dev 实例 `14318`） |
| `DSH_TELEMETRY_MODE` | 原生导出器：`DISABLED`（默认，本包钉死）/ `FEEDBACK_ONLY`（想用原生导出时开，同时把插件行 `telemetry.enabled: false` 关掉 adapter，避免双计） |
| `DSH_TELEMETRY_OTLP_URL` | 原生导出器的端点（仅原生路径生效） |
| `DSH_TELEMETRY_DISABLED` | 非空即一票否决原生遥测（launcher 层，最后生效；不影响 adapter） |
| `CC_OTEL_BIN` | 显式指定 cc-otel 可执行文件（开发调试用） |
| `CC_OTEL_HOME` | 覆盖用户主目录（仅测试用，影响 `~/.cc-otel` 的定位） |

在 profile 的 cordis.patch.yml（`$DSH_HOME/profiles/<name>/`）里按 id 覆盖插件行为：

```yaml
- id: dsh-cc-otel
  name: '@young1lin/dsh-cc-otel'
  config:
    autoStart: false        # 不自动拉起守护进程（默认 true）
    manageHomeBinary: false # 不同步 ~/.cc-otel，直接跑 node_modules 里的二进制
    stopOnUnload: true      # dsh 退出时停掉守护进程（默认 false，多客户端共享）
    telemetry:              # 遥测 adapter（默认开）
      enabled: true
      url: http://localhost:4318/v1/logs
```

> **隐私**：adapter 每次调用只上报用量与计时数字——**没有提示词、没有消息内容、没有工具输出**，流量不出 localhost。原生 `FEEDBACK_ONLY` 路径（默认关闭）在显式反馈时会上传该会话的完整前缀（cc-otel 仍只保存 token/费用、不落库正文）。

### 更新 / 卸载

```bash
dsh plugin --profile web update @young1lin/dsh-cc-otel
dsh plugin --profile web remove @young1lin/dsh-cc-otel
```

## English

Wires [cc-otel](https://github.com/young1lin/cc-otel) (self-hosted OTLP receiver + token dashboard) into [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness).

```bash
dsh plugin --profile web add @young1lin/dsh-cc-otel
```

The bundle ships per-platform binaries through optionalDependencies (`@young1lin/cc-otel-{linux,darwin,win32}-{x64,arm64}` — exactly one gets installed), keeps the shared `~/.cc-otel` daemon running via a tiny runtime plugin (version-gated `cc-otel install` + idempotent `start`), and — since dsh 0.1.5 removed the FULL mode, leaving the native exporter silent for ordinary conversation — reports every provider call itself: a transparent `llm/stream` listener pushes one OTLP `api_request` record per settled attempt (tokens, duration, TTFT; never prompts or tool output) to `http://localhost:4318/v1/logs`, real-time and fail-soft. The native `session-telemetry-otel` row is pinned to `DISABLED` by the bundle patch so its feedback-time full-prefix export cannot double-count beside the adapter (`DSH_TELEMETRY_MODE=FEEDBACK_ONLY` + `telemetry.enabled: false` restores the native path). Set `CC_OTEL_BIN` for a custom binary and `CC_OTEL_OTLP_URL` to redirect the adapter (e.g. a dev receiver on `14318`); override the plugin row by id in your profile patch to change `autoStart` / `manageHomeBinary` / `stopOnUnload` / `telemetry`.

License: MIT.
