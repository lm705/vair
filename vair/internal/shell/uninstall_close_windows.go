//go:build windows

package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"
)

// Closing the copy that is about to be removed.
//
// The uninstaller does not close a running program — Inno's Restart Manager
// support is for Setup only — and Vair spends its life in the tray. Uninstalled
// while it ran, it went on running from a folder the uninstaller could not
// delete, holding the kill switch, the system proxy and the tunnel, and the
// cleanup below released all three from under it.
//
// So the cleanup asks it to quit first, through the same channel a second
// launch uses to hand a running copy a vair:// link, and the copy quits the way
// the tray's Exit does. Only the copy being removed: the request names its path,
// and a copy running from anywhere else ignores it. One that does not answer —
// hung, or elevated while the cleanup is not, where Windows will not deliver
// the message — is stopped outright when that is possible, and the uninstall
// is called off when it is not.

// quitForUninstallFlag is the request; the path of the copy being removed
// follows it.
const quitForUninstallFlag = "--quit-for-uninstall"

// IsQuitForUninstall reports whether a second-instance launch is the
// uninstaller asking THIS copy to quit.
func IsQuitForUninstall(args []string) bool {
	for i, a := range args {
		if a == quitForUninstallFlag && i+1 < len(args) {
			self, err := ownExe()
			return err == nil && samePath(args[i+1], self)
		}
	}
	return false
}

func ownExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return exe, nil
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// vairProcesses lists the running processes with this exe's file name: the
// ones started from this very file, and the paths of any others that are
// Vair 2 or later — another copy, whose kill switch, proxy and tunnel are its
// own. The process asking is left out.
func vairProcesses(self string) (ours []uint32, others []string) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, nil
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	me := uint32(os.Getpid())
	name := filepath.Base(self)
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == me || !strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), name) {
			continue
		}
		path := processPath(e.ProcessID)
		switch {
		case path == "":
			// Gone already, or not ours to look at.
		case samePath(path, self):
			ours = append(ours, e.ProcessID)
		default:
			if major, product, ok := fileVersion(path); ok && major >= 2 && strings.EqualFold(strings.TrimSpace(product), "Vair") {
				others = append(others, path)
			}
		}
	}
	return ours, others
}

func processPath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// askToQuit sends the request to the running instance's message window — the
// one the single-instance lock in main.go created under AppID.
func askToQuit(self string) bool {
	id := "wails-app-" + AppID
	hwnd := w32.FindWindowW(windows.StringToUTF16Ptr(id+"-sic"), windows.StringToUTF16Ptr(id+"-siw"))
	if hwnd == 0 {
		return false
	}
	data, _ := json.Marshal(application.SecondInstanceData{Args: []string{quitForUninstallFlag, self}})
	payload, err := windows.UTF16FromString(string(data))
	if err != nil {
		return false
	}
	cds := w32.COPYDATASTRUCT{
		DwData: w32.WMCOPYDATA_SINGLE_INSTANCE_DATA,
		CbData: uint32(len(payload) * 2),
		LpData: uintptr(unsafe.Pointer(&payload[0])),
	}
	// With a timeout: a hung window must not hang the uninstaller with it.
	var result uintptr
	r, _, _ := procSendMessageTimeoutW.Call(uintptr(hwnd), w32.WM_COPYDATA, 0,
		uintptr(unsafe.Pointer(&cds)), smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result)))
	return r != 0
}

var procSendMessageTimeoutW = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

const smtoAbortIfHung = 0x0002

// waitGone waits until none of pids is running, or the time is up.
func waitGone(pids []uint32, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for _, pid := range pids {
		h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
		if err != nil {
			continue // already gone
		}
		left := time.Until(deadline)
		if left < 0 {
			left = 0
		}
		ev, _ := windows.WaitForSingleObject(h, uint32(left/time.Millisecond))
		windows.CloseHandle(h)
		if ev != windows.WAIT_OBJECT_0 {
			return false
		}
	}
	return true
}

func terminate(pids []uint32) {
	for _, pid := range pids {
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); err == nil {
			windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		}
	}
}

// closeRunningCopy makes sure the copy being removed is not running, and says
// which other copies are.
func closeRunningCopy(self string) (line string, others []string) {
	ours, others := vairProcesses(self)
	if len(ours) == 0 {
		return "running copy: none", others
	}
	// Shutting down takes the tunnel down and puts the proxy back, which can
	// take a few seconds; the wait is generous before anything is forced.
	if askToQuit(self) && waitGone(ours, 20*time.Second) {
		return "running copy: closed", others
	}
	terminate(ours)
	if waitGone(ours, 5*time.Second) {
		return "running copy: stopped (it did not answer the request to quit)", others
	}
	return "running copy: COULD NOT CLOSE — quit Vair from the tray, then uninstall again", others
}
