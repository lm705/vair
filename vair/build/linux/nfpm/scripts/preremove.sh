#!/bin/sh
# Unload the AppArmor profile while its file still exists — apparmor_parser -R
# reads the file to learn which profile to remove, so doing this after dpkg
# deletes it would leave the profile loaded until the next reboot.
#
# Every step is guarded and failure is swallowed: an apparmor-less system, a
# kernel without the security module, or a parser too old for abi 4.0 must not
# turn a package removal into a broken dpkg state.
if [ -f /etc/apparmor.d/vair ] && command -v apparmor_parser >/dev/null 2>&1; then
  apparmor_parser -R /etc/apparmor.d/vair 2>/dev/null || true
fi

# An upgrade is not a removal: the new package puts the binary straight back,
# and its postinstall restarts the helper on it. Disabling the unit here — as
# this script did on every upgrade, since dpkg runs it with "upgrade" and rpm
# with 1 — switched the helper off for good at each update, and TUN mode was
# gone until the user installed the helper again.
case "$1" in
  upgrade|1) exit 0 ;;
esac

# Stop the helper before its binary goes away. Left running it would keep a
# tunnel up with no package behind it, and the next boot would fail to start a
# unit whose ExecStart no longer exists.
#
# The drop-in in /etc is deliberately left alone on remove: it records which
# account the helper was installed for, and a reinstall should not ask again.
# `purge` is where /etc is supposed to be cleaned, and that is what postremove
# handles.
if command -v systemctl >/dev/null 2>&1; then
  systemctl disable --now vair-helper.service >/dev/null 2>&1 || true
fi

exit 0
