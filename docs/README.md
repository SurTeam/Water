# Water 设计与开发文档

这里记录当前实现的职责、必须遵守的规则和已知限制。修改前先阅读相关模块，再核对源码与测试；维护时机见 [工作约定](../AGENTS.md#文档更新时机)。每项规则由对应模块维护，总入口只描述共同边界。

## 整体组织原则

Water 由共享会话服务与独立客户端组成。server 管理 workspace/tab/pane 模型、PTY、元数据和按保留策略管理的原始 replay；Go GUI、TUI 与浏览器分别拥有自己的终端模拟、视图、选择和输入状态。

所有外部模型修改沿 connection transport → command dispatcher → model 执行。对象使用完整 typed UUID 身份，命令发送到其所属 connection；客户端不能越过服务边界直接修改共享模型。

设计要求 PTY 输出、resize 与退出有序、replay/live 完整衔接，耗时工作留在后台并以不可变视图交接。队列、缓存与关闭等待应有界，单一终端不应阻塞全局交互。当前预算、例外和静态审查发现的缺口由终端、性能及桌面模块明确说明，不把这些原则视为每条路径已通过验证的保证。

窗口选择、焦点与生命周期各有归属。共享状态不能覆盖其他窗口的选择，关闭一个客户端不能误停其他客户端使用的服务。产品版本、协议兼容性与运行变体分别管理。

## 按职责阅读

| 关注点 | 文档 | 主要实现入口 |
| --- | --- | --- |
| 整体 UI 布局、主题、弹窗与操作语义 | [界面布局与交互](ui-design.md) | `internal/goui/ebiten_layout.go` |
| Go GUI、窗口、输入与配置 | [Go 桌面界面与配置](desktop-gui.md) | `internal/gouiapp/`、`internal/goui/`、`internal/goconfig/` |
| 终端流、网格、滚动、选区与图形 | [终端输出与渲染](terminal-rendering.md) | `internal/goterminal/`、`internal/govt/`、`internal/xterm/` |
| 调度、背压、缓存与性能验收 | [性能与资源管理](performance.md) | GUI、client、PTY 管线 |
| 模型、协议、连接、多窗口与 SSH | [服务端与远程连接](remote-connections.md) | `internal/goserver/`、`internal/gomodel/`、`internal/goclient/`、`internal/goremote/` |
| 兼容性与受保护的恢复 | [服务端兼容与恢复](server-recovery.md) | session、recovery 与 Server Settings |
| Web listener、配对与浏览器权限 | [浏览器访问与配对](browser-access.md) | `internal/goserver/web.go`、`webassets/` |
| Agent 识别、状态与控制 | [Agent 识别与控制](agent-integration.md) | `internal/goagent/` |
| 终端内前端 | [TUI 终端界面](terminal-ui.md) | `internal/gotui/` |
| 控制命令与自动化 | [控制命令与自动化](control-api.md) | `cmd/water/ctl.go`、dispatcher |
| 构建、变体、更新、签名与发布 | [构建打包与发布](build-and-release.md) | `internal/gobuild/`、`internal/goupdate/`、`scripts/` |
| 回归选择、证据与实例所有权 | [测试与验证](testing.md) | `scripts/run-go-regression.py`、`scripts/water_test.py` |

完整配置字段见 [config.example.json](../config.example.json)，已发布版本说明见 [releases](releases/)。模块中的命令用于落实与验证相应规则，不再维护另一份全局操作手册。
