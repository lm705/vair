#!/usr/bin/env bash
# POSIX twin of download-engines.ps1: fetches the engine binaries and the geo
# rule-sets into bin/ so `go build` can //go:embed them. The binaries are NOT
# committed to git — see .gitignore.
#
# Usage:
#   scripts/download-engines.sh                     # linux/amd64, latest engines
#   scripts/download-engines.sh --os windows        # cross-fetch for a Windows build
#   scripts/download-engines.sh --arch arm64
#   scripts/download-engines.sh --xray v26.3.27 --singbox v1.13.15
#
# The geo files are OS-independent, so they are fetched once regardless of --os.
set -euo pipefail

cd "$(dirname "$0")/.."

OS=linux
ARCH=amd64
XRAY_VERSION=""
SINGBOX_VERSION=""

while [ $# -gt 0 ]; do
  case "$1" in
    --os)      OS="$2";      shift 2 ;;
    --arch)    ARCH="$2";    shift 2 ;;
    --xray)    XRAY_VERSION="$2";    shift 2 ;;
    --singbox) SINGBOX_VERSION="$2"; shift 2 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

step() { printf '  >>  %s\n' "$*"; }
ok()   { printf '  OK  %s\n' "$*"; }
fail() { printf '  ER  %s\n' "$*" >&2; exit 1; }

mkdir -p bin

# fetch downloads with retries — GitHub and the raw hosts are exactly the ones
# likely to be flaky or blocked for this project's audience.
fetch() {
  url="$1"; dest="$2"
  printf '      %s\n' "$url"
  for attempt in 1 2 3 4; do
    if curl -fsSL --connect-timeout 15 --max-time 300 -A vair-build -o "$dest" "$url"; then
      return 0
    fi
    [ "$attempt" -lt 4 ] && sleep $((attempt * 2))
  done
  fail "download failed after 4 attempts: $url"
}

# latest_tag reads the release feed into a VARIABLE first, deliberately.
#
# Piping curl straight into `grep -m1` makes grep exit on the first match and
# close the pipe underneath curl, which then fails its write. On Linux SIGPIPE
# kills curl quietly; in Git Bash on Windows it surfaces as
# "curl: (23) Failure writing output to destination" and, with `set -o pipefail`
# above, takes the whole script down before a single engine is fetched.
latest_tag() {
  body=$(curl -fsSL --max-time 60 -A vair-build "https://api.github.com/repos/$1/releases/latest") || return 1
  # tr -d '\r' guards a CRLF response: a lone carriage return survives the
  # sed, passes `[ -n ]` as a non-empty version, and then curl rejects the URL
  # it lands in with "Malformed input to a URL function" — an error that says
  # nothing about a stray control character.
  printf '%s\n' "$body" | tr -d '\r' | grep -m1 '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/'
}

# ── engine binaries ──────────────────────────────────────────────────────────
# Asset naming differs per project and per OS, so the mapping is explicit rather
# than pattern-built: a wrong guess yields a 404 that only shows up at runtime.
case "$OS/$ARCH" in
  linux/amd64)   XRAY_ASSET="Xray-linux-64.zip";        SB_SUFFIX="linux-amd64" ;;
  linux/arm64)   XRAY_ASSET="Xray-linux-arm64-v8a.zip"; SB_SUFFIX="linux-arm64" ;;
  windows/amd64) XRAY_ASSET="Xray-windows-64.zip";      SB_SUFFIX="windows-amd64" ;;
  *) fail "unsupported --os/--arch combination: $OS/$ARCH" ;;
esac

EXE=""
[ "$OS" = "windows" ] && EXE=".exe"

[ -n "$XRAY_VERSION" ]    || XRAY_VERSION="$(latest_tag XTLS/Xray-core)"
[ -n "$SINGBOX_VERSION" ] || SINGBOX_VERSION="$(latest_tag SagerNet/sing-box)"
[ -n "$XRAY_VERSION" ]    || fail "could not resolve the latest Xray release"
[ -n "$SINGBOX_VERSION" ] || fail "could not resolve the latest sing-box release"

