#!/usr/bin/env bash
# POSIX twin of download-engines.ps1: fetches the engine binaries and the geo
# rule-sets into bin/ so `go build` can //go:embed them. The binaries are NOT
# committed to git — see .gitignore.
#
# Usage:
#   scripts/download-engines.sh                     # linux/amd64, the pinned engines
#   scripts/download-engines.sh --os windows        # cross-fetch for a Windows build
#   scripts/download-engines.sh --arch arm64
#   scripts/download-engines.sh --xray v26.7.28 --singbox v1.13.16
#
# The geo files are OS-independent, so they are fetched once regardless of --os.
#
# The engine versions are pinned, each archive with its SHA-256 (see
# download-engines.ps1 for why): a download that differs stops the script. A
# version that is not pinned is checked against the SHA-256 GitHub lists for
# the release asset, and the script stops when that cannot be read.
set -euo pipefail

cd "$(dirname "$0")/.."

OS=linux
ARCH=amd64
XRAY_VERSION=""
SINGBOX_VERSION=""

# The versions a release ships with. Update with the hashes below, after testing.
# Xray stays below 26.9, which refuses VLESS without TLS or VLESS encryption to
# a public address — see download-engines.ps1.
PINNED_XRAY=v26.7.28
PINNED_SINGBOX=v1.13.16

# pinned_sha256 prints the SHA-256 GitHub lists for a pinned release asset.
pinned_sha256() {
  case "$1" in
    XTLS/Xray-core/v26.7.28/Xray-linux-64.zip)        echo 8195d909f1109b8f3d99eefe401a3c451d7bf4af71f24d3815420f77e5dd2a40 ;;
    XTLS/Xray-core/v26.7.28/Xray-linux-arm64-v8a.zip) echo f5698bb218ada3b4022db26fafc39601c5f53b46b19eb76c9616325985807501 ;;
    XTLS/Xray-core/v26.7.28/Xray-windows-64.zip)      echo c7172078fca4711bcd92a4774dcd1822544579c58816197575c47533317fd8d1 ;;
    SagerNet/sing-box/v1.13.16/sing-box-1.13.16-linux-amd64.tar.gz) echo e37c312859dfa84cba148f41072ff6369f08361ae91d622dc1fd3aab49611a8d ;;
    SagerNet/sing-box/v1.13.16/sing-box-1.13.16-linux-arm64.tar.gz) echo d587fb00bdc3c044227f35d15d154f271bc75108475091eda2542e4b82bb2949 ;;
    SagerNet/sing-box/v1.13.16/sing-box-1.13.16-windows-amd64.zip)  echo 6cbf90ec4ee87122ffce09b73928fb31e763bc1c75a119f79c61d24734c78807 ;;
  esac
}

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

# expected_sha256 prints the hash a release asset must have: the pinned one, or
# for another version the one GitHub lists in the release's asset "digest".
#
# The feed is read into a VARIABLE first, deliberately: piping curl straight
# into a reader that stops early closes the pipe underneath curl, which in Git
# Bash surfaces as "curl: (23) Failure writing output to destination" and, with
# `set -o pipefail` above, takes the whole script down. tr -d '\r' guards a CRLF
# response, whose stray carriage return would otherwise end up in the hash.
expected_sha256() {
  repo="$1"; tag="$2"; asset="$3"
  pinned=$(pinned_sha256 "$repo/$tag/$asset")
  if [ -n "$pinned" ]; then echo "$pinned"; return 0; fi
  body=$(curl -fsSL --max-time 120 -A vair-build "https://api.github.com/repos/$repo/releases/tags/$tag") || return 0
  # The API sends JSON in one line or pretty-printed; split it at the commas and
  # braces so each field stands alone. In each asset object "name" comes before
  # "digest", and the uploader object between them has no "name" field.
  printf '%s' "$body" | tr -d '\r' | tr ',{}' '\n\n\n' | awk -v asset="$asset" '
    { gsub(/": +"/, "\":\"") }
    index($0, "\"name\":\"" asset "\"") { hit = 1; next }
    hit && index($0, "\"digest\":\"sha256:") { sub(/.*sha256:/, ""); sub(/".*/, ""); print; exit }'
}

# fetch_verified downloads a release asset and stops unless its SHA-256 is the
# expected one.
fetch_verified() {
  repo="$1"; tag="$2"; asset="$3"; dest="$4"
  want=$(expected_sha256 "$repo" "$tag" "$asset")
  [ -n "$want" ] || fail "no SHA-256 to check $asset ($repo $tag) against — pin it in this script"
  fetch "https://github.com/$repo/releases/download/$tag/$asset" "$dest"
  got=$(sha256sum "$dest" | cut -d' ' -f1)
  if [ "$got" != "$want" ]; then
    rm -f "$dest"
    fail "$asset does not match its SHA-256 (expected $want, got $got)"
  fi
  ok "$asset SHA-256 checked"
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

[ -n "$XRAY_VERSION" ]    || XRAY_VERSION="$PINNED_XRAY"
[ -n "$SINGBOX_VERSION" ] || SINGBOX_VERSION="$PINNED_SINGBOX"

step "xray $XRAY_VERSION ($OS/$ARCH)"
fetch_verified XTLS/Xray-core "$XRAY_VERSION" "$XRAY_ASSET" bin/xray.zip
rm -rf bin/xray-tmp && mkdir -p bin/xray-tmp
unzip -qo bin/xray.zip -d bin/xray-tmp
mv "bin/xray-tmp/xray$EXE" "bin/xray$EXE"
chmod 0755 "bin/xray$EXE"
rm -rf bin/xray-tmp bin/xray.zip
ok "bin/xray$EXE"

step "sing-box $SINGBOX_VERSION ($OS/$ARCH)"
SB_VER_NUM="${SINGBOX_VERSION#v}"
if [ "$OS" = "windows" ]; then
  fetch_verified SagerNet/sing-box "$SINGBOX_VERSION" "sing-box-$SB_VER_NUM-$SB_SUFFIX.zip" bin/sb.zip
  rm -rf bin/sb-tmp && mkdir -p bin/sb-tmp
  unzip -qo bin/sb.zip -d bin/sb-tmp
  find bin/sb-tmp -name "sing-box$EXE" -exec mv {} "bin/sing-box$EXE" \;
  rm -rf bin/sb-tmp bin/sb.zip
else
  fetch_verified SagerNet/sing-box "$SINGBOX_VERSION" "sing-box-$SB_VER_NUM-$SB_SUFFIX.tar.gz" bin/sb.tgz
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
