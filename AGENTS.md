# Water · Agent 工作约定

本文件规定如何修改和验证 Water；模块入口和当前实现见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 工作范围

- 按用户当前任务推进，不自行扩展文件浏览器、图片预览或结构化 Agent Surface。现有 coding-agent 功能包括进程检测、绑定及侧栏交互，不受旧 Phase 门禁限制。
- 修改前核对相关实现和当前基线；用户限制读取范围时遵守范围，并明确未经验证的结论。
- 保持 Cargo.toml 中的 GPUI pin；仅在任务涉及依赖升级时调整。

## 架构约束

- 外部应用模型命令走 `AppCommand → CommandDispatcher → model`，PTY 元数据和退出事件也由 dispatcher 在模型线程应用。UI、控制接口和场景通过 transport/channel 调用，不直接改模型，不引入全局 `Mutex<ApplicationModel>`。
- 区分服务端模型与客户端局部状态：窗口选择、拖拽预览、终端模拟和滚动不等于应用模型变更。GPUI 主线程不做阻塞 I/O、ANSI 解析或重型滚动历史处理。
- 服务端 PTY 输出走有界 replay 和有序事件流，客户端负责终端模拟；不能恢复服务端逐屏解析或逐输出推送模型快照。保持 Output/Resize/Exit 顺序及 replay 到 live 的完整衔接。
- 保持 typed ID 的稳定身份和随机分配，不用集合下标或进程内计数替代；运行时 ID 使用完整 UUIDv4，JSON/CLI 使用 UUID 字符串；不得截取或经浮点数转换。命令发往对象所属 connection，不能仅凭当前选中的连接路由。
- 默认 GUI、新 tab 和 split 都启动配置的真实 shell，保留空工作区测试入口。功能开关、主题、字体、快捷键及容量限制集中在 `AppConfig`。
- 新配置项贯通默认值、可选 overrides、合并/解析及运行时投影；设置面板同步读取、编辑、校验、渲染和生效方式。按改动验证整条链路。
- 默认日志保持结构化、安静；按模块开启诊断（如 `RUST_LOG=water::pty=debug`），不默认打印逐 cell/逐 frame 日志。

## 验证

- 运行与改动匹配的检查。除编译外，测试执行应在一分钟内完成；超时先排查等待、死锁或测试设计，不靠加 sleep/放宽超时掩盖问题。
- 用 operation 完成、事件、revision、输出或进程退出等有界等待同步，不用固定 sleep。历史“已通过”或“预存失败”需当前基线佐证，不能直接豁免失败。
- GUI 交互验证使用 `water ctl`（Water control API），不用 `xdotool`、`xte` 等系统注入工具。缺少必要交互时，在任务范围内补 control/automation 接口并走真实交互处理及应用命令路径。
- shell 场景使用检测到的 zsh，优先 `/opt/homebrew/bin/zsh`；隔离启动文件的交互测试用 `-f`。`--no-initial-terminal` 保留 workspace 但不建终端，`--empty-workspace` 从无 workspace 开始。
- headless 测试不能替代 GUI 验证；按实际机器检查显示环境，不假定 Linux 没有 X/GPU，也不硬编码 `DISPLAY` / `XAUTHORITY`。

## 测试实例与进程安全

Agent 可能依附于正在运行的 Water server；不要终止承载当前会话或用户终端的进程。

- 测试使用独立 `WATER_CONTROL_SOCKET` 和临时配置（`WATER_CONFIG` / `--config`），记录本次启动的 GUI/server PID。dev 默认隔离不能替代测试实例隔离。
- 清理只针对已确认属于本次测试的 PID，server 也需单独确认归属。禁止 `pkill water`、`pkill -9 water`、`killall water`；`pkill -x` 也不能隔离同名实例。
- 进程名仅用于辨认：dev GUI/server 为 `water-dev` / `water-srv-dev`，release 为 `water` / `water-server`，不能仅凭名字判断可安全终止。
- 测试结束后，需要及时关闭测试使用的socket、server，避免僵尸进程

## 构建与仓库

