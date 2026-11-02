# Skill: cc-otel 版本发布 (release)

发布 cc-otel 新版本时必须完整走本清单。目标:**每次发布的内容可核对、文档同步、版本号有据、发布说明讲清楚做了什么/为什么/怎么实现**。

## 0. 发布机制(背景,不要改)

- 打 `vX.Y.Z` tag 并 push → `.github/workflows/release.yml`:goreleaser(`.goreleaser.yml`,ldflags 注入 `main.version`)构建全平台产物并创建 GitHub Release;随后 npm Trusted Publishing 发布 `@young1lin/dsh-cc-otel` 与平台二进制包(无 token)。
- 预发布 tag(如 `v1.2.3-rc.1`)发到 npm `next` dist-tag,不会覆盖 `latest`。
- CI 单测在 `.github/workflows/test.yml`(go test + race;前端 node 测试本地跑)。

## 1. 发布前检查(每次必做,缺一不可)

### 1.1 本次内容是否已包含

- `git status --short` 干净、`git log` 里能逐条对应本次要发布的功能/修复。
- `go test ./... -count=1`(可加 `CC_OTEL_TEST_NOSYNC=1`)全绿。
- 前端测试:`node --test internal/web/static/tests/*.test.mjs` 全绿。
- `go build ./...` 成功;dev 实例冒烟(推荐):`bin/cc-otel.exe` 起在 `:18899`,逐个点开本次改动涉及的面板/接口。

### 1.1b 本次包含 UI 改动时(强制)

- **先完整读 `.claude/harness/FRONTEND_TASTE.md`**(项目权威视觉规范)与 `.claude/CLAUDE.md` 的 Frontend Development Rules / Web UI Data and Display Rules。
- 三条图表铁律,改图表代码前后都要确认:`stack`(堆叠柱)禁止、`trigger: 'axis'` 禁止、`emphasis: { focus: 'series' }` 禁止;柱粒度最小是 时间 × 模型。
- Input 口径:Input = Uncached + Cache Read + Cache Create(三列都要),Output 不算 Input;表头/tooltip/KPI 三处定义一致。
- 验证标准:前端测试全绿 + 相关 Go 测试 + 重建重启 dev 实例(14317/18899)+ 带缓存参数打开页面;**无浏览器工具时至少用 API 核对 JSON 与显示逻辑,并明确说明未截图**。
- 改动过 CSS 或根模块要递增 `index.html` 的 `?v=` 版本号,并同步 `tests/intraday-markup.test.mjs` 里钉死的版本断言。

### 1.2 CHANGELOG 中英文

- `CHANGELOG.md` 与 `CHANGELOG_EN.md` **都要有本次条目且语义一致**。
- 把 `## [Unreleased]` 改成 `## [vX.Y.Z] - YYYY-MM-DD`;若没有 Unreleased 段则新建。
- 条目必须写清:**做了什么 / 为什么 / 怎么实现**(关键取舍、涉及文件、行为变化),不是一句话带过。

### 1.3 README 中英文

- 逐项核对:新增功能是否进了 `## 功能`/`## Features`;新增面板是否进了 `## Web UI → 面板`/`### Panels`;新增配置项或环境变量是否进了 `## 配置文件`/`## Configuration` 的表格与 `cc-otel.yaml.example`。
- `README.md`(中文)与 `README_en-US.md`(英文)同步修改,不要只改一份。
- 若 UI 截图已明显过时可提醒作者更新 `assets/images/`。

### 1.4 版本号(默认 patch)

| 情况 | 动作 |
|------|------|
| 只有 bug 修复 / 内部优化(无新能力) | **patch**(`0.2.0 → 0.2.1`,默认) |
| 新增向后兼容的功能(新面板 / 新 API / 新配置项) | **minor**(`0.1.x → 0.2.0`) |
| 破坏性变更(配置 schema / API 行为 / DB 结构不兼容) | **major**(`0.x → 1.0.0`),需在 CHANGELOG 写迁移指引 |

- 版本只存在于 git tag 与 CHANGELOG 标题(二进制由 goreleaser ldflags 注入),不要在源码里硬编码版本号。

### 1.5 价格种子刷新(release 时重跑)

