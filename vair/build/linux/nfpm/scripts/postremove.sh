#!/bin/sh
# Mirror of postinstall: the same three caches still hold entries pointing at
# files that no longer exist. Left stale, the launcher keeps a dead Vair icon and
# vair:// links resolve to a missing binary.
#
# Runs on upgrade too (dpkg calls postrm upgrade, rpm passes $1=1). Refreshing is
# idempotent and the new package's postinstall runs afterwards, so no argument
# handling is needed — and skipping it on upgrade would be the fragile path.
#
# Deliberately does NOT touch ~/.local/share/vair: the user's configs, tabs and
# statistics survive a remove/reinstall, as they do on Windows.

if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database -q /usr/share/applications
fi

if command -v update-mime-database >/dev/null 2>&1; then
  update-mime-database -n /usr/share/mime
fi

if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache -q -f /usr/share/icons/hicolor 2>/dev/null || true
fi

# On PURGE (and only then) the per-machine helper decision goes too — dpkg
# passes "purge", rpm passes 0. On a plain remove it stays, so reinstalling does
# not ask the user for a password again.
case "${1:-}" in
  purge|0)
    rm -f /etc/systemd/system/vair-helper.service.d/10-group.conf
    rmdir /etc/systemd/system/vair-helper.service.d 2>/dev/null || true
    if command -v systemctl >/dev/null 2>&1; then
      systemctl daemon-reload >/dev/null 2>&1 || true
    fi
    ;;
esac

exit 0
