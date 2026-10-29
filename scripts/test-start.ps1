# Isolated configuration checks only. Requires Docker running; starts no containers.
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$start = Join-Path $PSScriptRoot 'start.ps1'
$passed = 0
function Check([string]$Name, [bool]$Condition) {
  if (-not $Condition) { throw "FAIL $Name" }
  $script:passed++; Write-Host "PASS $Name"
}
function New-TestProject { return ('refund-deploy-test-config-' + [Guid]::NewGuid().ToString('N').Substring(0,10)) }
function Expect-Error([scriptblock]$Action, [string]$Pattern) {
  $matched = $false
  try { & $Action } catch { $matched = $_.Exception.Message -match $Pattern }
  return $matched
}

$tokens=$null; $errors=$null
[System.Management.Automation.Language.Parser]::ParseFile($start,[ref]$tokens,[ref]$errors) | Out-Null
Check 'PowerShell parser' ($errors.Count -eq 0)
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0)
$listener.Start(); $port=$listener.LocalEndpoint.Port; $listener.Stop()
$project = New-TestProject
& $start -Project $project -Port $port -Check
$file = Join-Path $root ".runtime/start-$project.env"
$hash = (Get-FileHash -LiteralPath $file).Hash
Check 'first config exists with generated independent random passwords' (
  ((Get-Content -LiteralPath $file | Where-Object { $_ -match '^MYSQL_(ROOT_)?PASSWORD=[0-9a-f]{64}$' }).Count -eq 2)
)
& $start -Project $project -Check
Check 'repeat check preserves config bytes' ((Get-FileHash -LiteralPath $file).Hash -eq $hash)
& $start -Project $project -Pull -Check
Check 'pull-mode check also preserves the same config' ((Get-FileHash -LiteralPath $file).Hash -eq $hash)
Check 'different explicit port rejected without overwriting' (Expect-Error { & $start -Project $project -Port 1 -Check } 'differs')
Check 'rejection preserves config bytes' ((Get-FileHash -LiteralPath $file).Hash -eq $hash)

$bad = New-TestProject
Check 'nonloopback requires explicit Origin' (Expect-Error { & $start -Project $bad -Host 0.0.0.0 -Check } 'requires explicit')
Check 'missing Origin did not create config' (-not (Test-Path (Join-Path $root ".runtime/start-$bad.env")))
$busy = New-TestProject
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0)
$listener.Start()
try {
  $busyPort = $listener.LocalEndpoint.Port
  Check 'occupied port fails without killing the listener' (Expect-Error { & $start -Project $busy -Port $busyPort -Check } 'unavailable')
  Check 'occupied port did not create config' (-not (Test-Path (Join-Path $root ".runtime/start-$busy.env")))
  Check 'original listener remains active' ($listener.Server.IsBound)
} finally { $listener.Stop() }

if ($env:OS -eq 'Windows_NT') {
  $access=(Get-Acl -LiteralPath $file).Access
  Check 'new Windows private config has no inherited access rules' (@($access | Where-Object { $_.IsInherited }).Count -eq 0)
  $aclProject=New-TestProject
  $aclProbe=@{empty=$false;privateAtBirth=$false}
  function Get-Acl {
    param([string]$LiteralPath)
    $aclProbe.empty=(Get-Item -LiteralPath $LiteralPath).Length -eq 0
    $bornAcl=Microsoft.PowerShell.Security\Get-Acl -LiteralPath $LiteralPath
    $currentSID=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $aclProbe.privateAtBirth=$bornAcl.AreAccessRulesProtected -and @($bornAcl.Access | Where-Object { $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -ne $currentSID -or $_.IsInherited }).Count -eq 0
    throw 'SYNTHETIC_ACL_FAILURE'
  }
  try {
    Check 'ACL verification failure aborts before writing any password' (Expect-Error { & $start -Project $aclProject -Port $port -Check } 'SYNTHETIC_ACL_FAILURE')
    Check 'temporary file is still empty when restrictive ACL is verified' $aclProbe.empty
    Check 'temporary file was born with protected current-user-only ACL' $aclProbe.privateAtBirth
    Check 'ACL failure leaves no saved secret config' (-not (Test-Path (Join-Path $root ".runtime/start-$aclProject.env")))
  } finally { Remove-Item Function:\Get-Acl }
}
# Load the same staging helper in check-only mode; no container/build is run.
. $start -Project $project -Check
Check 'Origin rejects port zero' (Expect-Error { Assert-Origin 'http://127.0.0.1:0' } 'Origin')
Check 'Origin rejects port above 65535' (Expect-Error { Assert-Origin 'http://127.0.0.1:65536' } 'Origin')
Check 'HTTPS requires secure session cookies in saved config' (Expect-Error { Assert-TransportConfiguration 'https://example.test:443' 'false' } 'requires SESSION_COOKIE_SECURE=true')
Check 'remote HTTP rejects secure cookies before backend startup' (Expect-Error { Assert-TransportConfiguration 'http://192.0.2.1:8080' 'true' } 'Non-loopback')
Check 'lookalike loopback hostname is not trusted' (Expect-Error { Assert-TransportConfiguration 'http://127.attacker.example:8080' 'true' } 'Non-loopback')
Check 'abbreviated IPv4 is not treated as Go loopback' (Expect-Error { Assert-TransportConfiguration 'http://127.1:8080' 'true' } 'Non-loopback')
Assert-TransportConfiguration 'http://127.0.0.1:8080' 'true'
Assert-TransportConfiguration 'http://localhost:8080' 'true'
Assert-TransportConfiguration 'https://example.test:443' 'true'
Assert-TransportConfiguration 'http://192.0.2.1:8080' 'false'
Check 'valid HTTPS and local/remote HTTP transport combinations pass' $true
$stage = New-SourceBuildContext
$staged = @(Get-ChildItem -LiteralPath $stage -File -Recurse)
Check 'BuildKit context is ASCII-only even from a Unicode checkout' ($stage -notmatch '[^\x20-\x7E]')
Check 'context includes the exact required Docker build inputs' ((Test-Path (Join-Path $stage 'Dockerfile.backend')) -and (Test-Path (Join-Path $stage 'deploy/nginx.conf')))
Check 'reviewed logo is staged with identical bytes' ((Get-FileHash -LiteralPath (Join-Path $stage 'web/src/assets/yishuld-logo.png')).Hash -eq (Get-FileHash -LiteralPath (Join-Path $root 'web/src/assets/yishuld-logo.png')).Hash)
Check 'context rejects private config/runtime/dependencies by allowlist' (@($staged | Where-Object { $_.Name -like '.env*' -or $_.FullName -match '[\\/](\.runtime|node_modules|evidence|samples|cases)[\\/]' }).Count -eq 0)
$secrets = @(Get-Content -LiteralPath (Join-Path $root ".runtime/start-$project.env") | Where-Object { $_ -match '^MYSQL_(ROOT_)?PASSWORD=' } | ForEach-Object { ($_ -split '=',2)[1] })
$found = @(Select-String -LiteralPath $staged.FullName -Pattern $secrets -SimpleMatch -List)
Check 'generated SQL secrets are absent from every staged source file' ($found.Count -eq 0)

