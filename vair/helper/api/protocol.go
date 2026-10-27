// Package api is the contract between the unprivileged GUI and the privileged
// helper that runs TUN mode. It is imported by BOTH, which is the whole point:
// the wire format cannot drift between the two halves if there is only one
// definition of it.
//
// It deliberately imports nothing of vair/core. The helper runs as root, and
// every package it links is code running as root — the app's config store,
// SQLite driver and HTTP clients have no business there.
package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ProtocolVersion is bumped whenever the meaning of a field changes.
//
// It exists because GUI and helper are updated separately and WILL disagree: an
// app update replaces the binary the user runs while the installed service
// keeps running the old code until it is reinstalled. Without a version, that
// mismatch shows up as a request the helper half-understands. With one, it
// shows up as a message telling the user to reinstall the helper.
const ProtocolVersion = 5

// Protocol 4: the kill-switch follows the session on Linux as it does on
// Windows — kill_switch turns it on or off for the running tunnel, and a stop
// can hand it to the next tunnel (keep_block) instead of taking it down.
//
// Protocol 5: the helper exists on Windows too, as a service the app talks to
// over a named pipe (PipeName). Four requests are added for what the Windows
// app used to do itself with administrator rights — the persistent block, the
// proxy kill-switch's block, the firewall rules for the web interface and the
// LAN-shared proxy, and the state of the blocks — and put_ruleset answers with
// the path it stored the set at. Linux answers the new requests with an error;
// its app never sends them.

// SocketPath is where the helper listens, and DirPath the directory systemd
// creates for it (RuntimeDirectory=vair, mode 0750, group = the installing
// user's).
//
// Plain constants on purpose — not build flags, not environment overrides.
// A released client once shipped with its service listening on the path its
// test harness used; a socket location is exactly the kind of thing that must
// be impossible to change by accident.
const (
	DirPath    = "/run/vair"
	SocketPath = DirPath + "/helper.sock"
)

// PipeName is where the Windows helper service listens. The same rule: a
// constant, never a setting. Only local clients reach it — the service refuses
// remote ones — and the app checks that the process at the other end is the
// service before it sends anything.
const PipeName = `\\.\pipe\vair-helper`

// ServiceName is the Windows service, as the service manager knows it. The app
// asks the manager for its process id to check the pipe's server against.
const ServiceName = "VairHelper"

// MaxFrameBytes bounds a single frame. Any process in the installing user's
// group can open the socket, so a frame length is attacker-controlled input to
// a root process; a generated sing-box config is a few kilobytes.
//
// The bound is 8 MiB rather than the 1 MiB it started at because put_ruleset
// carries a whole rule-set file: the largest we ship is 580 KiB, base64 makes
// that ~774 KiB, and country lists only grow. The reader cannot know the op
// before it has read the line, so one limit has to cover both. What keeps this
// from being a way to make root allocate freely is not the frame size but
// MaxRuleSetBytes and the helper's per-session budget — /run is a tmpfs, so a
// file written there is RAM.
const MaxFrameBytes = 8 << 20 // 8 MiB

// MaxRuleSetBytes bounds one rule-set file, and MaxRuleSets how many a session
// may stage. Together they cap what a caller can make the helper hold in the
// tmpfs that /run is.
const (
	MaxRuleSetBytes = 4 << 20 // 4 MiB
	MaxRuleSets     = 16
)

// Op is a request verb. Eleven, and no more without a version bump.
type Op string

