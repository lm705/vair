package main

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"vair/helper/api"
)

// The pipe, and who is on the other end of it.
//
// Who may connect is decided by the pipe's security descriptor before a
// connection exists:
//
//   - SYSTEM and administrators: everything;
//   - interactive users: read, and write data — and nothing else. Not the
//     generic write right: for a pipe that includes creating another instance
//     of it, and an instance a user created would take some of the other
//     clients' connections and read what they send;
//   - the mandatory label: no writing from below medium integrity. A
//     browser's sandboxed renderer, or anything else running at low
//     integrity, cannot open it for writing, which is what connecting needs.
//
// go-winio refuses remote clients on every pipe it creates — the hole Cure53
// found in Mullvad's daemon (MUL-02-007) — and creates the first instance
// with FILE_CREATE, so the name cannot have been taken by somebody else first.
const pipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x0012008B;;;IU)S:(ML;;NW;;;ME)"

// ClientAccess is what a client opens the pipe with: read, and write data —
// exactly what the descriptor grants. GENERIC_WRITE would be refused.
const ClientAccess = windows.GENERIC_READ | windows.FILE_WRITE_DATA

func listen() (net.Listener, error) {
	return winio.ListenPipe(api.PipeName, &winio.PipeConfig{
		SecurityDescriptor: pipeSDDL,
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
}

// client is who connected, as Windows says — never as the client says.
type client struct {
	pid       uint32
	sid       string
	user      string // DOMAIN\name: for the log, and for "whose tunnel is it"
	sessionID uint32
	integrity uint32 // the mandatory level's RID: 0x2000 medium, 0x3000 high…
	// admin is an elevated administrator: allowed to take over a tunnel or a
	// block another session holds. A filtered admin token — an administrator
	// who did not elevate — is an ordinary user here, as it is everywhere.
	admin bool
}

func (c client) String() string {
	return fmt.Sprintf("pid=%d user=%s session=%d integrity=%#x admin=%v", c.pid, c.user, c.sessionID, c.integrity, c.admin)
}

const integrityMedium = 0x2000

var (
	modKernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	modAdvapi32                     = windows.NewLazySystemDLL("advapi32.dll")
	procGetNamedPipeClientProcessId = modKernel32.NewProc("GetNamedPipeClientProcessId")
	procImpersonateNamedPipeClient  = modAdvapi32.NewProc("ImpersonateNamedPipeClient")
)

// identify reads the client's token through the pipe: the service takes on the
// client's identity for a moment on this thread, reads the token, and goes
// back to its own. The client must connect at identification level at least —
// an anonymous connection has no identity to read, and is refused.
func identify(nc net.Conn) (client, error) {
	var c client
	f, ok := nc.(interface{ Fd() uintptr })
	if !ok {
		return c, errors.New("not a pipe connection")
	}
	h := windows.Handle(f.Fd())

	var pid uint32
	if r, _, err := procGetNamedPipeClientProcessId.Call(uintptr(h), uintptr(unsafe.Pointer(&pid))); r == 0 {
		return c, fmt.Errorf("client process: %w", err)
	}
	c.pid = pid

	// Impersonation is per thread: the thread must stay ours until it has
	// reverted, or another goroutine would run as the client.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, err := procImpersonateNamedPipeClient.Call(uintptr(h)); r == 0 {
		return c, fmt.Errorf("impersonate: %w", err)
	}
	var tok windows.Token
	err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &tok)
	if rerr := windows.RevertToSelf(); rerr != nil {
		// Still the client: nothing on this thread may run on. This does not
		// happen; if it does, the process is not to be trusted with anything.
		panic("vair-helper: could not revert impersonation: " + rerr.Error())
	}
	if err != nil {
		return c, fmt.Errorf("client token: %w", err)
	}
	defer tok.Close()

	tu, err := tok.GetTokenUser()
	if err != nil {
		return c, fmt.Errorf("client user: %w", err)
	}
	c.sid = tu.User.Sid.String()
	if acct, dom, _, err := tu.User.Sid.LookupAccount(""); err == nil {
		c.user = dom + `\` + acct
	} else {
		c.user = c.sid
	}
	if anon, _ := windows.CreateWellKnownSid(windows.WinAnonymousSid); anon != nil && tu.User.Sid.Equals(anon) {
		return c, errors.New("anonymous connection: connect at identification level")
	}

	var sess uint32
	var n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&sess)), 4, &n); err == nil {
		c.sessionID = sess
	}
	c.integrity = tokenIntegrity(tok)
	if tok.IsElevated() {
		if admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid); err == nil {
			c.admin, _ = tok.IsMember(admins)
		}
	}
	return c, nil
}

// tokenIntegrity is the RID of the token's mandatory label, 0 when unreadable.
func tokenIntegrity(tok windows.Token) uint32 {
	var n uint32
	windows.GetTokenInformation(tok, windows.TokenIntegrityLevel, nil, 0, &n) //nolint:errcheck
	if n == 0 {
		return 0
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenIntegrityLevel, &buf[0], n, &n); err != nil {
		return 0
	}
	tml := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buf[0]))
	sid := tml.Label.Sid
	count := sid.SubAuthorityCount()
	if count == 0 {
		return 0
	}
	return sid.SubAuthority(uint32(count - 1))
}
