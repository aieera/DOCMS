# SeDoc test-server installer -- Windows (Docker Desktop / WSL2 backend).
# Entry point: install.bat at the repo root. PowerShell 5.1 compatible.
#
# Mirrors install.sh step-for-step: only Docker is required on the host --
# migrations run in a migrate/migrate container and seeding in a golang
# container, so Go, golang-migrate and openssl are NOT needed on Windows.
# Idempotent -- safe to re-run after fixing whatever a step reports.
#
# Options:
#   -Prebuilt     pull ghcr.io/aieera/sedoc images where published
#   -SkipSysctl   skip the vm.max_map_count fix (OpenSearch won't boot < 262144)
#   -TimeoutSec   health-wait timeout (default 900 -- cold first boot is slow)

param(
  [switch]$Prebuilt,
  [switch]$SkipSysctl,
  [int]$TimeoutSec = 900
)

$ErrorActionPreference = "Continue"
$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
Set-Location $RepoRoot

# Pin the compose project: overlays carrying their own `name:` (last-file-wins)
# would otherwise fork the stack into a second project and break the health
# wait + the network name used for dockerized migrate/seed below.
$env:COMPOSE_PROJECT_NAME = "sedoc-dev"

function Step([string]$Msg) { Write-Host ""; Write-Host "==> $Msg" -ForegroundColor Cyan }
function Fail([string]$Msg) { Write-Host "ERROR: $Msg" -ForegroundColor Red; exit 1 }

# ---- 1. Preflight -----------------------------------------------------------
Step "1/8 Preflight: Docker Desktop + compose v2 (Linux containers)"

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
  if (Get-Command winget -ErrorAction SilentlyContinue) {
    Write-Host "  docker not found -- attempting 'winget install Docker.DockerDesktop'..."
    winget install -e --id Docker.DockerDesktop --accept-source-agreements --accept-package-agreements
  }
  Fail @"
docker CLI not available. Install Docker Desktop (https://docs.docker.com/desktop/setup/install/windows-install/),
start it once so the WSL2 engine finishes first-run setup, then re-run install.bat.
"@
}

& docker info *> $null
if ($LASTEXITCODE -ne 0) {
  Fail "docker daemon unreachable. Start Docker Desktop, wait until it says 'running', then re-run install.bat."
}

$osType = (& docker info --format '{{.OSType}}' 2>$null)
if ("$osType".Trim() -ne "linux") {
  Fail "Docker is in Windows-containers mode. Right-click the Docker Desktop tray icon -> 'Switch to Linux containers', then re-run."
}

& docker compose version *> $null
if ($LASTEXITCODE -ne 0) {
  Fail "docker compose v2 not available. Update Docker Desktop (it bundles compose v2)."
}
Write-Host "  ok" -ForegroundColor Green

# ---- 2. Kernel: vm.max_map_count for OpenSearch ----------------------------
Step "2/8 Kernel: vm.max_map_count >= 262144 in the Docker Desktop VM (OpenSearch)"

if ($SkipSysctl) {
  Write-Host "  skipped (-SkipSysctl)"
} else {
  # Any container sees the VM kernel's global value -- cheap read.
  $current = (& docker run --rm alpine sysctl -n vm.max_map_count 2>$null)
  $currentVal = 0
  if ("$current".Trim() -match '^\d+$') { $currentVal = [int]"$current".Trim() }

  if ($currentVal -ge 262144) {
    Write-Host "  already $currentVal - nothing to do" -ForegroundColor Green
  } else {
    # Set it for the running VM: try the WSL distro first, then the
    # privileged-nsenter fallback (works on any Docker Desktop backend).
    $setOk = $false
    if (Get-Command wsl -ErrorAction SilentlyContinue) {
      & wsl -d docker-desktop sysctl -w vm.max_map_count=262144 2>$null
      if ($LASTEXITCODE -eq 0) { $setOk = $true }
    }
    if (-not $setOk) {
      & docker run --rm --privileged --pid=host alpine nsenter -t 1 -m -u -n -i sysctl -w vm.max_map_count=262144
    }
    $verify = (& docker run --rm alpine sysctl -n vm.max_map_count 2>$null)
    if ("$verify".Trim() -match '^\d+$' -and [int]"$verify".Trim() -ge 262144) {
      Write-Host "  set to $("$verify".Trim()) for the running VM" -ForegroundColor Green
    } else {
      Fail "could not raise vm.max_map_count (currently '$current'). OpenSearch will exit at boot without it."
    }
  }

  # Persist across 'wsl --shutdown' via .wslconfig kernelCommandLine.
  if (Get-Command wsl -ErrorAction SilentlyContinue) {
    $cfgPath = Join-Path $env:USERPROFILE ".wslconfig"
    $mmcToken = "sysctl.vm.max_map_count=262144"
    $changed = $false
    $lines = @()
    if (Test-Path $cfgPath) { $lines = @(Get-Content $cfgPath) }

    if (-not ($lines -match '^\s*\[wsl2\]')) {
      $lines += "[wsl2]"
      $changed = $true
    }
    $kclIdx = -1
    for ($i = 0; $i -lt $lines.Count; $i++) {
      if ($lines[$i] -match '^\s*kernelCommandLine\s*=') { $kclIdx = $i; break }
    }
    if ($kclIdx -ge 0) {
      if ($lines[$kclIdx] -notmatch [regex]::Escape($mmcToken)) {
        $lines[$kclIdx] = $lines[$kclIdx] + " " + $mmcToken
        $changed = $true
      }
    } else {
      # insert right after [wsl2]
      $out = @()
      foreach ($l in $lines) {
        $out += $l
        if ($l -match '^\s*\[wsl2\]') { $out += "kernelCommandLine = $mmcToken" }
      }
      $lines = $out
      $changed = $true
    }
    if ($changed) {
      Set-Content -Path $cfgPath -Value $lines -Encoding Default
      Write-Host "  persisted in $cfgPath (takes effect after the next 'wsl --shutdown' + Docker Desktop restart)"
    }
  }
}

