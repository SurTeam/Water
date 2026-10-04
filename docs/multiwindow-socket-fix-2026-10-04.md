# 多窗口 socket、选择和 PTY 尺寸修复

已在源码实现并完成本机验证；未替换当前 `/Applications/Water.app`，未终止用户原有 GUI PID 66089 或 server PID 66132。

## 现场证据与原因

原有 GUI 与 server 的 Unix 连接仍然存在，终端继续输出，但 `/tmp/water.sock` 已不存在，新建控制连接失败。源码中 `ListenAndServe` 在启动时无条件 `os.Remove(socket)`，退出时也无条件删除该路径。多个启动者或旧进程清理可以移除另一个 listener 的入口；已经连接的 stream 不受 pathname 删除影响，所以出现“终端仍能用、退出报连接错误”。具体哪一次进程删除了用户 socket，缺少历史追踪，无法断定。

退出菜单原先通过新建连接调用 `server.shutdown`，因此受到路径失效影响。选择状态原先直接采用共享模型的 active workspace/tab/pane；PTY resize 则来自每个窗口自己的 layout，导致窗口选择互相覆盖、不同尺寸的窗口争抢同一 PTY。

## 修改后的行为

- socket listener 全生命周期持有独占文件锁，拒绝覆盖活跃 listener 或普通文件，只回收 connection refused 的旧 socket。关闭 listener 禁用自动 unlink；最终清理先检查 inode，只删除自身创建的 socket。锁文件保留，避免锁 inode 替换竞争。
- Cmd+N 继续以相同 socket/config 启动独立 GUI，共享 server、终端和模型。客户端保存各自的 workspace、tab、pane 选择；选择命令的结果通过有序 `push.selection` 仅发给来源窗口。CLI 选择命令发给当前焦点 GUI。
- 新建 tab、split、关闭 pane 等 GUI 命令明确携带本窗口的 workspace/pane UUID，不依赖其他窗口改变过的模型默认选择。
- 焦点窗口控制可见 panes 的 PTY 尺寸；背景窗口不自动 resize。重新取得焦点时，即使本地几何缓存没变也重发尺寸。server 拒绝非焦点 GUI 的迟到 resize。显式 CLI resize 保留。
- 退出 server 使用现有 session。普通关闭只释放当前 GUI；`detach_on_quit=false` 时最后一个窗口才关闭共享 server。embedded server 的原始进程在 GUI 关闭后可继续服务其他窗口，直到最后一个 session 释放。
- `server info` 列出完整 UUID window ID；`ui ... --window UUID` 定向控制指定窗口。dump 分别提供窗口几何 columns/rows 和终端实际 terminal_columns/terminal_rows。

协议升级到 5，API signature 到 v6；终端二进制布局保持 WT4。新 GUI 不会悄悄附着不支持该行为的旧 server，也不会覆盖它的 socket。

## 验证

全量 `go test ./... -count=1 -timeout=60s`、`go vet ./...`、server/client/GUI 的 race 测试通过。并发 GUI 启动测试在 race 模式重复 10 次，只产生一个 embedded server owner。四个 scenario（workspace_basic、terminal_basic、agent_detection、terminal_zsh）通过。

新增回归覆盖活跃 socket 保护、外部 listener 保护、旧 socket 回收、旧 listener 不删除替代 inode、socket pathname 丢失后通过现有连接 shutdown、首个 session release 不影响第二个，以及焦点 resize 仲裁和客户端选择隔离。

真实 macOS GUI 使用 `scripts/go-ui-multiwindow-smoke.py`，全部操作走 `water ctl` 和真实 UI action。detached 与 embedded 均通过 Cmd+N、不同 workspace、背景窗口在自身 workspace 新建 tab、共享终端在不同窗口尺寸下随焦点从 162 列切换至 80 列、首窗口关闭后另一窗口继续使用、最后窗口关闭后 server/socket 消失。

最终运行记录：

| 模式 | GUI PID | server PID | 证据目录 |
| --- | --- | --- | --- |
| detached | 50645 | 50651 | `/tmp/water-multi-1k9iwucp` |
| embedded | 50971 | 50971 | `/tmp/water-multi-2c4wri48` |

本次测试 GUI/server 已关闭。原有用户进程保持运行。

v0.3.5 发布前对 `dist/Water.app/Contents/MacOS/water` 再次执行两种原生 GUI 检查，均通过：detached GUI/server PID 69302/69308，证据 `/tmp/water-multi-8g5sh7m3`；embedded GUI/server PID 69400，证据 `/tmp/water-multi-fslehyhi`。两种模式均验证 162 → 80 列的焦点尺寸切换，测试实例已关闭。unsigned zip 的非空检查和 `unzip -tq` 通过；它仅作为 signing-only workflow 的中间物。

## 当前正在使用的旧版本

源码修复不会热更新已经运行的旧进程。当前旧 GUI 可以从原生菜单选择 **Quit GUI**，避免需要访问失效路径的 **Quit GUI and Local Server**。这只退出 GUI，并保留旧 server/PTY；若其中仍有工作，应先处理好会话。应用新版本需要 GUI 与 server 同时使用新协议，旧 server 的 socket 不会被自动抢占。修复纳入 v0.3.5 发布，版本说明见 [v0.3.5](releases/v0.3.5.md)。
