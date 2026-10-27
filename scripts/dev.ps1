$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$runtime = Join-Path $root '.runtime'
$bin = Join-Path $root 'bin'
New-Item -ItemType Directory -Force -Path $runtime,$bin | Out-Null
$manifest = Join-Path $runtime 'dev-processes.json'
if (Test-Path -LiteralPath $manifest) {
  throw 'A previous dev manifest exists. Run ./scripts/stop-dev.ps1 first (only stops this project-owned processes).'
}
foreach ($port in @(18080,5173)) {
  if (Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue) {
    throw "Port $port is already occupied; no process will be stopped automatically."
  }
}
Push-Location -LiteralPath (Join-Path $root 'backend')
try {
  go build -o (Join-Path $bin 'refund-server.exe') ./cmd/server
  if ($LASTEXITCODE -ne 0) { throw 'Backend build failed' }
} finally { Pop-Location }
if (!(Test-Path -LiteralPath (Join-Path $root 'web/node_modules/vite/bin/vite.js'))) { throw 'Run ./scripts/setup.ps1 first' }
$started = @()
try {
  # Browser development always uses real sessions, never the exercise's static tokens.
  $oldBearer = [Environment]::GetEnvironmentVariable('ENABLE_DEMO_BEARER')
  $oldReset = [Environment]::GetEnvironmentVariable('ENABLE_DATA_RESET')
  try {
    $env:ENABLE_DEMO_BEARER = 'false'
    # Explicit local development capability; backend also checks loopback DB/schema.
    if ($null -eq $oldReset) { $env:ENABLE_DATA_RESET = 'true' }
    $server = Start-Process -FilePath (Join-Path $bin 'refund-server.exe') -WorkingDirectory $root -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $runtime 'server.stdout.log') -RedirectStandardError (Join-Path $runtime 'server.stderr.log')
  } finally {
    [Environment]::SetEnvironmentVariable('ENABLE_DEMO_BEARER', $oldBearer, 'Process')
    [Environment]::SetEnvironmentVariable('ENABLE_DATA_RESET', $oldReset, 'Process')
  }
  $started += $server
  $vite = Join-Path $root 'web/node_modules/vite/bin/vite.js'
  $web = Start-Process -FilePath (Get-Command node).Source -ArgumentList @("`"$vite`"",'--host','127.0.0.1','--port','5173','--strictPort') -WorkingDirectory (Join-Path $root 'web') -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $runtime 'web.stdout.log') -RedirectStandardError (Join-Path $runtime 'web.stderr.log')
  $started += $web
  @($started | ForEach-Object { @{ pid=$_.Id; startedAt=$_.StartTime.ToUniversalTime().ToString('o'); executable=$_.Path } }) | ConvertTo-Json | Set-Content -LiteralPath $manifest -Encoding utf8
  foreach ($url in @('http://127.0.0.1:18080/api/health','http://127.0.0.1:5173')) {
    $ok = $false
    for ($i=0; $i -lt 45; $i++) {
      try { if ((Invoke-WebRequest -Uri $url -TimeoutSec 2).StatusCode -eq 200) { $ok=$true; break } } catch {}
      if (@($started | Where-Object { $_.HasExited }).Count) { throw "Dev process exited. Inspect $runtime logs." }
      Start-Sleep -Milliseconds 500
    }
    if (!$ok) { throw "Timed out waiting for $url. Inspect $runtime logs." }
  }
  Write-Host 'Web: http://127.0.0.1:5173'
  Write-Host 'API: http://127.0.0.1:18080/api/health'
  Write-Host 'Stop application processes: ./scripts/stop-dev.ps1 (Docker/data preserved)'
} catch {
  foreach ($p in $started) { if (!$p.HasExited) { $p.Kill(); $p.WaitForExit() } }
  if (Test-Path -LiteralPath $manifest) { Remove-Item -LiteralPath $manifest }
  throw
}
