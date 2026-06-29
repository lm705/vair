//go:build windows

package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// startMinimized is set from the --autostart command-line flag (written into the
// HKCU Run key when autostart is enabled). When true, the window starts hidden
// to tray / minimized instead of grabbing the foreground at logon.
var startMinimized bool

// autostart registry location: HKCU\…\Run, value "Vair".
const (
	autostartRunKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	autostartValueName = "Vair"
)

// applyAutostart writes or removes the HKCU Run-key entry that launches Vair at
// Windows logon. When enabled the command is the quoted exe path plus
// --autostart so the boot launch comes up minimized (see openNativeWindow).
func applyAutostart(enabled bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if enabled {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return k.SetStringValue(autostartValueName, `"`+exe+`" --autostart`)
	}
	// Disabled → remove the value; a missing value is not an error.
	if err := k.DeleteValue(autostartValueName); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

// mainHWND is the main window handle, captured in openNativeWindow so a deep-link
// arriving while we're running can raise the window. 0 until the window exists.
var mainHWND uintptr

// registerDeepLink registers (or removes) the per-user vair:// URL scheme so a
// vair://import/<…> link opens this exe with the URL as argv[1].
func registerDeepLink(enabled bool) error {
	const base = `Software\Classes\vair`
	if !enabled {
		// Delete deepest keys first (DeleteKey refuses keys with subkeys).
		registry.DeleteKey(registry.CURRENT_USER, base+`\shell\open\command`)
		registry.DeleteKey(registry.CURRENT_USER, base+`\shell\open`)
		registry.DeleteKey(registry.CURRENT_USER, base+`\shell`)
		registry.DeleteKey(registry.CURRENT_USER, base)
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, base, registry.SET_VALUE)
	if err != nil {
		return err
	}
	k.SetStringValue("", "URL:Vair Protocol")
	k.SetStringValue("URL Protocol", "")
	k.Close()
	ck, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer ck.Close()
	return ck.SetStringValue("", `"`+exe+`" "%1"`)
}

// focusMainWindow restores and foregrounds the main window — used when a deep
// link is forwarded to this already-running instance.
func focusMainWindow() {
	if mainHWND == 0 {
		return
	}
	procShowWindow.Call(mainHWND, swRestore)
	procSetForegroundWindow.Call(mainHWND)
	procBringWindowToTop.Call(mainHWND)
}

// ── Embedded files ────────────────────────────────────────────────────────────

//go:embed bin/xray.exe
var embeddedXray []byte

//go:embed bin/sing-box.exe
var embeddedSingbox []byte

//go:embed bin/geoip.dat
var embeddedGeoIP []byte

//go:embed bin/geosite.dat
var embeddedGeosite []byte

//go:embed bin/icon.ico
var embeddedIcon []byte

// RU-bypass rule-sets for sing-box, embedded as a local fallback. At runtime we
// still try to download the freshest copy from upstream; these are used only
// when that download fails (e.g. GitHub blocked) so sing-box never aborts at
// start over an unreachable remote rule-set. See ruleset_windows.go.
//
//go:embed bin/geoip-ru.srs
var embeddedGeoipRuSrs []byte

//go:embed bin/geosite-ru.srs
var embeddedGeositeRuSrs []byte

// RU-blocked rule-sets — route only blocked-in-RU resources through the VPN.
// srs for sing-box, dat (xray geosite/geoip, category "ru-blocked") for xray.
// Same embed + refresh + fallback model as the RU-bypass sets above.
//
//go:embed bin/geosite-ru-blocked.srs
var embeddedGeositeRuBlockedSrs []byte

//go:embed bin/geoip-ru-blocked.srs
var embeddedGeoipRuBlockedSrs []byte

//go:embed bin/geosite-ru-blocked.dat
var embeddedGeositeRuBlockedDat []byte

//go:embed bin/geoip-ru-blocked.dat
var embeddedGeoipRuBlockedDat []byte

var binDir string

// ── Binary extraction ─────────────────────────────────────────────────────────

func binariesDir() (string, error) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		local = os.Getenv("APPDATA")
	}
	if local == "" {
		return os.MkdirTemp("", "vair-")
	}
	dir := filepath.Join(local, "vair", "bin")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}
	return dir, nil
}

func needsExtract(dst string, src []byte) bool {
	info, err := os.Stat(dst)
	if err != nil {
		return true // file missing
	}
	if info.Size() != int64(len(src)) {
		return true // size changed
	}
	// Also compare first 512 bytes to catch same-size updates
	f, err := os.Open(dst)
	if err != nil {
		return true
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n < 512 || n > len(src) {
		return true
	}
	for i := 0; i < n; i++ {
		if buf[i] != src[i] {
			return true
		}
	}
	return false
}

func extractBinaries() (fresh bool, err error) {
	dir, err := binariesDir()
	if err != nil {
		return false, err
	}
	binDir = dir

	type entry struct {
		name string
		data []byte
		perm os.FileMode
	}
	files := []entry{
		{"xray.exe", embeddedXray, 0755},
		{"sing-box.exe", embeddedSingbox, 0755},
		{"geoip.dat", embeddedGeoIP, 0644},
		{"geosite.dat", embeddedGeosite, 0644},
		{"icon.ico", embeddedIcon, 0644},
	}
	for _, f := range files {
		dst := filepath.Join(dir, f.name)
		if !needsExtract(dst, f.data) {
			continue
		}
		if err := os.WriteFile(dst, f.data, f.perm); err != nil {
			return false, fmt.Errorf("extract %s: %w", f.name, err)
		}
		fresh = true
	}
	// RU rule-set baselines: write the embedded copy only if the file is MISSING,
	// so a fresher version fetched at runtime (refreshRuRuleSets) isn't clobbered
	// on the next launch — unlike the size-compared binaries above.
	for _, rs := range []entry{
		{"geoip-ru.srs", embeddedGeoipRuSrs, 0644},
		{"geosite-ru.srs", embeddedGeositeRuSrs, 0644},
		{"geosite-ru-blocked.srs", embeddedGeositeRuBlockedSrs, 0644},
		{"geoip-ru-blocked.srs", embeddedGeoipRuBlockedSrs, 0644},
		{"geosite-ru-blocked.dat", embeddedGeositeRuBlockedDat, 0644},
		{"geoip-ru-blocked.dat", embeddedGeoipRuBlockedDat, 0644},
	} {
		dst := filepath.Join(dir, rs.name)
		if _, statErr := os.Stat(dst); statErr != nil {
			os.WriteFile(dst, rs.data, rs.perm) //nolint:errcheck
		}
	}
	state.xrayBin = filepath.Join(dir, "xray.exe")
	state.singboxBin = filepath.Join(dir, "sing-box.exe")
	return fresh, nil
}

func cleanupBinaries() {}

func prewarmBinary(binPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	runHiddenContext(ctx, binPath, "version").Run() //nolint:errcheck
}

// ── Win32 ─────────────────────────────────────────────────────────────────────

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")

	procGetWindowLongW               = user32.NewProc("GetWindowLongW")
	procSetWindowLongW               = user32.NewProc("SetWindowLongW")
	procSetWindowLongPtrW            = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW              = user32.NewProc("CallWindowProcW")
	procSetWindowPos                 = user32.NewProc("SetWindowPos")
	procShowWindow                   = user32.NewProc("ShowWindow")
	procIsZoomed                     = user32.NewProc("IsZoomed")
	procSendMessageW                 = user32.NewProc("SendMessageW")
	procReleaseCapture               = user32.NewProc("ReleaseCapture")
	procGetSystemMetrics             = user32.NewProc("GetSystemMetrics")
	procGetWindowRect                = user32.NewProc("GetWindowRect")
	procSystemParametersInfoW        = user32.NewProc("SystemParametersInfoW")
	procLoadImageW                   = user32.NewProc("LoadImageW")
	procPostMessageW                 = user32.NewProc("PostMessageW")
	procDwmExtendFrameIntoClientArea = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
	procSetForegroundWindow          = user32.NewProc("SetForegroundWindow")
	procBringWindowToTop             = user32.NewProc("BringWindowToTop")
	procIsWindowVisible              = user32.NewProc("IsWindowVisible")
	procDwmSetWindowAttribute        = dwmapi.NewProc("DwmSetWindowAttribute")
	procEnumChildWindows             = user32.NewProc("EnumChildWindows")
	procCreatePopupMenu              = user32.NewProc("CreatePopupMenu")
	procInsertMenuW                  = user32.NewProc("InsertMenuW")
	procTrackPopupMenu               = user32.NewProc("TrackPopupMenu")
	procDestroyMenu                  = user32.NewProc("DestroyMenu")
	procGetCursorPos                 = user32.NewProc("GetCursorPos")
	procMonitorFromWindow            = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW              = user32.NewProc("GetMonitorInfoW")

	shell32              = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW    = shell32.NewProc("ShellExecuteW")

	// File-open dialog (used by _goPickFiles).
	comdlg32             = windows.NewLazySystemDLL("comdlg32.dll")
	procGetOpenFileNameW = comdlg32.NewProc("GetOpenFileNameW")
	procGetSaveFileNameW = comdlg32.NewProc("GetSaveFileNameW")
)