- 只分 dev 和 release：普通 `cargo build` 即优化后的 dev，产物在 `target/debug`；release 用 `--release`，产物在 `target/release`。指定 target 时多一层 `<triple>`。打包默认 dev，显式 `WATER_APP_VARIANT=release` 才发布 release。需要真实签名时必须额外设置 `CODESIGN_IDENTITY` 和 `CODESIGN_REQUIRED=1`；否则本地/交叉编译仍允许历史上的 ad-hoc 或无 `codesign` 环境。
- dev 版必须完全独立：socket `/tmp/water-dev.sock`；配置 `~/Library/Application Support/water-dev/config.json`；Bundle ID `dev.water.terminal.dev`；进程 `water-dev` / `water-srv-dev`。release 使用 `/tmp/water.sock`、`~/Library/Application Support/water/config.json`、`dev.water.terminal`、`water` / `water-server`。
- 发布 dev 版前确认包内 GUI/server、远端 payload 和运行时身份均符合隔离要求；构建时必须校验 payload 变体，不能回退到任意已安装的 release server。签名发布统一使用 `.github/workflows/macos-signed.yml`：它在 `macos-14` 上导入临时 keychain，签名 `Water.app` 或 `Water Dev.app`，验证嵌套 GUI/server 与 zip 后再发布；不能把空 release 当成已交付安装包。

  - 证书安全：`SurTeamCode.p12` 只作为本地导入材料，推荐放在仓库外并限制为当前用户可读（例如 `~/.config/water/SurTeamCode.p12`，`chmod 600`）；当前仓库根目录的同名文件已由 `*.p12` 忽略，绝不能提交。`.env` 同样只在本地使用，`SurTeamSignPass` 的值不能写进源码、workflow 或日志。
  - GitHub Actions Secrets：在 `SurTeam/Water` 配置 `SURTEAM_CODE_P12_BASE64`（p12 的无换行 base64）、`SURTEAM_SIGN_PASS`（对应 `.env` 的 `SurTeamSignPass`），可选 `SURTEAM_SIGNING_IDENTITY`（p12 含多个 identity 时指定完整名称）。可用以下命令写入 Secrets；命令不会把值打印到终端，禁止打开 shell tracing：

    ```bash
    set -a; source .env; set +a
    base64 < SurTeamCode.p12 | tr -d '\n' | gh secret set SURTEAM_CODE_P12_BASE64 --repo SurTeam/Water
    printf '%s' "$SurTeamSignPass" | gh secret set SURTEAM_SIGN_PASS --repo SurTeam/Water
    # 可选：printf '%s' 'Developer ID Application: ... (TEAMID)' |
    #   gh secret set SURTEAM_SIGNING_IDENTITY --repo SurTeam/Water
    ```

  - 触发和发布：推送 `v*` tag 自动构建 `release` 并创建正式 release；推送 `dev-*` tag 自动构建 `dev` 并创建 prerelease。手动运行 `Signed macOS app` 时选择 `variant`（`release`/`dev`）、`publication`（`release`/`prerelease`/`draft`/`none`）和可选 `tag`；`release` 只允许正式版，`prerelease` 只允许 dev，`draft` 可用于两者，`none` 仅上传 Actions artifact。正式版默认使用 `vVERSION`，dev 默认使用 UTC 时间戳 tag。

  - 本地签名打包在已把 p12 导入用户 keychain 后使用同一构建入口：

    ```bash
    CODESIGN_IDENTITY='Developer ID Application: ... (TEAMID)' \
      CODESIGN_REQUIRED=1 WATER_APP_VARIANT=dev bash scripts/build-macos-app.sh
    CODESIGN_IDENTITY='Developer ID Application: ... (TEAMID)' \
      CODESIGN_REQUIRED=1 WATER_APP_VARIANT=release bash scripts/build-macos-app.sh
    ```

    `build-macos-app.sh` 会先签嵌套 server/GUI，再签 app 并执行 `codesign --verify --deep --strict`；Actions 的 keychain 导入和 identity 自动发现逻辑只存在于 CI，不会读取仓库中的 p12。

  ```bash
  # 旧式 CLI fallback（仅在已设置 CODESIGN_IDENTITY/CODESIGN_REQUIRED 后使用）
  VERSION="$(awk -F ' *= *' '/^version = / { gsub(/"/, "", $2); print $2; exit }' Cargo.toml)"
  IDENTITY="$(security find-identity -v -p codesigning |
    awk -F '"' '/^[[:space:]]*[0-9]+\)/ { print $2; exit }')"
  CODESIGN_IDENTITY="$IDENTITY" CODESIGN_REQUIRED=1 WATER_APP_VARIANT=dev \
    bash scripts/build-macos-app.sh
  gh release create "dev-$(date -u +%Y%m%d-%H%M)" --title "Water Dev ${VERSION}" \
    --prerelease "dist/Water Dev-${VERSION}-macOS-arm64.zip"

  CODESIGN_IDENTITY="$IDENTITY" CODESIGN_REQUIRED=1 WATER_APP_VARIANT=release \
    bash scripts/build-macos-app.sh
  gh release create "v${VERSION}" --title "Water ${VERSION}" --latest \
    --generate-notes "dist/Water-${VERSION}-macOS-arm64.zip"
  ```

  `VERSION` 取 `Cargo.toml` 当前版本；资产文件名含空格（`Water Dev-x.y.z-macOS-arm64.zip`），命令行必须加引号；发布前用 `test -s` 和 `unzip -l` 确认资产非空。优先使用 workflow，避免本地发布和签名状态漂移。

- Linux → macOS 构建沿用现有 zig linker、framework stubs 和平台 patch；Metal shader 在 Mac 预编译并维护仓库产物。入口见架构文档，交叉构建成功不等于 macOS 运行验证。
- 新增路径先查 `git ls-files` 的大小写冲突；只保留一个 `AGENTS.md`，不要再创建 `Agents.md`。
- Python 优先 `~/.venv/bin/python` / `~/.venv/bin/pip`；Node 工具先加载 fnm 环境并遵循项目指定版本。
