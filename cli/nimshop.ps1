# nim.shop CLI (Windows) — start / stop / status / logs for a release install.
#
#   nimshop start     start backend + frontend (one process)
#   nimshop stop
#   nimshop restart
#   nimshop status
#   nimshop logs [n]
#
# Linux/macOS installs use cli/nimshop (bash) instead.
[CmdletBinding()]
param(
  [Parameter(Position = 0)][string]$Command = 'help',
  [Parameter(Position = 1)][int]$Lines = 80
)

$ErrorActionPreference = 'Stop'

$Root    = Split-Path -Parent $PSScriptRoot
$Bin     = Join-Path $Root 'nimshop-server.exe'
$EnvFile = Join-Path $Root 'backend\.env'
$PidFile = Join-Path $Root 'nimshop.pid'
$LogDir  = Join-Path $Root 'logs'
$LogFile = Join-Path $LogDir 'nimshop.log'

function Die([string]$Message) { Write-Error "nimshop: $Message"; exit 1 }

function Get-ServerProcess {
  if (-not (Test-Path $PidFile)) { return $null }
  $id = 0
  if (-not [int]::TryParse((Get-Content $PidFile -Raw).Trim(), [ref]$id)) { return $null }
  return Get-Process -Id $id -ErrorAction SilentlyContinue
}

function Get-ListenPort {
  if (-not (Test-Path $EnvFile)) { return '8085' }
  $line = Select-String -Path $EnvFile -Pattern '^LISTEN_ADDR=:(\d+)' | Select-Object -First 1
  if ($line) { return $line.Matches[0].Groups[1].Value }
  return '8085'
}

switch ($Command) {
  'start' {
    if (-not (Test-Path $Bin)) { Die "binary missing: $Bin (re-run install.ps1)" }
    if (-not (Test-Path $EnvFile)) { Die "missing $EnvFile" }
    $existing = Get-ServerProcess
    if ($existing) { "already running (pid $($existing.Id))"; exit 0 }
    New-Item -ItemType Directory -Force -Path $LogDir | Out-Null
    New-Item -ItemType Directory -Force -Path (Join-Path $Root 'backend\data\badger') | Out-Null
    $errFile = Join-Path $LogDir 'nimshop.err.log'
    $proc = Start-Process -FilePath $Bin -WorkingDirectory (Join-Path $Root 'backend') `
      -RedirectStandardOutput $LogFile -RedirectStandardError $errFile `
      -WindowStyle Hidden -PassThru
    Set-Content -Path $PidFile -Value $proc.Id -NoNewline
    Start-Sleep -Milliseconds 600
    if (Get-Process -Id $proc.Id -ErrorAction SilentlyContinue) {
      "nim.shop started (pid $($proc.Id))  ->  http://127.0.0.1:$(Get-ListenPort)" # DevSkim: ignore DS162092 the CLI stack listens on loopback by design
    } else {
      if (Test-Path $LogFile) { Get-Content $LogFile -Tail 40 | Write-Error }
      Die "server exited — see $LogFile"
    }
  }
  'stop' {
    $proc = Get-ServerProcess
    if ($proc) {
      Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
      Remove-Item $PidFile -ErrorAction SilentlyContinue
      'stopped'
    } else {
      Remove-Item $PidFile -ErrorAction SilentlyContinue
      'not running'
    }
  }
  'restart' {
    & $PSCommandPath stop
    & $PSCommandPath start
  }
  'status' {
    $proc = Get-ServerProcess
    if ($proc) { "running  pid=$($proc.Id)" } else { 'stopped'; exit 1 }
  }
  'logs' {
    New-Item -ItemType Directory -Force -Path $LogDir | Out-Null
    if (Test-Path $LogFile) { Get-Content $LogFile -Tail $Lines -Wait } else { 'no logs yet' }
  }
  default {
    @"
nim.shop CLI

  nimshop start     start backend + frontend (one process)
  nimshop stop
  nimshop restart
  nimshop status
  nimshop logs [n]

Install dir: $Root
"@
  }
}
