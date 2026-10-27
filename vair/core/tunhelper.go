package core

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"vair/helper/api"
)

// The app's side of the privileged helper: the Linux helper, and the Windows
// helper service. The app never runs with the rights a tunnel needs; this file
// is the whole of what it may do to the half that does — connect, hand over a
// validated config, keep a lease alive, stop, and on Windows ask for the blocks
// and firewall rules it used to put up itself.
//
// Nothing here parses or generates a configuration; that stays with the rest of
// the engine code. This is transport. How a connection is opened, and how a
// missing or unreachable helper is explained, is per platform
// (tunhelper_linux.go, tunhelper_windows.go).

// helperClient is one connection to the helper, owning one tunnel.
type helperClient struct {
	conn *api.Conn
	nc   net.Conn

	mu   sync.Mutex
	next uint64
	// replies routes an answer back to whoever sent the request. The read loop
	// owns the socket, so every caller waits on a channel rather than reading.
	replies map[uint64]chan *api.Response

	closeOnce sync.Once
	done      chan struct{}

	// The kill-switch as this connection last set it, and what it is built
	// from: kill_switch requests after start_tun carry the same addresses.
	blockMu   sync.Mutex
	block     bool
	serverIPs []string
}

// HoldsBlock reports whether this tunnel's kill-switch is up.
func (h *helperClient) HoldsBlock() bool {
	h.blockMu.Lock()
	defer h.blockMu.Unlock()
	return h.block
}

// SetKillSwitch puts the block up or takes it down for the running tunnel.
func (h *helperClient) SetKillSwitch(on bool) error {
	h.blockMu.Lock()
	ips := h.serverIPs
	h.blockMu.Unlock()
	if _, err := h.call(&api.Request{Op: api.OpKillSwitch, KillSwitch: on, AllowLAN: allowLANTraffic(), IPv6Around: helperIPv6Around(), ServerIPs: ips}, 15*time.Second); err != nil {
		return err
	}
	h.blockMu.Lock()
	h.block = on
	h.blockMu.Unlock()
	return nil
}

// StopForHandover stops the tunnel and leaves its block for the next one — the
// teardown of a switch. The helper takes the block down itself if no tunnel
// follows within BlockHandoverSec; a disconnect takes it down at once
// (releaseTunnelBlock).
func (h *helperClient) StopForHandover() error {
	keep := h.HoldsBlock()
	_, err := h.call(&api.Request{Op: api.OpStop, KeepBlock: keep}, 15*time.Second)
	if err == nil && keep {
		noteBlockKept()
	}
	return err
}

// newHelperClient completes the handshake on a connection the platform's
// dialHelper opened.
func newHelperClient(nc net.Conn) (*helperClient, error) {
	h := &helperClient{
		conn:    api.NewConn(nc),
		nc:      nc,
		replies: make(map[uint64]chan *api.Response),
		done:    make(chan struct{}),
	}
	go h.readLoop()

	resp, err := h.call(&api.Request{Op: api.OpHello, Protocol: api.ProtocolVersion}, 5*time.Second)
	if err != nil {
		h.Close()
		return nil, err
	}
	if err := api.CheckProtocol(resp.Protocol); err != nil {
		h.Close()
		return nil, err
	}
	return h, nil
}

