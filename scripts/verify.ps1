# Convenience entry; the Python runner owns all isolation and report logic.
param([switch]$KeepRunning)
$ErrorActionPreference = 'Stop'
if (-not (Get-Command python -ErrorAction SilentlyContinue)) { throw 'Python 3.11+ is required.' }
$arguments = @((Join-Path $PSScriptRoot 'verify.py'))
if ($KeepRunning) { $arguments += '--keep-running' }
& python @arguments
if ($LASTEXITCODE -ne 0) { throw 'Acceptance failed. See the report path printed above; retained volumes were not deleted.' }
