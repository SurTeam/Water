param([ValidateSet('amd64','arm64')][string]$Arch = 'amd64', [string]$Python = "$HOME\.venv\Scripts\python.exe")
$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'Run this packaging entry on Windows' }
$root = Split-Path $PSScriptRoot -Parent
Set-Location $root
$variant = if ($env:WATER_APP_VARIANT) { $env:WATER_APP_VARIANT } else { 'dev' }
if ($variant -notin @('dev','release')) { throw 'Invalid WATER_APP_VARIANT' }
$version = if ($env:WATER_APP_VERSION) { $env:WATER_APP_VERSION } else { (Get-Content VERSION -Raw).Trim() }
$env:WATER_APP_VARIANT = $variant
$env:WATER_APP_VERSION = $version
& $Python scripts/build-go-embedded-servers.py
if ($LASTEXITCODE) { throw 'Payload build failed' }
$manifest = Get-Content internal/goremote/payloads/manifest.json -Raw | ConvertFrom-Json
$revision = $manifest.identity.server_revision
$flags = "-X github.com/SurTeam/Water/internal/gobuild.Variant=$variant -X github.com/SurTeam/Water/internal/gobuild.Version=$version -X github.com/SurTeam/Water/internal/gobuild.ServerRevision=$revision"
if ($variant -eq 'release') { $flags = '-s -w ' + $flags }
$stage = Join-Path $root "target/go-windows/$variant/$Arch/package"
if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
New-Item -ItemType Directory -Force $stage | Out-Null
$env:GOOS = 'windows'; $env:GOARCH = $Arch; $env:CGO_ENABLED = '0'
$suffix = if ($variant -eq 'dev') { '-dev' } else { '' }
$gui = "water$suffix.exe"
$server = if ($variant -eq 'dev') { 'water-srv-dev.exe' } else { 'water-server.exe' }
foreach ($item in @(@('water',$gui), @('water-server',$server), @('water-update',"water-update$suffix.exe"))) {
    & go build -trimpath -ldflags $flags -o (Join-Path $stage $item[1]) "./cmd/$($item[0])"
    if ($LASTEXITCODE) { throw "Build failed: $($item[0])" }
}
Copy-Item (Join-Path $stage $server) (Join-Path $stage "water-pty$suffix.exe") -Force
$licenses = @('third_party/ebiten/LICENSE','third_party/ebiten/NOTICE.md','third_party/ebiten/WATER_CHANGES.md','third_party/typesetting/LICENSE','third_party/typesetting/WATER_CHANGES.md','third_party/typesetting/harfbuzz/LICENSE','internal/xterm/LICENSE','internal/goserver/webassets/xterm-LICENSE.txt','internal/goserver/webassets/jsqr-LICENSE.txt','internal/goserver/webassets/fit-LICENSE.txt')
foreach ($file in $licenses) {
    $dest = Join-Path $stage "Licenses/$file"
    New-Item -ItemType Directory -Force (Split-Path $dest -Parent) | Out-Null
    Copy-Item $file $dest -Force
}
$goRoot = (& go env GOROOT).Trim()
if ($LASTEXITCODE) { throw 'Cannot locate Go toolchain licenses' }
foreach ($name in @('LICENSE','PATENTS')) {
    $source = Join-Path $goRoot $name
    if (!(Test-Path -LiteralPath $source -PathType Leaf)) { $source = Join-Path (Split-Path $goRoot -Parent) $name }
    if ($name -eq 'LICENSE' -and !(Test-Path -LiteralPath $source -PathType Leaf)) { throw 'Go toolchain LICENSE not found' }
    if (Test-Path -LiteralPath $source -PathType Leaf) {
        $destination = Join-Path $stage 'Licenses/go-stdlib'
        New-Item -ItemType Directory -Force $destination | Out-Null
        Copy-Item -LiteralPath $source -Destination $destination -Force
    }
}
# Include notices from the resolved Go modules as well as the local forks.
$moduleDirectories = & go list -m -f '{{.Path}}|{{.Dir}}' all
if ($LASTEXITCODE) { throw 'Cannot enumerate dependency licenses' }
foreach ($entry in $moduleDirectories) {
    $parts = $entry -split '\|', 2
    if ($parts.Count -ne 2 -or !$parts[1]) { continue }
    $destination = Join-Path $stage ("Licenses/go/" + $parts[0])
    foreach ($notice in (Get-ChildItem -LiteralPath $parts[1] -File | Where-Object { $_.Name -match '^(LICENSE|COPYING|NOTICE)' })) {
        New-Item -ItemType Directory -Force $destination | Out-Null
        Copy-Item -LiteralPath $notice.FullName -Destination $destination -Force
    }
}
Copy-Item internal/goremote/payloads/manifest.json (Join-Path $stage 'payload-manifest.json') -Force
New-Item -ItemType Directory -Force dist | Out-Null
$archive = Join-Path $root "dist/water$suffix-$version-windows-$Arch.zip"
Compress-Archive -Path "$stage/*" -DestinationPath $archive -Force
Get-FileHash $archive -Algorithm SHA256 | Format-List
Write-Output "Built $archive"
