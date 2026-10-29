# 构建打包与发布

实现入口：`internal/gobuild/`、`internal/goupdate/`、`scripts/build-go-*.sh`、`scripts/build-go-windows.ps1`、`scripts/publish-unsigned-macos.sh` 和 `.github/workflows/macos-signed.yml`。构建命令、更新安装限制与签名流程在本文统一维护。

版本、提交、push、tag 与发布分别需要相应用户授权。文档随已确认实现更新的时机见 [AGENTS.md](../AGENTS.md#文档更新时机)。

## 构建与变体

- 只有 `dev` 和 `release` 两种运行身份。普通 `go build` 使用 dev；打包脚本通过 `WATER_APP_VARIANT` 和 linker flags 设置身份，不从优化参数推断身份。macOS/Linux 构建产物分别在 `target/go-app/<variant>/<arch>` 和 `target/go-linux/<variant>/<arch>`；Windows 在 `target/go-windows/<variant>/<arch>/package`。
- dev 使用 `water-dev` namespace：`/tmp/water-dev.sock`、`~/.config/water-dev/config.json`、Bundle ID `dev.water.terminal.dev`、GUI/server/PTY supervisor 进程 `water-dev`/`water-srv-dev`/`water-pty-dev`。release 使用 `water` namespace：`/tmp/water.sock`、`~/.config/water/config.json`、Bundle ID `dev.water.terminal`、GUI/server/PTY supervisor 进程 `water`/`water-server`/`water-pty`。
- 默认配置根目录依次采用绝对路径 `$XDG_CONFIG_HOME`、兼容变量 `$XDG_CONFIG`、`~/.config`。macOS 首次读取新路径时复制旧 Application Support 配置，保留旧文件；目标已存在时不覆盖。
- Windows GUI/server/updater 可在 macOS 或 Linux 以 `CGO_ENABLED=0` 交叉编译打包。macOS GUI 使用本机 Apple SDK，可用 `GOARCH=amd64` 或 `arm64` 跨架构构建；Linux GUI 需要目标 Linux C 编译器及 X11/Wayland、EGL/OpenGL、xkbcommon 和 Vulkan 开发库，可在本机 Linux 容器内构建。headless server payload 使用 Go 交叉编译。交叉编译成功不等于原生运行验证。构建脚本必须使用本次变体和版本生成的 GUI/server/payload，不能回退到任意已安装的 release server。

macOS app 的 `Contents/Resources/Licenses` 、Linux archive 和 Windows ZIP 的 `Licenses` 携带实际使用的本地依赖许可证、NOTICE 和 Ebitengine 修改说明，由 `scripts/package-licenses.sh` 收集本地依赖和已解析 Go module 的许可证；Windows PowerShell 入口执行等价收集。Go 运行库 LICENSE 为必需文件；收集器兼容标准 GOROOT 和 Homebrew 的上级目录布局，缺少该文件时中止组包。

控制命令见 [控制 API](control-api.md)，回归流程见 [测试策略](testing.md)。

## 签名与发布规则

- `.github/workflows/macos-signed.yml` 是唯一保留的 CI，为 signing-only workflow，唯一触发方式是 `workflow_dispatch`；不得把源码 checkout、编译或完整构建放回该 workflow。本地执行构建和验证。
- workflow 必须接收 `variant`、`publication`、`tag`、`source_release`、`source_asset`。release 只接受 `v*` tag，dev 只接受 `dev-*` tag；`prerelease` 只给 dev，`release` 只给正式版，`draft`/`none` 可用于两者。
- 当前开发机是 macOS 时，优先在本机使用与 CI 相同的 SurTeam 证书完成签名：显式提供SurTeam证书的 `CODESIGN_IDENTITY="85B37EA8D5B9719CB5F443613573AD1F4986B17B"`、设置 `CODESIGN_REQUIRED=1`，本地构建后签 updater/server/PTY supervisor/GUI/app，验证签名和 archive，再上传已签名产物；不触发 CI 再签一次。
- 本地证书不可用时，CI signing-only 路径保留为备用：由 macOS 构建机使用 `CODESIGN_SKIP=1` 原生构建 unsigned zip → `test -s`/`unzip -t` → 上传目标 tag 的 draft release → 触发 signing workflow。本地签名失败必须报告原因，不自动降级为 ad-hoc 或触发 CI 掩盖失败。unsigned app 不提交 Git，也不能当作已交付安装包。
- 非 macOS 主机不能运行 macOS archive 构建脚本；只能接收 macOS 构建机生成的 archive 后上传交接。signing-only CI 不提供编译环境，也不能替代原生构建。
- 需要真实签名时显式设置 `CODESIGN_IDENTITY` 和 `CODESIGN_REQUIRED=1`；本地 ad-hoc/unsigned 产物只能作为构建或签名交接中间物。
- `SurTeamCode.p12` 只作为本地导入材料，放在仓库外并限制权限；`.env`、密码、p12、明文 identity 不得进入源码、workflow、日志或 shell tracing。Actions 只使用 `SURTEAM_CODE_P12_BASE64`、`SURTEAM_SIGN_PASS` 和可选的 `SURTEAM_SIGNING_IDENTITY`。
- 本地与 Action 均必须签 nested updater/server/PTY supervisor/GUI 和 app，并执行 `codesign --verify --deep --strict`；失败不得发布。Action 使用临时 keychain，`always()` 清理 keychain 和 p12。`CODESIGN_SKIP=1` 与 `CODESIGN_REQUIRED` 互斥。


## Build
```sh
# Dev identity; ordinary Go builds default to dev.
mkdir -p target/go-ui-smoke
go build -o target/go-ui-smoke/water ./cmd/water
go build -o target/go-ui-smoke/water-server ./cmd/water-server

# Assemble the macOS app; dev is the default variant.
bash scripts/build-go-macos-app.sh

# Assemble a release app explicitly.
WATER_APP_VARIANT=release bash scripts/build-go-macos-app.sh

# Linux package, on a Linux host/container with target C libraries.
WATER_APP_VARIANT=release bash scripts/build-go-linux.sh

# Cross-build both Windows architectures on macOS or Linux (no Windows host).
WATER_APP_VARIANT=release bash scripts/build-go-windows.sh all
# Or select a single target: amd64 / arm64.
WATER_APP_VARIANT=release bash scripts/build-go-windows.sh amd64

# Cross-build an Intel macOS app using this Mac's Apple SDK.
GOARCH=amd64 WATER_APP_VARIANT=release CODESIGN_REQUIRED=1 \
  bash scripts/build-go-macos-app.sh

# Refresh embedded server payloads only.
WATER_APP_VARIANT=dev bash scripts/build-go-embedded-servers.sh
```

裸 go build 使用 internal/gobuild/identity.go 的 dev 身份、源码默认产品版本（当前为 0.3.1）和 development revision，不读取 VERSION。要验证真实版本、更新或 payload 匹配，使用打包脚本或显式 linker flags。下述 VERSION 来源适用于脚本构建。

The packaging scripts build the GUI, dedicated server, PTY supervisor, updater helper and six embedded server targets (darwin/linux/windows × amd64/arm64). `build-go-embedded-servers.py` is the portable payload builder; the shell entry delegates to it. Its manifest records variant/version/server revision and each payload's size and SHA256. Windows desktop builds are pure-Go cross-builds; macOS uses the local Apple SDK, and Linux requires target C libraries. The app bundle must contain payloads whose variant and version match the GUI. Product version comes from the root `VERSION` file; use the same `WATER_APP_VERSION` for packaging and publishing when overriding it. `WATER_APP_VARIANT` selects dev/release and is injected through linker flags. macOS build directories are `target/go-app/<variant>/<arch>`; Linux uses `target/go-linux/<variant>/<arch>`; archives go to `dist`.
## Windows cross-packaging and signatures

`bash scripts/build-go-windows.sh [all|amd64|arm64]` works on macOS/Linux; the default is both architectures. It needs Go, Bash and Python 3 (`PYTHON` can select the interpreter), with no Windows host or SDK dependency. The resulting `dist/water[-dev]-VERSION-windows-ARCH.zip` contains the GUI, dedicated server, copied PTY supervisor, updater helper, payload manifest and licenses. Windows executables carry `.exe`; the dev server is `water-srv-dev.exe`.

For Authenticode signing, provide `WINDOWS_SIGN_P12`, `WINDOWS_SIGN_PASSWORD_FILE` and `WINDOWS_SIGN_CA_FILE`, and install `osslsigncode`. Set `WINDOWS_SIGN_REQUIRED=1` for credentialed release builds. The script signs all four executables with SHA256, verifies them against the supplied trust anchor, then creates and CRC-checks the ZIP. `WINDOWS_SIGN_TIMESTAMP_URL` optionally enables RFC3161 timestamping. Password contents are read from a protected file, never command-line text. Keep private signing materials outside tracked source; do not publish them. Verification validates the signature against the supplied trust anchor; confirming the intended signer additionally requires matching its certificate. This does not assert that every Windows machine already trusts that certificate or bypasses SmartScreen.

The native Windows alternative is `scripts/build-go-windows.ps1 -Arch amd64` (or `arm64`), with optional `-Python`. It builds the same package layout. Windows update checking/download uses the release metadata, but in-app installation remains explicitly unsupported despite the packaged helper.

For local multi-platform builds, run Windows and macOS scripts on the Mac; run the Linux script in a local Linux container with the selected architecture's C compiler and development libraries. `GOARCH` selects `amd64`/`arm64` for macOS and Linux; when cross-building Linux inside a container, also provide the target `CC`/`CXX` and `PKG_CONFIG_LIBDIR`. Build sequentially because the embedded payload set is shared. Cross-compilation is separate from runtime acceptance, including Windows ConPTY, clipboard, native dialogs and IME.

A release may additionally publish `SHA256SUMS`, its detached SHA256/RSA signature `SHA256SUMS.sig`, and the public signing certificate `release-signing-certificate.pem`. This binds the archive bytes, including Linux archives, to the signer. Verify the manifest signature using the public key extracted from a separately trusted copy of the certificate, then verify archive hashes. This supplements platform signatures; it is not an OS trust-store installation or notarization claim.

## 上传后的发布资产验证

上传完成后通过 GitHub release-assets API 查询资产元数据，将每个资产的 `digest`（`sha256:<hex>`）和 `size` 与本地待发布文件的 SHA256、字节数逐项比较；同时核对完整文件名集合及 `state=uploaded`。不重新下载 GitHub 发布资产计算哈希。摘要缺失或不一致时不得把 release 标记为验证通过，应等待摘要生成或调查上传问题。

先在 draft release 完成上述检查，再公开发布；发布后再次核对 tag、draft/prerelease 状态、latest 指向及资产元数据。本地归档完整性、平台签名和签名校验清单仍须在上传前验证，API 哈希比较只证明 GitHub 持有的资产与本地文件一致。

```sh
gh api repos/SurTeam/Water/releases/tags/vVERSION \
  --jq '.assets[] | {name, size, state, digest}'
shasum -a 256 dist/ARCHIVE
```

## In-app updates (macOS and Linux)
Open **Settings → Software update**, or the macOS application menu's
**Software update** item. Water checks its public GitHub Releases, offers a
download, then **Install and restart**. Checking and downloading run outside
the rendering loop. No DMG mounting or dragging an app is required after
installing a version that includes this updater.

Only strictly newer product versions with an exact platform/architecture asset
are eligible. Release builds accept non-draft, non-prerelease `v*` releases;
dev builds use non-draft `dev-*` releases, including timestamp tags whose
product version is obtained from the package name. Same-version dev rebuilds
are not updates: increment `WATER_APP_VERSION` when publishing an update.
macOS uses `Water-VERSION-macOS-ARCH.zip` for release and
`Water Dev-VERSION-macOS-ARCH.zip` for dev, where ARCH is arm64 or amd64
(GitHub replaces spaces with periods); Linux uses `water[-dev]-VERSION-{aarch64,x86_64}-linux.tar.gz`.
Publish Linux packages to the matching release after building them with the target Linux libraries.
macOS development machines sign locally with the same certificate as CI; the
signing-only workflow remains a fallback for machines without local signing.

Both platforms require the SHA256 digest provided by GitHub's release-assets
API and verify the complete download before extracting it. macOS additionally
requires the new bundle to satisfy the current app's certificate designated signing
requirement, and verifies bundle identity, version and nested signatures.
Unsigned/ad-hoc macOS development bundles cannot install updates. Linux trusts
the official HTTPS release metadata and its digest; it does not claim an
independent publisher signature. The GUI, sibling server and private headless installer identities are
checked before installation. Invalid packages and archive links/path traversal
are rejected.

The installation directory must be writable. macOS must run from an installed
app bundle, outside a mounted DMG or App Translocation; Linux requires the GUI, sibling server and water-update[-dev] helper
from the extracted package together in the same directory. A system
package manager installation should continue using that package manager when
its directory is not writable. Water does not request elevated privileges.

The GUI copies and runs the helper from the **currently installed package**, not
from the downloaded replacement. A helper/GUI protocol change therefore cannot
retroactively protect the first update started by an older installation. The
full protocol below applies once both installed GUI and helper include it;
installing only a new GUI beside an old helper does not provide these guarantees.

The GUI holds a stable advisory `.water-update-lock.guard` (or
`.water-dev-update-lock.guard`) and transfers its open descriptor to the helper.
A bounded ready/commit/takeover handshake must finish before the GUI enters its
update exit path. The helper first opens a keeper session to an explicitly
selected local control socket, when present; it only connects and never starts
a server. Failed takeover leaves the GUI and installation in place and allows
retry. After takeover the helper waits up to 30 seconds for the recorded GUI PID
to exit before replacing files; reparenting alone is not proof of exit.

The installer retains the old app/binaries and waits up to 20 seconds for a
private first-live-frame ACK from the replacement GUI. macOS launches app bundles
through LaunchServices using `open -n -W`, forwarding update environment and
original arguments explicitly. The ACK contains the GUI PID; its startup PID
marker identifies the process belonging to that launch attempt. Startup ACK and
server-preservation environment variables are consumed at process initialization
so child windows, servers and shells do not inherit those capabilities. ACK
writes are serialized, retry on failure, and release server protection only
after a successful write.

The control socket, config and SSH destinations are preserved. Detached terminal
servers continue running during the GUI restart, including when
`detach_on_quit` is false: outgoing update cleanup skips shutdown, the helper
holds its keeper session across replacement/rollback, and the replacement GUI
suppresses shutdown cleanup until its first-frame ACK. During this protected
startup, explicit GUI server shutdown/restart is also blocked. Update installation
and Quit GUI and Local Server cannot start concurrently; recovery restart checks
update protection again before requesting shutdown and cancels a prepared lease
if protection became active. An embedded local server blocks installation;
enable `server.detached` and restart Water before installing updates. Remote
connections reconnect through the existing transport. Protocol incompatibility
with a retained server causes startup failure and rollback rather than server
replacement or session termination.

Replacement or startup failure triggers rollback only after the attempted new
GUI has exited or its owned PID has been stopped. If its identity/exit cannot be
confirmed, files and backups remain for diagnosis without overwriting a possibly
running replacement. Incomplete rollback also disables automatic restart.
After a complete rollback the helper launches the restored GUI and waits for its
first frame, accepting the legacy `ready` ACK only on this rollback path. The
keeper session protects even legacy last-client cleanup while the helper remains
alive; an old restored GUI does not acquire the new startup guards by receiving
an environment variable.

Failed installations retain `.water-update-*` (release) or
`.water-dev-update-*` (dev) staging/backup files and append-only `install.log`
beside the installation. The visible `.water-update-lock` /
`.water-dev-update-lock` records the owner PID. Startup and install retries may
remove a stale visible lock only while holding the advisory guard and after
confirming its recorded owner is gone. Legacy empty locks require checking
matching staging directories' `installer.pid`; an active PID, malformed owner or
uncertain legacy plan is preserved and installation remains blocked. Automatic
lock recovery does not delete staging/backups. The stable `.guard` file remains
on disk; its existence is not evidence of an active installer and it must not be
unlinked to bypass locking. For uncertain locks, inspect `install.log`, plan,
owner PID and installation paths before manual recovery; never delete an active
installer's lock or backup. Normal successful installation cleans its staging
files and visible lock. No update is installed without clicking **Install and restart**.

Run `~/.venv/bin/python scripts/go-ui-update-smoke.py` with `WATER_BIN` pointing
to the native test binary. It uses an isolated socket/config and real Water
control actions, checks the localized panel/menu and bounded release lookup,
and captures screenshots without installing a release.
## macOS local signing and CI fallback
The macOS app also declares the Finder folder service **Open New Workspace in Water**
(**Open New Workspace in Water Dev** for dev). Each selected directory opens a new
local workspace with its terminal rooted there. Service names and ports follow the
app variant; the plist template is the packaging source. Finder visibility depends
on macOS service registration and enablement.

The macOS app registers as an alternate handler for `.command` files. Choose
Water in Finder's **Open With** menu to run an executable command file in a new
local workspace. Its working directory is the script's directory; the configured
shell remains open after it finishes. Local files are rejected in `--ssh` sessions.

On a macOS development machine, build and sign locally with the same SurTeam
certificate used by CI. Supply `CODESIGN_IDENTITY` securely from the local
keychain/environment; do not put the literal identity or credentials in source,
logs or shell tracing. Do not dispatch signing CI after successful local signing.

```sh
# CODESIGN_IDENTITY must already be set to the matching CI certificate.
WATER_APP_VARIANT=release CODESIGN_REQUIRED=1 \
  bash scripts/build-go-macos-app.sh
codesign --verify --deep --strict dist/Water.app
APP_VERSION="${WATER_APP_VERSION:-$(cat VERSION)}"
ARCH="${GOARCH:-$(go env GOHOSTARCH)}"
test -s "dist/Water-${APP_VERSION}-macOS-${ARCH}.zip"
unzip -tq "dist/Water-${APP_VERSION}-macOS-${ARCH}.zip"
```

The native script signs updater, server, PTY supervisor, GUI and app using the same runtime and
timestamp options as CI, then verifies the bundle. Commit/push the version and
tag before uploading the signed archive to the matching release. Local signing
failure is a blocker; never silently substitute ad-hoc signing or CI.

When local signing is unavailable, the existing two-stage CI handoff is retained:
a macOS build machine creates an unsigned archive and the signing-only GitHub Action
downloads, signs, verifies and publishes it. It never checks out or compiles source.

```sh
# Release: push the vVERSION tag first.
APP_VERSION="${WATER_APP_VERSION:-$(cat VERSION)}"
WATER_APP_VARIANT=release CODESIGN_SKIP=1 \
  bash scripts/build-go-macos-app.sh
WATER_APP_VARIANT=release WATER_RELEASE_TAG="v${APP_VERSION}" \
  WATER_RELEASE_PUBLICATION=release bash scripts/publish-unsigned-macos.sh

# Dev: use an already pushed dev-* tag.
DEV_TAG="dev-$(date -u +%Y%m%d-%H%M)"
WATER_APP_VARIANT=dev CODESIGN_SKIP=1 \
  bash scripts/build-go-macos-app.sh
WATER_APP_VARIANT=dev WATER_RELEASE_TAG="$DEV_TAG" \
  WATER_RELEASE_PUBLICATION=prerelease bash scripts/publish-unsigned-macos.sh
```

`publish-unsigned-macos.sh` validates the archive with `test -s` and `unzip -t`, creates or updates the target tag's draft release, then dispatches `macos-signed.yml` with `variant`, `publication`, `tag`, `source_release`, and `source_asset`. Release tags must start with `v`; dev tags must start with `dev-`. The Action only signs, verifies, optionally notarizes, and publishes. Keep signing materials outside the repository and provide them only through the documented GitHub secrets. Local unsigned/ad-hoc bundles are intermediate artifacts; credentialed signing must use `CODESIGN_IDENTITY` and `CODESIGN_REQUIRED=1`. `CODESIGN_SKIP=1` and `CODESIGN_REQUIRED` are mutually exclusive.
