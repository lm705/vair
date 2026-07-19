# download-engines.ps1 - fetch the proxy engines + geo databases + RU rule-sets
# into bin\ so `wails3 build` can //go:embed them. The binaries are NOT committed
# to the repository (they are large and update independently); run this once
# before building:
#
#     powershell -ExecutionPolicy Bypass -File download-engines.ps1
#
# Pin engine versions with -XrayVersion vX.Y.Z / -SingboxVersion vX.Y.Z
# (default: each project's latest release). The geo/RU URLs mirror the ones the
# app itself refreshes from at runtime (see core/ruleset_windows.go).

param(
    [string]$XrayVersion    = "",
    [string]$SingboxVersion = ""
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

# GitHub's release CDN needs TLS 1.2+; Windows PowerShell 5.1 still negotiates
# 1.0/1.1 by default, which drops mid-transfer. Force a modern protocol set.
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 `
    -bor [Net.SecurityProtocolType]::Tls11 -bor [Net.SecurityProtocolType]::Tls
try {
    [Net.ServicePointManager]::SecurityProtocol = `
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls13
} catch {}

function Step { Write-Host "  >>  $args" -ForegroundColor Cyan }
function OK   { Write-Host "  OK  $args" -ForegroundColor Green }
function Fail { Write-Host "  ER  $args" -ForegroundColor Red; exit 1 }

New-Item -ItemType Directory -Force -Path "bin" | Out-Null

function Get-LatestRelease {
    param([string]$Repo)
    (Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" `
        -Headers @{ "User-Agent" = "vair-build" }).tag_name
}

function Download-File {
    param([string]$Url, [string]$Dest)
    Write-Host "      $Url" -ForegroundColor DarkGray
    # Retry a few times (transient CDN blips), then fall back to WebClient.
    for ($i = 1; $i -le 4; $i++) {
        try {
            Invoke-WebRequest -Uri $Url -OutFile $Dest -UseBasicParsing `
                -Headers @{ "User-Agent" = "vair-build" } -TimeoutSec 300
            return
        } catch {
            if ($i -eq 4) {
                Write-Host "      retrying with WebClient..." -ForegroundColor DarkGray
                (New-Object System.Net.WebClient).DownloadFile($Url, $Dest)
                return
            }
            Start-Sleep -Seconds ([Math]::Min(10, $i * 2))
        }
    }
}

# -- xray-core (zip -> xray.exe) -----------------------------------------------
Step "xray-core..."
if (-not $XrayVersion) { $XrayVersion = Get-LatestRelease "XTLS/Xray-core" }
OK "version $XrayVersion"
$zip = "bin\xray.zip"
Download-File "https://github.com/XTLS/Xray-core/releases/download/$XrayVersion/Xray-windows-64.zip" $zip
Expand-Archive -Path $zip -DestinationPath "bin\xray-tmp" -Force
Copy-Item "bin\xray-tmp\xray.exe" "bin\xray.exe" -Force
Remove-Item "bin\xray-tmp" -Recurse -Force; Remove-Item $zip -Force
OK "bin\xray.exe"

# -- sing-box (zip -> sing-box.exe) --------------------------------------------
Step "sing-box..."
if (-not $SingboxVersion) { $SingboxVersion = Get-LatestRelease "SagerNet/sing-box" }
OK "version $SingboxVersion"
$sbVer = $SingboxVersion.TrimStart("v")
$zip = "bin\sing-box.zip"
Download-File "https://github.com/SagerNet/sing-box/releases/download/$SingboxVersion/sing-box-$sbVer-windows-amd64.zip" $zip
Expand-Archive -Path $zip -DestinationPath "bin\sb-tmp" -Force
$sbExe = Get-ChildItem "bin\sb-tmp" -Recurse -Filter "sing-box.exe" | Select-Object -First 1
if (-not $sbExe) { Fail "sing-box.exe not found in the downloaded archive" }
Copy-Item $sbExe.FullName "bin\sing-box.exe" -Force
Remove-Item "bin\sb-tmp" -Recurse -Force; Remove-Item $zip -Force
OK "bin\sing-box.exe"

# -- geo databases + RU rule-sets (URLs mirror core/ruleset_windows.go) --------
$files = @(
    @{ n = "geoip.dat";              u = "https://github.com/v2fly/geoip/releases/latest/download/geoip.dat" }
    @{ n = "geosite.dat";            u = "https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat" }
    @{ n = "geosite-ru.srs";         u = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ru.srs" }
    @{ n = "geoip-ru.srs";           u = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-ru.srs" }
    @{ n = "geosite-ru-blocked.srs"; u = "https://raw.githubusercontent.com/runetfreedom/russia-v2ray-rules-dat/release/sing-box/rule-set-geosite/geosite-ru-blocked.srs" }
    @{ n = "geoip-ru-blocked.srs";   u = "https://raw.githubusercontent.com/runetfreedom/russia-v2ray-rules-dat/release/sing-box/rule-set-geoip/geoip-ru-blocked.srs" }
    @{ n = "geosite-ru-blocked.dat"; u = "https://raw.githubusercontent.com/runetfreedom/russia-blocked-geosite/release/geosite-ru-only.dat" }
    @{ n = "geoip-ru-blocked.dat";   u = "https://raw.githubusercontent.com/runetfreedom/russia-blocked-geoip/release/ru-blocked.dat" }
    @{ n = "geosite-cn.srs";         u = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs" }
    @{ n = "geoip-cn.srs";           u = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs" }
    @{ n = "geosite-ir.srs";         u = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ir.srs" }
    @{ n = "geoip-ir.srs";           u = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-ir.srs" }
    @{ n = "geoip-kz.srs";           u = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-kz.srs" }
    @{ n = "gfw.txt";                u = "https://raw.githubusercontent.com/Loyalsoldier/v2ray-rules-dat/release/gfw.txt" }
)
foreach ($f in $files) {
    Step $f.n
    Download-File $f.u ("bin\" + $f.n)
    OK ("bin\" + $f.n)
}

Write-Host ""
OK "All engines are in bin\ - now run:  wails3 build"
