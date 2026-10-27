package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"vair/helper/api"
)

// The Windows helper service: a named pipe to a service running as SYSTEM.
//
// When it is installed, the app never needs administrator rights: the tunnel,
// the kill-switch's filters and the firewall rules are the service's to make,
// and the app asks over the pipe. When it is not — a portable copy on a machine
// without an installed Vair — the app does all of it itself, as before, and
// TUN needs the app to run as administrator.

// pipeClientAccess is what the pipe is opened with: read, and write data —
// what the service's descriptor grants interactive users, and no more. The
// generic write right would be refused: for a pipe it includes making another
// instance of it.
const pipeClientAccess = windows.GENERIC_READ | windows.FILE_WRITE_DATA

// serviceState caches whether the service is registered — asked of the
// service manager at most every few seconds, since half the app asks.
// noServiceForTests keeps the unit tests on the app's own paths, whatever is
// installed on the machine they run on (TestMain sets it).
var noServiceForTests bool

// serviceForTests puts a test on the service's paths, against a stand-in.
var serviceForTests bool

var serviceState struct {
	sync.Mutex
	at        time.Time
	installed bool
	exe       string
}

const serviceStateTTL = 5 * time.Second

// openServiceQuery opens the service with the least access that answers the
// question: the manager for connecting, the service for status and its
// configuration — what an ordinary user may do. nil when it is not installed.
func openServiceQuery() (windows.Handle, windows.Handle, error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, 0, fmt.Errorf("service manager: %w", err)
	}
	name, _ := windows.UTF16PtrFromString(api.ServiceName)
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG|windows.SERVICE_START)
	if err != nil {
		// No start right on a service set up by an older build: ask again for
		// the two that answer questions.
		h, err = windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	}
	if err != nil {
		windows.CloseServiceHandle(scm) //nolint:errcheck
		return 0, 0, err
	}
	return scm, h, nil
}

func refreshServiceState() {
	serviceState.Lock()
	defer serviceState.Unlock()
	if time.Since(serviceState.at) < serviceStateTTL {
		return
	}
	serviceState.at = time.Now()
	serviceState.installed, serviceState.exe = false, ""
	scm, h, err := openServiceQuery()
	if err != nil {
		return
	}
	defer windows.CloseServiceHandle(scm) //nolint:errcheck
	defer windows.CloseServiceHandle(h)   //nolint:errcheck
	serviceState.installed = true
	var need uint32
	windows.QueryServiceConfig(h, nil, 0, &need) //nolint:errcheck
	if need == 0 {
		return
	}
	buf := make([]byte, need)
	cfg := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buf[0]))
	if windows.QueryServiceConfig(h, cfg, need, &need) == nil {
		serviceState.exe = strings.Trim(windows.UTF16PtrToString(cfg.BinaryPathName), `"`)
	}
}

// serviceMode reports whether the helper service is installed on this machine:
// then everything that needs administrator rights goes through it.
func serviceMode() bool {
	if serviceForTests {
		return true
	}
	if noServiceForTests {
		return false
	}
	refreshServiceState()
	serviceState.Lock()
	defer serviceState.Unlock()
	return serviceState.installed
}

// ServiceMode is serviceMode for the shell.
func ServiceMode() bool { return serviceMode() }

// ServiceInstallDir is where the service is installed — Program Files\Vair —
// whose bin\ holds the engines the service runs and lets out of its blocks.
// The app runs its own engines from there too when the service is in use, so
// what the blocks permit is what actually runs. "" without a service.
func ServiceInstallDir() string {
	refreshServiceState()
	serviceState.Lock()
	defer serviceState.Unlock()
	if serviceState.exe == "" {
		return ""
	}
	return filepath.Dir(serviceState.exe)
}

// helperInstalled is serviceMode, under the name the shared code uses.
func helperInstalled() bool { return serviceMode() }

// servicePID is the service's process, or 0 when it is not running.
func servicePID() (uint32, error) {
	scm, h, err := openServiceQuery()
	if err != nil {
		return 0, err
	}
	defer windows.CloseServiceHandle(scm) //nolint:errcheck
	defer windows.CloseServiceHandle(h)   //nolint:errcheck
	var st windows.SERVICE_STATUS_PROCESS
	var need uint32
	if err := windows.QueryServiceStatusEx(h, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&st)), uint32(unsafe.Sizeof(st)), &need); err != nil {
		return 0, err
	}
	if st.CurrentState != windows.SERVICE_RUNNING {
		return 0, nil
	}
	return st.ProcessId, nil
}

