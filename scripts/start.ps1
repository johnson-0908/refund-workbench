[CmdletBinding()]
param(
  [Alias('Host')][string]$BindAddress,
  [ValidateRange(1,65535)][int]$Port,
  [string]$Origin,
  [ValidatePattern('^[a-z0-9][a-z0-9_-]{0,62}$')][string]$Project = 'yishuld-refund-app',
  [switch]$Pull,
  [switch]$Check
)
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$runtime = Join-Path $root '.runtime'
$config = Join-Path $runtime "start-$Project.env"

function New-Password {
  $bytes = New-Object byte[] 32
  $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
  try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
  return ([BitConverter]::ToString($bytes)).Replace('-', '').ToLowerInvariant()
}
function Assert-Address([string]$Value) {
  $parts = $Value.Split('.')
  if ($parts.Count -ne 4) { throw 'Host must be an IPv4 bind address; use Origin for the browser hostname.' }
  foreach ($part in $parts) {
    if ($part -notmatch '^(0|[1-9][0-9]{0,2})$' -or [int]$part -gt 255) { throw 'Invalid IPv4 bind address.' }
  }
}
function Assert-Origin([string]$Value) {
  $uri = $null
  if ($Value -cnotmatch '^https?://(?:[A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(?::[0-9]{1,5})?$' -or
      -not [Uri]::TryCreate($Value, [UriKind]::Absolute, [ref]$uri) -or
      $uri.Scheme -notin @('http','https') -or -not $uri.Host -or $uri.UserInfo -or
      $uri.Query -or $uri.Fragment -or $uri.Port -lt 1 -or $uri.AbsolutePath -ne '/' -or $Value.EndsWith('/') -or
      $Value -match '[\s*]') { throw 'Origin must be an exact http(s)://host:port without path, slash, or wildcard.' }
}
function Assert-TransportConfiguration([string]$Value, [string]$Secure) {
  Assert-Origin $Value
  if ($Value.StartsWith('https://') -and $Secure -ne 'true') { throw 'HTTPS Origin requires SESSION_COOKIE_SECURE=true.' }
  if ($Value.StartsWith('http://') -and $Secure -eq 'true') {
    # Inspect the original host, not Uri.Host: .NET normalizes shortened IPv4
    # spellings (127.1) that Go correctly does not treat as a loopback IP.
    $hostName = ($Value -replace '^http://','') -replace ':[0-9]+$',''
    $loopback = $hostName -ceq 'localhost'
    $ip = $null
    if ($hostName.StartsWith('[') -and $hostName.EndsWith(']')) {
      $loopback = [Net.IPAddress]::TryParse($hostName.Trim('[',']'),[ref]$ip) -and [Net.IPAddress]::IsLoopback($ip)
    } elseif ($hostName -match '^127\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})$') {
      $loopback = [Net.IPAddress]::TryParse($hostName,[ref]$ip) -and [Net.IPAddress]::IsLoopback($ip)
    }
    if (-not $loopback) { throw 'Non-loopback HTTP Origin requires SESSION_COOKIE_SECURE=false; use HTTPS for secure cookies.' }
  }
}
function Invoke-Docker([string[]]$DockerArgs) {
  & docker @DockerArgs
  if ($LASTEXITCODE -ne 0) { throw "Docker command failed (exit $LASTEXITCODE); no unrelated process was stopped." }
}
function Assert-NoSourceLinks([string]$Path) {
  $current = [IO.Path]::GetFullPath($Path)
  while ($current) {
    if (Test-Path -LiteralPath $current) {
      if ((Get-Item -LiteralPath $current -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) {
        throw 'Source/build paths must not contain symbolic links or junctions.'
      }
    }
    $current = [IO.Path]::GetDirectoryName($current)
  }
}
function New-SourceBuildContext {
  # BuildKit versions can put the absolute context path into ASCII-only gRPC
  # headers. Stage only explicitly allowed source inputs, never .runtime/.env.
  $preferred = Join-Path ([IO.Path]::GetPathRoot($root)) 'code'
  if ($env:REFUND_BUILD_ROOT) {
    $absolute = if ($env:OS -eq 'Windows_NT') { $env:REFUND_BUILD_ROOT -match '^(?:[A-Za-z]:[\\/]|\\\\[^\\/]+[\\/][^\\/]+(?:[\\/]|$))' } else { $env:REFUND_BUILD_ROOT.StartsWith('/') }
    if (-not $absolute) { throw 'REFUND_BUILD_ROOT must be an absolute path, not a relative or drive-relative path.' }
    $base = [IO.Path]::GetFullPath($env:REFUND_BUILD_ROOT)
  }
  elseif ((Test-Path -LiteralPath $preferred) -and $preferred -notmatch '[^\x20-\x7E]') { $base = Join-Path $preferred 'refund-workbench-build' }
  else { $base = Join-Path ([IO.Path]::GetTempPath()) 'refund-workbench-build' }
  if ($base -match '[^\x20-\x7E]') { throw 'Set REFUND_BUILD_ROOT to a writable ASCII-only absolute build directory.' }
  Assert-NoSourceLinks $base
  [IO.Directory]::CreateDirectory($base) | Out-Null
  if ((Get-Item -LiteralPath $base).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Build staging root must not be a link/junction.' }
  $stage = Join-Path $base ($Project + '-' + [Guid]::NewGuid().ToString('N'))
  [IO.Directory]::CreateDirectory($stage) | Out-Null
  $files = @('.dockerignore','Dockerfile.backend','Dockerfile.web','backend/go.mod','backend/go.sum','web/package.json','web/package-lock.json','web/index.html','web/tsconfig.json','web/vite.config.ts','deploy/nginx.conf')
  foreach ($folder in @('backend/cmd','backend/internal','web/src')) {
    Assert-NoSourceLinks (Join-Path $root $folder)
    foreach ($file in (Get-ChildItem -LiteralPath (Join-Path $root $folder) -Recurse -Force)) {
      if ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Source build inputs must not be symbolic links.' }
      if ($file.PSIsContainer) { continue }
      if ($folder -ne 'web/src' -and $file.Extension -ne '.go') { continue }
      if ($folder -eq 'web/src' -and $file.Extension -notin @('.ts','.vue','.css')) { continue }
      if ($file.Name -like '.env*') { continue }
      $files += $file.FullName.Substring($root.Length + 1)
    }
  }
  foreach ($relative in $files) {
    $source = Join-Path $root $relative
    Assert-NoSourceLinks $source
    $target = Join-Path $stage $relative
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($target)) | Out-Null
    Copy-Item -LiteralPath $source -Destination $target
  }
  @{ build_context=$stage; source_files=$files.Count; project=$Project } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $runtime "build-$Project.json") -Encoding utf8
  Write-Host "Source-only build context retained for diagnosis: $stage"
  return $stage
}

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'Docker CLI is required. Start Docker Desktop / Docker Engine first.' }
& docker info --format '{{.ServerVersion}}' *> $null
if ($LASTEXITCODE -ne 0) { throw 'Docker Engine is unavailable. Start it, then rerun this command.' }
& docker compose version *> $null
if ($LASTEXITCODE -ne 0) { throw 'Docker Compose v2+ is required.' }

