# nimiqshop.io installer (Windows) — downloads the latest GitHub Release (backend
# binary + frontend .zip) and wires a one-command CLI. No Go/Node compile needed.
#
#   irm https://github.com/emrealt34/nimiqshop/releases/latest/download/install.ps1 | iex
#
# Env / params:
#   -Version      release tag (default: latest)
#   -InstallDir   install directory (default: $env:USERPROFILE\nimshop)
#   -Port         listen port (default: 8085)
#
# Linux uses install.sh instead; macOS is not supported.
[CmdletBinding()]
param(
  [string]$Version = 'latest',
  [string]$InstallDir = (Join-Path $env:USERPROFILE 'nimshop'),
  [int]$Port = 8085
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

function Say([string]$Message) { Write-Host "==> $Message" -ForegroundColor Green }
function Die([string]$Message) { Write-Error "ERROR: $Message"; exit 1 }

$Repo = if ($env:NIMSHOP_REPO) { $env:NIMSHOP_REPO } else { 'emrealt34/nimiqshop' }

# Windows only: pick the matching asset (amd64 / arm64).
$archRaw = if ($env:PROCESSOR_ARCHITECTURE) { $env:PROCESSOR_ARCHITECTURE } else { 'AMD64' }
$arch = switch ($archRaw.ToUpper()) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { Die "unsupported architecture: $archRaw" }
}
if (-not $IsWindows -and $PSVersionTable.PSVersion.Major -ge 6) { Die 'this installer is Windows only' }

if ($Version -eq 'latest') {
  $api = "https://api.github.com/repos/$Repo/releases/latest"
} else {
  $api = "https://api.github.com/repos/$Repo/releases/tags/$Version"
}
Say "Looking up $Version on $Repo..."
$release = Invoke-RestMethod -Uri $api -Headers @{ 'User-Agent' = 'nimshop-install' }
$tag = $release.tag_name
if (-not $tag) { Die 'release JSON had no tag_name' }

$asset = "nimshop-windows-$arch.zip"
$url = "https://github.com/$Repo/releases/download/$tag/$asset"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("nimshop-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
  $zip = Join-Path $tmp $asset
  Say "Downloading $asset ($tag)..."
  Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing

  Say "Extracting into $InstallDir..."
  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  Expand-Archive -Path $zip -DestinationPath $tmp -Force
  # The archive holds one top-level directory (nimshop-windows-<arch>/) — flatten it.
  Get-ChildItem -Path (Join-Path $tmp "nimshop-windows-$arch") -Force |
    Copy-Item -Destination $InstallDir -Recurse -Force
}
finally {
  Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

# ---- backend/.env: JWT secret generated here; LISTEN_ADDR/STATIC_DIR for a
#      same-origin shop (one process serves API + frontend).
$envFile = Join-Path $InstallDir 'backend\.env'
$envExample = Join-Path $InstallDir 'backend\.env.example'
if (-not (Test-Path $envFile)) {
  if (-not (Test-Path $envExample)) { Die 'backend\.env.example missing from the release archive — re-download' }
  Say 'Writing backend\.env (JWT secret generated; edit CryptoRefills keys)...'
  $bytes = New-Object 'byte[]' 32
  # RandomNumberGenerator.Fill() is .NET Core only; Create().GetBytes() works on
  # Windows PowerShell 5.1 as well.
  $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
  $rng.GetBytes($bytes)
  $jwt = ($bytes | ForEach-Object { $_.ToString('x2') }) -join ''
  $lines = Get-Content $envExample
  $out = foreach ($line in $lines) {
    if ($line -match '^JWT_SECRET=') { "JWT_SECRET=$jwt" } else { $line }
  }
  $out += "LISTEN_ADDR=:$Port"
  $out += "STATIC_DIR=$(Join-Path $InstallDir 'frontend')"
  # UTF-8 WITHOUT a BOM: a BOM would glue itself to the first key and the Go
  # env loader would never see that variable.
  [System.IO.File]::WriteAllLines($envFile, $out, (New-Object System.Text.UTF8Encoding($false)))
}

# ---- frontend/config.js: same-origin API base
$config = Join-Path $InstallDir 'frontend\config.js'
if (Test-Path $config) {
  $js = (Get-Content $config -Raw) `
    -replace "API_BASE: *'[^']*'", "API_BASE: '/api'" `
    -replace 'API_BASE: *"[^"]*"', "API_BASE: '/api'"
  [System.IO.File]::WriteAllText($config, $js, (New-Object System.Text.UTF8Encoding($false)))
}

# ---- nimshop shim on the user PATH
$binDir = Join-Path $InstallDir 'bin'
New-Item -ItemType Directory -Force -Path $binDir | Out-Null
$cli = Join-Path $InstallDir 'cli\nimshop.ps1'
$shim = Join-Path $binDir 'nimshop.cmd'
Set-Content -Path $shim -Encoding ascii -Value @"
@echo off
powershell -NoProfile -ExecutionPolicy Bypass -File "$cli" %*
"@

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $userPath) { $userPath = '' }
if (($userPath -split ';') -notcontains $binDir) {
  $newPath = if ($userPath.TrimEnd(';')) { "$($userPath.TrimEnd(';'));$binDir" } else { $binDir }
  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
  Say "Added $binDir to your user PATH (open a new terminal to use it)"
}

Say "Installed $tag -> $InstallDir"
Write-Host ""
Write-Host "  Start:   nimshop start     (or $cli start)"
Write-Host "  Stop:    nimshop stop"
Write-Host "  Status:  nimshop status"
Write-Host "  Shop:    http://127.0.0.1:$Port" # DevSkim: ignore DS162092 the CLI stack listens on loopback by design
Write-Host ""
Write-Host "  Edit secrets:  $envFile"
Write-Host "  Then:          nimshop start"
Write-Host ""

try {
  & $cli start
} catch {
  Write-Warning "start failed: $($_.Exception.Message)"
}