// startService asks the service manager to start a stopped service — which
// an interactive user may do (the service's descriptor allows it), so the
// app can bring the service back without asking for administrator rights.
func startService() error {
	scm, h, err := openServiceQuery()
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(scm) //nolint:errcheck
	defer windows.CloseServiceHandle(h)   //nolint:errcheck
	if err := windows.StartService(h, 0, nil); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return err
	}
	for i := 0; i < 40; i++ {
		if pid, _ := servicePID(); pid != 0 {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("the service did not start")
}

var procGetNamedPipeServerProcessId = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetNamedPipeServerProcessId")

// dialHelper connects to the service and completes the handshake.
//
// At identification level, so the service can tell who is asking — an
// anonymous connection is refused. And only after checking that the process at
// the other end of the pipe is the service the service manager knows: while
// the service is stopped, any program can take the pipe's name, and a
// configuration carries the server's keys.
func dialHelper() (*helperClient, error) {
	nc, err := dialPipe()
	if err != nil {
		if !serviceMode() {
			return nil, errors.New("the Vair helper service is not installed")
		}
		// Stopped — by hand, or by a crash the service manager gave up
		// restarting: an interactive user may start it, so the app does,
		// rather than leave a persistent block nobody can take down.
		if pid, _ := servicePID(); pid == 0 {
			if serr := startService(); serr != nil {
				return nil, fmt.Errorf("the Vair helper service is stopped and could not be started: %w", serr)
			}
			nc, err = dialPipe()
		}
		if err != nil {
			return nil, fmt.Errorf("cannot reach the Vair helper service: %w", err)
		}
	}
	if err := checkPipeServer(nc); err != nil {
		nc.Close()
		return nil, err
	}
	return newHelperClient(nc)
}

func dialPipe() (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return winio.DialPipeAccessImpLevel(ctx, api.PipeName, pipeClientAccess, winio.PipeImpLevelIdentification)
}

func checkPipeServer(nc interface{}) error {
	f, ok := nc.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("not a pipe")
	}
	var pid uint32
	if r, _, err := procGetNamedPipeServerProcessId.Call(f.Fd(), uintptr(unsafe.Pointer(&pid))); r == 0 {
		return fmt.Errorf("the pipe's server: %w", err)
	}
	spid, err := servicePID()
	if err != nil {
		return fmt.Errorf("the Vair helper service: %w", err)
	}
	if spid == 0 || spid != pid {
		vlog("error", "helper: the pipe %s is served by process %d, not by the Vair helper service (%d) — refusing to use it", api.PipeName, pid, spid)
		return errors.New("the pipe is not served by the Vair helper service")
	}
	return nil
}

// control is the app's own connection to the service for everything that is
// not a tunnel: the persistent block, the proxy kill-switch's block, the
// firewall rules, the block's state. One, kept open, dialled again when it
// has gone — the proxy block belongs to the connection that put it up, so a
// connection per request would hand it over at every call.
var control struct {
	sync.Mutex
	h *helperClient
}

func controlHelper() (*helperClient, error) {
	control.Lock()
	defer control.Unlock()
	if control.h != nil {
		select {
		case <-control.h.done:
			control.h = nil
		default:
			return control.h, nil
		}
	}
	h, err := dialHelper()
	if err != nil {
		return nil, err
	}
	control.h = h
	return h, nil
}

// helperServiceState is the service as the settings show it: "running" or
// "stopped" when it is registered; "missing" when this copy was installed by
// the setup program, which installs the service, and it is not there — a
// repair puts it back; "absent" for a portable copy on a machine without one,
// which then works as administrator instead.
func helperServiceState() string {
	if !serviceMode() {
		if installedBySetupHere() {
			return "missing"
		}
		return "absent"
	}
	if pid, _ := servicePID(); pid != 0 {
		return "running"
	}
	return "stopped"
}

// keptBlockClient is the control connection: a block kept for a successor
// may be the proxy kill-switch's, which belongs to it.
func keptBlockClient() (*helperClient, func(), error) {
	h, err := controlHelper()
	if err != nil {
		return nil, nil, err
	}
	return h, func() {}, nil
}

// helperReachable answers Capabilities: is there a service we can talk to,
// speaking our protocol?
func helperReachable() (bool, string) {
	code, _ := helperProblem()
	return code == "", code
}

// helperProblem is the same question with the sentence for the user.
func helperProblem() (code, message string) {
	if !serviceMode() {
		return "no_service", "TUN mode needs the Vair helper service, which the setup program installs. Reinstall Vair."
	}
	// The control connection answers it: dialled when there is none, kept
	// for the requests that follow rather than a connection per question.
	if _, err := controlHelper(); err != nil {
		vlog("warning", "helper: %v", err)
		return "no_service", err.Error()
	}
	return "", ""
}

// packagedRuleSet: no fallback on Windows — the service reads rule-sets only
// from its own directory, so a set that could not be staged is left out.
func packagedRuleSet(string) string { return "" }
