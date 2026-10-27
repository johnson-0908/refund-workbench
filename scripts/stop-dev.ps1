$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$manifest = Join-Path $root '.runtime/dev-processes.json'
if (!(Test-Path -LiteralPath $manifest)) { Write-Host 'No owned dev processes recorded.'; exit 0 }
$records = @(Get-Content -Raw -LiteralPath $manifest | ConvertFrom-Json)
$matched = @()
foreach ($record in $records) {
  $p = Get-Process -Id $record.pid -ErrorAction SilentlyContinue
  if ($null -eq $p) { continue }
  # PowerShell 7.5 parses JSON ISO dates into DateTime, whereas Windows
  # PowerShell leaves strings. Compare UTC ticks rather than display strings.
  if ($p.Path -ne $record.executable -or $p.StartTime.ToUniversalTime().Ticks -ne ([datetime]$record.startedAt).ToUniversalTime().Ticks) {
    throw "PID $($record.pid) no longer matches this project; refusing to stop it."
  }
  $matched += $p
}
foreach ($p in $matched) { Stop-Process -Id $p.Id }
Remove-Item -LiteralPath $manifest
Write-Host 'Stopped project-owned app processes. Docker services and data volumes are untouched.'
