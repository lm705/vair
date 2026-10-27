package core

import (
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Does this server open the thing you need?
//
// For a list of free configurations, "is it fast" is the second question. The
// first is whether ChatGPT answers, whether YouTube plays, whether the bank
// lets you in — and every client that checks this checks the one server you are
// connected to, one service at a time. We test in bulk already, so we can ask
// the whole list at once and then filter it by the answer.
//
// Each probe is one HTTP request through a throwaway engine for that config —
// the same machinery ping and speed use. What makes a probe a probe rather than
// a fetch is reading the answer: a refusal for the country is a 200 with a
// particular body, not an error, and a client that only looks at the status
// code reports every blocked service as working.

// unlockService is one thing to check.
//
// The rule for reading an answer is data, not code, and that is deliberate: the
// seven services below were seven hand-written closures that all said the same
// three things in slightly different words, and a list the user can add to
// cannot have a closure per entry anyway. One evaluator, one shape, and a
// service somebody adds tomorrow is as expressive as the ones that shipped.
type unlockService struct {
	ID   string
	Name string
	URL  string
	// OpenIf: when set, the answer counts as open only if it contains one of
	// these. Cloudflare's trace naming a country is the example — a 200 that
	// does not say "loc=" is a challenge page, not an answer.
	OpenIf []string
	// BlockedIf: any of these in the answer is a refusal, whatever the status.
	// A regional block is usually a 200 with a particular page.
	BlockedIf []string
	// Builtin marks a service that shipped with the program, so the editor can
	// offer "restore" rather than only "delete".
	Builtin bool
}

// verdict turns one response into one answer. Empty body and status 0 mean the
// request failed outright.
func (s unlockService) verdict(status int, body string) unlockState {
	switch {
	case status == 0:
		return unlockFailed
	// 403 is how every one of these says "not from your country" when it says it
	// with a status code at all.
	case status == 403:
		return unlockBlocked
	}
	for _, marker := range s.BlockedIf {
		if marker != "" && strings.Contains(body, marker) {
			return unlockBlocked
		}
	}
	if len(s.OpenIf) > 0 {
		for _, marker := range s.OpenIf {
			if marker != "" && strings.Contains(body, marker) {
				return unlockOpen
			}
		}
		return unlockBlocked
	}
	if status >= 200 && status < 400 {
		return unlockOpen
	}
	return unlockBlocked
}

type unlockState string

const (
	unlockOpen    unlockState = "open"    // it works from this server
	unlockBlocked unlockState = "blocked" // it answered, and said no
	unlockFailed  unlockState = "failed"  // it did not answer at all
)

// unlockBodyLimit is how much of an answer is read. The tells are in the first
// few hundred bytes; a video service would otherwise hand us a playlist.
const unlockBodyLimit = 64 << 10

// builtinUnlockServices is the list as it ships, in the order the panel shows
// it. The user's own list starts as a copy of this one.
//
// The endpoints are the ones each service uses to tell a client it is not
// welcome — not the front page, which is usually served to everyone and proves
// nothing.
var builtinUnlockServices = []unlockService{
	{
		ID: "chatgpt", Name: "ChatGPT",
		URL: "https://chatgpt.com/cdn-cgi/trace",
		// Cloudflare's trace names the country it sees; anything else is a
		// challenge page. OpenAI refuses a list of countries outright with 403.
		OpenIf:  []string{"loc="},
		Builtin: true,
	},
	{
		ID: "claude", Name: "Claude",
		URL:     "https://claude.ai/cdn-cgi/trace",
		OpenIf:  []string{"loc="},
		Builtin: true,
	},
	{
		ID: "gemini", Name: "Gemini",
		URL:       "https://gemini.google.com/",
		BlockedIf: []string{"not available in your country"},
		Builtin:   true,
	},
	{
		ID: "youtube", Name: "YouTube",
		URL:     "https://www.youtube.com/generate_204",
		Builtin: true,
	},
	{
		ID: "netflix", Name: "Netflix",
		URL:       "https://www.netflix.com/title/81280792", // a title with no local rights everywhere
		BlockedIf: []string{"page-not-found"},
		Builtin:   true,
	},
	{
		ID: "spotify", Name: "Spotify",
		URL:     "https://open.spotify.com/",
		Builtin: true,
	},
	{
		ID: "tiktok", Name: "TikTok",
		URL:     "https://www.tiktok.com/",
		Builtin: true,
	},
}

// effectiveUnlockServices is the list to use: what shipped, as edited by the
// user. An empty saved list means nothing has been edited.
func effectiveUnlockServices() []unlockService {
	settingsMu.RLock()
	saved := append([]UnlockServiceDef(nil), appSettings.UnlockServices...)
	settingsMu.RUnlock()
	if len(saved) == 0 {
		return append([]unlockService(nil), builtinUnlockServices...)
	}
	builtinByID := map[string]unlockService{}
	for _, b := range builtinUnlockServices {
		builtinByID[b.ID] = b
	}
	out := make([]unlockService, 0, len(saved))
	for _, d := range saved {
		if strings.TrimSpace(d.ID) == "" || strings.TrimSpace(d.URL) == "" {
			continue
		}
		_, isBuiltin := builtinByID[d.ID]
		out = append(out, unlockService{
			ID:        d.ID,
			Name:      strings.TrimSpace(d.Name),
			URL:       strings.TrimSpace(d.URL),
			OpenIf:    nonEmpty(d.OpenIf),
			BlockedIf: nonEmpty(d.BlockedIf),
			Builtin:   isBuiltin,
		})
	}
	if len(out) == 0 {
		return append([]unlockService(nil), builtinUnlockServices...)
	}
	return out
}

// nonEmpty drops blank markers, which an editor with an empty field produces and
// which would otherwise match every answer.
func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// UnlockService is one entry of the list the panel offers, and one row of its
// editor. The markers are exposed because a service the user adds is only as
// good as its rule for reading an answer.
type UnlockService struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	OpenIf    []string `json:"open_if,omitempty"`
	BlockedIf []string `json:"blocked_if,omitempty"`
	Builtin   bool     `json:"builtin"`
}

