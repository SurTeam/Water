# AGENTS

## 进程管理（重要）

Agent 运行在 water-server 进程内部。**永远不要 `pkill water`、`pkill -9 water`、`killall water`**——这会把你自己也杀了。

### 进程命名规则

| 构建 | 进程 | comm 名称 |
|------|------|-----------|
| debug | GUI | `water-dev` |
| debug | Server | `water-srv-dev` |
| release | GUI | `water` |
| release | Server | `water-server` |

### 杀 dev/test 进程

```bash
# 杀 dev GUI
pkill -x water-dev

# 杀 dev server
pkill -x water-srv-dev

# 杀 release GUI（注意：不会匹配 water-server）
pkill -x water

# 绝对不要
pkill water          # ❌ 会匹配 water-server
pkill -9 water       # ❌ 同上
killall water        # ❌ 同上
```

### 启动测试实例

```bash
# 始终用独立的 socket 路径，避免碰到生产 server
WATER_CONTROL_SOCKET=/tmp/water-test-$RANDOM.sock ./target/debug/water &
TEST_PID=$!
# ... 测试 ...
kill $TEST_PID       # GUI
pkill -x water-srv-dev  # 如果启动了 embedded server
```

### 测试 release

```bash
# 同样用独立 socket
WATER_CONTROL_SOCKET=/tmp/water-test-$RANDOM.sock ./target/release/water &
TEST_PID=$!
# ... 测试 ...
kill $TEST_PID       # 按 PID 杀，不用 pkill
# 如果有 embedded server，按 PID 杀或 pkill -x water-server
# 但要先确认生产 server 不在跑，或用 pgrep 确认
```

### 绝对禁止

- `pkill -9 water`
- `killall water`
- `pkill water`（不带 `-x`）
- 任何可能匹配到 `water-server` 的 kill 命令

### 安全做法

- `pkill -x water-dev`（debug GUI）
- `pkill -x water-srv-dev`（debug server）
- 按 PID kill（`kill $PID`）
- `pgrep -x water` 先确认目标再 kill