const (
	// OpHello must be the first request on a connection. It carries the
	// caller's protocol version and gets the helper's in return.
	OpHello Op = "hello"
	// OpPutRuleSet stages one rule-set file for the tunnel that is about to
	// start.
	//
	// It exists because the two obvious alternatives are both wrong. Pointing
	// the config at the user's own ~/.local/share/vair/rulesets would have root
	// read a file the caller can rewrite at any moment; pointing it at the
	// packaged copies means the country lists in TUN mode are frozen at the
	// release, while proxy mode quietly uses refreshed ones. Sending the bytes
	// over the socket lets the helper own the file it reads — it writes them
	// into its own root-only directory — and the app keeps updating rule-sets
	// the way it always has.
	OpPutRuleSet Op = "put_ruleset"
	// OpStartTUN hands over a sing-box configuration to run. The helper
	// validates it before doing anything with it — a caller in the right group
	// is authenticated, not trusted.
	OpStartTUN Op = "start_tun"
	// OpStop tears the tunnel down: engine, routes, DNS, firewall rules — all
	// but the kill-switch when the request says keep_block (see KeepBlock).
	OpStop Op = "stop"
	// OpKillSwitch puts the kill-switch up or takes it down for the running
	// tunnel (protocol 4). With no tunnel running it can only take one down —
	// a block kept for a successor that is not coming.
	OpKillSwitch Op = "kill_switch"
	// OpStatus reports what is running now.
	OpStatus Op = "status"
	// OpPing renews the lease. A GUI that hangs or is killed stops pinging, and
	// the helper tears down rather than leaving a tunnel nobody owns.
	OpPing Op = "ping"

	// Windows only (protocol 5).

	// OpPersistentBlock puts the persistent kill-switch up (Enable) — the
	// block that stays after the tunnel, after the app and after a restart —
	// pointed at the tunnel running now, if any; or takes down every block of
	// ours that holds traffic (Enable false): the persistent set, the session
	// set, and a Windows Firewall block left by a build before 2.7.0.
	OpPersistentBlock Op = "persistent_block"
	// OpSessionBlock puts up (Enable) or takes down the proxy kill-switch's
	// block: the tunnel's policy with no tunnel to let through. It belongs to
	// the connection that asked, and is handed over like the tunnel's — a
	// stop with keep_block, or a dropped connection, leaves it BlockHandoverSec.
	OpSessionBlock Op = "session_block"
	// OpFirewall sets the inbound rules for the web interface (RemotePort) and
	// the LAN-shared proxy (LANHTTPPort, LANSocksPort); 0 removes a rule. The
	// helper names the rules and the programs in them; the client says ports.
	OpFirewall Op = "firewall"
	// OpBlockStatus reports which blocks are standing (Response.Block).
	OpBlockStatus Op = "block_status"
)

// Request is one line of JSON from the GUI.
type Request struct {
	// ID correlates a response with its request. Must be non-zero: id 0 is
	// reserved for the helper's unsolicited frames.
	ID uint64 `json:"id"`
	Op Op     `json:"op"`

	// Protocol is set on hello only.
	Protocol int `json:"protocol,omitempty"`

	// Config is the sing-box configuration for start_tun, kept as raw JSON so
	// this package needs no opinion about the engine's schema — the helper's
	// validator is the only thing that inspects it.
	Config json.RawMessage `json:"config,omitempty"`

	// LeaseSec is how long the helper should keep the tunnel up without a ping.
	// Zero means the helper's default; see DefaultLeaseSec.
	LeaseSec int `json:"lease_sec,omitempty"`

	// Name and Data carry one rule-set on put_ruleset. Name is a bare file name
	// — the helper decides the directory, so no request can express a path.
	// Data is base64 on the wire, which encoding/json does for []byte.
	Name string `json:"name,omitempty"`
	Data []byte `json:"data,omitempty"`

	// The kill-switch, on start_tun (protocol 3).
	//
	// The block is the helper's to install, put up with the tunnel and taken
	// down with it: an nftables table on Linux, WFP filters on Windows. (On
	// Windows strict_route was once taken for a kill-switch; it only holds DNS
	// in, which is why the filters exist.)
	//
	// KillSwitch asks for it. AllowLAN keeps private networks reachable, exactly
	// as the routing rules do. ServerIPs are the addresses the engine dials to
	// build the tunnel; they must be allowed out, or the block would cut the
	// connection it is protecting. The app has them already — it resolves the
	// server before connecting so the configuration carries an address rather
	// than a name.
	KillSwitch bool     `json:"kill_switch,omitempty"`
	AllowLAN   bool     `json:"allow_lan,omitempty"`
	ServerIPs  []string `json:"server_ips,omitempty"`

	// KeepBlock, on stop (protocol 4), leaves the kill-switch standing for the
	// tunnel that comes next: a switch to another server is a stop and a start,
	// and between the two the machine had no tunnel and no block. The helper
	// takes it down itself when no tunnel has come within BlockHandoverSec, or
	// when the connection that kept it goes away.
	KeepBlock bool `json:"keep_block,omitempty"`

	// IPv6Around, on start_tun (protocol 5, Windows), leaves IPv6 outside the
	// session block: the user sent IPv6 around the tunnel, and blocking it
	// would take away what they chose to keep. DNS on port 53 stays held in.
	IPv6Around bool `json:"ipv6_around,omitempty"`

	// Enable turns persistent_block and session_block on or off.
	Enable bool `json:"enable,omitempty"`

	// The ports of firewall. Each must be 1024–65535, or 0 to remove the rule.
	RemotePort   int `json:"remote_port,omitempty"`
	LANHTTPPort  int `json:"lan_http_port,omitempty"`
	LANSocksPort int `json:"lan_socks_port,omitempty"`
}

