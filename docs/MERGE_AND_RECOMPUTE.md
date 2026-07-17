## 数据库合并 + 成本清洗（标准流程）

[English version below](#english-version)

适用：把另一台机器或全局的 `~/.claude/cc-otel/cc-otel.db` 数据合到 `bin/cc-otel.db`，再用本地价目表把非 Claude 模型的 `cost_usd` 重新算一遍（修正反代上报错价、Codex 没上报、新模型未入价目表等问题）。

### 工具盘点

| 工具 | 作用 |
|---|---|
| `tools/merge_bin_global/run_merge` | 一键编排 9 步合并：backup → stop → snapshot → export jsonl → import → repair daily_agg → verify → replace bin |
| `tools/merge_bin_global/export_bin` | 把 bin/local.db 在指定时间窗口内的行导出为 JSONL |
| `tools/merge_bin_global/import_global` | 通过共享合并引擎把 JSONL 行按自然键合入 global 副本 |
| `tools/merge_bin_global/repair_daily_agg` | 重建 `daily_model_agg` / `codex_daily_model_agg` |
| `tools/merge_bin_global/verify_merge` | 用共享自然键检查全部导入表的包含性，并输出行数 / 成本诊断 |
| `tools/recompute_cost` | 按本地 pricing registry 重算非 Claude 行的 `cost_usd`，rebuild 聚合表。dry-run 默认 |
| `bin/cc-otel.yaml` `pricing:` 块 | 用户覆盖层，最高优先级，立即生效 |

### 共享合并引擎

Web 数据库导入和 `tools/merge_bin_global` 都使用 `internal/dbmerge` 维护受支持 schema、自然键、聚合增量、ledger 写入和包含性验证。ledger 命中本身永远不会导致跳过明细行。

### 默认假设

- **方向**：global (`~/.claude/cc-otel`) → 合并进 bin (`./bin`)。`run_merge` 写死了这个方向；要反向得改参数或自己拼。
- **时间窗口**：默认 1970-01-01 到 now，即全量。
- **副作用**：
  - 自动备份 bin db 到 `bin/backup-merge-bin-global-<时间戳>/`
  - 通过 `bin/cc-otel.pid` taskkill bin daemon（**会拒绝杀非 bin 路径下的进程**）
  - 最终把合并结果重命名替换 `bin/cc-otel.db`

### 标准流程

#### 1. dry-run 看计划

```bash
go run ./tools/merge_bin_global/run_merge
```

不带 `-yes` 只打印 9 步操作计划。先看看路径对不对（bin-dir / global-dir / 备份路径）。

#### 2. 执行合并

```bash
go run ./tools/merge_bin_global/run_merge -yes
```

加 `-yes` 才真正动手。9 步走完后：
- bin/cc-otel.db = 合并后的库（global + 本地）
- bin/backup-merge-bin-global-<stamp>/ = 合并前的 bin db 副本，回滚用
- bin/local.db、bin/global.db = 中间快照（合并完可选删）
- bin/merge-bin-global-<stamp>.jsonl = 导出的本地行，可选删

可选参数：
- `-from <RFC3339>` / `-to <RFC3339>`：限定导出窗口（默认全量）
- `-bin-dir` / `-global-dir`：覆盖默认路径（用于异机或非默认安装位置）
- `-repair-from-date YYYY-MM-DD` / `-repair-to-date`：限定 daily_agg 重建范围（默认从合并后数据自动推）
- `-timeout 2m`：等 bin 进程退出的超时

#### 3. 清洗成本（dry-run）

```bash
go run ./tools/recompute_cost \
  --db bin/cc-otel.db \
  --config bin/cc-otel.yaml \
  --table both
```

输出按模型聚合的 old/new/delta，先看着合理再 `--apply`。**`--config` 必须传**，否则 YAML 里的用户覆盖（如 `glm-5.1`、`deepseek-v4-pro` 这些 LiteLLM 没收录的模型）不会生效。

#### 4. 执行清洗

```bash
go run ./tools/recompute_cost \
  --db bin/cc-otel.db \
  --config bin/cc-otel.yaml \
  --table both \
  --apply
```

会自动 `VACUUM INTO` 一份 `bin/cc-otel.db.recompute-<stamp>.bak` 备份（`--backup=false` 关掉），然后逐行 UPDATE 并 DELETE+INSERT 重建聚合表。

#### 5. 启动 daemon 验证

```bash
./bin/cc-otel.exe start
curl -s "http://localhost:18899/api/dashboard?range=all"
curl -s "http://localhost:18899/api/codex/dashboard?range=all"
```

各模型实际成本对照（用现成的 `bin/tmp/verify.go`）：

```bash
go run bin/tmp/verify.go
```

### 关键细节 / 容易踩的坑

1. **必须传 `--config bin/cc-otel.yaml`**：YAML 的 `pricing:` 块是用户覆盖层，`recompute_cost` 不传就只用 seed.json + model_pricing 表，新加的模型（不在 LiteLLM 的 GLM-5/Xiaomi MiMo 等）会被当成未知模型保留原（错的）成本。
2. **Claude 行永远不动**：`pricing.IsClaudeModel` 的 `claude-` 前缀（大小写无关、去空白）会跳过，无论你怎么跑都不会改 Claude 行的 cost_usd。
3. **`run_merge` 不会做 recompute**：合并完默认还是上游报的 cost_usd（GLM 反代错价依然在）。所以"合并 + 清洗"是两步，不能省。
4. **PID 校验**：`run_merge` 会读 `bin/cc-otel.pid`，然后用 PowerShell 查那个 PID 的 ExecutablePath，**只有匹配 `bin/cc-otel.exe` 才会 taskkill**。如果你的 daemon 是别的路径起的，它会报错而不是误杀。
5. **WAL 一致性**：`snapshotDB` 用 `VACUUM INTO`（SQLite 在线 backup 等价物），不直接拷贝 .db / .db-wal / .db-shm，避免热文件不一致。
6. **重复行按自然键去重，ledger 只记账不拦路**：`import_global` 对每行先查目标库是否已有同一逻辑行（`api_requests` 优先 `request_id`；codex 用时间戳+会话+token 等自然键，**刻意排除 `cost_usd`**，因为清洗会改写它），不存在才插入。`import_ledger` 命中**不会**单独跳过行——历史上 ledger 有残留（行被 prune 后 ledger 还在）会导致丢数据，这个坑已修。两库同一 `request_id` 的行以目标库现有的为准。
6b. **verify 的行数差 ≠ 丢数据**：`verify_merge` 使用 `internal/dbmerge` 对全部 14 张导入表做自然键包含性检查；`codex_events` 作为兼容表被识别但忽略。源库自身的重复行被合并去重时，原始行数可以不同。cost 总和短缺只 WARN（清洗不对称），共享身份验证发现缺行才 FAIL。
7. **Codex 缓存计费**：清洗时 `recompute_cost` 自动从 `input_tokens` 减去 `cache_read_tokens` 再传给 pricer（OpenAI 的 `input_token_count` 包含缓存，与 Anthropic 约定不同），不需要手动调整。
8. **价目表覆盖优先级**：`bin/cc-otel.yaml` `pricing:` > `model_pricing` 表 > seed.json fallback。要改某个模型价，改 YAML 然后 `recompute_cost --apply` 一次即可。

### 回滚

```bash
# 1) 回滚 recompute（清洗前的 db）
cp bin/cc-otel.db.recompute-<stamp>.bak bin/cc-otel.db

# 2) 回滚整个 merge（合并前的 db）
cp bin/backup-merge-bin-global-<stamp>/cc-otel.db bin/cc-otel.db
cp bin/backup-merge-bin-global-<stamp>/cc-otel.db-wal bin/  # 如果有
cp bin/backup-merge-bin-global-<stamp>/cc-otel.db-shm bin/  # 如果有
```

### 全部串起来的 one-liner（复制即用）

```bash
./bin/cc-otel.exe stop
go run ./tools/merge_bin_global/run_merge -yes && \
go run ./tools/recompute_cost \
  --db bin/cc-otel.db \
  --config bin/cc-otel.yaml \
  --table both --apply && \
./bin/cc-otel.exe start && \
go run bin/tmp/verify.go
```

合并 + 清洗 + 重启 + 输出 per-model 校验，全程 ~10 秒。

---

## English version

Workflow for merging another `cc-otel.db` (typically global at `~/.claude/cc-otel/`) into the local `bin/cc-otel.db`, then recomputing non-Claude `cost_usd` from the local pricing table.

### Tools

| Tool | Role |
|---|---|
| `tools/merge_bin_global/run_merge` | 9-step pipeline: backup → stop → snapshot → export jsonl → import → repair agg → verify → replace bin |
| `tools/recompute_cost` | Recompute non-Claude `cost_usd` from registry, rebuild aggregates. Dry-run by default |
| `bin/cc-otel.yaml` `pricing:` | User override layer; highest priority |

### Standard sequence

```bash
# 1. dry-run merge plan
go run ./tools/merge_bin_global/run_merge

# 2. execute merge (global → bin)
go run ./tools/merge_bin_global/run_merge -yes

# 3. dry-run recompute (must pass --config to load YAML overrides)
go run ./tools/recompute_cost --db bin/cc-otel.db --config bin/cc-otel.yaml --table both

# 4. apply recompute
go run ./tools/recompute_cost --db bin/cc-otel.db --config bin/cc-otel.yaml --table both --apply

# 5. start and verify
./bin/cc-otel.exe start
curl -s "http://localhost:18899/api/dashboard?range=all"
go run bin/tmp/verify.go
```

### Gotchas

1. `--config bin/cc-otel.yaml` is **mandatory** for recompute — YAML `pricing:` overrides only kick in when the config is loaded.
2. `claude-*` rows are never recomputed (load-bearing rule).
3. `run_merge` does not recompute. Recompute is a separate step.
4. PID safety: `run_merge` refuses to kill a process whose ExecutablePath does not point at `bin/cc-otel.exe`.
5. Snapshots use `VACUUM INTO`, not raw file copy, so WAL consistency is preserved.
6. Duplicate rows are deduped by natural key (`request_id` for api_requests; timestamp+session+tokens for codex, deliberately excluding `cost_usd` which recompute rewrites). A ledger hit alone never skips a row — stale ledger entries used to drop data; fixed. Web import and the CLI share `internal/dbmerge`; `verify_merge` checks natural-identity containment across all 14 imported tables. `codex_events` is recognized as a compatibility table but ignored. Raw count differences and cost-sum shortfalls are diagnostics, while a genuinely missing identity fails verification.
7. Codex cache subtraction (`input_tokens − cache_read_tokens`) is applied automatically inside `recompute_cost` for `codex_api_requests`.

### Rollback

```bash
cp bin/cc-otel.db.recompute-<stamp>.bak bin/cc-otel.db                                     # undo recompute
cp bin/backup-merge-bin-global-<stamp>/cc-otel.db bin/cc-otel.db                           # undo merge
```