# Synthetic fixtures are isolated under ignored .runtime, never added to the
# real backend/web source tree. They exercise exclusion, not real secret data.
$originalRoot=$root
$originalBuildRoot=$env:REFUND_BUILD_ROOT
$fixture=Join-Path $runtime ('source-fixture-' + [Guid]::NewGuid().ToString('N'))
try {
  foreach ($relative in @('.dockerignore','Dockerfile.backend','Dockerfile.web','backend/go.mod','backend/go.sum','web/package.json','web/package-lock.json','web/index.html','web/tsconfig.json','web/vite.config.ts','deploy/nginx.conf','backend/cmd/server/main.go','backend/internal/app/app.go','web/src/allowed.ts','web/src/allowed.vue','web/src/allowed.css','web/src/assets/yishuld-logo.png','web/src/assets/unreviewed.png','web/src/synthetic-sensitive.txt','web/src/synthetic-sensitive.json','web/src/.env.synthetic','backend/internal/app/synthetic-sensitive.csv')) {
    $target=Join-Path $fixture $relative
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($target)) | Out-Null
    [IO.File]::WriteAllText($target,'SYNTHETIC_TEST_FIXTURE_NOT_A_REAL_SECRET')
  }
  $root=$fixture
  $fixtureStage=New-SourceBuildContext
  Check 'binary exception permits only the exact reviewed logo path' ((Test-Path (Join-Path $fixtureStage 'web/src/assets/yishuld-logo.png')) -and -not (Test-Path (Join-Path $fixtureStage 'web/src/assets/unreviewed.png')))
  Check 'closed web extension allowlist copies only ts/vue/css' (
    (Test-Path (Join-Path $fixtureStage 'web/src/allowed.ts')) -and
    (Test-Path (Join-Path $fixtureStage 'web/src/allowed.vue')) -and
    (Test-Path (Join-Path $fixtureStage 'web/src/allowed.css')) -and
    @(Get-ChildItem -LiteralPath (Join-Path $fixtureStage 'web/src') -File).Count -eq 3
  )
  Check 'synthetic sensitive txt/json/env/csv files stay outside context' (
    @(Get-ChildItem -LiteralPath $fixtureStage -Recurse -File | Where-Object { $_.Name -like '*sensitive*' -or $_.Name -like '.env*' }).Count -eq 0
  )
  $env:REFUND_BUILD_ROOT='relative-build-context'
  Check 'relative build root rejected before conversion to absolute' (Expect-Error { New-SourceBuildContext | Out-Null } 'absolute path')
  if ($env:OS -eq 'Windows_NT') {
    $env:REFUND_BUILD_ROOT='D:relative-build-context'
    Check 'Windows drive-relative build root also rejected' (Expect-Error { New-SourceBuildContext | Out-Null } 'absolute path')
  }
  [Environment]::SetEnvironmentVariable('REFUND_BUILD_ROOT',$originalBuildRoot,'Process')
  $junction=Join-Path $runtime ('source-fixture-link-' + [Guid]::NewGuid().ToString('N'))
  $linkCreated=$false
  try {
    $kind=if($env:OS -eq 'Windows_NT'){'Junction'}else{'SymbolicLink'}
    New-Item -ItemType $kind -Path $junction -Target $fixture | Out-Null
    $linkCreated=$true
  } catch { Write-Host 'SKIP source-parent link creation unavailable in this environment' }
  if ($linkCreated) {
    $root=$junction
    Check 'source parent junction/symlink is refused, not followed' (Expect-Error { New-SourceBuildContext | Out-Null } 'symbolic links|junctions')
  }
} finally {
  $root=$originalRoot
  [Environment]::SetEnvironmentVariable('REFUND_BUILD_ROOT',$originalBuildRoot,'Process')
}
Write-Host "$passed configuration checks passed. No containers started; generated test configs remain under ignored .runtime/."
