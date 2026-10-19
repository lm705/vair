package core

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Cloudflare WARP.
//
// WARP is a free WireGuard service. It needs no account and no payment: the
// client generates a key pair, posts the public half, and is handed an address
// and a peer to talk to. That is the whole enrolment.
//
// Why it earns a place in a client for public config lists: most of those lists
// live on data-centre address ranges, and a large part of the web now treats
// those differently — a captcha from Google, a refusal from a bank, "not
// available in your country" from services that are available in the country
// the server is in. WARP's exit addresses are consumer ranges, so the same
// request goes through. It is also a working tunnel when every config in the
// list is dead, which for this audience is not a rare afternoon.
//
// What it is not: privacy from Cloudflare, who terminate it, and it is not a
// way around geographical restrictions — WARP exits near you by design.

// warpRegisterURL is Cloudflare's enrolment endpoint. The version in the path is
// the client API version, not ours.
const warpRegisterURL = "https://api.cloudflareclient.com/v0a2158/reg"

// warpClientVersion and warpUserAgent identify us as the app Cloudflare expects
// on this endpoint. Sending neither is refused.
const (
	warpClientVersion = "a-6.30-3596"
	warpUserAgent     = "okhttp/3.12.1"
)

// warpRegistration is the part of the response worth reading. The account,
// policy and licence fields are deliberately ignored: nothing here should
// depend on the free tier's shape, which Cloudflare changes.
type warpRegistration struct {
	Config struct {
		ClientID  string `json:"client_id"`
		Interface struct {
			Addresses struct {
				V4 string `json:"v4"`
				V6 string `json:"v6"`
			} `json:"addresses"`
		} `json:"interface"`
		Peers []struct {
			PublicKey string `json:"public_key"`
			Endpoint  struct {
				Host string `json:"host"`
			} `json:"endpoint"`
		} `json:"peers"`
	} `json:"config"`
}

// RegisterWARP enrols a fresh WARP account and returns it as a wireguard:// link.
//
// A link rather than a settings blob so it is a configuration like any other:
// it can be tested, ranked, connected, put in a tab, exported and deleted with
// everything else, and nothing in the rest of the program needs to know where
// it came from.
func RegisterWARP() (string, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("warp: generate key: %w", err)
	}

	body, _ := json.Marshal(map[string]string{
		"key":           base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()),
		"install_id":    "",
		"fcm_token":     "",
		"tos":           time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"model":         "PC",
		"serial_number": "",
		"locale":        "en_US",
	})

	req, err := http.NewRequest("POST", warpRegisterURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("CF-Client-Version", warpClientVersion)
	req.Header.Set("User-Agent", warpUserAgent)

	// Through the tunnel when one is up: the enrolment endpoint is itself
	// blocked in some of the places this program is used, and the user who most
	// needs a working egress is the one who cannot reach the service that hands
	// them one.
	resp, err := updateHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return "", fmt.Errorf("warp: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return "", fmt.Errorf("warp: read response: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("warp: registration refused (HTTP %d)", resp.StatusCode)
	}
	return warpLinkFromRegistration(raw, base64.StdEncoding.EncodeToString(priv.Bytes()))
}

// warpLinkFromRegistration turns the enrolment response into a wireguard:// link.
//
// Separate from the request so the shape of the answer can be tested without a
// network, and so a change on Cloudflare's side shows up as one failing test
// rather than as a config that connects and carries nothing.
func warpLinkFromRegistration(raw []byte, privKey string) (string, error) {
	var reg warpRegistration
	if err := json.Unmarshal(raw, &reg); err != nil {
		return "", fmt.Errorf("warp: response is not the shape we know: %w", err)
	}
	if len(reg.Config.Peers) == 0 {
		return "", fmt.Errorf("warp: registration returned no peer")
	}
	peer := reg.Config.Peers[0]
	host, port := peer.Endpoint.Host, 2408
	if h, p, ok := splitHostPortLoose(host); ok {
		host, port = h, p
	}
	if host == "" || peer.PublicKey == "" {
		return "", fmt.Errorf("warp: registration is missing the peer's address or key")
	}

	var addrs []string
	if v := reg.Config.Interface.Addresses.V4; v != "" {
		addrs = append(addrs, v+"/32")
	}
	if v := reg.Config.Interface.Addresses.V6; v != "" {
		addrs = append(addrs, v+"/128")
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("warp: registration is missing the interface address")
	}

	q := url.Values{}
	q.Set("publickey", peer.PublicKey)
	q.Set("address", strings.Join(addrs, ","))
	// client_id is three bytes in base64, and those three bytes are what
	// WireGuard's reserved field must carry. Without them Cloudflare completes
	// the handshake and then discards every packet — a tunnel that is up and
	// silent, which is the hardest kind of broken to recognise.
	if b, err := base64.StdEncoding.DecodeString(reg.Config.ClientID); err == nil && len(b) == 3 {
		q.Set("reserved", fmt.Sprintf("%d,%d,%d", b[0], b[1], b[2]))
	}
	// 1280 is what Cloudflare's own client uses. The default 1420 leaves no room
	// for their encapsulation on some paths, and the symptom is that small
	// requests work and large ones hang.
	q.Set("mtu", "1280")

	link := fmt.Sprintf("wireguard://%s@%s:%d?%s#%s",
		url.QueryEscape(privKey), host, port, q.Encode(), url.QueryEscape("WARP"))

	// Parse what we just built. If it does not survive our own reader, it will
	// not survive the engine either, and finding that out now names the field.
	if _, err := parseWireGuard(link); err != nil {
		return "", fmt.Errorf("warp: built an unusable configuration: %w", err)
	}
	return link, nil
}

// splitHostPortLoose splits "host:port", tolerating a bare host and IPv6
// brackets.
func splitHostPortLoose(s string) (string, int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, false
	}
	i := strings.LastIndex(s, ":")
	if i < 0 || strings.HasSuffix(s, "]") {
		return strings.Trim(s, "[]"), 0, false
	}
	host := strings.Trim(s[:i], "[]")
	port := 0
	fmt.Sscanf(s[i+1:], "%d", &port)
	if host == "" || port <= 0 {
		return "", 0, false
	}
	return host, port, true
}

// AddWARPConfig enrols with Cloudflare and puts the result in a tab.
//
// The whole feature from the user's side: one button, one configuration that
// behaves like every other — testable, rankable, connectable, deletable.
// Returns the number of configurations added (0 or 1) and a reason when none.
func AddWARPConfig(tabID string) (int, error) {
	link, err := RegisterWARP()
	if err != nil {
		vlog("error", "WARP: %v", err)
		return 0, err
	}
	BeginPaste(tabID)
	n := PasteChunk(tabID, link)
	EndPaste(tabID)
	if n == 0 {
		return 0, fmt.Errorf("warp: the configuration was rejected on import")
	}
	vlog("info", "WARP: a free Cloudflare configuration was added")
	return n, nil
}
