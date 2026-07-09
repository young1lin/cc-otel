# 铁律：复制数据库不得停止运行中的进程

**Iron rule: copying the database must never require stopping a running cc-otel process.**

## Rule

- 复制 / 备份 / 快照 SQLite 数据库（`cc-otel.db`）时，**禁止**为了复制而 `stop` 进程。
- 尤其是**全局实例** `~/.claude/cc-otel/`（生产 daemon）**永远不停**。数据库是 WAL 模式，最新事务在 `.db-wal` 里 —— 直接 `copy` 单个 `.db` 文件会得到不一致/可能损坏的快照。
- 正确做法：用 **在线快照 `VACUUM INTO`**（WAL 安全，只对源库加读锁，不写源库、不停进程）。

```bash
# 全局（生产，保持运行）→ bin 临时文件
go run ./tools/snapshot_db ~/.claude/cc-otel/cc-otel.db bin/cc-otel.db.snap
```

`VACUUM INTO` 要求目标文件**不存在**，所以先写到临时名，再替换。

## 唯一允许停的进程

- **dev / bin daemon**（端口 14317 / 18899，`bin/cc-otel.pid`）可以停 —— 因为 Windows 下无法替换被它打开的 `bin/cc-otel.db`。这与 `tools/merge_bin_global` 的既有行为一致（它会按 `bin/cc-otel.exe` PID 校验后自动停 bin daemon）。
- 停 bin daemon 前后，删除残留的 `bin/cc-otel.db-wal` / `bin/cc-otel.db-shm`，避免旧 WAL 被错误地恢复到新 `.db` 上。

## 标准复制流程（全局 → bin，全局不停）

```bash
# 1. 在线快照全局库（全局 daemon 继续运行）
go run ./tools/snapshot_db ~/.claude/cc-otel/cc-otel.db bin/cc-otel.db.snap

# 2. 停 dev/bin daemon（不是全局！）
./bin/cc-otel.exe stop -config bin/cc-otel.yaml

# 3. 备份旧 bin 库，清掉残留 WAL，再替换
mv bin/cc-otel.db bin/cc-otel.db.bak-<ts>
rm -f bin/cc-otel.db-wal bin/cc-otel.db-shm
mv bin/cc-otel.db.snap bin/cc-otel.db

# 4. 重启 dev/bin daemon
./bin/cc-otel.exe start -config bin/cc-otel.yaml
```
