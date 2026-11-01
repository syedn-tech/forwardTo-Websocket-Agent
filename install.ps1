# Windows PowerShell One-Liner Installer for ForwardTo Agent
# Run in PowerShell:
#   irm https://forwardto.cloud/install.ps1 | iex

$ErrorActionPreference = 'Stop'

$Repo = "syedn-tech/forwardTo-Websocket-Agent"
$InstallDir = "$env:LOCALAPPDATA\Programs\forwardto-agent"
$BinaryName = "forwardto-agent.exe"

$Arch = if ([Environment]::Is64BitOperatingSystem) { "amd64" } else { "386" }
$DownloadUrl = "https://github.com/$Repo/releases/latest/download/forwardto-agent-windows-$Arch.exe"

Write-Host ">>> Installing ForwardTo Agent for Windows ($Arch)..." -ForegroundColor Cyan

if (!(Test-Path -Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}

$Destination = Join-Path $InstallDir $BinaryName

Write-Host ">>> Downloading from $DownloadUrl..." -ForegroundColor Yellow
try {
    Invoke-WebRequest -Uri $DownloadUrl -OutFile $Destination -UseBasicParsing
} catch {
    Write-Error "Failed to download binary from $DownloadUrl"
    exit 1
}

# Add to user PATH if not present
$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($UserPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$UserPath;$InstallDir", "User")
    $env:Path = "$env:Path;$InstallDir"
    Write-Host ">>> Added $InstallDir to user PATH." -ForegroundColor Green
}

Write-Host ">>> Successfully installed forwardto-agent to $Destination" -ForegroundColor Green
Write-Host ">>> You can now open a new PowerShell window and run: forwardto-agent" -ForegroundColor Cyan