# ---- 3. Memory guard --------------------------------------------------------
Step "3/8 Memory: WSL2 allocation for the stack (~10-12GB wanted)"

$totalGB = [math]::Floor((Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory / 1GB)
if ($totalGB -lt 16) {
  Write-Host "  warning: host has ${totalGB}GB RAM. The full stack (ClamAV ~2GB, OnlyOffice 4GB," -ForegroundColor Yellow
  Write-Host "  OpenSearch heap, 17 app services) wants ~10-12GB inside the VM." -ForegroundColor Yellow
}
$cfgPath = Join-Path $env:USERPROFILE ".wslconfig"
$hasMemory = $false
if (Test-Path $cfgPath) {
  $hasMemory = [bool]((Get-Content $cfgPath) -match '^\s*memory\s*=')
}
if (-not (Get-Command wsl -ErrorAction SilentlyContinue)) {
  Write-Host "  no wsl.exe - skipping .wslconfig (Hyper-V backend manages its own memory)"
} elseif (-not $hasMemory) {
  # Default WSL2 cap is min(50% RAM, 8GB) on recent builds -- too small.
  $target = [math]::Min(24, [math]::Max(8, [math]::Floor($totalGB * 0.75)))
  $lines = @()
  if (Test-Path $cfgPath) { $lines = @(Get-Content $cfgPath) }
  if (-not ($lines -match '^\s*\[wsl2\]')) { $lines += "[wsl2]" }
  $out = @()
  foreach ($l in $lines) {
    $out += $l
    if ($l -match '^\s*\[wsl2\]') { $out += "memory=${target}GB" }
  }
  Set-Content -Path $cfgPath -Value $out -Encoding Default
  Write-Host "  set [wsl2] memory=${target}GB in $cfgPath (applies after next WSL restart)"
} else {
  Write-Host "  .wslconfig already sets memory - leaving it alone"
}

# ---- 4. Environment (.env + web/.env) --------------------------------------
Step "4/8 Generating .env + web/.env (kept if they already exist)"

function New-RandBytes([int]$n) {
  $b = New-Object byte[] $n
  [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
  return $b
}
function New-RandHex { (New-RandBytes 32 | ForEach-Object { $_.ToString("x2") }) -join "" }
function New-RandB64 { [Convert]::ToBase64String((New-RandBytes 32)) }

function Set-EnvLine([string]$Content, [string]$Key, [string]$Value) {
  # Values are base64/hex -- no '$' possible, so the replacement string is safe.
  return $Content -replace "(?m)^$Key=.*$", "$Key=$Value"
}
function Get-EnvVal([string]$Path, [string]$Key) {
  if (-not (Test-Path $Path)) { return "" }
  foreach ($line in Get-Content $Path) {
    if ($line -match "^$Key=(.*)$") {
      $v = $Matches[1].Trim()
      # Strip surrounding double quotes the way compose's .env parser does.
      if ($v.Length -ge 2 -and $v.StartsWith('"') -and $v.EndsWith('"')) {
        $v = $v.Substring(1, $v.Length - 2)
      }
      return $v
    }
  }
  return ""
}

if (Test-Path ".env") {
  Write-Host "  .env already exists - keeping it"
  $gateway = Get-EnvVal ".env" "SEDOC_GATEWAY_SECRET"
  if (-not $gateway) {
    Fail ".env exists but SEDOC_GATEWAY_SECRET is empty - the stack refuses to boot without it. Set it (any 64-char hex) and re-run."
  }
} else {
  if (-not (Test-Path ".env.example")) { Fail ".env.example missing" }
  $content = [System.IO.File]::ReadAllText((Join-Path $RepoRoot ".env.example"))
  $gateway = New-RandHex
  $content = Set-EnvLine $content "SEDOC_LOCAL_KEK"        (New-RandB64)
  $content = Set-EnvLine $content "SEDOC_INTERNAL_API_KEY" (New-RandHex)
  $content = Set-EnvLine $content "SESSION_COOKIE_SECRET"  (New-RandB64)
  $content = Set-EnvLine $content "SEDOC_GATEWAY_SECRET"   $gateway
  # WriteAllText(path, string) = UTF-8 without BOM; a BOM would corrupt the
  # first line for docker compose's .env parser.
  [System.IO.File]::WriteAllText((Join-Path $RepoRoot ".env"), $content)
  Write-Host "  .env generated with fresh random secrets"
}

# Keep web/.env's SEDOC_GATEWAY_SECRET in sync with the backend (the dev
# proxy signs requests the backend verifies -- same rule as gen-dev-env.sh).
if ((Test-Path "web\.env") -or (Test-Path "web\.env.example")) {
  if (-not (Test-Path "web\.env")) { Copy-Item "web\.env.example" "web\.env" }
  $webContent = [System.IO.File]::ReadAllText((Join-Path $RepoRoot "web\.env"))
  $webContent = Set-EnvLine $webContent "SEDOC_GATEWAY_SECRET" $gateway
  [System.IO.File]::WriteAllText((Join-Path $RepoRoot "web\.env"), $webContent)
  Write-Host "  web/.env gateway secret synced"
}

# ---- 5. Stack up ------------------------------------------------------------
Step "5/8 Starting the stack (first run builds/pulls images - can take a while)"

$composeArgs = @("-f", "docker-compose.yml")
if ($Prebuilt) {
  $composeArgs += @("-f", "docker-compose.prebuilt.yml")
  & docker compose @composeArgs pull --ignore-buildable
  if ($LASTEXITCODE -ne 0) { & docker compose @composeArgs pull }
}
& docker compose @composeArgs up -d --build
if ($LASTEXITCODE -ne 0) { Fail "docker compose up failed - see output above." }

# ---- 6. Health wait ---------------------------------------------------------
Step "6/8 Waiting for container healthchecks (timeout ${TimeoutSec}s)"

function Get-UnhealthyServices {
  $raw = (& docker compose ps --format json 2>$null | Out-String).Trim()
  if (-not $raw) { return @() }
  $objs = @()
  if ($raw.StartsWith("[")) {
    $objs = @($raw | ConvertFrom-Json)
  } else {
    foreach ($line in ($raw -split "`r?`n")) {
      if ($line.Trim()) { $objs += ($line | ConvertFrom-Json) }
    }
  }
  return @($objs | Where-Object { $_.Health -eq "starting" -or $_.Health -eq "unhealthy" } | ForEach-Object { $_.Name })
}

$deadline = (Get-Date).AddSeconds($TimeoutSec)
while ($true) {
  $waiting = Get-UnhealthyServices
  if ($waiting.Count -eq 0) { Write-Host "  all services healthy" -ForegroundColor Green; break }
  if ((Get-Date) -gt $deadline) {
    & docker compose ps
    Fail "timeout waiting for: $($waiting -join ', '). Inspect with: docker compose logs <service>"
  }
  Write-Host "  waiting for: $($waiting -join ', ')"
  Start-Sleep -Seconds 5
}

# ---- 7. Migrations + seed (dockerized) --------------------------------------
Step "7/8 Database migrations + seed"

$Network      = "sedoc-dev_default"   # compose project 'sedoc-dev', default network
$DbUrl        = "postgres://sedoc:devpassword@postgres:5432/sedoc?sslmode=disable"
$MigrateImage = "migrate/migrate:v4.17.1"

# Same track ordering + per-service bookkeeping tables as scripts/migrate-all.sh
# (document uses the default schema_migrations table; see that script for why
# the order is interleaved).
function Invoke-Mig([string]$Svc, [string[]]$MigArgs) {
  $url = $DbUrl
  if ($Svc -ne "document") { $url = "$DbUrl&x-migrations-table=${Svc}_schema_migrations" }
  $vol = "${RepoRoot}\services\${Svc}\migrations:/migrations:ro"
  & docker run --rm --network $Network -v $vol $MigrateImage -database $url -path /migrations @MigArgs
}

# Pre-pull so pull-progress noise never lands in the parsed `version` output
# (layer-ID lines start with digits and would fake a non-fresh DB).
& docker pull -q $MigrateImage | Out-Null

# golang-migrate prints the version to STDERR; on PowerShell 5.1 native
# stderr arrives as NativeCommandError records that Out-String would render
# decorated ("docker : 99", "+ CategoryInfo ..."), so stringify each record
# ("$_" yields just the message text) before matching.
$verOut = (Invoke-Mig "document" @("version") 2>&1 | ForEach-Object { "$_" }) -join "`n"
if ($verOut -notmatch '(?m)^\d+') {
  Write-Host "  document: applying initial schema (000001)"
  Invoke-Mig "document" @("up", "1")
  if ($LASTEXITCODE -ne 0) { Fail "document initial migration failed" }
}
foreach ($svc in @("intelligence", "document")) {
  Write-Host "  ${svc}: up"
  Invoke-Mig $svc @("up")
  if ($LASTEXITCODE -ne 0) { Fail "$svc migrations failed" }
}
foreach ($svc in @("search", "audit", "billing", "connector", "notification")) {
  Write-Host "  ${svc}: up"
  Invoke-Mig $svc @("up")
  if ($LASTEXITCODE -ne 0) { Fail "$svc migrations failed" }
}

Write-Host "  seeding default tenant + admin (idempotent)"
$seedEnv = @(
  "-e", "GOWORK=off",
  "-e", "DATABASE_URL=$DbUrl"
)
foreach ($k in @("SEED_ADMIN_EMAIL", "SEED_ADMIN_PASSWORD", "SEED_TENANT_SLUG", "SEED_TENANT_NAME", "SEED_REGION")) {
  $v = Get-EnvVal ".env" $k
  if ($v) { $seedEnv += @("-e", "$k=$v") }
}
& docker run --rm --network $Network `
  -v "${RepoRoot}:/src" -w /src/scripts/seed `
  -v "sedoc-gomodcache:/go/pkg/mod" `
  @seedEnv golang:1.25-alpine go run .
if ($LASTEXITCODE -ne 0) { Fail "seed failed" }

# ---- 8. Smoke + summary -----------------------------------------------------
Step "8/8 Smoke check"

$answering = $false
try {
  Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 -Uri "http://localhost:8080/" | Out-Null
  $answering = $true
} catch {
  # 4xx/5xx still proves the gateway is answering; only no-response counts as down.
  if ($_.Exception.Response) { $answering = $true }
}
if ($answering) {
  Write-Host "  gateway answering on :8080" -ForegroundColor Green
} else {
  Write-Host "  warning: gateway did not answer on http://localhost:8080 - check: docker compose ps" -ForegroundColor Yellow
}

$seedEmail = Get-EnvVal ".env" "SEED_ADMIN_EMAIL";    if (-not $seedEmail)    { $seedEmail = "admin@acme.local" }
$seedPassword = Get-EnvVal ".env" "SEED_ADMIN_PASSWORD"; if (-not $seedPassword) { $seedPassword = "ChangeMe!Now2026" }
$seedSlug = Get-EnvVal ".env" "SEED_TENANT_SLUG";     if (-not $seedSlug)     { $seedSlug = "acme" }

Write-Host ""
Write-Host "=========================================="
Write-Host "  SeDoc test server is up."
Write-Host ""
Write-Host "  API gateway:  http://localhost:8080"
Write-Host ""
Write-Host "  Default admin login:"
Write-Host "    Tenant slug:  $seedSlug"
Write-Host "    Email:        $seedEmail"
Write-Host "    Password:     $seedPassword"
Write-Host ""
Write-Host "  Web UI (no container - runs on the host, needs Node 20+):"
Write-Host "    cd web; npm install; npm run dev    ->  http://localhost:3000"
Write-Host ""
Write-Host "  After a Docker Desktop/WSL restart:  make wake   (from WSL/Git Bash)"
Write-Host "  CHANGE THE ADMIN PASSWORD after first login."
Write-Host "=========================================="
exit 0
