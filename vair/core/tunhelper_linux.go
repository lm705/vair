package core

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"vair/helper/api"
)

// The GUI's side of the privileged helper.
//
// Windows runs the tunnel in-process because it can: the app is already
// elevated when TUN mode is on. Here the app is deliberately unprivileged and
// the tunnel belongs to a service, so this file is the whole of what the app
// may do to it — connect, hand over a validated config, keep a lease alive,
// stop.
//
// Nothing here parses or generates a configuration; that stays with the rest of
// the engine code, which already knows how. This is transport.

// helperInstalled reports whether the service has been set up on this machine.
//
// The socket, not the binary: the package ships vair-helper whether or not the
// user has ever enabled it, and a binary on disk says nothing about whether a
// service is running. systemd removes the whole RuntimeDirectory on stop, so
// the socket's existence tracks the service's.
func helperInstalled() bool {
	_, err := os.Stat(api.SocketPath)
	return err == nil
}

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
}

// dialHelper connects and completes the handshake.
func dialHelper() (*helperClient, error) {
	nc, err := net.DialTimeout("unix", api.SocketPath, 3*time.Second)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("the Vair helper is not installed")
		}
		// EACCES here means the socket exists but this account is not in its
		// group — the helper was installed for someone else.
		return nil, fmt.Errorf("cannot reach the helper: %w", err)
	}
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
				// where its output arrives — and rule statistics have no other
				// source on this path.
				if ruleStatsEnabled() {
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

// PutRuleSet stages one rule-set file with the helper.
//
// Timeout is generous because this is the one call that carries real data: a
// geosite list is hundreds of kilobytes, and the helper writes it to disk before
// answering.
func (h *helperClient) PutRuleSet(name string, data []byte) error {
	_, err := h.call(&api.Request{
		Op:   api.OpPutRuleSet,
		Name: name,
		Data: data,
	}, 20*time.Second)
	return err
}

// StartTUN hands a configuration over and starts pinging.
//
// The ping is not a health check for us; it is the lease the helper uses to
// decide we are still here. Stopping it — because this process was killed, or
// wedged — is what tears the tunnel down, so the goroutine deliberately has no
// error path that quietly gives up while the process lives.
func (h *helperClient) StartTUN(cfg []byte) (api.Status, error) {
	resp, err := h.call(&api.Request{
		Op:       api.OpStartTUN,
		Config:   cfg,
		LeaseSec: api.DefaultLeaseSec,
	}, 30*time.Second)
	if err != nil {
		return api.Status{}, err
	}
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

// helperReachable answers the question Capabilities needs: is there a helper we
// can actually talk to, and does it speak our protocol?
//
// It connects rather than stat-ing, because the failures that matter are not
// visible from the filesystem — a socket owned by a group this account is not
// in, or a helper left over from an older app version.
func helperReachable() (bool, string) {
	code, _ := helperProblem()
	return code == "", code
}

// helperProblem is the same question with the answer kept whole: a reason code
// for Capabilities, and the sentence to put in front of the user.
//
// The two are separate because they serve different consumers, and collapsing
// them lost the only message worth reading. A version-skewed helper used to
// surface as "installed but not reachable — check systemctl status", which sends
// the user to a command that reports the service is active and running. What
// they need is the line CheckProtocol already writes: which side is old, and
// that reinstalling the helper fixes it.
func helperProblem() (code, message string) {
	if !helperInstalled() {
		return "no_helper", "TUN mode needs the Vair helper service. Click \"install helper\" next to the TUN switch."
	}
	h, err := dialHelper()
	if err != nil {
		vlog("warn", "helper: %v", err)
		return "no_helper", err.Error()
	}
	h.Close()
	return "", ""
}
