# build.ps1 - Build standalone Vair.exe
# Run: powershell -ExecutionPolicy Bypass -File build.ps1

param(
    [string]$XrayVersion    = "",
    [string]$SingboxVersion = "",
    [switch]$SkipDownload
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

function Step  { Write-Host "  >>  $args" -ForegroundColor Cyan }
function OK    { Write-Host "  OK  $args" -ForegroundColor Green }
function Warn  { Write-Host "  !!  $args" -ForegroundColor Yellow }
function Fail  { Write-Host "  ER  $args" -ForegroundColor Red; exit 1 }

Write-Host ""
Write-Host "  vair - Standalone Build" -ForegroundColor Magenta
Write-Host ""

# -- Check Go
Step "Checking Go..."
try { $goVer = (go version 2>&1); OK $goVer }
catch { Fail "Go not found. Install from: https://go.dev/dl/" }

New-Item -ItemType Directory -Force -Path "bin" | Out-Null

function Get-LatestRelease {
    param([string]$Repo)
    $resp = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" `
                              -Headers @{ "User-Agent" = "build-script" }
    return $resp.tag_name
}

function Download-File {
    param([string]$Url, [string]$Dest)
    Write-Host "      $Url" -ForegroundColor DarkGray
    (New-Object System.Net.WebClient).DownloadFile($Url, $Dest)
}

if (-not $SkipDownload) {
    # -- xray-core
    Step "Downloading xray-core..."
    if (-not $XrayVersion) {
        Write-Host "      Getting latest version..." -ForegroundColor DarkGray
        $XrayVersion = Get-LatestRelease "XTLS/Xray-core"
    }
    OK "xray version: $XrayVersion"
    $xrayZip = "bin\xray-windows-64.zip"
    Download-File "https://github.com/XTLS/Xray-core/releases/download/$XrayVersion/Xray-windows-64.zip" $xrayZip
    Step "Extracting xray..."
    Expand-Archive -Path $xrayZip -DestinationPath "bin\xray-tmp" -Force
    Copy-Item "bin\xray-tmp\xray.exe" "bin\xray.exe" -Force
    Remove-Item "bin\xray-tmp" -Recurse -Force
    Remove-Item $xrayZip -Force
    OK "bin\xray.exe ($([math]::Round((Get-Item 'bin\xray.exe').Length/1MB,1)) MB)"

    # -- geoip.dat
    Step "Downloading geoip.dat..."
    Download-File "https://github.com/v2fly/geoip/releases/latest/download/geoip.dat" "bin\geoip.dat"
    OK "bin\geoip.dat ($([math]::Round((Get-Item 'bin\geoip.dat').Length/1MB,1)) MB)"

    # -- geosite.dat
    Step "Downloading geosite.dat..."
    Download-File "https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat" "bin\geosite.dat"
    OK "bin\geosite.dat ($([math]::Round((Get-Item 'bin\geosite.dat').Length/1MB,1)) MB)"

    # -- sing-box
    Step "Downloading sing-box..."
    if (-not $SingboxVersion) {
        Write-Host "      Getting latest version..." -ForegroundColor DarkGray
        $SingboxVersion = Get-LatestRelease "SagerNet/sing-box"
    }
    OK "sing-box version: $SingboxVersion"
    $sbVer = $SingboxVersion.TrimStart("v")
    $sbZip = "bin\sing-box-windows.zip"
    Download-File "https://github.com/SagerNet/sing-box/releases/download/$SingboxVersion/sing-box-${sbVer}-windows-amd64.zip" $sbZip
    Step "Extracting sing-box..."
    Expand-Archive -Path $sbZip -DestinationPath "bin\sb-tmp" -Force
    $sbExe = Get-ChildItem "bin\sb-tmp" -Recurse -Filter "sing-box.exe" | Select-Object -First 1
    Copy-Item $sbExe.FullName "bin\sing-box.exe" -Force
    Remove-Item "bin\sb-tmp" -Recurse -Force
    Remove-Item $sbZip -Force
    OK "bin\sing-box.exe ($([math]::Round((Get-Item 'bin\sing-box.exe').Length/1MB,1)) MB)"
} else {
    Warn "Skipping download (-SkipDownload flag set)."
    foreach ($f in @("bin\xray.exe","bin\sing-box.exe","bin\geoip.dat","bin\geosite.dat","bin\icon.ico")) {
        if (-not (Test-Path $f)) { Fail "$f not found!" }
    }
    OK "Using existing files in bin\"
}

# -- Generate resource file with icon (embeds icon into .exe)
Step "Embedding icon into exe (goversioninfo)..."
$goviPath = Join-Path (go env GOPATH) "bin\goversioninfo.exe"
if (-not (Test-Path $goviPath)) {
    Write-Host "      Installing goversioninfo..." -ForegroundColor DarkGray
    go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest
    if ($LASTEXITCODE -ne 0) { Fail "goversioninfo install failed" }
}
# goversioninfo usage: goversioninfo [flags] <versioninfo.json>
# -64 = generate amd64 resource, -o = output file
& $goviPath -64 -icon "bin\icon.ico" -o "resource.syso" versioninfo.json
if ($LASTEXITCODE -ne 0) { Fail "goversioninfo failed" }
OK "resource.syso generated"

# -- Go dependencies
Step "Fetching Go dependencies..."
# Fetch the latest available commit (no tagged releases exist for go-webview2)
go get github.com/jchv/go-webview2@master
if ($LASTEXITCODE -ne 0) {
    Write-Host "      master failed, trying HEAD..." -ForegroundColor DarkGray
    go get github.com/jchv/go-webview2
    if ($LASTEXITCODE -ne 0) { Fail "go get go-webview2 failed" }
}
go get golang.org/x/sys@latest
if ($LASTEXITCODE -ne 0) { Fail "go get sys failed" }
go mod tidy
if ($LASTEXITCODE -ne 0) { Fail "go mod tidy failed" }
OK "Dependencies ready"

# -- Build
Step "Building Vair.exe..."
$env:GOOS        = "windows"
$env:GOARCH      = "amd64"
$env:CGO_ENABLED = "0"

go build -ldflags="-H windowsgui -s -w" -o "Vair.exe" .

if ($LASTEXITCODE -ne 0) { Fail "Build failed" }

$sizeMB = [math]::Round((Get-Item "Vair.exe").Length / 1MB, 1)
OK "Vair.exe built! ($sizeMB MB)"

# -- Cleanup generated resource file
if (Test-Path "resource.syso") { Remove-Item "resource.syso" }

# -- Refresh Windows icon cache so Explorer shows the new icon immediately
Step "Refreshing icon cache..."
try {
    # Touch the icon cache database to force Explorer to re-read it
    $iconcache = "$env:LOCALAPPDATA\IconCache.db"
    if (Test-Path $iconcache) {
        # Stop Explorer, delete cache, restart
        # This is the only reliable way to force icon refresh on Windows 10/11
        taskkill /f /im explorer.exe 2>$null | Out-Null
        Start-Sleep -Milliseconds 500
        Remove-Item $iconcache -Force -ErrorAction SilentlyContinue
        $thumbcache = Get-ChildItem "$env:LOCALAPPDATA\Microsoft\Windows\Explorer" -Filter "thumbcache_*.db" -ErrorAction SilentlyContinue
        $thumbcache | Remove-Item -Force -ErrorAction SilentlyContinue
        Start-Process explorer.exe
        Start-Sleep -Milliseconds 800
    }
    # Notify shell of icon change via SHChangeNotify
    $code = @'
using System.Runtime.InteropServices;
public class Shell { 
    [DllImport("shell32.dll")] public static extern void SHChangeNotify(int e, int f, IntPtr a, IntPtr b);
}
'@
    Add-Type -TypeDefinition $code -ErrorAction SilentlyContinue
    [Shell]::SHChangeNotify(0x8000000, 0, [IntPtr]::Zero, [IntPtr]::Zero)
    OK "Icon cache refreshed"
} catch {
    Warn "Could not refresh icon cache --- right-click desktop > Refresh to see new icon"
}

Write-Host ""
Write-Host "  Done! Vair.exe is ready." -ForegroundColor Green
Write-Host "  - Double-click to run (Proxy mode)" -ForegroundColor Green
Write-Host "  - Run as Administrator for TUN mode" -ForegroundColor Green
Write-Host "  - Windows 10/11 required" -ForegroundColor Green

# -- Create desktop shortcut "Vair" (without .exe)
Step "Creating desktop shortcut..."
try {
    $desktop = [Environment]::GetFolderPath("Desktop")
    $shortcutPath = Join-Path $desktop "Vair.lnk"
    $targetPath = Join-Path $PSScriptRoot "Vair.exe"
    $iconPath = Join-Path $PSScriptRoot "bin\icon.ico"
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut($shortcutPath)
    $shortcut.TargetPath = $targetPath
    $shortcut.WorkingDirectory = $PSScriptRoot
    $shortcut.Description = "Vair - VLESS proxy checker"
    if (Test-Path $iconPath) { $shortcut.IconLocation = "$iconPath,0" }
    $shortcut.Save()
    OK "Desktop shortcut created: $shortcutPath"
} catch {
    Warn "Could not create desktop shortcut: $_"
}

Write-Host ""