type dwmMargins struct{ Left, Right, Top, Bottom int32 }

// Win32 structs for WM_GETMINMAXINFO
type winPoint struct{ X, Y int32 }
type winRect struct{ Left, Top, Right, Bottom int32 }
type minMaxInfo struct {
	Reserved     winPoint
	MaxSize      winPoint // maximized window size
	MaxPosition  winPoint // maximized window position
	MinTrackSize winPoint
	MaxTrackSize winPoint
}

// monitorInfo mirrors the Win32 MONITORINFO struct. RcWork is the monitor's
// work area (full bounds minus taskbar) in virtual-desktop coordinates.
type monitorInfo struct {
	CbSize    uint32
	RcMonitor winRect
	RcWork    winRect
	DwFlags   uint32
}

const monitorDefaultToNearest = 2 // MONITOR_DEFAULTTONEAREST

// GWL_STYLE = -16, GWL_EXSTYLE = -20, GWLP_WNDPROC = -4
// Must be int32 vars — uintptr can't hold negative constants at compile time.
var (
	gwlStyleVal int32 = -16
	gwlExStyle  int32 = -20
	gwlpWndproc int32 = -4
)

const (
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsThickFrame   = 0x00040000
	wsMinimizeBox  = 0x00020000
	wsMaximizeBox  = 0x00010000
	wsExAppWindow  = 0x00040000
	wsExToolWindow = 0x00000080

	swMinimize = 6
	swMaximize = 3
	swRestore  = 9
	swHide     = 0
	swShow     = 5

	swpFrameChanged = 0x0020
	swpNomove       = 0x0002
	swpNosize       = 0x0001
	swpNozorder     = 0x0004
	swpNoactivate   = 0x0010

	wmNclbuttondown = 0x00A1
	wmSysCommand    = 0x0112
	wmSeticon       = 0x0080
	wmClose         = 0x0010
	scMinimize      = 0xF020
	wmApp           = 0x8000 // WM_APP — used for tray icon callback
	wmLbuttonup     = 0x0202
	wmRbuttonup     = 0x0205

	htCaption       = 2
	wmNccalcsize    = 0x0083
	wmNchittest     = 0x0084
	wmGetMinMaxInfo = 0x0024
	spiGetWorkArea  = 0x0030

	// WM_NCHITTEST return codes for resize borders
	htLeft        = 10
	htRight       = 11
	htTop         = 12
	htTopLeft     = 13
	htTopRight    = 14
	htBottom      = 15
	htBottomLeft  = 16
	htBottomRight = 17
	htClient      = 1

	// HTTRANSPARENT: child returns this to pass WM_NCHITTEST to the parent window.
	// Win32 value is -1; as uintptr on 64-bit = 0xFFFFFFFFFFFFFFFF.
	htTransparent = ^uintptr(0)

	iconSmall      = 0
	iconBig        = 1
	imageIcon      = 1
	lrLoadfromfile = 0x00000010
	lrDefaultsize  = 0x00000040
)

// origWndProc stores the original WebView2 window procedure for CallWindowProc.
var origWndProc uintptr

// ── Child-window resize subclassing ──────────────────────────────────────────
//
// Problem: WebView2 creates child HWNDs that cover the entire parent window.
// Mouse events near the window edge go to those child HWNDs, which return
// HTCLIENT.  The parent's WM_NCHITTEST handler is never reached because the
// parent has zero NC area (WM_NCCALCSIZE returns 0).
//
// Fix: enumerate all WebView2 child windows and subclass them.  In the child
// WndProc, when the cursor is within `resizeBorder` pixels of the parent
// window edge, return HTTRANSPARENT.  Windows then re-sends the hit-test to
// the window *behind* the child — the parent — where our WndProc returns the
// correct HTLEFT / HTRIGHT / HTTOP / etc. resize code.
//
// This is the same technique used by VS Code / Electron / Wails.

const resizeBorder = 8 // pixels, slightly wider than default for easy grabbing

var (
	resizeParentHWND uintptr  // set once in addChildSubclassing
	childOrigProcs   sync.Map // hwnd (uintptr) → original WndProc (uintptr)
	childWndProcCB   uintptr  // single stable callback for all children
)

// preDetachRect remembers the window rect before the AUTO panel was detached
// into a compact window, so _goRestoreFromDetach can put it back. preDetachValid
// guards against restoring when nothing was saved.
//
// detachActive is true while the compact detached AUTO window is showing, and
// detachRect is that compact window's rect. They let _goWinMaximize restore to
// the compact size (not the full app size) when the user un-maximizes a detached
// window — otherwise the titlebar maximize button would blow the panel back up
// to the full application window.
var (
	preDetachRect  winRect
	preDetachValid bool
	detachActive   bool
	detachRect     winRect
)

// initChildResizeCB initialises childWndProcCB.  Must be called before
// addChildSubclassing.  Separate from init() to avoid import-cycle issues.
func initChildResizeCB() {
	childWndProcCB = syscall.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
		if msg == wmNchittest {
			// Cursor position in screen coordinates (signed 16-bit in lParam)
			cx := int32(int16(lParam & 0xFFFF))
			cy := int32(int16((lParam >> 16) & 0xFFFF))
			var wr winRect
			procGetWindowRect.Call(resizeParentHWND, uintptr(unsafe.Pointer(&wr)))
			b := int32(resizeBorder)
			if cx < wr.Left+b || cx >= wr.Right-b || cy < wr.Top+b || cy >= wr.Bottom-b {
				// Cursor is in the resize border zone — pass to parent
				return htTransparent
			}
		}
		orig, ok := childOrigProcs.Load(hwnd)
		if !ok {
			ret, _, _ := procCallWindowProcW.Call(0, hwnd, msg, wParam, lParam)
			return ret
		}
		ret, _, _ := procCallWindowProcW.Call(orig.(uintptr), hwnd, msg, wParam, lParam)
		return ret
	})
}

// addChildSubclassing enumerates all child HWNDs of parentHWND and subclasses
// each one so that WM_NCHITTEST near the edges returns HTTRANSPARENT.
// Safe to call multiple times; children that are already subclassed are skipped.
func addChildSubclassing(parentHWND uintptr) {
	resizeParentHWND = parentHWND
	gwlp := uintptr(uint32(gwlpWndproc))
	cb := syscall.NewCallback(func(child, _ uintptr) uintptr {
		if _, done := childOrigProcs.Load(child); !done {
			orig, _, _ := procSetWindowLongPtrW.Call(child, gwlp, childWndProcCB)
			if orig != 0 && orig != childWndProcCB {
				childOrigProcs.Store(child, orig)
			}
		}
		return 1 // continue enumeration
	})
	procEnumChildWindows.Call(parentHWND, cb, 0)
}