// BlockHandoverSec is how long a kept block waits for the next tunnel — the
// same grace the Windows block has (tunnelHandoverGrace in core).
const BlockHandoverSec = 20

// MaxServerIPs bounds the allow-list. A chain is a hop each; nobody has more.
const MaxServerIPs = 8

// RuleSetDir is where staged rule-sets land. Inside DirPath, so it is covered by
// the same root-owned 0750 directory the validator already trusts.
const RuleSetDir = DirPath + "/rulesets"

// RuleSetPath is the path a config should reference for a staged set.
func RuleSetPath(name string) string { return RuleSetDir + "/" + name }

// Response is one line of JSON from the helper.
//
// A response with ID == 0 is unsolicited. Two kinds: a log line, which is how
// the engine's stderr reaches the app's log panel in the same shape it arrives
// in when the GUI spawns the engine itself; and a Status, which the helper sends
// when the tunnel state changes without being asked — an engine that died on its
// own. The second exists because the app was otherwise left showing a connection
// that had already ended, and a log line is not something a client should have to
// parse to learn that.
type Response struct {
	ID    uint64 `json:"id"`
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`

	// Protocol and Version answer hello.
	Protocol int    `json:"protocol,omitempty"`
	Version  string `json:"version,omitempty"`

	Status *Status  `json:"status,omitempty"`
	Log    *LogLine `json:"log,omitempty"`

	// Path answers put_ruleset (protocol 5): where the helper stored the set,
	// for the configuration to point at. Older helpers leave it empty, and the
	// client falls back to RuleSetPath.
	Path string `json:"path,omitempty"`

	// Block answers block_status, and comes with status on Windows.
	Block *BlockState `json:"block,omitempty"`
}

// BlockState is which of the blocks that hold traffic are standing (Windows).
type BlockState struct {
	// Persistent is the set that outlives the tunnel and the app; Known is
	// false when the filtering engine could not be asked.
	Persistent bool `json:"persistent"`
	Known      bool `json:"known"`
	// Session is a block held for a session — the tunnel's, or the proxy
	// kill-switch's — including one waiting out a hand-over.
	Session bool `json:"session"`
	// Legacy is a Windows Firewall block of the kind builds before 2.7.0 put
	// up, with evidence that it was ours.
	Legacy bool `json:"legacy,omitempty"`
}

// Status is what the helper is doing right now.
type Status struct {
	Running bool `json:"running"`
	// Iface is the tunnel interface, empty when nothing is running.
	Iface string `json:"iface,omitempty"`
	// PID of the engine, for the app's own process bookkeeping.
	PID int `json:"pid,omitempty"`
	// UptimeSec since the tunnel came up.
	UptimeSec int64 `json:"uptime_sec,omitempty"`
	// LeaseExpiresInSec is how long the tunnel survives without another ping.
	LeaseExpiresInSec int `json:"lease_expires_in_sec,omitempty"`
	// Mine says the tunnel belongs to the connection that asked; Owner names
	// the account whose session holds it, for a client that does not
	// (protocol 5, Windows: one tunnel per machine, several users).
	Mine  bool   `json:"mine,omitempty"`
	Owner string `json:"owner,omitempty"`
}

// LogLine mirrors the shape the app's log panel already consumes.
type LogLine struct {
	// Src is "singbox" or "helper".
	Src  string `json:"src"`
	Lvl  string `json:"lvl"`
	Text string `json:"text"`
}

// DefaultLeaseSec is how long a tunnel outlives the last ping. Long enough to
// ride out a stalled GUI, short enough that a crashed one does not leave the
// machine tunnelled for a coffee break.
const DefaultLeaseSec = 30

// PingIntervalSec is how often the GUI should renew the lease — comfortably
// inside DefaultLeaseSec, so one dropped ping is not a teardown.
const PingIntervalSec = 10

// ErrFrameTooLarge is returned when a peer sends a frame over MaxFrameBytes.
var ErrFrameTooLarge = errors.New("api: frame exceeds the maximum size")

// Conn is one side of a helper connection: newline-delimited JSON, one value
// per line.
//
// Line-delimited JSON rather than length-prefixed frames because both ends have
// encoding/json, the traffic is tiny, and a protocol a human can read off a
// socket with `nc` is a protocol a human can debug at 2am.
type Conn struct {
	rw  io.ReadWriteCloser
	br  *bufio.Reader
	enc *json.Encoder
}

func NewConn(rw io.ReadWriteCloser) *Conn {
	return &Conn{
		rw:  rw,
		br:  bufio.NewReaderSize(rw, 64<<10),
		enc: json.NewEncoder(rw),
	}
}

func (c *Conn) Close() error { return c.rw.Close() }

// WriteRequest and WriteResponse each emit exactly one line.
func (c *Conn) WriteRequest(r *Request) error   { return c.enc.Encode(r) }
func (c *Conn) WriteResponse(r *Response) error { return c.enc.Encode(r) }

// ReadRequest reads one line and decodes it.
func (c *Conn) ReadRequest() (*Request, error) {
	line, err := c.readLine()
	if err != nil {
		return nil, err
	}
	var r Request
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, fmt.Errorf("api: malformed request: %w", err)
	}
	return &r, nil
}

// ReadResponse reads one line and decodes it.
func (c *Conn) ReadResponse() (*Response, error) {
	line, err := c.readLine()
	if err != nil {
		return nil, err
	}
	var r Response
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, fmt.Errorf("api: malformed response: %w", err)
	}
	return &r, nil
}

// readLine returns one frame, refusing anything oversized.
//
// It reads in bounded chunks rather than with bufio.Scanner, whose default
// 64 KiB limit would silently truncate a large config into a parse error, and
// whose grow-to-fit alternative would let a peer decide how much memory a root
// process allocates.
func (c *Conn) readLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := c.br.ReadSlice('\n')
		if len(buf)+len(chunk) > MaxFrameBytes {
			return nil, ErrFrameTooLarge
		}
		buf = append(buf, chunk...)
		if err == nil {
			return buf, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue // a long line: keep going, bounded by the check above
		}
		if errors.Is(err, io.EOF) && len(buf) > 0 {
			// A final line without its newline is still a frame.
			return buf, nil
		}
		return nil, err
	}
}

// CheckProtocol compares a peer's version with ours and explains a mismatch in
// terms the user can act on.
//
// A version skew is normal after an app update, not exotic: the new binary
// talks to the service that is still installed. The message has to say what to
// do, because "protocol mismatch" tells a user nothing.
func CheckProtocol(theirs int) error {
	switch {
	case theirs == ProtocolVersion:
		return nil
	case theirs == 0:
		return errors.New("the helper did not report a protocol version — reinstall it")
	case theirs < ProtocolVersion:
		return fmt.Errorf("the installed helper is older than this app (protocol %d, expected %d) — reinstall the helper", theirs, ProtocolVersion)
	default:
		return fmt.Errorf("the installed helper is newer than this app (protocol %d, expected %d) — update the app", theirs, ProtocolVersion)
	}
}
