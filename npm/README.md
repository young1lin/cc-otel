# npm 分发（npm distribution）

English summary at the bottom.

## 包族

| 包 | 内容 | 说明 |
|----|------|------|
| `@young1lin/dsh-cc-otel` | `cordis.patch.yml` + `lib/index.js`（运行时插件）+ `bin/cc-otel.mjs`（CLI shim） | dsh 插件主包；`package.json` 里声明 `dsh.bundle.patch`，被 `dsh plugin --profile <name> add` 安装后自动登记为 profile 的 bundle 层 |
| `@young1lin/cc-otel-linux-x64` 等 6 个 | 平台二进制（放在包根目录，**不放 `bin/`**） | 主包的 optionalDependencies；npm/pnpm 按 `os`/`cpu` 只装一个；无 install 脚本，不触发 pnpm 构建脚本白名单 |

> 二进制必须放平台包**根目录**：cc-otel 的数据目录规则会把「位于名为 `bin` 的目录里的可执行文件」当成开发构建，把数据库写进那个目录（见 `internal/config/config.go` 的 `defaultDataDir`）。

平台矩阵（与 `.goreleaser.yml` 一致）：

| goos/goarch | npm 包 |
|-------------|--------|
| linux/amd64 | `cc-otel-linux-x64` |
| linux/arm64 | `cc-otel-linux-arm64` |
| darwin/amd64 | `cc-otel-darwin-x64` |
| darwin/arm64 | `cc-otel-darwin-arm64` |
| windows/amd64 | `cc-otel-win32-x64` |
| windows/arm64 | `cc-otel-win32-arm64` |

## 发布流程（打 tag 触发）

`v*` tag → `publish.yml` 两个 job：

1. `goreleaser`：照旧构建 6 平台归档 + checksums 上传 GitHub Release（**原有发布方式零改动**）。
2. `npm`：`gh release download` 拉取刚上传的归档 → `scripts/build-npm.mjs` 组装 `dist/npm`（版本号注入、二进制 `chmod 755`）→ 逐包 `npm publish --access public --provenance`（`--provenance` 触发取 OIDC token，Trusted Publishing 搭同一趟车）。预发布版本（如 `v1.2.3-rc.1`）以 `--tag next` 发布。

**认证：npm Trusted Publishing（OIDC），全程无 NPM_TOKEN。** GitHub Actions 的 OIDC 令牌直接向 npm 证明本 workflow 的身份（与 dsh-cc 的 publish.yml 同一套方案），provenance 由 npm 自动生成。一次性引导（每个包都要做，共 7 个）：

1. **新包名首次发布必须手工完成**——trusted publisher 无法在包存在之前配置。本地组装后带 OTP 发布：
   ```bash
   node scripts/build-npm.mjs --version <X.Y.Z> --artifacts dist/release --out dist/npm
   for d in dist/npm/*/; do (cd "$d" && npm publish --access public); done   # npm 会要求 OTP 验证码
   ```
2. 到 npmjs.com 逐个包 **Settings → Trusted publishing** 添加绑定：user `young1lin` / repo `cc-otel` / workflow `publish.yml`（**文件名必须与仓库里的完全一致**——GitHub OIDC claim 携带的是工作流文件名，不一致时 token 交换失败、npm 报 ENEEDAUTH）。

绑定完成后，此后所有 `vX.Y.Z` tag 直接由 `publish.yml` 的 `npm` job 免密发布。注意 Trusted Publishing 需要 **npm ≥ 11.5.1**（workflow 用 Node 24 并在版本不足时自动升级 npm）。

版本号规则：npm 版本 = git tag 去掉 `v` 前缀，与 goreleaser 版本一致；仓库内 `package.json` 永远写 `0.0.0` 占位，由组装脚本注入，避免发布后回改仓库。

## 本地验证（不发布）

```bash
# 1. 构建六平台二进制并伪造 goreleaser 归档布局
go build -ldflags "-s -w -X main.version=0.0.0-dev" -o dist/release/cc-otel_0.0.0-dev_linux_amd64/cc-otel ./cmd/cc-otel   # tar.gz 需自行打包
# 2. 组装
node scripts/build-npm.mjs --version 0.0.0-dev --artifacts dist/release --out dist/npm
# 3. 干跑发布，检查内容与体积
cd dist/npm/dsh-cc-otel && npm pack --dry-run
```

或者只验证 patch 合成（不装包）：

```bash
dsh --profile web --dump-config --patch npm/dsh-cc-otel/cordis.patch.yml | Select-String -Context 2 "dsh-cc-otel|session-telemetry-otel"
```

## 为什么这么设计（速记）

- **dsh plugin 就是 pnpm 转发器**（deepseek-harness `apps/cli/src/plugin.ts`）：`dsh plugin --profile web add X` = 在 `$DSH_HOME/profiles/web` 里 `pnpm add X`，然后把声明了 `dsh.bundle` 的依赖追加进 `dsh.profile.bundles`。因此「npm 可安装」是唯一硬需求，主包不需要进 dsh 的 monorepo。
- **patch 层按 id 整键覆盖**：bundle 层顺序 = `dsh.profile.bundles` 顺序，我们后于 dsh-base 追加，所以能覆盖 `session-telemetry-otel`；用户的 `$DSH_HOME/cordis.patch.yml` 与 `--patch` 覆盖层、`DSH_TELEMETRY_DISABLED` 开关都在我们之后生效——与既有手工接入文档完全兼容，互不干扰。
- **守护进程跑在 `~/.cc-otel`，不跑 node_modules**：Windows 不能替换正在运行的可执行文件，直接跑 node_modules 二进制会让 `dsh plugin update/remove` 失败；`cc-otel install` 版本对齐后，升级/卸载 npm 包不再持有运行中的二进制。`~/.cc-otel` 与 Claude Code 插件共享，是项目自己的规范安装位置。
- **optionalDependencies 按平台分发**：esbuild 同款方案，无生命周期脚本、离线友好、pnpm/npm/yarn 均按 `os`/`cpu` 跳过不匹配的包。

## English summary

`dsh plugin --profile <name> add @young1lin/dsh-cc-otel` installs the bundle from npm; six `@young1lin/cc-otel-<os>-<cpu>` packages carry the binaries via optionalDependencies (binary at the package root — never in bin/, per cc-otel's dev-mode data-dir rule). Tagging `vX.Y.Z` runs the unchanged goreleaser job plus an `npm` job that repackages those artifacts and publishes them (prereleases under the `next` dist-tag). Publishing authenticates via npm Trusted Publishing (GitHub Actions OIDC) — no NPM_TOKEN anywhere; each package is bootstrapped once by hand (first publish with OTP, then bind user young1lin / repo cc-otel / workflow release.yml in its npm Settings → Trusted publishing). The bundle patch overrides the `session-telemetry-otel` row by id; user patch layers and the `DSH_TELEMETRY_DISABLED` kill switch still apply after it.