// idealWindowSize returns the preferred (w,h) for a window on a monitor whose
// work area is workW×workH: pct% of the work area, floored at minWinW×minWinH —
// but never larger than the work area itself (so a small monitor gets a small
// window). No upper cap: 100% fills the monitor's work area, 50% is half, etc.,
// so the percentage is honest. This is the SINGLE source of truth for "default
// window size", used both at launch and on un-maximize, so the result is
// deterministic per monitor and never path-dependent.
func idealWindowSize(workW, workH, pct int32) (int32, int32) {
	if pct < 40 {
		pct = 40
	}
	if pct > 100 {
		pct = 100
	}
	w := workW * pct / 100
	h := workH * pct / 100
	// Lower floor: the in-app layout (header stats + controls, conn-bar) needs
	// ~960 CSS px to lay out without wrapping. minWinW gives headroom over that
	// (window frame + a little slack). Below it the body's min-width + the OS
	// window's horizontal scroll keep the UI usable rather than collapsing.
	const minWinW, minWinH = 980, 620
	if w < minWinW {
		w = minWinW
	}
	if h < minWinH {
		h = minWinH
	}
	// Never exceed the monitor's work area (a 1366×768 laptop must not get a
	// 980-wide window forced past its edge). On a monitor smaller than the
	// floor this yields a window = the whole work area, which is the best we
	// can do; the CSS min-width + horizontal scroll then handle the overflow.
	if w > workW {
		w = workW
	}
	if h > workH {
		h = workH
	}
	return w, h
}

// applyIdealWindowSize resizes + centers the window to idealWindowSize for the
// monitor it currently sits on, using the user's WindowSizePct setting. Called
// on un-maximize (so restore is recomputed per-monitor, not the stale launch
// rect) and when the size setting changes. Deterministic: the same monitor + %
// always yields the same window, regardless of how the user got there — which
// fixes the "small size sticks after moving between monitors" bug.
func applyIdealWindowSize(hwnd uintptr) {
	hMon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if hMon == 0 {
		return
	}
	var mi monitorInfo
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	if r, _, _ := procGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return
	}
	work := mi.RcWork
	workW := work.Right - work.Left
	workH := work.Bottom - work.Top
	w, h := idealWindowSize(workW, workH, int32(currentWindowSizePct()))
	// Center within the work area.
	x := work.Left + (workW-w)/2
	y := work.Top + (workH-h)/2
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		swpNozorder|swpNoactivate)
}

// wndProcCallback is the custom WndProc that handles SC_MINIMIZE from taskbar.
// Windows sends WM_SYSCOMMAND/SC_MINIMIZE when the user clicks the taskbar
// button of the active (foreground) window.  Without a native title bar the
// default WebView2 WndProc does not handle this, so the window never minimizes
// on taskbar click.  We intercept it here and call ShowWindow(SW_MINIMIZE).
var wndProcCallback uintptr

func init() {
	wndProcCallback = syscall.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
		switch msg {

		case wmNccalcsize:
			// wParam != 0: tell Windows client area = full window rect → no NC stripe.
			// This is the only reliable way to remove the DWM white title-bar stripe
			// on frameless WebView2 windows (same technique as VS Code / Electron).
			if wParam != 0 {
				return 0
			}

		case wmSysCommand:
			switch wParam & 0xFFF0 {
			case scMinimize:
				// CRITICAL: NEVER use PostMessage(SC_MINIMIZE) here.
				// PostMessage re-queues the message → WndProc catches it again →
				// infinite loop → message queue flood → app freeze.
				// ShowWindow goes directly to the kernel, no re-entry.
				procShowWindow.Call(hwnd, swMinimize)
				return 0
			}

		case wmNchittest:
			// When WM_NCCALCSIZE returns 0, the NC area is zero-sized, so Windows
			// no longer has resize border hit-test areas. We must implement them
			// ourselves via WM_NCHITTEST, otherwise the window cannot be resized.
			//
			// Decode cursor position from lParam (screen coordinates, signed 16-bit).
			cx := int32(int16(lParam & 0xFFFF))
			cy := int32(int16((lParam >> 16) & 0xFFFF))
			var wr winRect
			procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
			const border = 6 // resize border width in pixels
			left := cx < wr.Left+border
			right := cx >= wr.Right-border
			top := cy < wr.Top+border
			bottom := cy >= wr.Bottom-border
			switch {
			case top && left:
				return htTopLeft
			case top && right:
				return htTopRight
			case bottom && left:
				return htBottomLeft
			case bottom && right:
				return htBottomRight
			case top:
				return htTop
			case bottom:
				return htBottom
			case left:
				return htLeft
			case right:
				return htRight
			}
			// Anything else: let the default proc decide (htClient for the content area)
			ret, _, _ := procCallWindowProcW.Call(origWndProc, hwnd, msg, wParam, lParam)
			return ret

		case wmGetMinMaxInfo:
			// Limit a maximized window to the work area of the monitor the window
			// is CURRENTLY on (not the primary monitor). The old code used
			// SystemParametersInfo(SPI_GETWORKAREA), which always returns the
			// PRIMARY monitor's work area — so maximizing on a smaller secondary
			// display produced an oversized window spilling past its edges.
			//
			// MaxSize/MaxPosition are in coordinates relative to the target
			// monitor's top-left, so we offset by RcMonitor (the monitor origin
			// in the virtual desktop) — for a monitor left of/above the primary
			// these are negative, which is exactly what Windows expects here.
			if lParam != 0 {
				mmi := (*minMaxInfo)(unsafe.Pointer(lParam))
				hMon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
				var mi monitorInfo
				mi.CbSize = uint32(unsafe.Sizeof(mi))
				if hMon != 0 {
					r1, _, _ := procGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi)))
					if r1 != 0 {
						work := mi.RcWork
						mon := mi.RcMonitor
						mmi.MaxPosition.X = work.Left - mon.Left
						mmi.MaxPosition.Y = work.Top - mon.Top
						mmi.MaxSize.X = work.Right - work.Left
						mmi.MaxSize.Y = work.Bottom - work.Top
						return 0
					}
				}
				// Fallback: primary-monitor work area (previous behaviour).
				var workArea winRect
				procSystemParametersInfoW.Call(spiGetWorkArea, 0,
					uintptr(unsafe.Pointer(&workArea)), 0)
				mmi.MaxPosition.X = workArea.Left
				mmi.MaxPosition.Y = workArea.Top
				mmi.MaxSize.X = workArea.Right - workArea.Left
				mmi.MaxSize.Y = workArea.Bottom - workArea.Top
				return 0
			}

		case wmApp:
			// Tray icon callback — lParam contains the mouse message
			switch lParam {
			case wmLbuttonup:
				// Toggle window visibility on left-click
				visible, _, _ := procIsWindowVisible.Call(hwnd)
				if visible != 0 {
					procShowWindow.Call(hwnd, swHide)
				} else {
					procShowWindow.Call(hwnd, swRestore)
					procSetForegroundWindow.Call(hwnd)
				}
			case wmRbuttonup:
				// Show context menu
				showTrayMenu(hwnd)
			}
			return 0

		}
		ret, _, _ := procCallWindowProcW.Call(origWndProc, hwnd, msg, wParam, lParam)
		return ret
	})
}

