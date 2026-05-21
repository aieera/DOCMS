# One-shot reset: stops everything, rebuilds, restarts services as background jobs.
# Run from the repo root:  .\restart-dev.ps1

$ErrorActionPreference = "Continue"
Set-Location $PSScriptRoot

Write-Host "==> 1. Stopping any running Go services..." -ForegroundColor Cyan
$ports = 8180,8181,8182,8184,8185,8186,8187,8188,8189,8190,8194
foreach ($p in $ports) {
  Get-NetTCPConnection -LocalPort $p -State Listen -ErrorAction SilentlyContinue |
    ForEach-Object { Stop-Process -Id $_.OwningProcess -Force -ErrorAction SilentlyContinue }
}
Get-Job -ErrorAction SilentlyContinue | Stop-Job -ErrorAction SilentlyContinue
Get-Job -ErrorAction SilentlyContinue | Remove-Job -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2

Write-Host "==> 2. Verifying Postgres / Redis / NATS are up..." -ForegroundColor Cyan
$infra = @{ "postgres" = 5432; "redis" = 6379; "nats" = 4222 }
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
$svcs = @("auth","policy","document","search","audit","workflow","notification","signature","storage","connector","billing")
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
# No hardcoded fallback for VAULTDMS_GATEWAY_SECRET — the previous default
# was a globally-known string in the source tree and any deployment that
# inherited it was forge-able. Require the caller to export it.
if (-not $env:VAULTDMS_GATEWAY_SECRET) {
  Write-Host "VAULTDMS_GATEWAY_SECRET is required. Generate one and export it before running this script:" -ForegroundColor Red
  Write-Host "  `$env:VAULTDMS_GATEWAY_SECRET = (openssl rand -hex 32)" -ForegroundColor Yellow
  exit 1
}
$GatewaySecret = $env:VAULTDMS_GATEWAY_SECRET
$portMap = @{
  "auth"=8180; "policy"=8181; "document"=8182; "search"=8184; "audit"=8185
  "workflow"=8186; "notification"=8187; "signature"=8188; "storage"=8189
  "connector"=8190; "billing"=8194
}
foreach ($s in $svcs) {
  $port = $portMap[$s]
  Start-Job -Name $s -ArgumentList $s, $port, $PSScriptRoot, $GatewaySecret -ScriptBlock {
    param($svc, $port, $root, $gatewaySecret)
    Set-Location $root
    $env:VAULTDMS_HTTP_PORT      = "$port"
    $env:VAULTDMS_GRPC_PORT      = "$($port + 1000)"
    $env:VAULTDMS_HEALTH_PORT    = "$($port + 2000)"
    $env:VAULTDMS_GATEWAY_SECRET = $gatewaySecret
    $env:VAULTDMS_DATABASE_URL   = "postgres://vaultdms:devpassword@localhost:5432/vaultdms?sslmode=disable"
    $env:VAULTDMS_REDIS_URL      = "localhost:6379"
    $env:VAULTDMS_NATS_URL       = "nats://localhost:4222"
    $env:VAULTDMS_LOCAL_KEK      = "dev-32-byte-kek-not-for-production!!"
    $env:VAULTDMS_PUBLIC_URL     = "http://localhost:3000"
    & ".\bin\$svc.exe"
  } | Out-Null
  Write-Host "  $s started on :$port"
}

Write-Host "==> 5. Waiting up to 30s for services to bind ports..." -ForegroundColor Cyan
$deadline = (Get-Date).AddSeconds(30)
$ready = @{}
while ((Get-Date) -lt $deadline -and $ready.Count -lt $svcs.Count) {
  foreach ($s in $svcs) {
    if (-not $ready.ContainsKey($s)) {
      if (Test-NetConnection localhost -Port $portMap[$s] -InformationLevel Quiet -WarningAction SilentlyContinue) {
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
