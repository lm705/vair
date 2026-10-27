package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The VPN server's name, asked for encrypted.
//
// The bootstrap lookup is the one name that has to be resolved before there is
// a tunnel, and it was asked over plain DNS on port 53: the provider's resolver
// was no longer the one answering, but anyone on the path — the provider's DPI
// first of all — still read which VPN server this machine was about to use. It
// is now asked over DNS over HTTPS (RFC 8484) at the bootstrap server's own
// address, which every preset answers on (Quad9 over HTTP/2 only). A server
// that does not — a custom address — is asked in plain DNS as before, and the
// log says so once; one that did answer is not dropped to plain DNS for a
// single failure, which an attacker on the path could cause on purpose.

// bootstrapDoHURL is where a server's DoH endpoint is; a var only for tests.
var bootstrapDoHURL = func(server string) string {
	return "https://" + net.JoinHostPort(server, "443") + "/dns-query"
}

// bootstrapDoHClient is shared, without keep-alive (see ownHTTPClient), with
// HTTP/2 allowed — Quad9 refuses HTTP/1.1.
var bootstrapDoHClient = &http.Client{
	Timeout:   2500 * time.Millisecond,
	Transport: &http.Transport{ForceAttemptHTTP2: true, DisableKeepAlives: true, TLSHandshakeTimeout: 2 * time.Second},
}

// dohState remembers, per server, whether it has answered over DoH: one that
// never has is treated as a plain-DNS server after the first failure (no point
// waiting on it at every lookup), one that has is never quietly downgraded.
var dohState = struct {
	sync.Mutex
	works map[string]bool // true: answered; false: tried and does not
}{works: map[string]bool{}}

// lookupBootstrap resolves host at the bootstrap server: DoH, and plain DNS
// only for a server that has never answered DoH.
func lookupBootstrap(ctx context.Context, server, host string) ([]net.IP, error) {
	dohState.Lock()
	works, known := dohState.works[server]
	dohState.Unlock()
	if !known || works {
		ips, err := dohLookupA(ctx, server, host)
		if err == nil || errors.Is(err, errDNSNotFound) {
			// "No such name" is an answer too, and it came over DoH: the
			// server speaks it, and nothing more is asked anywhere else.
			dohState.Lock()
			dohState.works[server] = true
			dohState.Unlock()
			return ips, err
		}
		if works {
			// It has answered before: a failure now is not a reason to hand
			// the name to anyone listening on the path.
			return nil, fmt.Errorf("DNS over HTTPS at %s: %w", server, err)
		}
		dohState.Lock()
		dohState.works[server] = false
		dohState.Unlock()
		vlog("warning", "bootstrap DNS %s does not answer DNS over HTTPS (%v) — the VPN server's name is asked for in plain DNS, which anyone on the path can read", server, err)
	}
	return bootstrapResolver(server).LookupIP(ctx, "ip4", host)
}

// dohLookupA asks server for host's IPv4 addresses with an RFC 8484 POST.
func dohLookupA(ctx context.Context, server, host string) ([]net.IP, error) {
	q, err := dnsQueryA(host)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bootstrapDoHURL(server), bytes.NewReader(q))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := bootstrapDoHClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	return dnsAnswersA(body)
}

// dnsQueryA is a DNS question for host's A records, recursion desired, id 0
// (RFC 8484 recommends 0 for cacheability).
func dnsQueryA(host string) ([]byte, error) {
	host = strings.TrimSuffix(host, ".")
	q := []byte{0, 0, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return nil, fmt.Errorf("not a DNS name: %q", host)
		}
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	return append(q, 0, 0, 1, 0, 1), nil
}

var errShortDNS = errors.New("truncated DNS answer")

// errDNSNotFound is a server's answer that the name has no IPv4 address —
// NXDOMAIN, or a name with no A record. An answer, not a failure to get one:
// asking another resolver would only hand the name to one more party.
var errDNSNotFound = errors.New("no such host")

// dnsNotFound reports whether err is that answer, from DoH or plain DNS.
func dnsNotFound(err error) bool {
	var de *net.DNSError
	return errors.Is(err, errDNSNotFound) || (errors.As(err, &de) && de.IsNotFound)
}

// dnsAnswersA reads the A records out of a DNS response.
func dnsAnswersA(m []byte) ([]net.IP, error) {
	if len(m) < 12 {
		return nil, errShortDNS
	}
	if rcode := m[3] & 0x0f; rcode == 3 {
		return nil, errDNSNotFound
	} else if rcode != 0 {
		return nil, fmt.Errorf("DNS error code %d", rcode)
	}
	qd, an := int(binary.BigEndian.Uint16(m[4:])), int(binary.BigEndian.Uint16(m[6:]))
	off := 12
	skipName := func() error {
		for {
			if off >= len(m) {
				return errShortDNS
			}
			l := int(m[off])
			switch {
			case l == 0:
				off++
				return nil
			case l&0xc0 == 0xc0:
				off += 2
				return nil
			default:
				off += 1 + l
			}
		}
	}
	for i := 0; i < qd; i++ {
		if err := skipName(); err != nil {
			return nil, err
		}
		off += 4
	}
	var ips []net.IP
	for i := 0; i < an; i++ {
		if err := skipName(); err != nil {
			return nil, err
		}
		if off+10 > len(m) {
			return nil, errShortDNS
		}
		typ := binary.BigEndian.Uint16(m[off:])
		rdlen := int(binary.BigEndian.Uint16(m[off+8:]))
		off += 10
		if off+rdlen > len(m) {
			return nil, errShortDNS
		}
		if typ == 1 && rdlen == 4 {
			ips = append(ips, net.IPv4(m[off], m[off+1], m[off+2], m[off+3]))
		}
		off += rdlen
	}
	if len(ips) == 0 {
		return nil, errDNSNotFound
	}
	return ips, nil
}
