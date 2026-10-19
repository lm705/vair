package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Linux has no single system-proxy switch, so there are two real backends and a
// truthful "nothing here" fallback.
//
// What matters more than the code is what the setting REACHES, because it is
// much less than users assume from the Windows behaviour. GNOME's
// org.gnome.system.proxy and KDE's kioslaverc are read by GIO/KIO applications
// and by browsers configured to "use system proxy settings". They are not read
// by curl, apt, git, ssh, Docker, most Electron apps, or anything else that
// looks at http_proxy in its environment — and an environment variable cannot
// be injected into processes that are already running. There is no mechanism on
// Linux that covers everything the Windows switch covers.
//
// Hence Capabilities.SystemProxy and SystemProxyBackend: the UI says which
// backend is driving, or that none is, instead of implying whole-system reach.

func sysProxy() sysProxyBackend {
	desktop := strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP") + " " + os.Getenv("XDG_SESSION_DESKTOP"))
	kdeFirst := strings.Contains(desktop, "kde") || strings.Contains(desktop, "plasma")

	kde := kdeProxy{tool: firstTool("kwriteconfig6", "kwriteconfig5"), reader: firstTool("kreadconfig6", "kreadconfig5")}
	gs := gsettingsProxy{}

	// Desktop first, tool presence second. Both toolchains can be installed at
	// once — a GNOME box with a KDE application pulls in kwriteconfig — and
	// writing the setting the session does not read is the same as doing nothing
	// while claiming success.
	if kdeFirst && kde.available() {
		return kde
	}
	if gs.available() {
		return gs
	}
	if kde.available() {
		return kde
	}
	return noopProxy{}
}

