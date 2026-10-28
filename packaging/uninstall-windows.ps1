# uninstall.ps1 - shareto Windows Uninstaller
[CmdletBinding()]
param (
    [string]$InstallDir = "$env:LOCALAPPDATA\Programs\shareto",
    [switch]$RemoveData = $false
)

$ErrorActionPreference = "SilentlyContinue"

Write-Host "============================================================" -ForegroundColor Cyan
Write-Host "  shareto - Windows Uninstallation Wizard" -ForegroundColor Cyan
Write-Host "============================================================" -ForegroundColor Cyan

# 1. Stop daemon process
Write-Host "[1/4] Stopping background service..." -ForegroundColor Yellow
$TargetExe = Join-Path $InstallDir "shareto.exe"
if (Test-Path $TargetExe) {
    & $TargetExe service uninstall
}
Stop-Process -Name "shareto" -Force -ErrorAction SilentlyContinue

# 2. Remove autostart registry and startup items
Write-Host "[2/4] Cleaning autostart registry and startup shortcuts..." -ForegroundColor Yellow
reg delete "HKCU\Software\Microsoft\Windows\CurrentVersion\Run" /v "shareto" /f 2>$null
$StartupDir = "$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Startup"
Remove-Item -Path "$StartupDir\shareto.vbs" -Force -ErrorAction SilentlyContinue
Remove-Item -Path "$StartupDir\shareto.lnk" -Force -ErrorAction SilentlyContinue
schtasks /delete /tn "shareto" /f 2>$null

# 3. Remove from User PATH
Write-Host "[3/4] Removing installation directory from User PATH..." -ForegroundColor Yellow
$UserPath = [Environment]::GetEnvironmentVariable("Path", [EnvironmentVariableTarget]::User)
if ($UserPath) {
    $PathEntries = $UserPath -split ";" | Where-Object { $_ -ne "" -and $_ -ne $InstallDir }
    $NewPath = $PathEntries -join ";"
    [Environment]::SetEnvironmentVariable("Path", $NewPath, [EnvironmentVariableTarget]::User)
    Write-Host "  Removed $InstallDir from User PATH." -ForegroundColor Green
}

# 4. Remove installation files
Write-Host "[4/4] Removing installation files..." -ForegroundColor Yellow
if (Test-Path $InstallDir) {
    Remove-Item -Path $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
    Write-Host "  Deleted $InstallDir." -ForegroundColor Green
}

if ($RemoveData) {
    Remove-Item -Path "$env:APPDATA\shareto" -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -Path "$env:LOCALAPPDATA\shareto" -Recurse -Force -ErrorAction SilentlyContinue
    Write-Host "  Cleaned configuration and state data." -ForegroundColor Green
}

Write-Host ""
Write-Host "============================================================" -ForegroundColor Green
Write-Host "  shareto has been completely uninstalled." -ForegroundColor Green
Write-Host "============================================================" -ForegroundColor Green
