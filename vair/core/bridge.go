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
const appVersion = "2.6.0"

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

// checkExitHost is the fallback hostname for the "check IP" button when the
// configured URL cannot be parsed. The service itself is a setting — see
// currentCheckIPURL — and the routing follows it through checkIPHost().
const checkExitHost = "ip-api.com"

// checkIPURLDefault is the service the "check IP" button asks by default: free,
// no key, and it answers over plain HTTP, which matters because this request has
// to survive whatever the routing is doing.
const checkIPURLDefault = "http://ip-api.com/json/?fields=status,message,country,countryCode,city,query,isp"

// binDir holds the engine binaries (xray/sing-box) + the geo baselines shipped
// beside them. The shell sets it via SetBinDir once the engines are on disk.
//
// It may be READ-ONLY. On Linux the engines are meant to come from the package
// (/usr/lib/vair/bin, root-owned) rather than being extracted into the user's
// home — that is what lets the privileged helper spawn a binary the user cannot
// replace. So nothing may write here.
var binDir string

// ruleSetDir is where refreshed rule-sets and downloaded blocklists are WRITTEN.
// It is always user-writable.
//
// These used to share binDir, which held while both meant "%LOCALAPPDATA%\vair\
// bin". The moment the engines ship as package files the refresher would start
// writing to a read-only directory — and the failure is silent, because
// singboxCountryRuleSets skips rule-set files that aren't there by design. The
// result would be routing quietly losing its country rules with nothing in the
// log to explain it.
var ruleSetDir string

// SetBinDir is called by the shell once the engines are located.
func SetBinDir(path string) { binDir = path }

// SetRuleSetDir points the rule-set refresher at a writable directory. The shell
// calls it when that differs from binDir; when it doesn't, ruleSetRoot falls
// back to binDir and behaviour is unchanged.
func SetRuleSetDir(path string) { ruleSetDir = path }

// ruleSetRoot is the directory rule-set and blocklist files live in — the
// dedicated writable one when the shell set it, otherwise binDir (Windows,
// where the two are the same folder, and the tests, which set binDir alone).
func ruleSetRoot() string {
	if ruleSetDir != "" {
		return ruleSetDir
	}
	return binDir
}

// SetEngines records the on-disk paths of the extracted engine binaries so the
// connect path can spawn them — and so the routing rules can name them.
//
// Under the lock: the paths are registered once before anything can connect and
// then written again by the extraction goroutine, while the kill-switch script
// and the engine carve-out read them from whichever goroutine is building a
// configuration.
func SetEngines(xray, singbox string) {
	state.mu.Lock()
	state.xrayBin, state.singboxBin = xray, singbox
	state.mu.Unlock()
	// Ask the engine what it is, off this goroutine: it spawns a process, and
	// this is called on the startup path. What it learns decides one key in
	// every DNS block we build — see dnsIndependentCache — and answers the
	// first question of every bug report.
	go detectSingboxVersion()
}

// engineBins returns the two engine paths, or empty strings before they are
// registered (which is every unit test that does not set them).
//
// For readers that hold no lock — which is the probe helpers in connection.go
// and nothing else. Every connect path reads state.xrayBin/state.singboxBin
// directly and must keep doing so: those run with state.mu already held by
// their caller (the "…Locked" helpers around them say so), and a second
// acquisition here would deadlock rather than protect anything.
func engineBins() (xray, singbox string) {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.xrayBin, state.singboxBin
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