// readLoop is the only reader. It routes answers by id and turns the helper's
// unsolicited frames into log lines, which is how the engine's output reaches
// the app's log panel looking exactly as it does when the app spawns the engine
// itself.
func (h *helperClient) readLoop() {
	defer close(h.done)
	for {
		resp, err := h.conn.ReadResponse()
		if err != nil {
			h.failPending(err)
			return
		}
		if resp.ID == 0 {
			if resp.Log != nil {
				// In TUN mode the engine belongs to the helper, so this socket is
				// where its output arrives — and the counters and the connections
				// panel have no other source on this path.
				if ruleStatsEnabled() || connectionsWatched() {
					noteEngineLine(resp.Log.Src, resp.Log.Text)
				}
				logs.add(resp.Log.Src, resp.Log.Lvl, resp.Log.Text)
			}
			// An unsolicited status means the helper's own view changed without
			// us asking — today, an engine that died. Acting on it is what keeps
			// the app from showing a connection that has already ended.
			if resp.Status != nil && !resp.Status.Running {
				go tunnelLost("the tunnel engine stopped unexpectedly")
			}
			continue
		}
		h.mu.Lock()
		ch := h.replies[resp.ID]
		delete(h.replies, resp.ID)
		h.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
}

func (h *helperClient) failPending(err error) {
	h.mu.Lock()
	for id, ch := range h.replies {
		ch <- &api.Response{ID: id, Error: "helper connection lost: " + err.Error()}
		delete(h.replies, id)
	}
	h.mu.Unlock()
}

func (h *helperClient) call(req *api.Request, timeout time.Duration) (*api.Response, error) {
	h.mu.Lock()
	h.next++
	req.ID = h.next
	ch := make(chan *api.Response, 1)
	h.replies[req.ID] = ch
	h.mu.Unlock()

	h.nc.SetWriteDeadline(time.Now().Add(timeout)) //nolint:errcheck
	err := h.conn.WriteRequest(req)
	h.nc.SetWriteDeadline(time.Time{}) //nolint:errcheck
	if err != nil {
		h.mu.Lock()
		delete(h.replies, req.ID)
		h.mu.Unlock()
		return nil, fmt.Errorf("send %s: %w", req.Op, err)
	}

	select {
	case resp := <-ch:
		if resp.Error != "" {
			return resp, errors.New(resp.Error)
		}
		return resp, nil
	case <-time.After(timeout):
		h.mu.Lock()
		delete(h.replies, req.ID)
		h.mu.Unlock()
		return nil, fmt.Errorf("%s timed out after %s", req.Op, timeout)
	case <-h.done:
		return nil, errors.New("helper connection closed")
	}
}

// PutRuleSet stages one rule-set file with the helper and returns the path the
// configuration should name — the helper's answer (protocol 5), or the fixed
// path an older helper uses.
//
// Timeout is generous because this is the one call that carries real data: a
// geosite list is hundreds of kilobytes, and the helper writes it to disk before
// answering.
func (h *helperClient) PutRuleSet(name string, data []byte) (string, error) {
	resp, err := h.call(&api.Request{
		Op:   api.OpPutRuleSet,
		Name: name,
		Data: data,
	}, 20*time.Second)
	if err != nil {
		return "", err
	}
	if resp.Path != "" {
		return resp.Path, nil
	}
	return api.RuleSetPath(name), nil
}

// StartTUN hands a configuration over and starts pinging.
//
// The ping is not a health check for us; it is the lease the helper uses to
// decide we are still here. Stopping it — because this process was killed, or
// wedged — is what tears the tunnel down, so the goroutine deliberately has no
// error path that quietly gives up while the process lives.
func (h *helperClient) StartTUN(cfg []byte, killSwitch, allowLAN, ipv6Around bool, serverIPs []string) (api.Status, error) {
	resp, err := h.call(&api.Request{
		Op:         api.OpStartTUN,
		Config:     cfg,
		LeaseSec:   api.DefaultLeaseSec,
		KillSwitch: killSwitch,
		AllowLAN:   allowLAN,
		IPv6Around: ipv6Around,
		ServerIPs:  serverIPs,
	}, 30*time.Second)
	if err != nil {
		return api.Status{}, err
	}
	h.blockMu.Lock()
	h.block, h.serverIPs = killSwitch, serverIPs
	h.blockMu.Unlock()
	go h.keepAlive()
	if resp.Status != nil {
		return *resp.Status, nil
	}
	return api.Status{}, nil
}

func (h *helperClient) keepAlive() {
	t := time.NewTicker(time.Duration(api.PingIntervalSec) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-t.C:
			if _, err := h.call(&api.Request{Op: api.OpPing}, 5*time.Second); err != nil {
				// A failed ping means the connection is gone, which the helper
				// has already noticed from its own side. Nothing to recover
				// here; the read loop will close done.
				return
			}
		}
	}
}

// Stop asks for a clean teardown. Closing the connection would achieve the same
// thing, but an explicit stop lets the helper report problems.
func (h *helperClient) Stop() error {
	_, err := h.call(&api.Request{Op: api.OpStop}, 15*time.Second)
	return err
}

func (h *helperClient) Status() (api.Status, error) {
	resp, err := h.call(&api.Request{Op: api.OpStatus}, 5*time.Second)
	if err != nil {
		return api.Status{}, err
	}
	if resp.Status == nil {
		return api.Status{}, errors.New("helper returned no status")
	}
	return *resp.Status, nil
}

func (h *helperClient) Close() {
	h.closeOnce.Do(func() { h.nc.Close() })
}

// The Windows requests (protocol 5): the blocks and rules the app used to put
// up itself with administrator rights.

// SetPersistentBlock puts the persistent block up (pointed at the tunnel
// running now, if any) or takes every block of that kind down.
func (h *helperClient) SetPersistentBlock(on, allowLAN bool) (api.BlockState, error) {
	resp, err := h.call(&api.Request{Op: api.OpPersistentBlock, Enable: on, AllowLAN: allowLAN}, 30*time.Second)
	if err != nil {
		return api.BlockState{}, err
	}
	if resp.Block != nil {
		return *resp.Block, nil
	}
	return api.BlockState{}, nil
}

// SetSessionBlock puts the proxy kill-switch's block up, or takes it down —
// keep hands it over for BlockHandoverSec instead.
func (h *helperClient) SetSessionBlock(on, allowLAN, keep bool) error {
	_, err := h.call(&api.Request{Op: api.OpSessionBlock, Enable: on, AllowLAN: allowLAN, KeepBlock: keep}, 30*time.Second)
	return err
}

// Firewall sets the inbound rules: the web interface's port and the LAN
// proxy's two; 0 removes a rule.
func (h *helperClient) Firewall(remotePort, lanHTTP, lanSocks int) error {
	_, err := h.call(&api.Request{Op: api.OpFirewall, RemotePort: remotePort, LANHTTPPort: lanHTTP, LANSocksPort: lanSocks}, 60*time.Second)
	return err
}

// QuickBlockState is BlockStatus without the question about a 2.6.0
// firewall block, which costs the service a PowerShell launch: the state the
// status request carries. For the frequent checks.
func (h *helperClient) QuickBlockState() (api.BlockState, error) {
	resp, err := h.call(&api.Request{Op: api.OpStatus}, 10*time.Second)
	if err != nil {
		return api.BlockState{}, err
	}
	if resp.Block == nil {
		return api.BlockState{}, errors.New("helper returned no block state")
	}
	return *resp.Block, nil
}

// BlockStatus says which blocks stand.
func (h *helperClient) BlockStatus() (api.BlockState, error) {
	resp, err := h.call(&api.Request{Op: api.OpBlockStatus}, 30*time.Second)
	if err != nil {
		return api.BlockState{}, err
	}
	if resp.Block == nil {
		return api.BlockState{}, errors.New("helper returned no block state")
	}
	return *resp.Block, nil
}
