# download-engines.ps1 - fetch the proxy engines + geo databases + RU rule-sets
# into bin\ so `wails3 build` can //go:embed them. The binaries are NOT committed
# to the repository (they are large and update independently); run this once
# before building:
#
#     powershell -ExecutionPolicy Bypass -File download-engines.ps1
#
# The engine versions are pinned below, each archive with its SHA-256: a build
# gets the engines the release was tested with, byte for byte, and a download
# that differs - a re-uploaded asset, a damaged transfer - stops the script
# instead of ending up inside Vair.exe. "Latest" used to be the default, and for
# Xray-core it meant the newest release NOT marked pre-release, which could be
# months behind the one carrying a security fix.
#
# Another version: -XrayVersion vX.Y.Z / -SingboxVersion vX.Y.Z. Its archive is
# checked against the SHA-256 GitHub lists for the release asset; when that
# cannot be read the script stops. The geo/RU URLs mirror the ones the app
# itself refreshes from at runtime (see core/ruleset_windows.go); files already
# in bin\ are kept unless -RefreshGeo is given, because they are the tracked
# offline baseline.

param(
    [string]$XrayVersion    = "",
    [string]$SingboxVersion = "",
    [switch]$RefreshGeo
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

# The versions a release ships with, and the SHA-256 of each archive as GitHub
# lists it for the asset. Update both together, after testing the new engine.
#
# Xray stays below 26.9: from 26.9.x it refuses to start a VLESS outbound with
# no TLS and no VLESS encryption to a public address ("vless without TLS or
# other encryption is prohibited"), and such configurations are common in
# subscriptions. 26.7.28 carries the fix for GHSA-5wf9-h793-w73c (a pinned
# certificate, pcs=, did not hold for gRPC and Hysteria2) and accepts every
# configuration 26.3.27 did.
$PinnedXray    = "v26.7.28"
$PinnedSingbox = "v1.13.16"
$PinnedSha256 = @{
    "XTLS/Xray-core/v26.7.28/Xray-windows-64.zip"                 = "c7172078fca4711bcd92a4774dcd1822544579c58816197575c47533317fd8d1"
    "SagerNet/sing-box/v1.13.16/sing-box-1.13.16-windows-amd64.zip" = "6cbf90ec4ee87122ffce09b73928fb31e763bc1c75a119f79c61d24734c78807"
}

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

# Expected-Sha256 is the pinned hash for a release asset, or, for a version
# that is not pinned, the one GitHub lists for it. "" means nothing to check
# against, which the caller treats as a failure.
function Expected-Sha256 {
    param([string]$Repo, [string]$Tag, [string]$Asset)
    $key = "$Repo/$Tag/$Asset"
    if ($PinnedSha256.ContainsKey($key)) { return $PinnedSha256[$key] }
    try {
        $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/tags/$Tag" `
            -Headers @{ "User-Agent" = "vair-build" } -TimeoutSec 120
    } catch {
        return ""
    }
    $a = $rel.assets | Where-Object { $_.name -eq $Asset } | Select-Object -First 1
    if (-not $a -or -not $a.digest -or -not $a.digest.StartsWith("sha256:")) { return "" }
    return $a.digest.Substring(7)
}

# Fetch-Verified downloads a release asset and stops the script unless its
# SHA-256 is the expected one.
function Fetch-Verified {
    param([string]$Repo, [string]$Tag, [string]$Asset, [string]$Dest)
    $want = Expected-Sha256 $Repo $Tag $Asset
    if (-not $want) { Fail "no SHA-256 to check $Asset ($Repo $Tag) against - pin it in this script" }
    Download-File "https://github.com/$Repo/releases/download/$Tag/$Asset" $Dest
    $got = (Get-FileHash -Algorithm SHA256 -Path $Dest).Hash.ToLowerInvariant()
    if ($got -ne $want.ToLowerInvariant()) {
        Remove-Item $Dest -Force
        Fail "$Asset does not match its SHA-256 (expected $want, got $got)"
    }
    OK "$Asset SHA-256 checked"
}

# -- xray-core (zip -> xray.exe) -----------------------------------------------
Step "xray-core..."
if (-not $XrayVersion) { $XrayVersion = $PinnedXray }
OK "version $XrayVersion"
$zip = "bin\xray.zip"
Fetch-Verified "XTLS/Xray-core" $XrayVersion "Xray-windows-64.zip" $zip
Expand-Archive -Path $zip -DestinationPath "bin\xray-tmp" -Force
Copy-Item "bin\xray-tmp\xray.exe" "bin\xray.exe" -Force
Remove-Item "bin\xray-tmp" -Recurse -Force; Remove-Item $zip -Force
OK "bin\xray.exe"

# -- sing-box (zip -> sing-box.exe) --------------------------------------------
Step "sing-box..."
if (-not $SingboxVersion) { $SingboxVersion = $PinnedSingbox }
OK "version $SingboxVersion"
$sbVer = $SingboxVersion.TrimStart("v")
$zip = "bin\sing-box.zip"
Fetch-Verified "SagerNet/sing-box" $SingboxVersion "sing-box-$sbVer-windows-amd64.zip" $zip
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
    $dest = "bin\" + $f.n
    if (-not $RefreshGeo -and (Test-Path $dest) -and (Get-Item $dest).Length -gt 0) {
        Write-Host ("  ..  " + $dest + " (present)") -ForegroundColor DarkGray
        continue
    }
    Step $f.n
    Download-File $f.u $dest
    OK $dest
}

Write-Host ""
OK "All engines are in bin\ - now run:  wails3 build"