// setupWindow makes the WebView2 window frameless while keeping all expected
// Windows behaviours: taskbar minimize, Aero Snap, alt-tab, window icon.
func setupWindow(hwnd uintptr) {
	gwl := uintptr(uint32(gwlStyleVal))
	gwlEx := uintptr(uint32(gwlExStyle))
	gwlp := uintptr(uint32(gwlpWndproc))

	// ── 1. Strip title bar, keep resize/minimize/maximize behaviour ──────────
	style, _, _ := procGetWindowLongW.Call(hwnd, gwl)
	style &^= wsCaption | wsSysMenu
	style |= wsThickFrame | wsMinimizeBox | wsMaximizeBox
	procSetWindowLongW.Call(hwnd, gwl, style)

	// ── 2. Ensure the taskbar button is always visible ────────────────────────
	exStyle, _, _ := procGetWindowLongW.Call(hwnd, gwlEx)
	exStyle |= wsExAppWindow
	exStyle &^= wsExToolWindow
	procSetWindowLongW.Call(hwnd, gwlEx, exStyle)

	// ── 3. Subclass WndProc FIRST so our handler is in place before any ────────
	//       WM_NCCALCSIZE messages arrive from step 4.
	origWndProc, _, _ = procSetWindowLongPtrW.Call(hwnd, gwlp, wndProcCallback)

	// ── 4. NOW trigger frame recalculation — WM_NCCALCSIZE will go to our ────
	//       handler which returns 0 (client area = window area → no stripe).
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpFrameChanged|swpNomove|swpNosize|swpNozorder)

	// ── 5. DWM: 1-pixel top shadow so DWM knows a frame exists → enables
	//       minimize/maximize/restore slide animations on Windows 10/11. ────────
	//       With {0,0,0,0} DWM sees "no frame" and may skip animations.
	m := dwmMargins{0, 0, 1, 0} // 1px top margin only
	procDwmExtendFrameIntoClientArea.Call(hwnd, uintptr(unsafe.Pointer(&m)))

	// ── 6. Explicitly un-disable DWM transitions for this window. ─────────────
	//       DWMWA_TRANSITIONS_FORCEDISABLED = 3.  Value 0 = transitions enabled.
	const dwmwaTransitions = 3
	var transitionsEnabled uint32 = 0
	procDwmSetWindowAttribute.Call(hwnd, dwmwaTransitions,
		uintptr(unsafe.Pointer(&transitionsEnabled)), 4)

	// ── 7. Dark title bar (for Alt+Tab and system dialogs). ───────────────────
	const dwmwaImmersiveDark = 20
	var dark uint32 = 1
	procDwmSetWindowAttribute.Call(hwnd, dwmwaImmersiveDark,
		uintptr(unsafe.Pointer(&dark)), 4)
}

// setWindowIcon loads icon.ico and sets it as the window/taskbar/alt-tab icon.
// ICON_BIG goes to the taskbar button + Alt+Tab thumbnail (we request 48px so it
// stays sharp); ICON_SMALL goes to the title bar / notification area (SM_CXSMICON,
// 16px at 100% DPI). The multi-size .ico supplies the exact pixel sizes.
func setWindowIcon(hwnd uintptr) {
	iconPath := filepath.Join(binDir, "icon.ico")
	if _, err := os.Stat(iconPath); err != nil {
		return
	}
	pathW, err := windows.UTF16PtrFromString(iconPath)
	if err != nil {
		return
	}

	// Query system icon sizes so we load the exact pixel size Windows needs.
	// For the taskbar (ICON_BIG): use 48px — larger than SM_CXICON (32px)
	// which makes the icon sharper and more prominent on the taskbar.
	// Windows will downscale if the taskbar slot is smaller.
	bigSz := uintptr(48)
	smallSz, _, _ := procGetSystemMetrics.Call(49) // SM_CXSMICON (16 at 100%, 20 at 125%)
	if smallSz == 0 {
		smallSz = 16
	}

	hBig, _, _ := procLoadImageW.Call(
		0, uintptr(unsafe.Pointer(pathW)),
		imageIcon, bigSz, bigSz,
		lrLoadfromfile,
	)
	hSmall, _, _ := procLoadImageW.Call(
		0, uintptr(unsafe.Pointer(pathW)),
		imageIcon, smallSz, smallSz,
		lrLoadfromfile,
	)
	if hBig != 0 {
		procSendMessageW.Call(hwnd, wmSeticon, iconBig, hBig)
	}
	if hSmall != 0 {
		procSendMessageW.Call(hwnd, wmSeticon, iconSmall, hSmall)
	}
}

// ── Logo base64 (64×64 PNG of the app icon, injected into the title bar) ─────
const logoBase64 = "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"

// ── WebView2 window ───────────────────────────────────────────────────────────

