param([string]$Output, [switch]$Desktop)
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
Set-Location $root
if (!$Output) { $Output = Join-Path $root ('target/test-runs/windows-native-' + [guid]::NewGuid().ToString('N')) }
New-Item -ItemType Directory -Force $Output | Out-Null
$Output = (Resolve-Path $Output).Path
$env:WATER_WINDOWS_SERVER_BINARY = Join-Path $root 'target/go-windows/dev/amd64/package/water-srv-dev.exe'
if (!(Test-Path $env:WATER_WINDOWS_SERVER_BINARY)) { throw 'Build the native dev package before running regressions' }
# Portable suites run in full. Unix shell, Unix updater and macOS shortcut
# fixtures stay in the host regression; Windows behavior has native equivalents.
$checks = @()
foreach ($package in @('goagent','gobridge','goclient','goconfig','gomodel','goplatform','goprotocol','gopty','gosupervisor','goterminal','govt','xterm')) {
    $checks += @{ package = $package; pattern = '.' }
}
$checks += @{ package = 'goremote'; pattern = '^TestNativeSSH(Config|Host|Accept|Lease)|^TestPowerShell' }
$checks += @{ package = 'goserver'; pattern = '^TestWindows|^TestWebAutostart|^TestWebAuthorization|^TestWebPairing|^TestWebRevocation|^TestWebSingleHTTPS|^TestWebTLSImport' }
$checks += @{ package = 'goui'; pattern = '^TestWindows|^TestSelectedLocalTLS|^TestWebAutostart|^TestWebPairing' }
$checks += @{ package = 'goupdate'; pattern = '^TestWindows' }
$results = @()
$go = (Get-Command go -ErrorAction Stop).Source
foreach ($check in $checks) {
    $package = $check.package
    $log = Join-Path $Output "$package.jsonl"
    $errors = Join-Path $Output "$package.stderr.log"
    $process = Start-Process -FilePath $go -ArgumentList @('test',"./internal/$package","-run=$($check.pattern)",'-json','-count=1','-timeout=60s') -NoNewWindow -Wait -PassThru -RedirectStandardOutput $log -RedirectStandardError $errors
    $events = @(Get-Content $log | ForEach-Object { $_ | ConvertFrom-Json })
    $passed = @($events | Where-Object { $_.Action -eq 'pass' -and $_.Test }).Count
    $skipped = @($events | Where-Object { $_.Action -eq 'skip' -and $_.Test } | ForEach-Object { $_.Test })
    $results += @{ package = $package; exit_code = $process.ExitCode; passed_tests = $passed; skipped_tests = $skipped; log = $log; stderr = $errors }
    $results | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $Output 'summary.json')
    if ($process.ExitCode -or !$passed) { throw "Native Windows regression failed or ran no tests: $package ($log)" }
}
if ($Desktop) { & "$PSScriptRoot/run-windows-desktop-test.ps1" -Output (Join-Path $Output 'desktop') }
