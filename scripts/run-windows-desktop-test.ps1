param([string]$Python = "$HOME\.venv\Scripts\python.exe", [string]$Binary, [string]$Output, [switch]$CoreOnly)
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
if (!$Binary) { $Binary = Join-Path $root 'target/go-windows/dev/amd64/package/water-dev.exe' }
if (!$Output) { $Output = Join-Path $root ('target/test-runs/windows-' + [guid]::NewGuid().ToString('N')) }
New-Item -ItemType Directory -Force $Output | Out-Null
$session = (Get-Process -Id $PID).SessionId
@{ session_id = $session; launcher_pid = $PID; binary = $Binary } | ConvertTo-Json | Set-Content (Join-Path $Output 'desktop.json')
if ($session -eq 0) { throw 'Desktop test must run in a logged-on interactive session' }
$arguments = @((Join-Path $PSScriptRoot 'go-ui-windows-smoke.py'), '--binary', $Binary, '--output', $Output)
if ($CoreOnly) { $arguments += '--core-only' }
& $Python @arguments
$code = $LASTEXITCODE
Set-Content (Join-Path $Output 'exit-code.txt') $code
exit $code
