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
)

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
// ICON_BIG (256x256) is used by taskbar buttons and Alt+Tab thumbnail.
// ICON_SMALL (32x32) is used for notification area and legacy title bars.
// setWindowIcon loads icon.ico and sets it as the window/taskbar/alt-tab icon.
// Windows selects icon size based on DPI:
//
//	SM_CXICON  (index 11): large icon size — used for WM_SETICON ICON_BIG (taskbar)
//	SM_CXSMICON (index 49): small icon size — used for WM_SETICON ICON_SMALL
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

// ── Logo base64 (32×32 PNG of the app logo, injected into the title bar) ─────
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
	setupWindow(hwnd)
	setWindowIcon(hwnd)

	// Bring window to foreground so it appears on top (not behind other windows)
	procSetForegroundWindow.Call(hwnd)
	procBringWindowToTop.Call(hwnd)

	// Show tray icon if enabled
	settingsMu.RLock()
	trayOn := appSettings.TrayEnabled
	settingsMu.RUnlock()
	if trayOn {
		addTrayIcon(hwnd, filepath.Join(binDir, "icon.ico"))
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
	// Recover from a dirty shutdown: if the last session left the Windows
	// system proxy enabled (pointed at a now-dead localhost port), internet
	// would be broken until cleared. Do this before anything else network.
	clearStaleProxy()
	registerRoutes()
	loadTabs()
	loadSettings()
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