func openNativeWindow(addr string) {
	time.Sleep(300 * time.Millisecond)

	// Initial size = WindowSizePct% of the primary monitor (the window opens
	// centered there), via the same idealWindowSize logic used on un-maximize so
	// launch and restore agree. SM_CXSCREEN/CYSCREEN is the primary monitor's
	// full size; close enough to its work area for the initial placement, and
	// the caps/floors in idealWindowSize keep it sane.
	screenW, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	screenH, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	iw, ih := idealWindowSize(int32(screenW), int32(screenH), int32(currentWindowSizePct()))
	winW := int(iw)
	winH := int(ih)

	w := webview.NewWithOptions(webview.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview.WindowOptions{
			Title:  "Vair",
			Width:  uint(winW),
			Height: uint(winH),
			Center: true,
		},
	})
	if w == nil {
		fmt.Printf("\n!! WebView2 unavailable. Open manually: %s\n", addr)
		select {}
	}
	defer w.Destroy()

	hwnd := uintptr(w.Window())
	mainHWND = hwnd
	setupWindow(hwnd)
	setWindowIcon(hwnd)

	// Show tray icon if enabled
	settingsMu.RLock()
	trayOn := appSettings.TrayEnabled
	settingsMu.RUnlock()
	if trayOn {
		addTrayIcon(hwnd, filepath.Join(binDir, "icon.ico"))
	}

	if startMinimized {
		// Logon launch: don't steal focus. Hide to tray when the tray icon is on
		// (added just above), otherwise minimize to the taskbar. Re-applied on a
		// short delay because go-webview2 shows the window during Run() — the
		// delayed call lands after that and sticks.
		hideOrMin := func() {
			if trayOn {
				procShowWindow.Call(hwnd, swHide)
			} else {
				procShowWindow.Call(hwnd, swMinimize)
			}
		}
		hideOrMin()
		go func() {
			time.Sleep(250 * time.Millisecond)
			hideOrMin()
		}()
	} else {
		// Bring window to foreground so it appears on top (not behind other windows)
		procSetForegroundWindow.Call(hwnd)
		procBringWindowToTop.Call(hwnd)
	}

	// Periodically update tray tooltip with connection info
	go func() {
		lastTip := ""
		for {
			time.Sleep(2 * time.Second)
			settingsMu.RLock()
			on := appSettings.TrayEnabled
			settingsMu.RUnlock()
			if !on {
				continue
			}
			cs := state.conn.snap()
			settingsMu.RLock()
			autoOn := appSettings.AutoConnect
			settingsMu.RUnlock()
			tip := "Vair"
			if cs.Status == ConnConnected {
				mode := "Proxy"
				if cs.Mode == ModeTUN {
					mode = "TUN"
				}
				// EntryName is already the joined "A → B" for a chain; prefix it
				// so the tray clearly distinguishes a chain from a single config.
				if len(cs.Chain) > 1 {
					tip = fmt.Sprintf("Vair — ⛓ %s [%s]", cs.EntryName, mode)
				} else {
					tip = fmt.Sprintf("Vair — %s [%s]", cs.EntryName, mode)
				}
			}
			if autoOn {
				tip += " ⟳ auto"
			}
			if tip != lastTip {
				updateTrayTooltip(hwnd, tip)
				lastTip = tip
			}
		}
	}()

	// ── JS bindings ───────────────────────────────────────────────────────────
	icoFullPath := filepath.Join(binDir, "icon.ico")

	w.Bind("_goWinClose", func() {
		settingsMu.RLock()
		trayOn := appSettings.TrayEnabled
		settingsMu.RUnlock()
		if trayOn {
			// Ensure tray icon exists before hiding window
			addTrayIcon(hwnd, icoFullPath)
			procShowWindow.Call(hwnd, swHide)
		} else {
			procPostMessageW.Call(hwnd, wmClose, 0, 0)
		}
	})
	w.Bind("_goToggleTray", func(on bool) {
		if on {
			addTrayIcon(hwnd, icoFullPath)
		} else {
			removeTrayIcon(hwnd)
		}
	})
	w.Bind("_goWinMinimize", func() {
		// SW_MINIMIZE = 6 animates correctly on Windows 10/11 for frameless windows.
		// Do NOT use PostMessage(SC_MINIMIZE) — that re-enters WndProc → freeze.
		procShowWindow.Call(hwnd, swMinimize)
	})
	w.Bind("_goWinMaximize", func() bool {
		zoomed, _, _ := procIsZoomed.Call(hwnd)
		if zoomed != 0 {
			procShowWindow.Call(hwnd, swRestore)
			if detachActive {
				// In the compact detached AUTO window: restore to the compact SIZE,
				// not the full app size — otherwise un-maximizing blows the panel
				// back up to the whole application window. Re-center on the monitor
				// the window is CURRENTLY on (it may have been moved to another
				// monitor before maximizing), so it doesn't jump back to the primary
				// screen where it was first detached.
				cw := detachRect.Right - detachRect.Left
				ch := detachRect.Bottom - detachRect.Top
				cx, cy := detachRect.Left, detachRect.Top
				if hMon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest); hMon != 0 {
					var mi monitorInfo
					mi.CbSize = uint32(unsafe.Sizeof(mi))
					if r, _, _ := procGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi))); r != 0 {
						workW := mi.RcWork.Right - mi.RcWork.Left
						workH := mi.RcWork.Bottom - mi.RcWork.Top
						cx = mi.RcWork.Left + (workW-cw)/2
						cy = mi.RcWork.Top + (workH-ch)/2
					}
				}
				procSetWindowPos.Call(hwnd, 0, uintptr(cx), uintptr(cy),
					uintptr(cw), uintptr(ch), swpNozorder|swpNoactivate)
				detachRect = winRect{Left: cx, Top: cy, Right: cx + cw, Bottom: cy + ch}
				return false
			}
			// Recompute the restore size for THIS monitor (deterministic per
			// monitor + size%), instead of reusing the stale launch rect that was
			// based on the primary monitor. Fixes both "too big on small monitor"
			// and "stays small after moving to a big monitor".
			applyIdealWindowSize(hwnd)
			return false
		}
		procShowWindow.Call(hwnd, swMaximize)
		return true
	})
	w.Bind("_goWinIsMaximized", func() bool {
		zoomed, _, _ := procIsZoomed.Call(hwnd)
		return zoomed != 0
	})
	// _goApplyWindowSize re-applies the default window size for the current
	// monitor, using the freshly-saved WindowSizePct. Called from the Settings
	// UI right after the % changes. No-op while maximized (the change takes
	// effect on the next un-maximize instead).
	w.Bind("_goApplyWindowSize", func() {
		if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
			return
		}
		applyIdealWindowSize(hwnd)
	})
	w.Bind("_goWinDragStart", func() {
		procReleaseCapture.Call()
		procSendMessageW.Call(hwnd, wmNclbuttondown, htCaption, 0)
	})
	// _goDetachResize resizes the window to the AUTO panel's size (CSS px ×
	// devicePixelRatio = physical px) when the panel is detached into a compact
	// standalone window, saving the pre-detach rect so it can be restored. The
	// window keeps its top-left corner; size is clamped to the current monitor's
	// work area so the panel can't end up larger than the screen.
	w.Bind("_goDetachResize", func(cssW, cssH, dpr float64) {
		if dpr <= 0 {
			dpr = 1
		}
		var wr winRect
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
		// Don't capture a maximized rect as the "restore" target.
		if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
			procShowWindow.Call(hwnd, swRestore)
			procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
		}
		// Save the pre-detach rect ONLY on the first detach. This bind is also
		// called when the panel's settings are expanded/collapsed (to re-fit the
		// window); if we re-saved here, "Back to app" would restore the compact
		// rect and crop the main window. detachActive is true after the first
		// detach, so guard on it.
		if !detachActive {
			preDetachRect = wr
			preDetachValid = true
		}

		pw := int32(cssW*dpr + 0.5)
		ph := int32(cssH*dpr + 0.5)
		// Keep the window's CENTER fixed: detaching, or expanding/collapsing the
		// settings, grows/shrinks the window in place instead of jumping it to the
		// screen center. (On first detach the center = the main window's center, so
		// a centered main window yields a centered compact window.) The HEIGHT cap
		// honors the user's window-size-% setting (the same percent the main window
		// uses) so expanded settings grow only up to pct% of the work area and the
		// panel scrolls past that. Width is capped at the full work area only, so
		// the ~560px panel is never squished.
		pct := int32(currentWindowSizePct())
		if pct < 40 {
			pct = 40
		} else if pct > 100 {
			pct = 100
		}
		cx := wr.Left + (wr.Right-wr.Left)/2
		cy := wr.Top + (wr.Bottom-wr.Top)/2
		if hMon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest); hMon != 0 {
			var mi monitorInfo
			mi.CbSize = uint32(unsafe.Sizeof(mi))
			if r, _, _ := procGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi))); r != 0 {
				workW := mi.RcWork.Right - mi.RcWork.Left
				workH := mi.RcWork.Bottom - mi.RcWork.Top
				maxH := workH * pct / 100
				if pw > workW {
					pw = workW
				}
				if ph > maxH {
					ph = maxH
				}
			}
		}
		if pw < 320 {
			pw = 320
		}
		if ph < 200 {
			ph = 200
		}
		// Place by center, then nudge fully on-screen if growing pushed an edge off
		// the monitor's work area (keeps it "where it is" without spilling off).
		x := cx - pw/2
		y := cy - ph/2
		if hMon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest); hMon != 0 {
			var mi monitorInfo
			mi.CbSize = uint32(unsafe.Sizeof(mi))
			if r, _, _ := procGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi))); r != 0 {
				if x+pw > mi.RcWork.Right {
					x = mi.RcWork.Right - pw
				}
				if y+ph > mi.RcWork.Bottom {
					y = mi.RcWork.Bottom - ph
				}
				if x < mi.RcWork.Left {
					x = mi.RcWork.Left
				}
				if y < mi.RcWork.Top {
					y = mi.RcWork.Top
				}
			}
		}
		procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(pw), uintptr(ph),
			swpNozorder|swpNoactivate)
		// Remember the compact rect + that we're detached, so an un-maximize from
		// the titlebar restores to this size rather than the full app size.
		detachRect = winRect{Left: x, Top: y, Right: x + pw, Bottom: y + ph}
		detachActive = true
	})
	// _goRestoreFromDetach restores the window to the size/position it had before
	// the AUTO panel was detached.
	w.Bind("_goRestoreFromDetach", func() {
		detachActive = false
		if !preDetachValid {
			return
		}
		// If the detached window was left maximized, un-maximize first so the
		// restored rect actually takes effect (and the zoomed flag is cleared).
		if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
			procShowWindow.Call(hwnd, swRestore)
		}
		r := preDetachRect
		procSetWindowPos.Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top),
			uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), swpNozorder|swpNoactivate)
		preDetachValid = false
	})
	w.Bind("_goLogoBase64", func() string { return logoBase64 })

	// File picker — opens the native Windows open-file dialog and returns
	// metadata for each chosen file. Returns:
	//   [{name, path, size, mtime}, …]
	// We deliberately do NOT read file content here. The server reads
	// content directly from disk when it needs to parse configs, so the
	// renderer never has to hold a copy. This makes any file size work
	// without memory pressure on the WebView2 process.
	w.Bind("_goPickFiles", func() []map[string]interface{} {
		paths := pickConfigFiles(hwnd)
		out := make([]map[string]interface{}, 0, len(paths))
		for _, p := range paths {
			info, statErr := os.Stat(p)
			if statErr != nil {
				fmt.Fprintf(os.Stderr, "⚠ pick %s: %v\n", p, statErr)
				continue
			}
			out = append(out, map[string]interface{}{
				"name":  filepath.Base(p),
				"path":  p,
				"size":  info.Size(),
				"mtime": info.ModTime().Unix(),
			})
		}
		return out
	})

	// Lists running processes (executable names) for the "Apps without
	// VPN" chip input. Returns a deduplicated, sorted, lowercased slice.
	// Result is small (typically a few hundred entries) so we just send
	// the whole list each call.
	w.Bind("_goListProcesses", func() []string {
		return listRunningProcessNames()
	})

	// Settings export — show a native Save As dialog, write the supplied
	// JSON to the chosen path. Returns the path on success, empty string on
	// cancel. Errors propagate to the JS .catch.
	w.Bind("_goSaveExport", func(suggestedName, content string) (string, error) {
		return saveJSONFile(hwnd, suggestedName, []byte(content))
	})

	// QR scan from an image file: native picker → decode → return the payload
	// (config URL / subscription / base64). "" = cancelled.
	w.Bind("_goScanQRFile", func() (string, error) {
		return scanQRFromFile(hwnd)
	})
	// QR scan from the screen: minimise our window first so it doesn't cover the
	// QR (it's usually in another app), grab the desktop, decode, restore.
	w.Bind("_goScanQRScreen", func() (string, error) {
		procShowWindow.Call(hwnd, swMinimize)
		time.Sleep(350 * time.Millisecond)
		txt, err := scanQRFromScreen()
		procShowWindow.Call(hwnd, swRestore)
		procSetForegroundWindow.Call(hwnd)
		return txt, err
	})
	// Open an external link (attribution) in the default browser. The WebView
	// must never navigate away from the app itself.
	w.Bind("_goOpenURL", func(u string) {
		openExternalURL(u)
	})

	w.Navigate(addr)

	// ── Resize fix: subclass WebView2 child windows ───────────────────────────
	// WebView2 creates child HWNDs that intercept all mouse events.  We must
	// subclass them to return HTTRANSPARENT at the resize border area so that
	// hit-test messages bubble up to our parent WndProc.
	//
	// WebView2 creates its child windows asynchronously during the first paint.
	// We retry every 300 ms for up to 5 s to catch all children.
	initChildResizeCB()
	go func() {
		for i := 0; i < 17; i++ { // ~5 s total
			time.Sleep(300 * time.Millisecond)
			addChildSubclassing(hwnd)
		}
	}()

	w.Run()

	stopConnection()
	killOrphanedXray()
	os.Exit(0)
}

