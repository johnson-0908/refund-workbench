$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $root
docker compose up -d --wait --wait-timeout 240
if ($LASTEXITCODE -ne 0) { throw 'Docker middleware startup failed' }
Push-Location -LiteralPath (Join-Path $root 'backend')
try {
  go mod download
  if ($LASTEXITCODE -ne 0) { throw 'go mod download failed' }
  go run ./cmd/admin seed
  if ($LASTEXITCODE -ne 0) { throw 'Non-destructive seed failed' }
} finally { Pop-Location }
Push-Location -LiteralPath (Join-Path $root 'web')
try {
  npm ci
  if ($LASTEXITCODE -ne 0) { throw 'npm ci failed' }
} finally { Pop-Location }
Write-Host 'Ready. Run: ./scripts/dev.ps1'