// UnlockServices lists what can be checked.
func UnlockServices() []UnlockService {
	eff := effectiveUnlockServices()
	out := make([]UnlockService, 0, len(eff))
	for _, s := range eff {
		out = append(out, UnlockService{
			ID: s.ID, Name: s.Name, URL: s.URL,
			OpenIf: s.OpenIf, BlockedIf: s.BlockedIf, Builtin: s.Builtin,
		})
	}
	return out
}

// SetUnlockServices saves the edited list. An entry with no id or an address
// that is not a web address is dropped; an empty list means "back to what
// shipped". Ids are made unique: two services sharing one overwrote each
// other's answers and were picked together.
func SetUnlockServices(list []UnlockService) {
	defs := make([]UnlockServiceDef, 0, len(list))
	seen := map[string]bool{}
	for _, s := range list {
		id := strings.TrimSpace(s.ID)
		url := strings.TrimSpace(s.URL)
		if id == "" || !isWebAddress(url) {
			continue
		}
		for base, n := id, 2; seen[id]; n++ {
			id = base + "-" + strconv.Itoa(n)
		}
		seen[id] = true
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = id
		}
		defs = append(defs, UnlockServiceDef{
			ID: id, Name: name, URL: url,
			OpenIf: nonEmpty(s.OpenIf), BlockedIf: nonEmpty(s.BlockedIf),
		})
	}
	settingsMu.Lock()
	appSettings.UnlockServices = defs
	settingsMu.Unlock()
	saveSettings()
}

// isWebAddress is an http(s) address with a host in it.
func isWebAddress(s string) bool {
	u, err := neturl.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && strings.Contains(u.Host, ".")
}

// UnlockResult is one config's answers.
type UnlockResult struct {
	Index int               `json:"index"`
	Raw   string            `json:"raw"`
	Name  string            `json:"name"`
	State map[string]string `json:"state"` // service id → open | blocked | failed
	Done  bool              `json:"done"`
}

// UnlockProgress is what the panel polls while a run is going.
type UnlockProgress struct {
	Running bool           `json:"running"`
	Done    int            `json:"done"`
	Total   int            `json:"total"`
	Rows    []UnlockResult `json:"rows"`
}

var unlockRun = struct {
	sync.Mutex
	running bool
	cancel  chan struct{}
	done    int32
	total   int
	rows    map[int]*UnlockResult
	order   []int
}{rows: map[int]*UnlockResult{}}