if (-not (Test-Path -LiteralPath $config)) {
  $existingVolumes = (& docker volume ls --quiet --filter "label=com.docker.compose.project=$Project" 2>$null | Out-String).Trim()
  if ($LASTEXITCODE -ne 0) { throw 'Could not inspect this project volumes safely.' }
  if ($existingVolumes) { throw 'Project volumes already exist but its saved config is missing. Restore that config; new passwords were not generated.' }
  if (-not $BindAddress) { $BindAddress = '127.0.0.1' }
  if ($BindAddress -eq 'localhost') { $BindAddress = '127.0.0.1' }
  if (-not $Port) { $Port = 8080 }
  Assert-Address $BindAddress
  if (-not $Origin) {
    if (-not $BindAddress.StartsWith('127.')) { throw 'A non-loopback Host requires explicit -Origin http://server-ip:port.' }
    $Origin = "http://${BindAddress}:$Port"
  }
  Assert-Origin $Origin
  $probe = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Parse($BindAddress), $Port)
  try { $probe.Server.ExclusiveAddressUse = $true; $probe.Start() }
  catch { throw "Bind address/port unavailable: ${BindAddress}:$Port. No configuration or process was changed." }
  finally { $probe.Stop() }
  [IO.Directory]::CreateDirectory($runtime) | Out-Null
  $password = New-Password
  do { $rootPassword = New-Password } while ($rootPassword -eq $password)
  $secure = if ($Origin.StartsWith('https://')) { 'true' } else { 'false' }
  $content = @"
