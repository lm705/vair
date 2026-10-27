package core

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

// SOCKS over UDP, through Vair.
//
// A SOCKS client that wants UDP sends UDP ASSOCIATE on its TCP connection and
// is told where to send its datagrams. The engine behind Vair's SOCKS port
// listens on 127.0.0.1 and answers with that: right for a program on this
// machine, and useless to a phone, for which 127.0.0.1 is the phone itself — a
// device on the LAN could use the proxy for TCP and not for UDP (calls in
// Telegram, games, QUIC). For such a client Vair opens a relay of its own on
// the address the device reached the proxy at, puts that address in the
// answer, and carries the datagrams between the device and the engine. The
// SOCKS header of each datagram is the engine's business and passes unchanged.
// The relay lives as long as the TCP connection that asked for it, and takes
// datagrams only from the address of that connection's client.
//
// A client on this machine gets the same relay, on 127.0.0.1. It could reach
// the engine's own, but then its datagrams went past Vair's counter: in TUN
// mode — where sing-box is that client, carrying every UDP flow on the
// machine — a thousand NTP exchanges moved the session's traffic by the few
// kilobytes of TCP around them. QUIC, calls and games were missing from the
// statistics and from what the liveness check reads as the server answering.

// socksCmdWatch reads a SOCKS5 client's side of the handshake as it passes and
// remembers the command of its request (1 CONNECT, 3 UDP ASSOCIATE).
type socksCmdWatch struct {
	buf  []byte
	done bool
	cmd  atomic.Uint32
}

func (w *socksCmdWatch) feed(p []byte) {
	if w.done {
		return
	}
	w.buf = append(w.buf, p...)
	cmd, state := socks5ClientCmd(w.buf)
	if state == spyNeedMore && len(w.buf) < 1024 {
		return
	}
	if state == spyFound {
		w.cmd.Store(uint32(cmd))
	}
	w.done, w.buf = true, nil
}

// socks5ClientCmd walks the greeting and the optional username/password
// exchange to the request's command byte.
func socks5ClientCmd(b []byte) (byte, int) {
	if len(b) < 2 {
		return 0, spyNeedMore
	}
	if b[0] != 0x05 {
		return 0, spyNot
	}
	off := 2 + int(b[1])
	if len(b) < off+1 {
		return 0, spyNeedMore
	}
	if b[off] == 0x01 { // RFC 1929
		if len(b) < off+2 {
			return 0, spyNeedMore
		}
		ulen := int(b[off+1])
		if len(b) < off+3+ulen {
			return 0, spyNeedMore
		}
		off += 3 + ulen + int(b[off+2+ulen])
	}
	if len(b) < off+2 {
		return 0, spyNeedMore
	}
	if b[off] != 0x05 {
		return 0, spyNot
	}
	return b[off+1], spyFound
}

// socksReplyRewriter passes the engine's side of a SOCKS5 handshake through,
// and replaces the address in the answer to a UDP ASSOCIATE with the one
// returned by relay. Everything after the handshake passes untouched.
type socksReplyRewriter struct {
	r     io.Reader
	cmd   func() byte
	relay func(engine *net.UDPAddr) (atyp byte, addr net.IP, port int, ok bool)

	buf    []byte
	out    []byte
	stage  int // 0 method choice, 1 password answer, 2 request answer, 3 through
	method byte
}

func (w *socksReplyRewriter) Read(p []byte) (int, error) {
	for {
		if len(w.out) > 0 {
			n := copy(p, w.out)
			w.out = w.out[n:]
			return n, nil
		}
		if w.stage == 3 {
			return w.r.Read(p)
		}
		tmp := make([]byte, 512)
		n, err := w.r.Read(tmp)
		w.buf = append(w.buf, tmp[:n]...)
		w.parse()
		if err != nil {
			// Whatever was held back still goes out before the end.
			w.out = append(w.out, w.buf...)
			w.buf = nil
			w.stage = 3
			if len(w.out) == 0 {
				return 0, err
			}
		}
	}
}

