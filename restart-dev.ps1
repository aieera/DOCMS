# One-shot reset: stops everything, rebuilds, restarts services as background jobs.
# Run from the repo root:  .\restart-dev.ps1
#
# Port assignments mirror scripts/run-all-services.sh exactly (svc:grpc:health:http)
# so the Vite dev proxy (web/vite.config.ts) and inter-service gRPC addrs work
# identically in both host modes. Infra comes from docker compose, which maps
# Postgres to host port 15432 (docker-compose.yml "15432:5432").

$ErrorActionPreference = "Continue"
Set-Location $PSScriptRoot

# Load .env (KEY=VALUE lines) into the process environment. Unlike
# `set -a; source .env`, values already exported in the shell WIN over
# .env here (compose-style precedence) -- export to override.
function Import-DotEnv([string]$Path) {
  if (-not (Test-Path $Path)) { return }
  foreach ($line in Get-Content $Path) {
    if ($line -match '^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$') {
      $key = $Matches[1]
      $val = $Matches[2].Trim()
      if ($val.Length -ge 2 -and $val.StartsWith('"') -and $val.EndsWith('"')) { $val = $val.Substring(1, $val.Length - 2) }
      if (-not (Test-Path "env:$key")) { Set-Item -Path "env:$key" -Value $val }
    }
  }
}
Import-DotEnv "$PSScriptRoot\.env"

Write-Host "==> 1. Stopping any running Go services..." -ForegroundColor Cyan
$ports = 8180,8181,8182,8183,8184,8185,8186,8187,8188,8189,8190,8191
foreach ($p in $ports) {
  Get-NetTCPConnection -LocalPort $p -State Listen -ErrorAction SilentlyContinue |
    ForEach-Object { Stop-Process -Id $_.OwningProcess -Force -ErrorAction SilentlyContinue }
}
Get-Job -ErrorAction SilentlyContinue | Stop-Job -ErrorAction SilentlyContinue
Get-Job -ErrorAction SilentlyContinue | Remove-Job -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2

Write-Host "==> 2. Verifying Postgres / Redis / NATS are up..." -ForegroundColor Cyan
# Compose host mappings: postgres 15432->5432, redis 6379, nats 4222.
$infra = @{ "postgres" = 15432; "redis" = 6379; "nats" = 4222 }
$missing = @()
foreach ($k in $infra.Keys) {
  if (-not (Test-NetConnection localhost -Port $infra[$k] -InformationLevel Quiet -WarningAction SilentlyContinue)) {
    $missing += $k
  }
}
if ($missing.Count -gt 0) {
  Write-Host "MISSING: $($missing -join ', ')" -ForegroundColor Red
  Write-Host "Run:  docker compose up -d postgres redis nats minio" -ForegroundColor Yellow
  exit 1
}
Write-Host "  ok" -ForegroundColor Green

Write-Host "==> 3. Building all service binaries..." -ForegroundColor Cyan
$svcs = @("auth","policy","document","search","audit","workflow","notification","signature","storage","connector","billing","graphql-gateway")
New-Item -ItemType Directory -Force -Path .\bin | Out-Null
foreach ($s in $svcs) {
  Write-Host "  building $s..."
  $out = & go build -o ".\bin\$s.exe" "./services/$s/cmd/server" 2>&1
  if ($LASTEXITCODE -ne 0) {
    Write-Host "BUILD FAILED for $s:" -ForegroundColor Red
    Write-Host $out
    exit 1
  }
}
Write-Host "  all binaries built into .\bin" -ForegroundColor Green

Write-Host "==> 4. Starting services as background jobs..." -ForegroundColor Cyan
# No hardcoded fallback for SEDOC_GATEWAY_SECRET -- the previous default
# was a globally-known string in the source tree and any deployment that
# inherited it was forge-able. Comes from .env (make gen-env) or the caller.
if (-not $env:SEDOC_GATEWAY_SECRET) {
  Write-Host "SEDOC_GATEWAY_SECRET is required. Run 'make gen-env' (WSL/Git Bash) or export it:" -ForegroundColor Red
  Write-Host "  `$env:SEDOC_GATEWAY_SECRET = (openssl rand -hex 32)" -ForegroundColor Yellow
  exit 1
}
if (-not $env:SEDOC_LOCAL_KEK) {
  Write-Host "  warning: SEDOC_LOCAL_KEK not set (no .env?) -- envelope-crypto features disabled" -ForegroundColor Yellow
}