# Private, generated once. Keep this file and the same project for future starts.
DEPLOY_NAMESPACE=$Project
BIND_IP=$BindAddress
WEB_PORT=$Port
APP_ORIGIN=$Origin
SESSION_COOKIE_SECURE=$secure
MYSQL_ROOT_PASSWORD=$rootPassword
MYSQL_PASSWORD=$password
ENABLE_DATA_RESET=true
REGISTRY=crpi-vpvynwydljgcai3w.cn-guangzhou.personal.cr.aliyuncs.com
IMAGE_NAMESPACE=pandacount
IMAGE_TAG=0.1.0
"@
  $temp = Join-Path $runtime ('.start-' + [Guid]::NewGuid().ToString('N') + '.tmp')
  try {
    # Apply private permissions atomically at file birth. Creating a publicly
    # readable empty file then tightening its ACL would leave a handle race.
    if ($env:OS -eq 'Windows_NT') {
      $acl = New-Object Security.AccessControl.FileSecurity
      $acl.SetAccessRuleProtection($true, $false)
      $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
      $acl.SetOwner($sid)
      $rule = New-Object Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', 'Allow')
      $acl.SetAccessRule($rule)
      if ('System.IO.FileSystemAclExtensions' -as [type]) {
        $empty = [IO.FileSystemAclExtensions]::Create([IO.FileInfo]$temp, [IO.FileMode]::CreateNew, [Security.AccessControl.FileSystemRights]::FullControl, [IO.FileShare]::None, 4096, [IO.FileOptions]::None, $acl)
      } else {
        # Windows PowerShell / .NET Framework exposes the ACL constructor.
        $empty = New-Object IO.FileStream($temp, [IO.FileMode]::CreateNew, [Security.AccessControl.FileSystemRights]::FullControl, [IO.FileShare]::None, 4096, [IO.FileOptions]::None, $acl)
      }
      $empty.Dispose()
      $createdAcl = Get-Acl -LiteralPath $temp
      $unexpected = @($createdAcl.Access | Where-Object {
        $_.IsInherited -or $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -ne $sid.Value
      })
      if (-not $createdAcl.AreAccessRulesProtected -or $unexpected.Count -ne 0) { throw 'Could not verify private permissions on the generated config file.' }
    } else {
      # PowerShell 7.3+ supports atomic Unix create permissions. Older Unix
      # runtimes fail closed; the Bash launcher is the portable alternative.
      $options = New-Object IO.FileStreamOptions
      $options.Mode = [IO.FileMode]::CreateNew
      $options.Access = [IO.FileAccess]::Write
      $options.Share = [IO.FileShare]::None
      $options.UnixCreateMode = [IO.UnixFileMode]::UserRead -bor [IO.UnixFileMode]::UserWrite
      $empty = [IO.FileStream]::new($temp, $options)
      $empty.Dispose()
      & chmod 600 -- $temp
      if ($LASTEXITCODE -ne 0) { throw 'Could not protect the generated config file.' }
    }
    [IO.File]::WriteAllText($temp, $content + "`n", (New-Object Text.UTF8Encoding $false))
    # Move without overwrite: a concurrent starter must never replace secrets.
    try { [IO.File]::Move($temp, $config) } catch { if (-not (Test-Path -LiteralPath $config)) { throw } }
  } finally {
    if (Test-Path -LiteralPath $temp) { Remove-Item -LiteralPath $temp }
    $password = $null; $rootPassword = $null; $content = $null
  }
}