step "xray $XRAY_VERSION ($OS/$ARCH)"
fetch "https://github.com/XTLS/Xray-core/releases/download/$XRAY_VERSION/$XRAY_ASSET" bin/xray.zip
rm -rf bin/xray-tmp && mkdir -p bin/xray-tmp
unzip -qo bin/xray.zip -d bin/xray-tmp
mv "bin/xray-tmp/xray$EXE" "bin/xray$EXE"
chmod 0755 "bin/xray$EXE"
rm -rf bin/xray-tmp bin/xray.zip
ok "bin/xray$EXE"

step "sing-box $SINGBOX_VERSION ($OS/$ARCH)"
SB_VER_NUM="${SINGBOX_VERSION#v}"
if [ "$OS" = "windows" ]; then
  fetch "https://github.com/SagerNet/sing-box/releases/download/$SINGBOX_VERSION/sing-box-$SB_VER_NUM-$SB_SUFFIX.zip" bin/sb.zip
  rm -rf bin/sb-tmp && mkdir -p bin/sb-tmp
  unzip -qo bin/sb.zip -d bin/sb-tmp
  find bin/sb-tmp -name "sing-box$EXE" -exec mv {} "bin/sing-box$EXE" \;
  rm -rf bin/sb-tmp bin/sb.zip
else
  fetch "https://github.com/SagerNet/sing-box/releases/download/$SINGBOX_VERSION/sing-box-$SB_VER_NUM-$SB_SUFFIX.tar.gz" bin/sb.tgz
  rm -rf bin/sb-tmp && mkdir -p bin/sb-tmp
  tar -xzf bin/sb.tgz -C bin/sb-tmp
  find bin/sb-tmp -name "sing-box" -type f -exec mv {} bin/sing-box \;
  rm -rf bin/sb-tmp bin/sb.tgz
fi
chmod 0755 "bin/sing-box$EXE"
ok "bin/sing-box$EXE"

# ── geo data and rule-sets (OS-independent) ──────────────────────────────────
# Skipped when already present: these are the offline baseline the app falls back
# to when the upstreams are blocked, and they change rarely. Delete a file to
# force a refresh.
geo() {
  dest="bin/$1"; url="$2"
  if [ -s "$dest" ]; then printf '  ..  %s (present)\n' "$dest"; return 0; fi
  fetch "$url" "$dest"
  ok "$dest"
}

step "geo data"
geo geoip.dat   "https://github.com/v2fly/geoip/releases/latest/download/geoip.dat"
geo geosite.dat "https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat"

SING_GEOIP=https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set
SING_GEOSITE=https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set
geo geoip-ru.srs   "$SING_GEOIP/geoip-ru.srs"
geo geoip-cn.srs   "$SING_GEOIP/geoip-cn.srs"
geo geoip-ir.srs   "$SING_GEOIP/geoip-ir.srs"
geo geoip-kz.srs   "$SING_GEOIP/geoip-kz.srs"
geo geosite-ru.srs "$SING_GEOSITE/geosite-category-ru.srs"
geo geosite-cn.srs "$SING_GEOSITE/geosite-cn.srs"
geo geosite-ir.srs "$SING_GEOSITE/geosite-category-ir.srs"

RUNET=https://raw.githubusercontent.com/runetfreedom
geo geosite-ru-blocked.srs "$RUNET/russia-v2ray-rules-dat/release/sing-box/rule-set-geosite/geosite-ru-blocked.srs"
geo geoip-ru-blocked.srs   "$RUNET/russia-v2ray-rules-dat/release/sing-box/rule-set-geoip/geoip-ru-blocked.srs"
geo geosite-ru-blocked.dat "$RUNET/russia-blocked-geosite/release/geosite-ru-only.dat"
geo geoip-ru-blocked.dat   "$RUNET/russia-blocked-geoip/release/ru-blocked.dat"
geo gfw.txt "https://raw.githubusercontent.com/Loyalsoldier/v2ray-rules-dat/release/gfw.txt"

printf '\n  done: engines for %s/%s + geo data in bin/\n' "$OS" "$ARCH"
