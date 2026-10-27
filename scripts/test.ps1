$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$evidence = Join-Path $root 'evidence'
New-Item -ItemType Directory -Force -Path $evidence | Out-Null
$validation = @{ result='RUNNING'; started_at=[DateTime]::UtcNow.ToString('o'); commands=@('go test -count=1 -v ./...','go vet ./...','go build ./...','npm test','npm run build','python scripts/acceptance.py','python scripts/auth_acceptance.py','python scripts/reset_acceptance.py') }
$validation | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $evidence 'validation.json') -Encoding utf8
$old = @{}
foreach ($name in @('TEST_MYSQL_DSN','TEST_REDIS_ADDR','TEST_KAFKA_BROKERS')) { $old[$name] = [Environment]::GetEnvironmentVariable($name) }
try {
  $env:TEST_MYSQL_DSN = 'demo:demo-refund-only@tcp(127.0.0.1:3307)/refund_test?parseTime=true&loc=UTC'
  $env:TEST_REDIS_ADDR = '127.0.0.1:6379'
  $env:TEST_KAFKA_BROKERS = '127.0.0.1:19092'
  Push-Location -LiteralPath (Join-Path $root 'backend')
  try {
    go test -count=1 -v ./... 2>&1 | Tee-Object -FilePath (Join-Path $evidence 'go-tests.txt')
    if ($LASTEXITCODE -ne 0) { throw 'Go tests failed' }
    go vet ./... 2>&1 | Tee-Object -FilePath (Join-Path $evidence 'go-vet.txt')
    if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
    go build ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }
  } finally { Pop-Location }
  Push-Location -LiteralPath (Join-Path $root 'web')
  try {
    npm test 2>&1 | Tee-Object -FilePath (Join-Path $evidence 'web-tests.txt')
    if ($LASTEXITCODE -ne 0) { throw 'Frontend tests failed' }
    npm run build 2>&1 | Tee-Object -FilePath (Join-Path $evidence 'web-build.txt')
    if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed' }
  } finally { Pop-Location }
  python (Join-Path $root 'scripts/acceptance.py')
  if ($LASTEXITCODE -ne 0) { throw 'Real process acceptance failed' }
  python (Join-Path $root 'scripts/auth_acceptance.py')
  if ($LASTEXITCODE -ne 0) { throw 'Session authorization acceptance failed' }
  python (Join-Path $root 'scripts/reset_acceptance.py')
  if ($LASTEXITCODE -ne 0) { throw 'Data reset acceptance failed' }
  $validation.result='PASS'
} catch {
  $validation.result='FAIL'
  $validation.error=$_.Exception.Message.Replace($root,'<project>')
  throw
} finally {
  foreach ($name in $old.Keys) { [Environment]::SetEnvironmentVariable($name, $old[$name], 'Process') }
  foreach ($name in @('go-tests.txt','go-vet.txt','web-tests.txt','web-build.txt')) {
    $path = Join-Path $evidence $name
    if (Test-Path -LiteralPath $path) {
      $content = Get-Content -Raw -LiteralPath $path
      if ($null -ne $content) { $content.Replace($root,'<project>').Replace($root.Replace('\','/'),'<project>') | Set-Content -LiteralPath $path -Encoding utf8 }
    }
  }
  $validation.finished_at=[DateTime]::UtcNow.ToString('o')
  $validation | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $evidence 'validation.json') -Encoding utf8
}
Write-Host 'All checks passed. Re-runnable evidence is in ./evidence.'