// ── Entry point ───────────────────────────────────────────────────────────────

// ── System Tray (notify icon) ─────────────────────────────────────────────────

const (
	nimAdd     = 0x00000000
	nimDelete  = 0x00000002
	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
)

// NOTIFYICONDATAW (simplified, 64-bit layout)
type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	_                uint32 // padding
	HIcon            uintptr
	SzTip            [128]uint16
}

func addTrayIcon(hwnd uintptr, icoPath string) {
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	nid.UFlags = nifMessage | nifIcon | nifTip
	nid.UCallbackMessage = wmApp

	// Load icon from file
	icoPathW, _ := syscall.UTF16PtrFromString(icoPath)
	icon, _, _ := procLoadImageW.Call(
		0, uintptr(unsafe.Pointer(icoPathW)),
		1, // IMAGE_ICON
		16, 16,
		0x00000010, // LR_LOADFROMFILE
	)
	nid.HIcon = icon

	// Set tooltip
	tip, _ := syscall.UTF16FromString("Vair")
	copy(nid.SzTip[:], tip)

	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
}

func removeTrayIcon(hwnd uintptr) {
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

type point struct{ X, Y int32 }

const (
	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfGreyed    = 0x00000001
)

var trayMainHwnd uintptr

func showTrayMenu(hwnd uintptr) {
	trayMainHwnd = hwnd
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}

	// Check connection state
	cs := state.conn.snap()
	isConnected := cs.Status == ConnConnected

	settingsMu.RLock()
	autoOn := appSettings.AutoConnect
	settingsMu.RUnlock()

	menuIdx := uint32(0)

	if isConnected {
		// Show connected config name (greyed, informational). For a chain,
		// EntryName is the joined "A → B"; mark it with ⛓ so it's distinguishable.
		mode := "Proxy"
		if cs.Mode == ModeTUN {
			mode = "TUN"
		}
		marker := "●"
		if len(cs.Chain) > 1 {
			marker = "⛓"
		}
		label := fmt.Sprintf("%s %s [%s]", marker, cs.EntryName, mode)
		labelW, _ := syscall.UTF16PtrFromString(label)
		procInsertMenuW.Call(hMenu, uintptr(menuIdx), mfString|mfGreyed, 0, uintptr(unsafe.Pointer(labelW)))
		menuIdx++
	}

	if autoOn {
		// Greyed info line so the user can see auto mode is active from the tray.
		autoLabelW, _ := syscall.UTF16PtrFromString("⟳ Auto-connect: ON")
		procInsertMenuW.Call(hMenu, uintptr(menuIdx), mfString|mfGreyed, 0, uintptr(unsafe.Pointer(autoLabelW)))
		menuIdx++
	}

	if isConnected || autoOn {
		procInsertMenuW.Call(hMenu, uintptr(menuIdx), mfSeparator, 0, 0)
		menuIdx++
	}

	showW, _ := syscall.UTF16PtrFromString("Show")
	procInsertMenuW.Call(hMenu, uintptr(menuIdx), mfString, 1001, uintptr(unsafe.Pointer(showW)))
	menuIdx++

	if autoOn {
		// Switch now: ask the supervisor to fail over to the next-best config
		// (or connect, if currently idle) on its next tick.
		switchW, _ := syscall.UTF16PtrFromString("Switch now")
		procInsertMenuW.Call(hMenu, uintptr(menuIdx), mfString, 1004, uintptr(unsafe.Pointer(switchW)))
		menuIdx++
	}

	if isConnected {
		disconnectW, _ := syscall.UTF16PtrFromString("Disconnect")
		procInsertMenuW.Call(hMenu, uintptr(menuIdx), mfString, 1002, uintptr(unsafe.Pointer(disconnectW)))
		menuIdx++
	}

	procInsertMenuW.Call(hMenu, uintptr(menuIdx), mfSeparator, 0, 0)
	menuIdx++
	exitW, _ := syscall.UTF16PtrFromString("Exit")
	procInsertMenuW.Call(hMenu, uintptr(menuIdx), mfString, 1003, uintptr(unsafe.Pointer(exitW)))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(hMenu, 0x0100, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	procDestroyMenu.Call(hMenu)

	switch cmd {
	case 1001:
		procShowWindow.Call(hwnd, swRestore)
		procSetForegroundWindow.Call(hwnd)
	case 1002:
		// Deliberate disconnect from the tray = same intent as the in-app
		// Disconnect button: disarm auto so the supervisor doesn't immediately
		// reconnect/failover. Without this, auto kept running after a tray
		// disconnect and pulled a new connection right back up.
		autoWant.Store(false)
		autoManaged.Store(false)
		autoLiveRtt.Store(0)
		autoForce.Store(false)
		broadcastAuto("paused", "", "", "manual")
		go func() {
			stopConnection()
			// Update tray tooltip
			updateTrayTooltip(hwnd, "Vair")
		}()
	case 1003:
		removeTrayIcon(hwnd)
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	case 1004:
		// Switch now (same as the panel button / HTTP endpoint): arm intent and
		// force the supervisor to fail over / connect on its next tick.
		autoWant.Store(true)
		autoForce.Store(true)
		autoKick()
	}
}

// updateTrayTooltip updates the tray icon tooltip text
func updateTrayTooltip(hwnd uintptr, text string) {
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	nid.UFlags = nifTip
	tip, _ := syscall.UTF16FromString(text)
	copy(nid.SzTip[:], tip)
	procShellNotifyIconW.Call(0x00000001, uintptr(unsafe.Pointer(&nid))) // NIM_MODIFY
}

// CREATE_BREAKAWAY_FROM_JOB lets the spawned process escape our Job Object.
// The job is created with JOB_OBJECT_LIMIT_BREAKAWAY_OK so this is honoured.
const createBreakawayFromJob = 0x01000000

// restartAsAdmin relaunches the current exe elevated, then exits this
// (non-elevated) instance.
//
// The naive approach — ShellExecute "runas" then os.Exit(0) from inside this
// process — does not work here, and ShellExecuteEx/NOASYNC did not fix it:
// this process lives in a Job Object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
// so the instant we exit the kernel tears the whole group down. Anything the
// dying process started for the elevation (and the elevation RPC itself) dies
// with it, so the app just closed and nothing came back elevated.
//
// Fix: hand the relaunch to a detached PowerShell helper that has *broken
// away* from our job (CREATE_BREAKAWAY_FROM_JOB). Because it is no longer in
// the job, our os.Exit + KILL_ON_JOB_CLOSE cannot touch it. It waits for us
// to fully exit (freeing the HTTP port), then performs the UAC elevation
// itself via Start-Process -Verb RunAs. The prompt therefore comes from a
// healthy standalone process, not a process being torn down.
func restartAsAdmin() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)

	// Single-quote escaping for the PowerShell string literals.
	esc := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	script := "Start-Sleep -Milliseconds 900; " +
		"Start-Process -FilePath '" + esc(exe) + "'" +
		" -WorkingDirectory '" + esc(dir) + "' -Verb RunAs"

	// PowerShell -EncodedCommand expects base64 of the UTF-16LE script. This
	// sidesteps every quoting pitfall for exe paths with spaces/specials.
	u16 := utf16LE(script)
	enc := base64.StdEncoding.EncodeToString(u16)

	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-EncodedCommand", enc)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createBreakawayFromJob | createNoWindow,
	}
	if err := cmd.Start(); err != nil {
		// Helper could not be spawned — stay running rather than vanish.
		return
	}
	os.Exit(0)
}

