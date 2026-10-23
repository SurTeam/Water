# 构建打包与发布

实现入口：`internal/gobuild/`、`internal/goupdate/`、`scripts/build-go-*.sh`、`scripts/publish-unsigned-macos.sh` 和 `.github/workflows/macos-signed.yml`。构建命令、更新安装限制与签名流程在本文统一维护。

版本、提交、push、tag 与发布分别需要相应用户授权。文档随已确认实现更新的时机见 [AGENTS.md](../AGENTS.md#文档更新时机)。

## 构建与变体

- 只有 `dev` 和 `release` 两种运行身份。普通 `go build` 使用 dev；打包脚本通过 `WATER_APP_VARIANT` 和 linker flags 设置身份，不从优化参数推断身份。macOS/Linux 打包产物分别在 `target/go-app/<variant>` 和 `target/go-linux/<variant>`。
- dev 使用 `water-dev` namespace：`/tmp/water-dev.sock`、`~/.config/water-dev/config.json`、Bundle ID `dev.water.terminal.dev`、GUI/server/PTY supervisor 进程 `water-dev`/`water-srv-dev`/`water-pty-dev`。release 使用 `water` namespace：`/tmp/water.sock`、`~/.config/water/config.json`、Bundle ID `dev.water.terminal`、GUI/server/PTY supervisor 进程 `water`/`water-server`/`water-pty`。
- 默认配置根目录依次采用绝对路径 `$XDG_CONFIG_HOME`、兼容变量 `$XDG_CONFIG`、`~/.config`。macOS 首次读取新路径时复制旧 Application Support 配置，保留旧文件；目标已存在时不覆盖。
- GUI 在目标平台原生构建；headless server payload 使用 Go 交叉编译。交叉编译成功不等于原生运行验证。构建脚本必须使用本次变体和版本生成的 GUI/server/payload，不能回退到任意已安装的 release server。

macOS app 的 `Contents/Resources/Licenses` 和 Linux archive 的 `Licenses` 携带实际使用的本地依赖许可证、NOTICE 和 Ebitengine 修改说明，由 `scripts/package-licenses.sh` 统一收集。

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

# Native Linux package, on a Linux host.
WATER_APP_VARIANT=release bash scripts/build-go-linux.sh

# Refresh embedded server payloads only.
WATER_APP_VARIANT=dev bash scripts/build-go-embedded-servers.sh
```

裸 go build 使用 internal/gobuild/identity.go 的 dev 身份、源码默认产品版本（当前为 0.3.1）和 development revision，不读取 VERSION。要验证真实版本、更新或 payload 匹配，使用打包脚本或显式 linker flags。下述 VERSION 来源适用于脚本构建。

The packaging scripts build the GUI, dedicated server and four embedded server targets. GUI builds are native to the target desktop platform; headless server payloads use Go cross-compilation. The app bundle must contain payloads whose variant and version match the GUI. Product version comes from the root `VERSION` file; use the same `WATER_APP_VERSION` for packaging and publishing when overriding it. `WATER_APP_VARIANT` selects dev/release and is injected through linker flags. macOS build directories are `target/go-app/<variant>`; Linux uses `target/go-linux/<variant>`; archives go to `dist`.
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
Publish Linux packages to the matching release after building them natively.
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

An independent installer waits for the GUI to exit, retains the old app/binaries,
and rolls back if replacement fails or the new GUI fails to render a live frame
within 20 seconds. The control socket, config and SSH destinations are preserved.
Detached terminal servers continue running during the GUI restart, including
when `detach_on_quit` is false. An embedded local server blocks installation;
enable `server.detached` and restart Water before installing updates. Remote
connections reconnect through the existing transport. Protocol incompatibility
with a retained server causes startup failure and rollback rather than server
replacement or session termination.

Failed installations retain `.water-update-*` (release) or `.water-dev-update-*` (dev) staging/backup files and
`install.log` beside the installation. A crashed installer may leave
`.water-update-lock` / `.water-dev-update-lock`; remove that lock only after confirming no installer for
this installation is running. Normal successful installation cleans its files.
No update is installed without clicking **Install and restart**.

Run `~/.venv/bin/python scripts/go-ui-update-smoke.py` with `WATER_BIN` pointing
to the native test binary. It uses an isolated socket/config and real Water
control actions, checks the localized panel/menu and bounded release lookup,
and captures screenshots without installing a release.
## macOS local signing and CI fallback
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
case "$(uname -m)" in arm64) ARCH=arm64 ;; x86_64) ARCH=amd64 ;; esac
test -s "dist/Water-${APP_VERSION}-macOS-${ARCH}.zip"
unzip -tq "dist/Water-${APP_VERSION}-macOS-${ARCH}.zip"
```

The native script signs updater, server, GUI and app using the same runtime and
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
