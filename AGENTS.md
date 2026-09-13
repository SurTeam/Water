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
- 发布 dev 版前确认包内 GUI/server、远端 payload 和运行时身份均符合隔离要求；构建时必须校验 payload 变体，不能回退到任意已安装的 release server。签名发布采用两阶段：构建机用 `CODESIGN_SKIP=1` 产出 unsigned `Water.app`/`Water Dev.app`，再由 `.github/workflows/macos-signed.yml` 在 `macos-14` 上只下载、签名、校验和发布；不能把空 release 或未签名资产当成已交付安装包。

  - 证书安全：`SurTeamCode.p12` 只作为本地导入材料，推荐放在仓库外并限制为当前用户可读（例如 `~/.config/water/SurTeamCode.p12`，`chmod 600`）；当前仓库根目录的同名文件已由 `*.p12` 忽略，绝不能提交。`.env` 同样只在本地使用，`SurTeamSignPass` 的值不能写进源码、workflow 或日志。
  - GitHub Actions Secrets：在 `SurTeam/Water` 配置 `SURTEAM_CODE_P12_BASE64`（p12 的无换行 base64）、`SURTEAM_SIGN_PASS`（对应 `.env` 的 `SurTeamSignPass`），可选 `SURTEAM_SIGNING_IDENTITY`（p12 含多个 identity 时指定完整名称）。可用以下命令写入 Secrets；命令不会把值打印到终端，禁止打开 shell tracing：

    ```bash
    set -a; source .env; set +a
    base64 < SurTeamCode.p12 | tr -d '\n' | gh secret set SURTEAM_CODE_P12_BASE64 --repo SurTeam/Water
    printf '%s' "$SurTeamSignPass" | gh secret set SURTEAM_SIGN_PASS --repo SurTeam/Water
    # 可选：printf '%s' 'Developer ID Application: ... (TEAMID)' |
    #   gh secret set SURTEAM_SIGNING_IDENTITY --repo SurTeam/Water
    ```

  - 触发和发布：签名 workflow 不响应 tag push，也不做 Rust/交叉编译；先在构建机生成 unsigned zip，并将其上传到目标 tag 对应的 draft release，再用 `scripts/publish-unsigned-macos.sh` 触发 `Sign macOS app`。workflow 的 `variant` 为 `release`/`dev`，`publication` 为 `release`/`prerelease`/`draft`/`none`，同时必须传入目标 `tag`、承载 unsigned 资产的 `source_release` 和精确 `source_asset` 文件名；`release` 只允许正式版，`prerelease` 只允许 dev，`draft` 可用于两者，`none` 保留 draft。正式版 tag 使用 `vVERSION`，dev tag 使用 `dev-*`。

  - unsigned 构建和签名发布的固定流程：先在 macOS 构建机生成不调用 `codesign` 的 unsigned 包，再上传/触发签名 workflow；`CODESIGN_SKIP=1` 与 `CODESIGN_REQUIRED` 互斥。

    ```bash
    # release：tag 应先 commit、push 并推送到 origin
    VERSION="$(awk -F ' *= *' '/^version = / { gsub(/"/, "", $2); print $2; exit }' Cargo.toml)"
    WATER_APP_VARIANT=release CODESIGN_SKIP=1 \
      bash scripts/build-macos-app.sh
    WATER_APP_VARIANT=release WATER_RELEASE_TAG="v${VERSION}" \
      WATER_RELEASE_PUBLICATION=release bash scripts/publish-unsigned-macos.sh

    # dev：使用已推送的 dev-* tag，并发布为 prerelease
    DEV_TAG="dev-$(date -u +%Y%m%d-%H%M)"
    WATER_APP_VARIANT=dev CODESIGN_SKIP=1 \
      bash scripts/build-macos-app.sh
    WATER_APP_VARIANT=dev WATER_RELEASE_TAG="$DEV_TAG" \
      WATER_RELEASE_PUBLICATION=prerelease bash scripts/publish-unsigned-macos.sh
    ```

    `publish-unsigned-macos.sh` 将 unsigned zip 放入目标 tag 的 draft release，然后用 `gh workflow run` 传入 `variant`、`publication`、`tag`、`source_release` 和 `source_asset`。Actions 只下载该 zip，导入 Secrets 中的 p12，签 nested GUI/server 和 app，验证 zip 后用同名 signed asset 替换 draft 中的 unsigned asset，再按 publication 发布。`CODESIGN_SKIP=1` 会让 `build-macos-app.sh` 保留真正未签名的 bundle；发布前仍需用 `test -s`、`unzip -t` 和 Actions 的 `codesign --verify --deep --strict` 检查。

- Linux → macOS 构建沿用现有 zig linker、framework stubs 和平台 patch；Metal shader 在 Mac 预编译并维护仓库产物。入口见架构文档，交叉构建成功不等于 macOS 运行验证。
- 新增路径先查 `git ls-files` 的大小写冲突；只保留一个 `AGENTS.md`，不要再创建 `Agents.md`。
- Python 优先 `~/.venv/bin/python` / `~/.venv/bin/pip`；Node 工具先加载 fnm 环境并遵循项目指定版本。

## GitHub Actions 签名发布规则

- `.github/workflows/macos-signed.yml` 是 signing-only workflow，唯一触发方式是 `workflow_dispatch`；tag push 不触发它。不得为了发布把 `cargo`、`cargo-zigbuild`、`zig`、Rust checkout 或完整构建重新放回该 workflow。
- workflow 必须同时接收 `variant`、`publication`、`tag`、`source_release` 和 `source_asset`：`release` 只能使用 `v*` tag 并发布正式版，`dev` 只能使用 `dev-*` tag；`prerelease` 只用于 dev，`draft`/`none` 可用于两者。
- `source_release` 必须是目标 tag 对应的 draft release，`source_asset` 必须是精确的 `Water-x.y.z-macOS-arm64.zip` 或 `Water Dev-x.y.z-macOS-arm64.zip` 文件名。Action 只下载该 zip、解压、导入临时 keychain、签 nested server/GUI 和 app、执行 `codesign --verify --deep --strict`，然后以同名 signed asset 替换 draft 中的 unsigned asset；校验失败时禁止发布。
- 固定交接顺序是：本地 `CODESIGN_SKIP=1 WATER_APP_VARIANT=... bash scripts/build-macos-app.sh` → 确认 `test -s` 和 `unzip -t` → `scripts/publish-unsigned-macos.sh` 创建/更新 draft 并触发 Action。不要直接把本地 `.app` 提交进 Git；unsigned zip 只作为 draft release 的临时传输资产。
- 手动触发时使用 `gh workflow run`，并从 `main` 读取最新 workflow；例如：

  ```bash
  gh workflow run macos-signed.yml --repo SurTeam/Water --ref main \
    -f variant=release -f publication=release -f tag=v${VERSION} \
    -f source_release=v${VERSION} \
    -f source_asset="Water-${VERSION}-macOS-arm64.zip"
  ```

- 签名 Secret 只能使用 `SURTEAM_CODE_P12_BASE64`、`SURTEAM_SIGN_PASS` 和可选的 `SURTEAM_SIGNING_IDENTITY`；p12、密码和明文 identity 不得进入仓库、workflow 输出或 shell tracing。Action 必须使用临时 keychain，并在 `always()` 清理 keychain 和 p12 文件。自签名证书在 runner 上发现 identity 时不得恢复 `security find-identity -v`，最终以实际 `codesign` 验证为准。