- `go run ./tools/dump_pricing_snapshot` 从 BerriAI/litellm 重新生成 `internal/pricing/embed/seed.json`(离线兜底价格);生成后跑 `go test ./internal/pricing/`。Claude 条目按惯例**故意缺席**,不要手工补。网络不通时允许跳过并在发布说明里注明「seed 未刷新」,现有 seed 继续作为兜底。

### 1.6 隐私检查(公开仓库,推送前必做)

- `git diff <上一个tag>..HEAD` 扫描:个人路径(`C:\Users\...`)、密钥(`sk-`/`api[_-]?key`/`password`/`Bearer`)、真实用量数据(自己的 token 数 / 成本数字)。
- 确认 `.gitignore` 覆盖 `bin/`、`*.db`、`*.exe`、`*.bak`、`cc-otel.yaml`、`.env`;`git ls-files bin/` 必须为空。
- 提交信息里不得包含上述内容。

## 1.7 npm Trusted Publishing(易踩坑,v0.1.4 三次失败换来的)

发布链路:goreleaser(资产)→ npm job(`scripts/build-npm.mjs` 组 7 个包 → `npm publish`)。认证是 OIDC 信任发布,没有 NPM_TOKEN。三个必要条件缺一不可:

1. **npmjs 侧每包注册 Trusted Publisher**(一次性,只能账号本人在 npmjs.com 操作):包 Settings → Trusted publishing → GitHub Actions → 填 `young1lin` / `cc-otel` / `release.yml`,不勾 environment。**未注册时的报错是误导性的**:npm 拿 OIDC token 去 `/-/npm/v1/oidc/token/exchange/package/<name>` 换 token,失败会静默放弃,最终报 `ENEEDAUTH`(像没登录)或 `404 PUT`(像包不存在)——看到这两个错先查这里,不要去改 CI。
2. **CI 侧剥掉 setup-node 注入的 token**:setup-node 会往 runner `.npmrc` 写 `_authToken=${NODE_AUTH_TOKEN}`,有 token 在 npm 就不做 OIDC 交换 → 404。发布步骤里已内置 `unset NODE_AUTH_TOKEN` + `sed -i '/_authToken/d'`,别删。
3. **`npm publish --provenance`**:provenance 生成会取 OIDC token(信任发布搭同一趟车)。工作流已带,别删。

诊断顺序:Actions 里 npm job 挂 → 看错误码:404 PUT = 条件2被破坏;ENEEDAUTH = 交换失败(多半条件1);goreleaser 挂 asset 已存在 = 旧 release 未删,先 DELETE release 再重跑。重跑注意 rerun 用 tag 所指 commit 的工作流,改完 workflow 要 `git tag -f` + 删 release 重触发。

## 2. 发布步骤

```powershell
# 1) 1.x 全部通过后,提交 release 准备(CHANGELOG 定版 + README 更新)
git add CHANGELOG.md CHANGELOG_EN.md README.md README_en-US.md
git commit -m "chore(release): prepare vX.Y.Z"

# 2) 打 tag(版本号按 1.4 的表决定)
git tag vX.Y.Z

# 3) 推送(这会触发公开发布:goreleaser + npm)
git push origin main --tags

# 4) 观察直到完成
#    https://github.com/young1lin/cc-otel/actions  Release 工作流变绿
#    Release 页面出现 vX.Y.Z 附件;npm view @young1lin/dsh-cc-otel version
```

## 3. 发布后

- `npm view @young1lin/cc-otel-windows-x64@X.Y.Z` 等平台包版本确认。
- 本机升级全局实例(**铁律:agent 不得停全局 daemon,由用户执行**):
  ```powershell
  ~\.cc-otel\cc-otel.exe stop
  # 用 Release 下载或本地构建的二进制替换 ~\.cc-otel/cc-otel.exe
  ~\.cc-otel\cc-otel.exe start
  ```
- 如需用最新全局数据做集成验证:在线快照 `go run ./tools/snapshot_db ~/.cc-otel/cc-otel.db bin/cc-otel.db.snap`,停 bin daemon → 清 `bin/cc-otel.db*`(先确认绝对路径)→ 换文件 → 重启。

## 4. 发布说明质量标准(写给未来的人)

每条 CHANGELOG 至少回答:
1. **做了什么**:用户可见的变化。
2. **为什么**:动机 / 修的什么问题(可附复现要点)。
3. **怎么实现**:关键技术点(取舍、数据来源、涉及端点/表/文件)。
4. **升级注意**:是否需要迁移、旧数据是否兼容、新配置默认值。