// UnlockCheck starts a run over the given rows of a tab, or — with no rows
// named — over everything the table is currently showing.
//
// "Everything showing" is viewIndices, the same answer PING ALL and SPEED ALL
// get, so all three cover the same set. It is not "every row in the tab": a tab
// exclude filter, a search or a protocol pill all narrow what is on screen, and
// a check that ignored them would test rows the user has hidden. That was the
// behaviour until now, because an empty index list reached the loader as "these
// zero rows" and was then read as "all of them".
//
// Returns a reason when it will not start. One run at a time: each config is a
// whole engine, and two sweeps at once would measure each other's contention.
func UnlockCheck(tabID, sort, filter string, proto []string, indices []int, serviceIDs []string) string {
	unlockRun.Lock()
	if unlockRun.running {
		unlockRun.Unlock()
		return "a check is already running"
	}
	var picked []unlockService
	want := map[string]bool{}
	for _, id := range serviceIDs {
		want[id] = true
	}
	for _, s := range effectiveUnlockServices() {
		if want[s.ID] {
			picked = append(picked, s)
		}
	}
	if len(picked) == 0 {
		unlockRun.Unlock()
		return "no services selected"
	}
	if len(indices) == 0 {
		// nil from viewIndices means "nothing narrows the view", which
		// loadTestEntries reads as the whole tab. An empty slice would have meant
		// "these zero rows" — the bug this replaced.
		indices = viewIndices(sort, filter, proto)
	}
	entries := loadTestEntries(tabID, indices)
	if len(entries) == 0 {
		unlockRun.Unlock()
		return "nothing to check"
	}
	unlockRun.running = true
	unlockRun.cancel = make(chan struct{})
	unlockRun.done = 0
	unlockRun.total = len(entries)
	unlockRun.rows = map[int]*UnlockResult{}
	unlockRun.order = nil
	for _, e := range entries {
		es := e.snap()
		unlockRun.rows[es.Index] = &UnlockResult{Index: es.Index, Raw: es.Raw, Name: es.Name, State: map[string]string{}}
		unlockRun.order = append(unlockRun.order, es.Index)
	}
	cancel := unlockRun.cancel
	unlockRun.Unlock()

	go runUnlockSweep(entries, picked, cancel)
	return ""
}

// UnlockCancel stops a running check.
func UnlockCancel() {
	unlockRun.Lock()
	if unlockRun.running && unlockRun.cancel != nil {
		close(unlockRun.cancel)
		unlockRun.cancel = nil
	}
	unlockRun.Unlock()
}

// UnlockState is the current progress and whatever has been answered so far.
func UnlockState() UnlockProgress {
	unlockRun.Lock()
	defer unlockRun.Unlock()
	p := UnlockProgress{
		Running: unlockRun.running,
		Done:    int(atomic.LoadInt32(&unlockRun.done)),
		Total:   unlockRun.total,
	}
	for _, idx := range unlockRun.order {
		r := unlockRun.rows[idx]
		if r == nil {
			continue
		}
		copyState := make(map[string]string, len(r.State))
		for k, v := range r.State {
			copyState[k] = v
		}
		p.Rows = append(p.Rows, UnlockResult{Index: r.Index, Raw: r.Raw, Name: r.Name, State: copyState, Done: r.Done})
	}
	return p
}