func firstTool(names ...string) string {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// ─────────────────────────── GNOME / GIO ─────────────────────────
//
// Drives org.gnome.system.proxy, which is also what Cinnamon, MATE, Budgie and
// Pantheon use — they are all GSettings desktops and share the schema.

type gsettingsProxy struct{}

func (gsettingsProxy) name() string { return "gsettings" }

// available probes the schema rather than just the binary: gsettings is present
// on plenty of systems that have no GNOME schemas installed, and writing to a
// missing schema aborts the process rather than returning an error.
func (gsettingsProxy) available() bool {
	if firstTool("gsettings") == "" {
		return false
	}
	return exec.Command("gsettings", "get", "org.gnome.system.proxy", "mode").Run() == nil
}

// gsettingsKeys are every key we overwrite, as schema/key pairs. The snapshot
// covers all of them because "manual" mode is a package: restoring the mode
// alone would leave our host and port behind in the user's settings, visible in
// the Network panel forever after.
var gsettingsKeys = [][2]string{
	{"org.gnome.system.proxy", "mode"},
	{"org.gnome.system.proxy", "ignore-hosts"},
	{"org.gnome.system.proxy", "use-same-proxy"},
	{"org.gnome.system.proxy.http", "host"},
	{"org.gnome.system.proxy.http", "port"},
	{"org.gnome.system.proxy.https", "host"},
	{"org.gnome.system.proxy.https", "port"},
	{"org.gnome.system.proxy.socks", "host"},
	{"org.gnome.system.proxy.socks", "port"},
}

// pointsAt asks GSettings for the three values that together mean "manual
// proxy, ours". Mode alone is not enough: a user's own manual proxy is also
// "manual", and re-pointing that at us would be the very thing this guard
// exists to prevent somebody else doing to them.
func (g gsettingsProxy) pointsAt(host string, port int) (bool, error) {
	mode, err := gsettingsGet("org.gnome.system.proxy", "mode")
	if err != nil {
		return false, err
	}
	if strings.Trim(mode, "'\"") != "manual" {
		return false, nil
	}
	h, err := gsettingsGet("org.gnome.system.proxy.http", "host")
	if err != nil {
		return false, err
	}
	pt, err := gsettingsGet("org.gnome.system.proxy.http", "port")
	if err != nil {
		return false, err
	}
	return strings.Trim(h, "'\"") == host && strings.TrimSpace(pt) == fmt.Sprint(port), nil
}

func (g gsettingsProxy) set(host string, port int) (proxySnapshot, error) {
	snap := proxySnapshot{}
	for _, k := range gsettingsKeys {
		if v, err := gsettingsGet(k[0], k[1]); err == nil {
			snap[k[0]+" "+k[1]] = v
		}
	}

	// ignore-hosts is an array of strings in GVariant text form.
	quoted := make([]string, 0, len(proxyBypass))
	for _, h := range bypassUnix() {
		quoted = append(quoted, "'"+h+"'")
	}
	writes := [][3]string{
		{"org.gnome.system.proxy", "ignore-hosts", "[" + strings.Join(quoted, ", ") + "]"},
		// use-same-proxy defaults to true, which would apply the HTTP proxy to
		// SOCKS as well — pointing SOCKS clients at an HTTP listener, where they
		// get a protocol error rather than a connection. Each protocol is set
		// explicitly instead.
		{"org.gnome.system.proxy", "use-same-proxy", "false"},
		{"org.gnome.system.proxy.http", "host", "'" + host + "'"},
		{"org.gnome.system.proxy.http", "port", fmt.Sprint(port)},
		{"org.gnome.system.proxy.https", "host", "'" + host + "'"},
		{"org.gnome.system.proxy.https", "port", fmt.Sprint(port)},
		// Vair's SOCKS listener is on its own port, which this interface does not
		// carry, so leave SOCKS unset rather than guess. Blanking it matters:
		// a stale SOCKS host from the user's own configuration would still be
		// live while our HTTP proxy is in force.
		{"org.gnome.system.proxy.socks", "host", "''"},
		{"org.gnome.system.proxy.socks", "port", "0"},
		// Mode LAST: it is the switch that makes the rest take effect, so
		// flipping it first would expose a half-written configuration.
		{"org.gnome.system.proxy", "mode", "'manual'"},
	}
	for _, w := range writes {
		if err := exec.Command("gsettings", "set", w[0], w[1], w[2]).Run(); err != nil {
			return snap, fmt.Errorf("gsettings set %s %s: %w", w[0], w[1], err)
		}
	}
	return snap, nil
}

func (g gsettingsProxy) revert(snap proxySnapshot) error {
	// Nothing recorded (a lock file from before snapshots existed): the most we
	// can safely assert is that we switched the mode on, so switch it off.
	if len(snap) == 0 {
		return exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", "'none'").Run()
	}
	// Mode first here, for the mirror-image reason it went last in set: drop out
	// of manual mode before restoring the individual keys.
	if v, ok := snap["org.gnome.system.proxy mode"]; ok {
		exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", v).Run() //nolint:errcheck
	}
	for _, k := range gsettingsKeys {
		key := k[0] + " " + k[1]
		if key == "org.gnome.system.proxy mode" {
			continue
		}
		if v, ok := snap[key]; ok {
			exec.Command("gsettings", "set", k[0], k[1], v).Run() //nolint:errcheck
		}
	}
	return nil
}

// gsettingsGet reads one key. The value comes back in GVariant text form —
// 'none', 8080, true, ['a', 'b'] — which is exactly what `gsettings set`
// accepts, so a snapshot round-trips without any parsing on our side.
func gsettingsGet(schema, key string) (string, error) {
	out, err := exec.Command("gsettings", "get", schema, key).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ─────────────────────────────── KDE ─────────────────────────────

type kdeProxy struct {
	tool   string // kwriteconfig6, or 5
	reader string // kreadconfig6, or 5
}

func (kdeProxy) name() string { return "kde" }

func (k kdeProxy) available() bool { return k.tool != "" }

// kdeProxyKeys are the kioslaverc keys we overwrite, in the [Proxy Settings]
// group. ProxyType 1 is "manual"; 0 is "none".
var kdeProxyKeys = []string{"ProxyType", "httpProxy", "httpsProxy", "socksProxy", "NoProxyFor"}

// kdeAbsent marks a key that did not exist before we wrote it, so revert can
// delete it instead of leaving an empty string behind.
//
// kreadconfig prints nothing for a missing key AND for an empty one, which
// makes the difference invisible — and the visible consequence is stray
// httpsProxy=, socksProxy= and NoProxyFor= lines left in the user's kioslaverc
// after Vair disconnects. Passing a sentinel as --default is how the two are
// told apart.
// A plain unlikely string, not a NUL: exec arguments are NUL-terminated C
// strings, so a NUL would truncate the argument before kreadconfig saw it.
const kdeAbsent = "__vair_absent__"

func (k kdeProxy) set(host string, port int) (proxySnapshot, error) {
	snap := proxySnapshot{}
	for _, key := range kdeProxyKeys {
		if v, err := k.read(key); err == nil {
			snap[key] = v
		}
	}
	addr := fmt.Sprintf("http://%s:%d", host, port)
	writes := [][2]string{
		{"httpProxy", addr},
		{"httpsProxy", addr},
		{"socksProxy", ""},
		{"NoProxyFor", strings.Join(bypassUnix(), ",")},
		{"ProxyType", "1"}, // last, same reasoning as the GNOME mode key
	}
	for _, w := range writes {
		if err := k.write(w[0], w[1]); err != nil {
			return snap, fmt.Errorf("kwriteconfig %s: %w", w[0], err)
		}
	}
	k.notify()
	return snap, nil
}

// pointsAt reads kioslaverc. Without kreadconfig there is no way to tell, and
// the error says so rather than guessing.
func (k kdeProxy) pointsAt(host string, port int) (bool, error) {
	mode, err := k.read("ProxyType")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(mode) != "1" { // 1 = manual
		return false, nil
	}
	v, err := k.read("httpProxy")
	if err != nil {
		return false, err
	}
	// KDE stores "http://host port" or "http://host:port" depending on version.
	v = strings.TrimSpace(v)
	return strings.Contains(v, fmt.Sprintf("%s %d", host, port)) ||
		strings.Contains(v, fmt.Sprintf("%s:%d", host, port)), nil
}

func (k kdeProxy) revert(snap proxySnapshot) error {
	if len(snap) == 0 {
		err := k.write("ProxyType", "0")
		k.notify()
		return err
	}
	if v, ok := snap["ProxyType"]; ok {
		k.write("ProxyType", v) //nolint:errcheck
	}
	for _, key := range kdeProxyKeys {
		if key == "ProxyType" {
			continue
		}
		if v, ok := snap[key]; ok {
			k.write(key, v) //nolint:errcheck
		}
	}
	k.notify()
	return nil
}

func (k kdeProxy) write(key, value string) error {
	if value == kdeAbsent {
		return exec.Command(k.tool, "--file", "kioslaverc", "--group", "Proxy Settings", "--key", key, "--delete").Run()
	}
	return exec.Command(k.tool, "--file", "kioslaverc", "--group", "Proxy Settings", "--key", key, value).Run()
}

func (k kdeProxy) read(key string) (string, error) {
	if k.reader == "" {
		return "", errors.New("no kreadconfig")
	}
	out, err := exec.Command(k.reader, "--file", "kioslaverc", "--group", "Proxy Settings",
		"--key", key, "--default", kdeAbsent).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// notify tells running KIO workers to re-read the config. Without it the change
// applies only to applications started afterwards, which looks exactly like the
// setting having been ignored. Best-effort: no dbus-send, no notification, and
// the setting still lands for new processes.
func (k kdeProxy) notify() {
	exec.Command("dbus-send", "--session", "--type=signal", "/KIO/Scheduler",
		"org.kde.KIO.Scheduler.reparseSlaveConfiguration", "string:").Run() //nolint:errcheck
}

// ────────────────────────── nothing at all ───────────────────────

// noopProxy is what a desktop with neither toolchain gets — a plain window
// manager, XFCE without xfconf's GNOME schemas, a bare Wayland compositor.
//
// It reports itself unavailable rather than pretending, which is what makes
// Capabilities.SystemProxyBackend == "none" true: the UI then tells the user to
// point applications at the local port themselves, instead of offering a switch
// that silently does nothing. That failure mode turns into "the VPN doesn't
// work" reports that are really "nothing read the setting".
type noopProxy struct{}

func (noopProxy) name() string    { return "none" }
func (noopProxy) available() bool { return false }

func (noopProxy) set(host string, port int) (proxySnapshot, error) {
	return nil, errors.New("no system-proxy backend on this desktop (no GNOME schemas, no KDE tools)")
}

// revert is a no-op and returns nil on purpose: we never set anything, so there
// is nothing to undo, and clearStaleProxy must not report a failure for a lock
// file it is cleaning up.
func (noopProxy) revert(proxySnapshot) error { return nil }

// pointsAt reports "could not tell". There is nothing here that could have been
// set, so the guard must never conclude a mismatch and start re-applying into a
// desktop with no backend.
func (noopProxy) pointsAt(string, int) (bool, error) {
	return false, errors.New("no system-proxy backend")
}