# svc -> grpc, health, http -- MUST match scripts/run-all-services.sh.
$portMap = @{
  "auth"         = @(9090, 8081, 8180)
  "policy"       = @(9091, 8082, 8181)
  "document"     = @(9092, 8083, 8182)
  "storage"      = @(9093, 8084, 8183)
  "search"       = @(9094, 8085, 8184)
  "audit"        = @(9095, 8086, 8185)
  "workflow"     = @(9096, 8087, 8186)
  "notification" = @(9097, 8088, 8187)
  "signature"    = @(9098, 8089, 8188)
  "billing"      = @(9099, 8090, 8189)
  "connector"    = @(9100, 8091, 8190)
  # HTTP-only, but pkg/config validates SEDOC_GRPC_PORT > 0 at boot, so it
  # gets an unused 9101 (same note as run-all-services.sh). Vite proxies
  # /api/v1/graphql -> :8191.
  "graphql-gateway" = @(9101, 8093, 8191)
}

# Shared per-job environment. Inter-service addrs point at the gRPC ports
# above (same block as run-all-services.sh).
$common = @{
  "SEDOC_GATEWAY_SECRET"       = $env:SEDOC_GATEWAY_SECRET
  "SEDOC_DATABASE_URL"         = "postgres://sedoc:devpassword@localhost:15432/sedoc?sslmode=disable"
  "SEDOC_REDIS_URL"            = "localhost:6379"
  "SEDOC_NATS_URL"             = "nats://localhost:4222"
  "SEDOC_PUBLIC_URL"           = "http://localhost:3000"
  "POLICY_SERVICE_ADDR"        = "localhost:9091"
  "STORAGE_SERVICE_ADDR"       = "localhost:9093"
  "DOCUMENT_SERVICE_ADDR"      = "localhost:9092"
  "WORKFLOW_SERVICE_ADDR"      = "localhost:9096"
  "AUDIT_SERVICE_ADDR"         = "localhost:9095"
  "COLLABORATION_SERVICE_ADDR" = "localhost:9092"
  "AUTH_SERVICE_ADDR"          = "localhost:9090"
}
if ($env:SEDOC_LOCAL_KEK) { $common["SEDOC_LOCAL_KEK"] = $env:SEDOC_LOCAL_KEK }

foreach ($s in $svcs) {
  $p = $portMap[$s]
  Start-Job -Name $s -ArgumentList $s, $p[0], $p[1], $p[2], $PSScriptRoot, $common -ScriptBlock {
    param($svc, $grpcPort, $healthPort, $httpPort, $root, $commonEnv)
    Set-Location $root
    foreach ($k in $commonEnv.Keys) { Set-Item -Path "env:$k" -Value $commonEnv[$k] }
    $env:SEDOC_GRPC_PORT   = "$grpcPort"
    $env:SEDOC_HEALTH_PORT = "$healthPort"
    $env:SEDOC_HTTP_PORT   = "$httpPort"
    & ".\bin\$svc.exe"
  } | Out-Null
  Write-Host "  $s started on :$($p[2]) (grpc=$($p[0]) health=$($p[1]))"
}

Write-Host "==> 5. Waiting up to 30s for services to bind ports..." -ForegroundColor Cyan
$deadline = (Get-Date).AddSeconds(30)
$ready = @{}
while ((Get-Date) -lt $deadline -and $ready.Count -lt $svcs.Count) {
  foreach ($s in $svcs) {
    if (-not $ready.ContainsKey($s)) {
      if (Test-NetConnection localhost -Port $portMap[$s][2] -InformationLevel Quiet -WarningAction SilentlyContinue) {
        $ready[$s] = $true
        Write-Host "  $s ready" -ForegroundColor Green
      }
    }
  }
  if ($ready.Count -lt $svcs.Count) { Start-Sleep -Milliseconds 500 }
}

$failed = $svcs | Where-Object { -not $ready.ContainsKey($_) }
if ($failed.Count -gt 0) {
  Write-Host ""
  Write-Host "FAILED to start within 30s: $($failed -join ', ')" -ForegroundColor Red
  Write-Host "Check logs:" -ForegroundColor Yellow
  foreach ($f in $failed) { Write-Host "  Receive-Job $f -Keep" }
  exit 1
}

Write-Host ""
Write-Host "==> All backend services up." -ForegroundColor Green
Write-Host ""
Write-Host "Now in a SEPARATE terminal start the frontend:" -ForegroundColor Cyan
Write-Host "  cd web; npm run dev" -ForegroundColor Yellow
Write-Host ""
Write-Host "Login at http://localhost:3000 with:" -ForegroundColor Cyan
Write-Host "  tenant slug:  acme"
Write-Host "  email:        admin@acme.local"
Write-Host "  password:     ChangeMe!Now2026"
Write-Host ""
Write-Host "Useful commands:" -ForegroundColor Cyan
Write-Host "  Get-Job                          # see all service states"
Write-Host "  Receive-Job document -Keep       # tail document service logs"
Write-Host "  Receive-Job signature -Keep      # tail signature service logs"
Write-Host "  Get-Job | Stop-Job; Get-Job | Remove-Job   # stop everything"
