package core

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"time"
)

// appVersion is the running version. 1.10 kept this const in selfupdate.go;
// relocated here (remove selfupdate.go's copy when it ports). Must stay a const
// because subFetchUserAgent (fetch.go) concatenates it at compile time.
const appVersion = "2.0.0"

// Engine timeouts (relocated from the 1.10 main.go const block; the others there
// are already defined within the ported engine files).
const (
	xrayStartupTimeout    = 8 * time.Second
	singboxStartupTimeout = 10 * time.Second
	xrayConnTimeout       = 12 * time.Second
	singboxConnTimeout    = 15 * time.Second
	tunStartupTimeout     = 3 * time.Second
	dialTimeout           = 5 * time.Second
	warmupTimeout         = 4 * time.Second
	startupTimeout        = 4 * time.Second
	pingTimeout           = 1500 * time.Millisecond
	pingRounds            = 3
	speedDuration         = 4 * time.Second
	pingTestURLDefault    = "https://www.gstatic.com/generate_204"
	speedTestURLDefault   = "https://speed.cloudflare.com/__down?bytes=50000000"
	speedUserAgent        = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// Local proxy / (legacy) web ports, relocated from main.go. webPort is vestigial
// in 2.0 (no HTTP server) but settings.go still validates user ports against it.
const (
	connHTTPPort  = 10819
	connSOCKSPort = 10818
	webPort       = 19876
)

// WebPort is the LAN remote-control server port (the 1.10 web port). Exposed so
// the shell's remote server and the settings UI agree on it.
func WebPort() int { return webPort }

// checkExitHost is the geo/IP service for the "check IP" button (relocated from
// handlers.go); routing forces it through the proxy in only-blocked mode.
const checkExitHost = "ip-api.com"

// binDir holds the extracted engine binaries (xray/sing-box) + geo data. The
// shell sets it via SetBinDir after extracting the embedded engines on disk.
var binDir string

// SetBinDir is called by the shell once the engines are extracted.
func SetBinDir(path string) { binDir = path }

// SetEngines records the on-disk paths of the extracted engine binaries so the
// connect path can spawn them.
func SetEngines(xray, singbox string) {
	state.xrayBin = xray
	state.singboxBin = singbox
}

// proxyAuthUser/Pass are the credentials for Vair's local SOCKS/HTTP inbound,
// randomised per run (relocated from main.go).
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
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Spawned engine PIDs are tracked so test/connection processes can be reaped on
// shutdown (relocated from routes.go).
var (
	spawnedPIDs   []int
	spawnedPIDsMu sync.Mutex
)

func trackPID(pid int) {
	spawnedPIDsMu.Lock()
	spawnedPIDs = append(spawnedPIDs, pid)
	spawnedPIDsMu.Unlock()
}

func untrackPID(pid int) {
	spawnedPIDsMu.Lock()
	for i, p := range spawnedPIDs {
		if p == pid {
			spawnedPIDs = append(spawnedPIDs[:i], spawnedPIDs[i+1:]...)
			break
		}
	}
	spawnedPIDsMu.Unlock()
}

// KillOrphanedXray reaps any tracked engine processes; the shell calls it on quit.
func KillOrphanedXray() {
	spawnedPIDsMu.Lock()
	pids := make([]int, len(spawnedPIDs))
	copy(pids, spawnedPIDs)
	spawnedPIDsMu.Unlock()
	for _, pid := range pids {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
}

// loadedSignal tells the frontend a tab's config set changed; it then re-fetches
// the visible window. (Relocated from handlers.go.)
func loadedSignal(tabID string) {
	state.broadcast(SSEEvent{Type: "loaded", Payload: nil, Tab: tabID})
}

// activeTabID returns the id of the currently active tab. (Relocated from handlers.go.)
func activeTabID() string {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.activeTab
}
