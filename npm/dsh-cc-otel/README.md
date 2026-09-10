# @young1lin/dsh-cc-otel

[中文](#中文) | [English](#english)

## 中文

把 [cc-otel](https://github.com/young1lin/cc-otel)（本地 OTLP 接收器 + Token 用量仪表盘）接进 [DeepSeek Harness (dsh)](https://github.com/deepseek-ai/deepseek-harness)。

### 安装（任意 profile 通用）

```bash
dsh plugin --profile web add @young1lin/dsh-cc-otel
```

`dsh plugin` 是 pnpm 转发器：它在 profile 目录里执行安装，然后把这个包登记为 profile 的一个 bundle 层。本包做了三件事：

1. **按平台分发二进制**——主包通过 optionalDependencies 声明 6 个平台子包（`@young1lin/cc-otel-{linux,darwin,win32}-{x64,arm64}`），npm/pnpm 只安装与当前 os/cpu 匹配的那一个，没有 install 脚本、不触发 pnpm 的构建脚本白名单。
2. **遥测指向本地**——bundle 的 cordis.patch.yml 按 id 覆盖 dsh-base 的 `session-telemetry-otel` 行，把会话遥测导出到 `http://localhost:4318/v1/logs`（mode 默认 `FEEDBACK_ONLY`；dsh 0.1.5 起 `FULL` 已被移除，设了会在启动时校验失败）。
3. **守护进程保活**——profile 启动时加载本包的运行时插件：解析平台二进制 → 版本对齐 `~/.cc-otel`（必要时执行 `cc-otel install`，与 Claude Code 插件共享同一份安装）→ 幂等拉起守护进程（`cc-otel start`）。

### 环境变量与配置

| 变量 | 作用 |
|------|------|
| `DSH_TELEMETRY_MODE` | `FEEDBACK_ONLY`（默认）/ `DISABLED`（dsh 0.1.5 起不再接受 `FULL`） |
| `DSH_TELEMETRY_OTLP_URL` | 覆盖导出端点（默认 `http://localhost:4318/v1/logs`） |
| `DSH_TELEMETRY_DISABLED` | 非空即一票否决（launcher 层，最后生效） |
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
```

> **隐私与上报时机**：`FEEDBACK_ONLY`（默认）下普通对话**零上报**，只在显式反馈（点赞 / 点踩 / `/feedback`）时把该会话的完整前缀上传一次（cc-otel 只保存 token/费用，不落库正文，流量不出 localhost）。要持续统计，用 cc-otel 仓库的 `tools/dsh_ingest` 直接从本地会话日志补录——数据不出本机。设 `DSH_TELEMETRY_MODE=DISABLED` 可彻底关闭。

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

The bundle ships per-platform binaries through optionalDependencies (`@young1lin/cc-otel-{linux,darwin,win32}-{x64,arm64}` — exactly one gets installed), repoints the harness `session-telemetry-otel` row at `http://localhost:4318/v1/logs` (`mode: FEEDBACK_ONLY` by default — dsh 0.1.5 removed `FULL` and rejects it at startup, so ordinary conversation uploads nothing and only an explicit feedback action exports once; `DSH_TELEMETRY_MODE` / `DSH_TELEMETRY_OTLP_URL` / `DSH_TELEMETRY_DISABLED` keep working), and keeps the shared `~/.cc-otel` daemon running via a tiny runtime plugin (version-gated `cc-otel install` + idempotent `start`). Set `CC_OTEL_BIN` to use a custom binary; override the plugin row by id in your profile patch to change `autoStart` / `manageHomeBinary` / `stopOnUnload`.

License: MIT.