// runUnlockSweep walks the configs, several at a time, asking each one every
// selected service.
func runUnlockSweep(entries []*ConfigEntry, services []unlockService, cancel chan struct{}) {
	defer func() {
		unlockRun.Lock()
		unlockRun.running = false
		unlockRun.cancel = nil
		unlockRun.Unlock()
	}()

	sem := make(chan struct{}, currentPingConcurrency())
	var wg sync.WaitGroup
	for _, e := range entries {
		select {
		case <-cancel:
			return
		default:
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(entry *ConfigEntry) {
			defer wg.Done()
			defer func() { <-sem }()
			checkOneConfig(entry, services, cancel)
			atomic.AddInt32(&unlockRun.done, 1)
		}(e)
	}
	wg.Wait()
	if sum := unlockSummary(UnlockState()); sum != "" {
		vlog("info", "service check finished: %s", sum)
	}
}

// checkOneConfig runs every selected probe through one config, in one engine
// session: starting an engine is most of the cost, and the answers are about
// the server, not about the moment.
func checkOneConfig(entry *ConfigEntry, services []unlockService, cancel chan struct{}) {
	es := entry.snap()
	set := func(id string, st unlockState) {
		unlockRun.Lock()
		if r := unlockRun.rows[es.Index]; r != nil {
			r.State[id] = string(st)
		}
		unlockRun.Unlock()
		rememberUnlock(es.Raw, id, st)
	}
	finish := func() {
		unlockRun.Lock()
		if r := unlockRun.rows[es.Index]; r != nil {
			r.Done = true
		}
		unlockRun.Unlock()
	}
	defer finish()

	n, err := parseNode(es.Raw)
	if err != nil || nodeUnsupportedReason(n) != "" {
		for _, s := range services {
			set(s.ID, unlockFailed)
		}
		return
	}
	ttl := startupTimeout + time.Duration(len(services)+1)*8*time.Second
	_ = withEngine(n, ttl, func(_ int, tr *http.Transport) error {
		client := unlockClient(tr)
		for _, s := range services {
			select {
			case <-cancel:
				return nil
			default:
			}
			set(s.ID, unlockState(probeService(client, s)))
		}
		return nil
	})
}

// unlockClient asks through one engine. Redirects are followed, and the page
// they end on is what is judged — its status, its body and its address
// (probeService). Stopping at the first redirect read every 3xx as "opens",
// including the one that leads to a "not available in your country" page.
func unlockClient(tr *http.Transport) *http.Client {
	return &http.Client{
		Transport: tr,
		Timeout:   8 * time.Second,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// probeService asks one service and reads enough of the answer to judge it.
func probeService(client *http.Client, s unlockService) unlockState {
	req, err := http.NewRequest("GET", s.URL, nil)
	if err != nil {
		return unlockFailed
	}
	// A browser's headers: several of these services answer a bare Go client
	// with a challenge page that says nothing about the country.
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return unlockFailed
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, unlockBodyLimit))
	// The address the redirects ended on is part of the answer: a refusal is
	// often a page whose path says so (/unavailable, /blocked, ?country=…), and
	// a marker can name it.
	where := ""
	if resp.Request != nil && resp.Request.URL != nil {
		where = resp.Request.URL.String() + "\n"
	}
	return s.verdict(resp.StatusCode, where+string(body))
}

// unlockSummary is used by the log line at the end of a run.
func unlockSummary(p UnlockProgress) string {
	open := map[string]int{}
	for _, r := range p.Rows {
		for id, st := range r.State {
			if st == string(unlockOpen) {
				open[id]++
			}
		}
	}
	var parts []string
	for _, s := range effectiveUnlockServices() {
		if n, ok := open[s.ID]; ok {
			parts = append(parts, fmt.Sprintf("%s %d", s.Name, n))
		}
	}
	return strings.Join(parts, ", ")
}

// ─────────────────── what the table filter reads ───────────────────
//
// The results of a run are also an answer to "which of these servers opens
// ChatGPT", and that is a question about the LIST, not about the panel. Keyed by
// the config's raw URL rather than its row index: the index is a position in the
// current sort and means nothing an hour later, while the URL is the server.
//
// In memory, like everything else here, and dropped when the program closes.

var unlockIndex = struct {
	sync.RWMutex
	m   map[string]map[string]unlockState // raw URL → service id → answer
	ver int64                             // bumped on every write, so the table's cache knows
}{m: map[string]map[string]unlockState{}}

func rememberUnlock(raw, service string, st unlockState) {
	if raw == "" {
		return
	}
	unlockIndex.Lock()
	byService := unlockIndex.m[raw]
	if byService == nil {
		byService = map[string]unlockState{}
		unlockIndex.m[raw] = byService
	}
	byService[service] = st
	unlockIndex.ver++
	unlockIndex.Unlock()
}

// unlockOpens reports whether the last check said this config opens the service.
// An unchecked config is not a match: "show me what opens ChatGPT" cannot
// honestly include the ones nobody asked.
func unlockOpens(raw, service string) bool {
	unlockIndex.RLock()
	defer unlockIndex.RUnlock()
	return unlockIndex.m[raw][service] == unlockOpen
}

// unlockVersion lets the window cache notice that answers have arrived.
func unlockVersion() int64 {
	unlockIndex.RLock()
	defer unlockIndex.RUnlock()
	return unlockIndex.ver
}
