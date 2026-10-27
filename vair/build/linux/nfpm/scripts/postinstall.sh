#!/bin/sh
# Refresh the three system caches our shipped files feed. Each tool is probed
# first: a package must not fail to install because an optional desktop utility
# is absent on a headless or minimal system.

# Desktop database — makes the app appear in menus AND registers it as the
# handler for vair:// (MimeType=x-scheme-handler/vair in the .desktop file).
# Without this, xdg-open on a share link finds nothing until the next login.
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database -q /usr/share/applications
fi

# MIME database — the scheme handler above is looked up through it.
if command -v update-mime-database >/dev/null 2>&1; then
  update-mime-database -n /usr/share/mime
fi

# Icon cache — GTK reads the mmap'd cache in preference to scanning the theme
# directory, so a stale cache means our newly installed icon is invisible and
# the window falls back to the generic-application glyph.
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache -q -f /usr/share/icons/hicolor 2>/dev/null || true
fi

# AppArmor — grants the `userns` permission WebKitGTK's bubblewrap sandbox needs.
# Ubuntu 24.04+ refuses to start the app without it; see /etc/apparmor.d/vair.
# aa-enabled is the cheap way to ask whether the module is actually active,
# rather than merely installed.
if command -v apparmor_parser >/dev/null 2>&1 && [ -f /etc/apparmor.d/vair ]; then
  if aa-enabled --quiet 2>/dev/null || [ -d /sys/kernel/security/apparmor ]; then
    # -T -W: skip the cache on read, write a fresh one. Errors are non-fatal —
    # a parser predating abi 4.0 rejects the file, and that must not fail the
    # install on a distro where the restriction does not exist in the first place.
    apparmor_parser -r -T -W /etc/apparmor.d/vair 2>/dev/null || true
  fi
fi

# The helper, when the user has installed it: enabled and restarted on the new
# binary. The app and the helper must speak the same protocol version, and an
# update replaces the app's binary at once while the running service would
# otherwise keep the old code until the next boot — TUN refused with
# "reinstall the helper" in the meantime.
#
# "Installed" is the drop-in vair-helper-install writes (and its uninstall
# removes), not the unit's enabled state: packages up to 2.6.0 disabled the
# unit in their prerm on every upgrade — and on an upgrade it is the OLD
# package's prerm that runs — so the state is not to be trusted here. Without
# the drop-in nothing is started: installing the helper is the user's decision,
# taken through the app, and a package never makes it.
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
  if [ -f /etc/systemd/system/vair-helper.service.d/10-group.conf ]; then
    systemctl enable vair-helper.service >/dev/null 2>&1 || true
    systemctl restart vair-helper.service >/dev/null 2>&1 || true
  fi
fi

exit 0
