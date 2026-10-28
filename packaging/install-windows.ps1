# install.ps1 - shareto Windows Installer
[CmdletBinding()]
param (
    [string]$InstallDir = "$env:LOCALAPPDATA\Programs\shareto"
)

$ErrorActionPreference = "Stop"

Write-Host "============================================================" -ForegroundColor Cyan
Write-Host "  shareto - Windows Installation Wizard" -ForegroundColor Cyan
Write-Host "============================================================" -ForegroundColor Cyan

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$ExePath = Join-Path $ScriptDir "shareto.exe"

if (-not (Test-Path $ExePath)) {
    Write-Error "shareto.exe not found in $ScriptDir"
    exit 1
}

# 1. Create target installation directory
Write-Host "[1/4] Creating installation directory at $InstallDir..." -ForegroundColor Yellow
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}

# 2. Copy binary
$TargetExe = Join-Path $InstallDir "shareto.exe"
Write-Host "[2/4] Installing shareto.exe to $TargetExe..." -ForegroundColor Yellow
Copy-Item -Path $ExePath -Destination $TargetExe -Force

# 3. Add to User PATH if not already present
Write-Host "[3/4] Configuring User PATH..." -ForegroundColor Yellow
$UserPath = [Environment]::GetEnvironmentVariable("Path", [EnvironmentVariableTarget]::User)
$PathEntries = $UserPath -split ";" | Where-Object { $_ -ne "" }
if ($PathEntries -notcontains $InstallDir) {
    $NewPath = ($PathEntries + $InstallDir) -join ";"
    [Environment]::SetEnvironmentVariable("Path", $NewPath, [EnvironmentVariableTarget]::User)
    $env:Path = "$InstallDir;$env:Path"
    Write-Host "  Added $InstallDir to User PATH." -ForegroundColor Green
} else {
    Write-Host "  $InstallDir is already in PATH." -ForegroundColor Green
}

# 4. Install Background User Service
Write-Host "[4/4] Registering background daemon service..." -ForegroundColor Yellow
& $TargetExe service install
if ($LASTEXITCODE -eq 0) {
    Write-Host "  Background service installed and started successfully!" -ForegroundColor Green
    & $TargetExe status
} else {
    Write-Warning "Service install returned code $LASTEXITCODE. Attempting to start daemon directly..."
    & $TargetExe service start
    & $TargetExe status
}

Write-Host ""
Write-Host "============================================================" -ForegroundColor Green
Write-Host "  Installation Completed Successfully!" -ForegroundColor Green
Write-Host "============================================================" -ForegroundColor Green
Write-Host "Quickstart:"
Write-Host "  1. Restart your PowerShell / Windows Terminal session."
Write-Host "  2. Run 'shareto version' to verify installation."
Write-Host "  3. Run 'shareto init' to configure network and public URL."
Write-Host "  4. Share a file: 'shareto C:\path\to\yourfile.zip'"
Write-Host "============================================================" -ForegroundColor Green
