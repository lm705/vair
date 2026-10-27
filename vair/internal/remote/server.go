// Package remote serves the web interface: the same UI the desktop window
// shows, over HTTP on the local network, so a phone or another computer can
// drive a running Vair.
//
// Access is a single secret key. Everything the desktop UI can call is callable
// here, which is why the key is treated as a credential rather than a
// convenience — see remoteAuth.
package remote

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"vair/core"
	"vair/internal/shell"
)

// Remote control over the LAN: an opt-in HTTP + SSE server, in the SAME process
// as the desktop app, that serves the exact same embedded frontend to a phone or
// another browser on the network. Because it shares this process it controls the
// running instance (same core state, same connection) — not a second copy.
//
// How the unchanged frontend works in a plain browser:
//   - Config calls: the bundled @wailsio/runtime posts every Call.ByID to
//     `<origin>/wails/runtime`. We hand that to a real Wails HTTPTransport +
//     MessageProcessor, which dispatches to the SAME bound services (they resolve
//     through the process-global binding registry), so no per-method wiring.
//   - Events: the webview normally pushes them via injected IPC, which a browser
//     doesn't get. Instead we stream them as Server-Sent Events on /wails/events;
//     a tiny client shim (main.tsx) feeds each one to window._wails
//     .dispatchWailsEvent, which drives the existing Events.On listeners.
//
// Security: bound to all interfaces (so the phone can reach it) but gated by a
// random per-install token — the first request must carry ?key=<token>, after
// which an auth cookie carries it. No token, no access; disabled by default.

var (
	remoteMu     sync.Mutex
	remoteServer *http.Server
	remoteHub    = &sseHub{clients: map[chan []byte]struct{}{}}
	// listenErr is why the server is not running although it is switched on,
	// "" otherwise. The settings screen shows it under the port: a server that
	// could not start used to leave a link and a QR code there that led
	// nowhere, with the reason only on stderr.
	listenErr string
)

// Status reports whether the server is accepting connections and, when it
// was asked to and is not, why.
func Status() (listening bool, reason string) {
	remoteMu.Lock()
	defer remoteMu.Unlock()
	return remoteServer != nil, listenErr
}

const remoteCookie = "vair_remote"

// startRemoteServer brings the LAN server up (idempotent). Called when the
// setting is enabled and at startup if it was left on.
func start() {
	remoteMu.Lock()
	defer remoteMu.Unlock()
	if remoteServer != nil {
		return
	}
	token := core.EnsureRemoteToken()

	if uiAssets == nil {
		log.Print("remote: no frontend to serve — SetAssets was never called")
		return
	}
	fileServer := http.FileServer(http.FS(uiAssets))

	// Reuse Wails' own call machinery: a MessageProcessor resolves Call.ByID
	// against the process-global bindings; the HTTP transport decodes the
	// /wails/runtime POST body and invokes it.
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	mp := application.NewMessageProcessor(quiet)
	tr := application.NewHTTPTransport()
	_ = tr.Start(context.Background(), mp)
	runtimeMW := tr.Handler() // middleware: intercepts /wails/runtime, else next

	mux := http.NewServeMux()
	mux.HandleFunc("/wails/events", remoteHub.serve)
	// Everything else: the runtime middleware (handles /wails/runtime) falling
	// back to the static frontend.
	mux.Handle("/", runtimeMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isIndexRequest(r.URL.Path) {
			serveRemoteIndex(w, r, uiAssets)
			return
		}
		fileServer.ServeHTTP(w, r)
	})))

	port := Port()
	shell.EnsureFirewallRule(port) // best-effort; harmless when the firewall is off

	// Listen on the IPv4 wildcard EXPLICITLY. &http.Server{Addr: ":port"} binds
	// the IPv6 wildcard (`::`) in dual-stack mode; on Windows with several
	// adapters (Ethernet + Radmin/Tailscale + IPv6) that has been flaky for
	// inbound IPv4 from a phone on the LAN, while the PC's own loopback test to
	// its LAN IP still succeeds — masking the problem. tcp4/0.0.0.0 accepts every
	// IPv4 interface with no dual-stack ambiguity.
	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		listenErr = fmt.Sprintf("port %d is taken by another program", port)
		core.LogEvent("error", "web interface: cannot listen on port %d: %v", port, err)
		shell.RemoveFirewallRule()
		remoteServer = nil
		return
	}
	listenErr = ""
	remoteServer = &http.Server{Handler: remoteAuth(token, mux)}
	srv := remoteServer
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			core.LogEvent("error", "web interface: the server stopped: %v", err)
			remoteMu.Lock()
			if remoteServer == srv {
				remoteServer = nil
				listenErr = "the server stopped; the log says why"
			}
			remoteMu.Unlock()
		}
	}()
	// The key is deliberately NOT logged. It is a full-control credential —
	// every binding is callable with it — and this line goes to stderr, which on
	// a desktop session means the system journal. The complete link, with the
	// key and a QR beside it, is in Settings.
	if ips := LocalIPv4s(); len(ips) > 0 {
		log.Printf("remote: listening — http://%s:%d/ (key in Settings)", ips[0], port)
	} else {
		log.Printf("remote: listening on 0.0.0.0:%d (key in Settings)", port)
	}
}

// Port is the web-interface port in effect: the VAIR_REMOTE_PORT env override wins,
// then the Settings value, then core.WebPort() (19876, the 1.10 port). The
// override/setting matter when the 1.10 release is still running on 19876 on
// the same machine.
func Port() int {
	if v := os.Getenv("VAIR_REMOTE_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	if p := core.RemotePortSetting(); p > 0 {
		return p
	}
	return core.WebPort()
}

