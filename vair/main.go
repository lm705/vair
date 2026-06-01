package main

import (
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// ─────────────────────────── constants ───────────────────────────

const (
	pingTestURLDefault = "https://www.gstatic.com/generate_204"
	pingTimeout        = 1500 * time.Millisecond
	warmupTimeout      = 4 * time.Second // default warm-up; user-overridable via WarmupTimeoutMs
	pingRounds         = 3

	// 50 MB: large enough to fill the measurement window (speedDuration) for
	// any realistic proxy speed so the window isn't cut short by an early
	// EOF (the old "response too fast" false positive), yet within
	// Cloudflare's accepted __down range — bytes=100000000 is rejected by
	// the endpoint, so we cap at 50 MB. We stop reading at the deadline
	// anyway, so this size is an upper bound, not an actual 50 MB download.
	speedTestURLDefault = "https://speed.cloudflare.com/__down?bytes=50000000"
	speedDuration       = 4 * time.Second
	speedUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

	startupTimeout = 4 * time.Second
	dialTimeout    = 5 * time.Second

	// xrayStartupTimeout: time to wait for xray HTTP port to open during ping/speed test.
	// Increased for Windows Defender scan delay on first run of extracted binary.
	xrayStartupTimeout = 8 * time.Second

	// xrayConnTimeout: time to wait for xray to start in persistent proxy connection mode.
	xrayConnTimeout = 12 * time.Second

	// singboxStartupTimeout: time to wait for the sing-box HTTP port to open
	// during ping/speed test of a UDP-family node (Hysteria2/TUIC). The QUIC
	// handshake is slower to come up than a TCP outbound, so this is a touch
	// more generous than xrayStartupTimeout.
	singboxStartupTimeout = 10 * time.Second

	// singboxConnTimeout: time to wait for sing-box to start in a persistent
	// proxy connection. sing-box binds the local HTTP inbound before it ever
	// dials the QUIC outbound, so this only bounds local listener readiness;
	// kept generous to cover first-run Defender scan of the extracted binary.
	singboxConnTimeout = 15 * time.Second

	// tunStartupTimeout: time to wait for sing-box TUN adapter to come up.
	tunStartupTimeout = 3 * time.Second

	// Default ports for persistent proxy connection
	connHTTPPort  = 10819
	connSOCKSPort = 10818

	webPort = 19876
)

// ─────────────────────────── proxy auth ──────────────────────────
// Random credentials generated once per program launch.
// Protects SOCKS5 inbound from abuse by malicious apps on the same machine.
// See: https://habr.com/ru/articles/1020080/
var (
	proxyAuthUser string
	proxyAuthPass string
)

func init() {
	proxyAuthUser = randomHex(16)
	proxyAuthPass = randomHex(32)
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func httpListenAndServe() error {
	addr := fmt.Sprintf(":%d", webPort)
	// The elevated relaunch (restartAsAdmin) starts the new instance before
	// the old one has fully released the port. Retry the bind for a few
	// seconds so the admin handoff doesn't kill the fresh process with a
	// "address already in use" error.
	var ln net.Listener
	var err error
	for i := 0; i < 40; i++ {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	if err := http.Serve(ln, nil); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

// ─────────────────────────── main ────────────────────────────────

func main() {
	// On Windows the standalone build calls standaloneMain() which uses
	// embedded binaries and shows a native Fyne UI window (no browser needed).
	// On other platforms (or when LEGACY=1 env is set), fall through to
	// the classic CLI mode that takes xray/sing-box paths as arguments.
	if os.Getenv("LEGACY") == "" {
		standaloneMain()
		return
	}

	// ── Legacy / cross-platform CLI mode ─────────────────────────────
	xrayBin := "xray"
	if len(os.Args) > 1 {
		xrayBin = os.Args[1]
	}
	if _, err := exec.LookPath(xrayBin); err != nil {
		if _, err2 := os.Stat(xrayBin); err2 != nil {
			fmt.Fprintf(os.Stderr,
				"❌  xray not found.\n    Usage: %s [xray] [sing-box]\n    Releases: https://github.com/XTLS/Xray-core/releases\n",
				os.Args[0])
			os.Exit(1)
		}
	}
	state.xrayBin = xrayBin

	if len(os.Args) > 2 {
		sb := os.Args[2]
		if _, err := exec.LookPath(sb); err == nil {
			state.singboxBin = sb
		} else if _, err2 := os.Stat(sb); err2 == nil {
			state.singboxBin = sb
		} else {
			fmt.Fprintf(os.Stderr, "⚠  sing-box not found at %q — TUN mode unavailable\n", sb)
		}
	} else if path, err := exec.LookPath("sing-box"); err == nil {
		state.singboxBin = path
		fmt.Printf("ℹ  sing-box auto-detected: %s\n", path)
	}

	isAdmin := checkAdmin()
	fmt.Printf("✅  vair → http://localhost:%d\n    xray:     %s\n", webPort, xrayBin)
	if state.singboxBin != "" {
		fmt.Printf("    sing-box: %s\n", state.singboxBin)
	} else {
		fmt.Printf("    sing-box: not found (TUN unavailable)\n")
	}
	fmt.Printf("    admin: %v\n\n", isAdmin)
	if state.singboxBin != "" && !isAdmin {
		fmt.Println("⚠  Run as administrator to enable TUN mode.")
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		fmt.Println("\n⏹  Shutting down — cleaning up…")
		stopConnection()
		killOrphanedXray()
		os.Exit(0)
	}()

	registerRoutes()
	loadTabs()
	loadSettings()
	// Auto-connect now implies connect-on-startup (the separate toggle was
	// removed): if the feature is on, arm intent so the supervisor connects to
	// the fastest working config once entries load.
	if appSettings.AutoConnect {
		autoWant.Store(true)
	}
	go startAutoSupervisor()
	go startAutoRefresh()
	go fetchAndInit()
	if err := httpListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
