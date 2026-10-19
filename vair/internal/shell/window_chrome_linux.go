package shell

// FramelessWindow is TRUE on Linux: the app draws its own titlebar here exactly
// as it does on Windows.
//
// This file previously returned false, on the strength of a spike that observed
// a frameless build starting, serving every asset and never mapping a window,
// while an otherwise identical non-frameless build appeared immediately. The
// conclusion — "frameless windows never map on GTK4/Wayland" — was wrong.
//
// What that spike was actually watching was WebKitGTK aborting a few seconds
// after launch because Ubuntu 24.04's apparmor_restrict_unprivileged_userns
// blocks its bubblewrap sandbox (see build/linux/apparmor/vair). The abort lands
// after the assets load and before the window is shown, which looks exactly like
// a window that refuses to map — and the comparison runs that "worked" had
// simply had the sandbox disabled by an environment variable. With the AppArmor
// profile in place, re-measuring on the same VM (GNOME 46 / Wayland, GTK 4.14.5,
// WebKitGTK 2.52.3, Wails v3.0.0-alpha2.109) showed the frameless window mapping
// instantly.
//
// The lesson worth keeping: two variables differed between those spike builds,
// and the one nobody was testing was the one that mattered.
func FramelessWindow() bool { return true }