// Restart applies a port/token change: the running server binds the
// old port and its auth middleware holds the old token in a closure, so both
// changes need a stop + fresh start (no-op parts are cheap/idempotent).
func Restart() {
	stop()
	ApplySetting()
}

// stopRemoteServer shuts the LAN server down (idempotent).
func stop() {
	remoteMu.Lock()
	srv := remoteServer
	remoteServer = nil
	listenErr = ""
	remoteMu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	remoteHub.closeAll()
	shell.RemoveFirewallRule()
}

// ApplySetting starts or stops the server to match the current setting.
// VAIR_REMOTE_FORCE=1 forces it on regardless (headless / testing).
func ApplySetting() {
	enabled, _ := core.RemoteConfig()
	if os.Getenv("VAIR_REMOTE_FORCE") == "1" {
		enabled = true
	}
	if enabled {
		start()
	} else {
		stop()
	}
}

// uiAssets is the built frontend, handed over by main at startup — //go:embed
// cannot name frontend/dist from in here.
var uiAssets fs.FS

// SetAssets gives this package the frontend to serve. Called once, before the
// server can be started.
func SetAssets(f fs.FS) { uiAssets = f }

// sameSecret compares two credentials without leaking where they diverge.
func sameSecret(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// remoteAuth gates every request on the token: a valid ?key= (which then sets an
// auth cookie so subsequent asset/API requests pass) or the cookie itself.
func remoteAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.Error(w, "remote access not configured", http.StatusServiceUnavailable)
			return
		}
		// Constant time: the comparison is against a secret, and a byte-by-byte
		// one answers a little faster the earlier it differs.
		if key := r.URL.Query().Get("key"); sameSecret(key, token) {
			http.SetCookie(w, &http.Cookie{
				Name: remoteCookie, Value: token, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600,
			})
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(remoteCookie); err == nil && sameSecret(c.Value, token) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

// Emit fans a core event out to every connected SSE client, shaped like a
// Wails event ({name,data}) so the browser shim can hand it straight to
// window._wails.dispatchWailsEvent. No-op when nothing is connected.
func Emit(event string, data any) {
	if !remoteHub.hasClients() {
		return
	}
	b, err := json.Marshal(struct {
		Name string `json:"name"`
		Data any    `json:"data"`
	}{event, data})
	if err != nil {
		return
	}
	remoteHub.broadcast(b)
}

// ── SSE hub ────────────────────────────────────────────────────────────────

type sseHub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func (h *sseHub) hasClients() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients) > 0
}

func (h *sseHub) add() chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *sseHub) remove(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *sseHub) closeAll() {
	h.mu.Lock()
	for ch := range h.clients {
		delete(h.clients, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *sseHub) broadcast(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- b:
		default: // slow client — drop this event rather than block the emitter
		}
	}
}

func (h *sseHub) serve(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := h.add()
	defer h.remove(ch)

	// A retry hint + an initial comment so EventSource considers the stream open.
	fmt.Fprint(w, "retry: 2000\n: connected\n\n")
	flusher.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case b, ok := <-ch:
			if !ok {
				return
			}
			w.Write([]byte("data: "))
			w.Write(b)
			w.Write([]byte("\n\n"))
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// LocalIPv4s returns the machine's non-loopback IPv4 addresses (for the
// "open on your phone" URL in Settings), REAL home-LAN ranges first: a PC with
// Radmin VPN (26.0.0.0/8), Hamachi (25/8), Tailscale (100.64/10) or other
// virtual adapters otherwise gets one of those as [0] — an address the phone's
// Wi-Fi can't reach. Link-local 169.254.* (APIPA) is dropped entirely.
func LocalIPv4s() []string {
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	rank := func(ip net.IP) int {
		switch {
		case ip[0] == 192 && ip[1] == 168:
			return 0 // typical home router LAN
		case ip[0] == 10:
			return 1
		case ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31:
			return 2
		default:
			return 3 // virtual/VPN adapters (26.x Radmin, 25.x Hamachi, 100.64+ CGNAT…)
		}
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		v4 := ipnet.IP.To4()
		if v4 == nil || (v4[0] == 169 && v4[1] == 254) {
			continue
		}
		out = append(out, v4.String())
	}
	sort.SliceStable(out, func(i, j int) bool {
		return rank(net.ParseIP(out[i]).To4()) < rank(net.ParseIP(out[j]).To4())
	})
	return out
}

// ── remote-mode marker ────────────────────────────────────────────────────────
//
// The frontend has to know whether it is running inside the native window or in
// a browser over the LAN: native window controls, the tray and second windows
// only exist in the former. It used to infer this from the presence of the
// WebView2 bridge (window.chrome.webview), which is a WINDOWS-ONLY object — so
// on Linux the native app decided it was a remote browser and hid its own window
// controls, opened a redundant event stream and branched wrongly everywhere else.
//
// The signal is now explicit and set by the only party that actually knows: the
// remote server stamps the pages IT serves. Anything without the stamp is the
// native window, on every platform.
const remoteMarker = `<script>window.__VAIR_REMOTE__=true</script>`

func isIndexRequest(p string) bool {
	return p == "/" || p == "/index.html"
}

// serveRemoteIndex sends index.html with the marker injected. It falls back to
// the untouched file if anything is unexpected — a missing marker degrades to
// the old behaviour rather than a blank page.
func serveRemoteIndex(w http.ResponseWriter, r *http.Request, dist fs.FS) {
	raw, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	html := string(raw)
	if i := strings.Index(html, "<head>"); i >= 0 {
		html = html[:i+len("<head>")] + remoteMarker + html[i+len("<head>"):]
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "index.html", time.Time{}, strings.NewReader(html))
}