// utf16LE encodes s as little-endian UTF-16 bytes (no BOM, no NUL
// terminator) for PowerShell's -EncodedCommand.
func utf16LE(s string) []byte {
	u, _ := windows.UTF16FromString(s)
	out := make([]byte, 0, len(u)*2)
	for _, c := range u {
		if c == 0 { // drop the terminating NUL UTF16FromString appends
			break
		}
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

// openExternalURL opens an http(s) link in the user's default browser via
// ShellExecute. Only http/https are allowed so a crafted value can't launch an
// arbitrary file or protocol handler.
func openExternalURL(u string) {
	u = strings.TrimSpace(u)
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return
	}
	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return
	}
	target, err := syscall.UTF16PtrFromString(u)
	if err != nil {
		return
	}
	// ShellExecuteW(NULL, "open", url, NULL, NULL, SW_SHOWNORMAL=1)
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(target)), 0, 0, 1)
}

// openStorageLocation opens the given directory in Explorer (used by the
// "Open storage location" button to reveal %LOCALAPPDATA%\vair). Explorer
// inherits its own visible window — we deliberately do NOT use the
// runHidden helper here, that would defeat the purpose. Errors are
// returned to the HTTP layer so the UI can show a notification.
func openStorageLocation(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("ensure %s: %w", dir, err)
	}
	cmd := exec.Command("explorer.exe", dir)
	// Detach from our Job Object so closing the Explorer window does not
	// take Vair down — and so Vair's KILL_ON_JOB_CLOSE does not snap
	// Explorer shut when the user closes Vair.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createBreakawayFromJob,
	}
	// Explorer.exe returns exit code 1 even when it successfully opened the
	// folder (long-standing Windows quirk). Run-and-forget; ignore the wait.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start explorer: %w", err)
	}
	go cmd.Wait() //nolint:errcheck — exit code is meaningless here
	return nil
}

// ── Native file picker (used by JS `_goPickFiles`) ───────────────────────────
//
// Uses the classic GetOpenFileNameW dialog (comdlg32) with multi-select.
// We pick the classic dialog over the modern IFileOpenDialog to keep the
// implementation in plain syscalls without COM plumbing — multi-select works
// fine for our purposes (text/config files).
//
// Multi-select result formatting: the buffer is zero-separated with the
// directory first, then each file name, ending with a double-NUL. If only
// one file is chosen, the buffer holds the single full path. We parse both
// shapes below.

const (
	ofnAllowMultiSelect = 0x00000200
	ofnExplorer         = 0x00080000
	ofnPathMustExist    = 0x00000800
	ofnFileMustExist    = 0x00001000
	ofnHideReadOnly     = 0x00000004
	ofnNoChangeDir      = 0x00000008
	ofnOverwritePrompt  = 0x00000002 // SaveAs: confirm before overwriting
)

type openFileNameW struct {
	StructSize    uint32
	Owner         uintptr
	Instance      uintptr
	Filter        *uint16
	CustomFilter  *uint16
	MaxCustFilter uint32
	FilterIndex   uint32
	File          *uint16
	MaxFile       uint32
	FileTitle     *uint16
	MaxFileTitle  uint32
	InitialDir    *uint16
	Title         *uint16
	Flags         uint32
	FileOffset    uint16
	FileExtension uint16
	DefExt        *uint16
	CustData      uintptr
	FnHook        uintptr
	TemplateName  *uint16
	PvReserved    uintptr
	DwReserved    uint32
	FlagsEx       uint32
}

// pickConfigFiles shows the file picker and returns the selected paths.
// Returns an empty slice if the user cancels or anything goes wrong.
func pickConfigFiles(ownerHwnd uintptr) []string {
	// 64 KiB buffer — enough for ~1000 selected files even with long paths.
	const bufLen = 64 * 1024
	buf := make([]uint16, bufLen)

	filter := utf16Z("Config files\x00*.txt;*.conf;*.list;*.json;*.yaml;*.yml\x00All files\x00*.*\x00")
	title := utf16Z("Select config file(s)")

	ofn := openFileNameW{
		Owner:   ownerHwnd,
		Filter:  &filter[0],
		File:    &buf[0],
		MaxFile: bufLen,
		Title:   &title[0],
		Flags:   ofnAllowMultiSelect | ofnExplorer | ofnPathMustExist | ofnFileMustExist | ofnHideReadOnly | ofnNoChangeDir,
	}
	ofn.StructSize = uint32(unsafe.Sizeof(ofn))

	ret, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		return nil // user cancelled, or error
	}

	// Parse: walk null-separated UTF-16 strings until we hit a double-NUL.
	var parts []string
	i := 0
	for i < bufLen {
		// Find the next NUL.
		j := i
		for j < bufLen && buf[j] != 0 {
			j++
		}
		if j == i {
			// Empty string => terminator (double-NUL).
			break
		}
		parts = append(parts, syscall.UTF16ToString(buf[i:j]))
		i = j + 1
	}
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		// Single-select form: the only entry is the full path.
		return parts
	}
	// Multi-select form: parts[0] is the directory, parts[1:] are file names.
	dir := parts[0]
	out := make([]string, 0, len(parts)-1)
	for _, name := range parts[1:] {
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// saveJSONFile shows the native Save As dialog seeded with suggestedName,
// writes content to the chosen path on confirm, and returns that path. An
// empty path with a nil error means the user cancelled. We pick GetSaveFile
// to match pickConfigFiles' classic-dialog style and avoid COM plumbing.
func saveJSONFile(ownerHwnd uintptr, suggestedName string, content []byte) (string, error) {
	// 32 KiB buffer for the chosen path — way more than any real path needs.
	const bufLen = 32 * 1024
	buf := make([]uint16, bufLen)
	if suggestedName == "" {
		suggestedName = "vair_settings.json"
	}
	// Pre-fill the file name with the suggestion. UTF-16 copy + NUL.
	for i, r := range suggestedName {
		if i >= bufLen-1 {
			break
		}
		buf[i] = uint16(r)
	}

	filter := utf16Z("JSON files\x00*.json\x00All files\x00*.*\x00")
	title := utf16Z("Save settings as")
	defExt := utf16Z("json")

	ofn := openFileNameW{
		Owner:   ownerHwnd,
		Filter:  &filter[0],
		File:    &buf[0],
		MaxFile: bufLen,
		Title:   &title[0],
		DefExt:  &defExt[0],
		Flags:   ofnExplorer | ofnOverwritePrompt | ofnHideReadOnly | ofnNoChangeDir,
	}
	ofn.StructSize = uint32(unsafe.Sizeof(ofn))

	ret, _, _ := procGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		return "", nil // user cancelled (or GetSaveFile failed silently)
	}
	path := syscall.UTF16ToString(buf)
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// utf16Z converts a Go string with embedded NULs into a NUL-terminated
// UTF-16 buffer suitable for the filter field (which uses NULs as
// internal separators).
func utf16Z(s string) []uint16 {
	out := make([]uint16, 0, len(s)+1)
	for _, r := range s {
		if r < 0x10000 {
			out = append(out, uint16(r))
		} else {
			r -= 0x10000
			out = append(out, 0xD800|uint16(r>>10))
			out = append(out, 0xDC00|uint16(r&0x3FF))
		}
	}
	out = append(out, 0)
	return out
}

// ── Process enumeration (used by JS `_goListProcesses`) ─────────────────────

const (
	th32csSnapProcess = 0x00000002
	maxPath           = 260
)

type processEntry32W struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [maxPath]uint16
}

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	procProcess32NextW           = kernel32.NewProc("Process32NextW")
	procCloseHandle              = kernel32.NewProc("CloseHandle")
)