// parse moves complete messages from buf to out, rewriting the one it is for.
func (w *socksReplyRewriter) parse() {
	for w.stage < 3 {
		switch w.stage {
		case 0, 1:
			if len(w.buf) < 2 {
				return
			}
			msg := w.buf[:2]
			if w.stage == 0 {
				if msg[0] != 0x05 || msg[1] == 0xFF {
					w.through()
					return
				}
				w.method = msg[1]
				w.stage = 2
				if w.method == 0x02 {
					w.stage = 1
				}
			} else {
				if msg[1] != 0x00 {
					w.through()
					return
				}
				w.stage = 2
			}
			w.out = append(w.out, msg...)
			w.buf = w.buf[2:]
		case 2:
			if len(w.buf) < 5 {
				return
			}
			var alen int
			switch w.buf[3] {
			case 0x01:
				alen = 4
			case 0x04:
				alen = 16
			case 0x03:
				alen = 1 + int(w.buf[4])
			default:
				w.through()
				return
			}
			total := 4 + alen + 2
			if len(w.buf) < total {
				return
			}
			msg := w.buf[:total]
			w.buf = w.buf[total:]
			w.stage = 3
			if msg[0] == 0x05 && msg[1] == 0x00 && w.cmd() == 0x03 && msg[3] != 0x03 {
				ip := net.IP(append([]byte(nil), msg[4:4+alen]...))
				port := int(binary.BigEndian.Uint16(msg[4+alen:]))
				if ip.IsUnspecified() {
					ip = net.IPv4(127, 0, 0, 1)
				}
				if atyp, addr, rport, ok := w.relay(&net.UDPAddr{IP: ip, Port: port}); ok {
					rep := []byte{0x05, 0x00, 0x00, atyp}
					rep = append(rep, addr...)
					rep = binary.BigEndian.AppendUint16(rep, uint16(rport))
					msg = rep
				}
			}
			w.out = append(w.out, msg...)
			w.out = append(w.out, w.buf...)
			w.buf = nil
		}
	}
}

// through gives up on reading the handshake and passes everything on.
func (w *socksReplyRewriter) through() {
	w.out = append(w.out, w.buf...)
	w.buf = nil
	w.stage = 3
}

// udpAssoc is one relay: a socket on the client-facing address, one towards
// the engine's UDP port.
type udpAssoc struct {
	once   sync.Once
	front  *net.UDPConn
	back   *net.UDPConn
	client atomic.Pointer[net.UDPAddr]
}

func (a *udpAssoc) close() {
	a.once.Do(func() {
		a.front.Close()
		a.back.Close()
	})
}

// openUDPAssoc starts a relay for a client at clientIP, reached on localIP,
// towards the engine's relay address. It counts what it carries.
func openUDPAssoc(localIP, clientIP net.IP, engine *net.UDPAddr, counter *trafficCounter) (*udpAssoc, error) {
	front, err := net.ListenUDP("udp", &net.UDPAddr{IP: localIP})
	if err != nil {
		return nil, err
	}
	back, err := net.DialUDP("udp", nil, engine)
	if err != nil {
		front.Close()
		return nil, err
	}
	a := &udpAssoc{front: front, back: back}
	go func() { // device → engine
		buf := make([]byte, 64<<10)
		for {
			n, from, err := front.ReadFromUDP(buf)
			if err != nil {
				a.close()
				return
			}
			if !from.IP.Equal(clientIP) {
				continue // only the client that asked
			}
			a.client.Store(from)
			if _, err := back.Write(buf[:n]); err == nil && counter != nil {
				counter.Up.Add(int64(n))
			}
		}
	}()
	go func() { // engine → device
		buf := make([]byte, 64<<10)
		for {
			n, err := back.Read(buf)
			if err != nil {
				a.close()
				return
			}
			if to := a.client.Load(); to != nil {
				if _, err := front.WriteToUDP(buf[:n], to); err == nil && counter != nil {
					counter.Down.Add(int64(n))
					// Datagrams from the engine's relay carry what the far end
					// sent: the server's bytes, by definition (see ServerDown).
					counter.ServerDown.Add(int64(n))
				}
			}
		}
	}()
	return a, nil
}

// socksUDPRelay wraps the two directions of a SOCKS connection so that a UDP
// ASSOCIATE it makes gets Vair's relay: one the client can reach — on the
// address it reached the proxy at — and that counts what it carries. cleanup
// ends the relay with the connection.
func socksUDPRelay(client net.Conn, counter *trafficCounter) (watch *socksCmdWatch, wrap func(io.Reader) io.Reader, cleanup func()) {
	remote, _ := client.RemoteAddr().(*net.TCPAddr)
	local, _ := client.LocalAddr().(*net.TCPAddr)
	if remote == nil || local == nil {
		return nil, nil, func() {}
	}
	watch = &socksCmdWatch{}
	var mu sync.Mutex
	var assocs []*udpAssoc
	relay := func(engine *net.UDPAddr) (byte, net.IP, int, bool) {
		a, err := openUDPAssoc(local.IP, remote.IP, engine, counter)
		if err != nil {
			vlog("warn", "SOCKS UDP for %s: no relay: %v", remote.IP, err)
			return 0, nil, 0, false
		}
		mu.Lock()
		assocs = append(assocs, a)
		mu.Unlock()
		port := a.front.LocalAddr().(*net.UDPAddr).Port
		if ip4 := local.IP.To4(); ip4 != nil {
			return 0x01, ip4, port, true
		}
		return 0x04, local.IP.To16(), port, true
	}
	wrap = func(r io.Reader) io.Reader {
		return &socksReplyRewriter{r: r, cmd: func() byte { return byte(watch.cmd.Load()) }, relay: relay}
	}
	cleanup = func() {
		mu.Lock()
		defer mu.Unlock()
		for _, a := range assocs {
			a.close()
		}
	}
	return watch, wrap, cleanup
}