$values = @{}
foreach ($line in [IO.File]::ReadAllLines($config)) {
  if (-not $line.Trim() -or $line.TrimStart().StartsWith('#')) { continue }
  if ($line -notmatch '^([A-Z][A-Z0-9_]*)=([^\r\n]*)$' -or $values.ContainsKey($Matches[1])) {
    throw 'Invalid or duplicate setting in saved config; it was not changed.'
  }
  $values[$Matches[1]] = $Matches[2]
}
foreach ($key in @('DEPLOY_NAMESPACE','BIND_IP','WEB_PORT','APP_ORIGIN','MYSQL_ROOT_PASSWORD','MYSQL_PASSWORD','SESSION_COOKIE_SECURE','ENABLE_DATA_RESET')) {
  if (-not $values.ContainsKey($key)) { throw "Saved config is missing $key; it was not changed." }
}
if ($values.DEPLOY_NAMESPACE -ne $Project) { throw 'Saved config project does not match the requested project.' }
Assert-Address $values.BIND_IP
Assert-Origin $values.APP_ORIGIN
if ($values.WEB_PORT -notmatch '^[0-9]{1,5}$' -or [int]$values.WEB_PORT -lt 1 -or [int]$values.WEB_PORT -gt 65535) { throw 'Saved port is invalid.' }
foreach ($key in @('MYSQL_ROOT_PASSWORD','MYSQL_PASSWORD')) {
  if ($values[$key] -cnotmatch '^[0-9a-fA-F]{64}$') { throw "Saved $key must contain 64 random hex characters; it was not changed." }
}
if ($values.MYSQL_PASSWORD -eq $values.MYSQL_ROOT_PASSWORD) { throw 'Saved SQL passwords must be different.' }
foreach ($key in @('SESSION_COOKIE_SECURE','ENABLE_DATA_RESET')) {
  if ($values[$key] -cnotin @('true','false')) { throw "Saved $key must be true or false." }
}
Assert-TransportConfiguration $values.APP_ORIGIN $values.SESSION_COOKIE_SECURE
if ($BindAddress -eq 'localhost') { $BindAddress = '127.0.0.1' }
if (($BindAddress -and $BindAddress -ne $values.BIND_IP) -or
    ($Port -and $Port -ne [int]$values.WEB_PORT) -or ($Origin -and $Origin -ne $values.APP_ORIGIN)) {
  throw "Saved configuration differs from the requested address. It was not overwritten. Edit $config deliberately, or use a new -Project."
}
if (-not $values.BIND_IP.StartsWith('127.')) {
  Write-Warning 'Remote access enabled: predefined identities are not real SSO. Restrict firewall/security-group sources; no firewall was changed.'
}
if ($values.APP_ORIGIN.StartsWith('https://')) { Write-Warning 'This Compose publishes HTTP only. HTTPS requires your separately configured TLS reverse proxy.' }

$compose = @('compose', '--project-name', $Project, '--env-file', $config, '-f', (Join-Path $root 'compose.deploy.yaml'))
if (-not $Pull) { $compose += @('-f', (Join-Path $root 'compose.build.yaml')) }
# Neutralize inherited interpolation values from other projects. Never print the
# expanded config or passwords; restore the caller process environment afterward.
$names = @('REGISTRY','IMAGE_NAMESPACE','IMAGE_TAG','DEPLOY_NAMESPACE','BIND_IP','WEB_PORT','APP_ORIGIN','MYSQL_ROOT_PASSWORD','MYSQL_PASSWORD','ENABLE_DATA_RESET','SESSION_COOKIE_SECURE','MYSQL_MEMORY_LIMIT','REDIS_MEMORY_LIMIT','KAFKA_MEMORY_LIMIT','BACKEND_MEMORY_LIMIT','WEB_MEMORY_LIMIT','BUILD_CONTEXT')
$previous = @{}
try {
  foreach ($name in $names) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name)
    [Environment]::SetEnvironmentVariable($name, $values[$name], 'Process')
  }
  Invoke-Docker ($compose + @('config','--quiet'))
  $webID = (& docker @compose ps --status running --quiet web 2>$null | Out-String).Trim()
  if (-not $webID) {
    $listener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Parse($values.BIND_IP), [int]$values.WEB_PORT)
    try { $listener.Server.ExclusiveAddressUse = $true; $listener.Start() }
    catch { throw "Bind address/port unavailable: $($values.BIND_IP):$($values.WEB_PORT). No process was killed; choose a free port deliberately." }
    finally { $listener.Stop() }
  }
  if ($Check) { Write-Host "Configuration and port checks passed: $Project (no containers started)."; return }
  if ($Pull) {
    Invoke-Docker ($compose + @('pull'))
    Invoke-Docker ($compose + @('up','-d','--wait','--wait-timeout','300'))
  } else {
    if ($root -match '[^\x20-\x7E]') { $env:BUILD_CONTEXT = New-SourceBuildContext }
    Invoke-Docker ($compose + @('up','-d','--build','--wait','--wait-timeout','300'))
  }
  Write-Host "Ready: $($values.APP_ORIGIN)"
  Write-Host "Project: $Project; private configuration preserved at $config"
  Write-Host "Stop without deleting data: docker compose --project-name $Project --env-file `"$config`" -f `"$(Join-Path $root 'compose.deploy.yaml')`" stop"
} finally {
  foreach ($name in $names) { if ($previous.ContainsKey($name)) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') } }
}