// listRunningProcessNames returns a sorted, deduplicated list of executable
// names of currently running processes (e.g. "chrome.exe"). Names are
// lowercased so the chip-input's case-insensitive matching stays consistent.
func listRunningProcessNames() []string {
	hSnap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	// INVALID_HANDLE_VALUE is -1 on x64; on a uintptr that's all-bits-set.
	if hSnap == 0 || hSnap == ^uintptr(0) {
		return nil
	}
	defer procCloseHandle.Call(hSnap)

	var entry processEntry32W
	entry.Size = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32FirstW.Call(hSnap, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return nil
	}

	seen := make(map[string]bool, 256)
	for {
		name := syscall.UTF16ToString(entry.ExeFile[:])
		if name != "" {
			lower := strings.ToLower(name)
			seen[lower] = true
		}
		ret, _, _ := procProcess32NextW.Call(hSnap, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}

	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ── Process grouping (Job Object + AppUserModelID) ──────────────────────────
//
// Goal: everything Vair launches shows up *as part of Vair* in Task Manager
// and dies with Vair — including the embedded WebView2 runtime, which used
// to appear as a separate top-level "Microsoft Edge WebView2" entry.
//
// Two levers:
//   1. A Job Object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE. Child processes
//      inherit job membership by default, so once the current process is in
//      the job every later child — xray.exe, sing-box.exe and WebView2's
//      msedgewebview2.exe helper processes — is captured. Task Manager then
//      shows them within Vair's process group, and when Vair exits (even via
//      "End task") the job's last handle closes and the OS tears the whole
//      group down. No orphaned WebView2/xray/sing-box left behind.
//   2. An explicit AppUserModelID so the shell/Task Manager treats every
//      Vair window and child under one stable app identity.

var procSetCurrentProcessExplicitAppUserModelID = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")

// jobHandle is intentionally kept open for the whole process lifetime.
// KILL_ON_JOB_CLOSE fires when the last handle closes — we want that to
// happen at (and only at) process exit, which the OS does for us.
var jobHandle windows.Handle

func bindChildrenToJob() {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil || h == 0 {
		return
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	// KILL_ON_JOB_CLOSE: WebView2/xray/sing-box children (created without the
	// breakaway flag) stay in the job and die with Vair — no orphans.
	// BREAKAWAY_OK: but a child created with CREATE_BREAKAWAY_FROM_JOB (only
	// the elevated-relaunch helper) may escape, so it survives our exit.
	info.BasicLimitInformation.LimitFlags =
		windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(h)
		return
	}
	cur, err := windows.GetCurrentProcess()
	if err != nil {
		windows.CloseHandle(h)
		return
	}
	// On Windows 8+ a process may belong to nested jobs, so this succeeds
	// even when Vair was itself launched inside someone else's job. On
	// legacy Windows it can fail with ACCESS_DENIED — we degrade silently
	// (behaviour identical to before this change).
	if err := windows.AssignProcessToJobObject(h, cur); err != nil {
		windows.CloseHandle(h)
		return
	}
	jobHandle = h // kept alive deliberately; do not close before exit
}

func setAppUserModelID() {
	idW, err := windows.UTF16PtrFromString("Vair.App")
	if err != nil {
		return
	}
	procSetCurrentProcessExplicitAppUserModelID.Call(uintptr(unsafe.Pointer(idW))) //nolint:errcheck
}

func standaloneMain() {
	// Launched at logon via the Run key? Start minimized / to tray instead of
	// stealing focus. (The Run-key command appends --autostart.) Also pick up a
	// vair:// deep-link passed as an argument.
	var deepLinkArg string
	for _, a := range os.Args[1:] {
		if a == "--autostart" {
			startMinimized = true
		} else if strings.HasPrefix(strings.ToLower(a), "vair://") {
			deepLinkArg = a
		}
	}
	// If a deep-link was passed and an instance is already running, hand it over
	// and exit — don't start a duplicate. Otherwise we're the one instance and
	// will import it once our UI connects (pendingDeepLink → handleSSE).
	if deepLinkArg != "" {
		if forwardDeepLink(deepLinkArg) {
			os.Exit(0)
		}
		pendingDeepLink = parseDeepLink(deepLinkArg)
	}
	// Must run before any child process is spawned (extractBinaries →
	// prewarmBinary launches `xray version`), so every descendant lands
	// inside the job and is grouped with / cleaned up alongside Vair.
	bindChildrenToJob()
	setAppUserModelID()

	fresh, extractErr := extractBinaries()
	if extractErr != nil {
		fmt.Fprintf(os.Stderr, "Startup error: %v\n", extractErr)
		os.Exit(1)
	}
	if fresh {
		prewarmBinary(state.xrayBin)
		prewarmBinary(state.singboxBin)
	}
	if !checkAdmin() {
		fmt.Println("!! Run as Administrator to enable TUN mode.")
	}
	// Remove any leftover <exe>.new / .old from a previous self-update.
	cleanupUpdateLeftovers()
	// Recover from a dirty shutdown: if the last session left the Windows
	// system proxy enabled (pointed at a now-dead localhost port), internet
	// would be broken until cleared. Do this before anything else network.
	clearStaleProxy()
	// Sweep any TUN adapters orphaned by a previous crash/kill. A clean
	// disconnect already removes its own adapter (and the teardown glob also
	// catches ghosts on the next TUN disconnect), so this is just hygiene for
	// the crash case — now that the per-connect cleanup was dropped to speed up
	// TUN connects. Backgrounded so it never delays launch; a real TUN connect
	// can't happen for seconds (entries must load / the window must come up),
	// long after this finishes.
	go removeTUNAdapter()
	registerRoutes()
	migrateDataLayout() // move legacy flat files into data/ + runtime/ (one-time)
	if s, err := openConfigStore(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ config store: %v\n", err)
	} else {
		store = s
		go s.resultFlusher() // periodically flush batched test results
	}
	loadTabs()
	// Sweep DB rows of tabs that no longer exist (a tab delete whose background
	// row-cleanup didn't finish before a prior exit).
	if store != nil {
		state.mu.RLock()
		ids := make([]string, 0, len(state.tabs))
		for _, t := range state.tabs {
			ids = append(ids, t.ID)
		}
		state.mu.RUnlock()
		store.sweepOrphanTabRows(ids)
	}
	loadConfigsIntoMemory() // populate the in-memory working copy from SQLite
	// Rewrite tabs.json without the legacy embedded "configs" (older builds saved
	// them there; they're now in configs.db). Shrinks a multi-MB legacy file and
	// speeds every future startup. Safe: the store is authoritative + already loaded.
	saveTabs()
	loadSettings()
	// Register the vair:// scheme if enabled (default on) so deep links work.
	if deepLinkEnabled() {
		if err := registerDeepLink(true); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ registerDeepLink: %v\n", err)
		}
	}
	// Self-heal the logon Run key: the path captured when the user enabled
	// autostart goes stale if they move/rename the exe or run a differently-named
	// build (a self-update replaces in place, so that path stays valid). Re-point
	// it at THIS exe every startup so "launch at logon" keeps working.
	if autostartEnabled() {
		if err := applyAutostart(true); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ autostart refresh: %v\n", err)
		}
	}
	// Auto-connect now implies connect-on-startup (the separate toggle was
	// removed): if the feature is on, arm intent so the supervisor connects to
	// the fastest working config once entries load.
	if appSettings.AutoConnect {
		autoWant.Store(true)
	}
	go startAutoSupervisor()
	go func() {
		if err := httpListenAndServe(); err != nil {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
			os.Exit(1)
		}
	}()
	go startAutoRefresh()
	go fetchAndInit()
	openNativeWindow(fmt.Sprintf("http://localhost:%d", webPort))
}
