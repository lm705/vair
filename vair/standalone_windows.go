//go:build windows

package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
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
		{"xray.exe",    embeddedXray,    0755},
		{"sing-box.exe",embeddedSingbox, 0755},
		{"geoip.dat",   embeddedGeoIP,   0644},
		{"geosite.dat", embeddedGeosite, 0644},
		{"icon.ico",    embeddedIcon,    0644},
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
	state.xrayBin    = filepath.Join(dir, "xray.exe")
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
	user32  = windows.NewLazySystemDLL("user32.dll")
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")

	procGetWindowLongW              = user32.NewProc("GetWindowLongW")
	procSetWindowLongW              = user32.NewProc("SetWindowLongW")
	procSetWindowLongPtrW           = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW             = user32.NewProc("CallWindowProcW")
	procSetWindowPos                = user32.NewProc("SetWindowPos")
	procShowWindow                  = user32.NewProc("ShowWindow")
	procIsZoomed                    = user32.NewProc("IsZoomed")
	procSendMessageW                = user32.NewProc("SendMessageW")
	procReleaseCapture              = user32.NewProc("ReleaseCapture")
	procGetSystemMetrics            = user32.NewProc("GetSystemMetrics")
	procGetWindowRect               = user32.NewProc("GetWindowRect")
	procSystemParametersInfoW      = user32.NewProc("SystemParametersInfoW")
	procLoadImageW                  = user32.NewProc("LoadImageW")
	procPostMessageW                = user32.NewProc("PostMessageW")
	procDwmExtendFrameIntoClientArea = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
	procSetForegroundWindow          = user32.NewProc("SetForegroundWindow")
	procBringWindowToTop             = user32.NewProc("BringWindowToTop")
	procIsWindowVisible              = user32.NewProc("IsWindowVisible")
	procDwmSetWindowAttribute       = dwmapi.NewProc("DwmSetWindowAttribute")
	procEnumChildWindows            = user32.NewProc("EnumChildWindows")
	procCreatePopupMenu             = user32.NewProc("CreatePopupMenu")
	procInsertMenuW                 = user32.NewProc("InsertMenuW")
	procTrackPopupMenu              = user32.NewProc("TrackPopupMenu")
	procDestroyMenu                 = user32.NewProc("DestroyMenu")
	procGetCursorPos                = user32.NewProc("GetCursorPos")

	shell32                        = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIconW           = shell32.NewProc("Shell_NotifyIconW")
)

type dwmMargins struct{ Left, Right, Top, Bottom int32 }

// Win32 structs for WM_GETMINMAXINFO
type winPoint struct{ X, Y int32 }
type winRect  struct{ Left, Top, Right, Bottom int32 }
type minMaxInfo struct {
	Reserved    winPoint
	MaxSize     winPoint // maximized window size
	MaxPosition winPoint // maximized window position
	MinTrackSize winPoint
	MaxTrackSize winPoint
}

// GWL_STYLE = -16, GWL_EXSTYLE = -20, GWLP_WNDPROC = -4
// Must be int32 vars — uintptr can't hold negative constants at compile time.
var (
	gwlStyleVal  int32 = -16
	gwlExStyle   int32 = -20
	gwlpWndproc  int32 = -4
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

	wmNclbuttondown = 0x00A1
	wmSysCommand    = 0x0112
	wmSeticon       = 0x0080
	wmClose         = 0x0010
	scMinimize      = 0xF020
	wmApp           = 0x8000 // WM_APP — used for tray icon callback
	wmLbuttonup     = 0x0202
	wmRbuttonup     = 0x0205

	htCaption     = 2
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

	iconSmall     = 0
	iconBig       = 1
	imageIcon     = 1
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
	resizeParentHWND uintptr          // set once in addChildSubclassing
	childOrigProcs   sync.Map         // hwnd (uintptr) → original WndProc (uintptr)
	childWndProcCB   uintptr          // single stable callback for all children
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
	cb := syscall.NewCallback(func(child, _ uintptr) uintptr {
		gwlp := uintptr(uint32(gwlpWndproc))
		// Subclass the child: replace its WndProc with ours
		orig, _, _ := procSetWindowLongPtrW.Call(child, gwlp, childWndProcCB)
		if orig != 0 && orig != childWndProcCB {
			childOrigProcs.Store(child, orig)
		}
		return 1 // continue enumeration
	})
	procEnumChildWindows.Call(parentHWND, cb, 0)
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
			left   := cx < wr.Left  + border
			right  := cx >= wr.Right - border
			top    := cy < wr.Top   + border
			bottom := cy >= wr.Bottom - border
			switch {
			case top && left:  return htTopLeft
			case top && right: return htTopRight
			case bottom && left:  return htBottomLeft
			case bottom && right: return htBottomRight
			case top:    return htTop
			case bottom: return htBottom
			case left:   return htLeft
			case right:  return htRight
			}
			// Anything else: let the default proc decide (htClient for the content area)
			ret, _, _ := procCallWindowProcW.Call(origWndProc, hwnd, msg, wParam, lParam)
			return ret

		case wmGetMinMaxInfo:
			// Limit maximized window to the work area so it never covers the taskbar.
			if lParam != 0 {
				var workArea winRect
				procSystemParametersInfoW.Call(spiGetWorkArea, 0,
					uintptr(unsafe.Pointer(&workArea)), 0)
				mmi := (*minMaxInfo)(unsafe.Pointer(lParam))
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
	gwl   := uintptr(uint32(gwlStyleVal))
	gwlEx := uintptr(uint32(gwlExStyle))
	gwlp  := uintptr(uint32(gwlpWndproc))

	// ── 1. Strip title bar, keep resize/minimize/maximize behaviour ──────────
	style, _, _ := procGetWindowLongW.Call(hwnd, gwl)
	style &^= wsCaption | wsSysMenu
	style |=  wsThickFrame | wsMinimizeBox | wsMaximizeBox
	procSetWindowLongW.Call(hwnd, gwl, style)

	// ── 2. Ensure the taskbar button is always visible ────────────────────────
	exStyle, _, _ := procGetWindowLongW.Call(hwnd, gwlEx)
	exStyle |=  wsExAppWindow
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
//   SM_CXICON  (index 11): large icon size — used for WM_SETICON ICON_BIG (taskbar)
//   SM_CXSMICON (index 49): small icon size — used for WM_SETICON ICON_SMALL
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
	bigSz   := uintptr(48)
	smallSz, _, _ := procGetSystemMetrics.Call(49) // SM_CXSMICON (16 at 100%, 20 at 125%)
	if smallSz == 0 { smallSz = 16 }

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

	// Scale window to ~80% of screen, capped at 1440x920
	screenW, _, _ := procGetSystemMetrics.Call(0)  // SM_CXSCREEN
	screenH, _, _ := procGetSystemMetrics.Call(1)  // SM_CYSCREEN
	winW := int(screenW) * 80 / 100
	winH := int(screenH) * 80 / 100
	if winW > 1440 { winW = 1440 }
	if winH > 920 { winH = 920 }
	if winW < 900 { winW = 900 }
	if winH < 600 { winH = 600 }

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
			tip := "Vair"
			if cs.Status == ConnConnected {
				mode := "Proxy"
				if cs.Mode == ModeTUN {
					mode = "TUN"
				}
				tip = fmt.Sprintf("Vair — %s [%s]", cs.EntryName, mode)
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
			return false
		}
		procShowWindow.Call(hwnd, swMaximize)
		return true
	})
	w.Bind("_goWinIsMaximized", func() bool {
		zoomed, _, _ := procIsZoomed.Call(hwnd)
		return zoomed != 0
	})
	w.Bind("_goWinDragStart", func() {
		procReleaseCapture.Call()
		procSendMessageW.Call(hwnd, wmNclbuttondown, htCaption, 0)
	})
	w.Bind("_goLogoBase64", func() string { return logoBase64 })

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
	nimAdd    = 0x00000000
	nimDelete = 0x00000002
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

	menuIdx := uint32(0)

	if isConnected {
		// Show connected config name (greyed, informational)
		mode := "Proxy"
		if cs.Mode == ModeTUN {
			mode = "TUN"
		}
		label := fmt.Sprintf("● %s [%s]", cs.EntryName, mode)
		labelW, _ := syscall.UTF16PtrFromString(label)
		procInsertMenuW.Call(hMenu, uintptr(menuIdx), mfString|mfGreyed, 0, uintptr(unsafe.Pointer(labelW)))
		menuIdx++
		procInsertMenuW.Call(hMenu, uintptr(menuIdx), mfSeparator, 0, 0)
		menuIdx++
	}

	showW, _ := syscall.UTF16PtrFromString("Show")
	procInsertMenuW.Call(hMenu, uintptr(menuIdx), mfString, 1001, uintptr(unsafe.Pointer(showW)))
	menuIdx++

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
		go func() {
			stopConnection()
			// Update tray tooltip
			updateTrayTooltip(hwnd, "Vair")
		}()
	case 1003:
		removeTrayIcon(hwnd)
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
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

// restartAsAdmin relaunches the current exe with admin privileges via UAC prompt
func restartAsAdmin() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	verbW, _ := syscall.UTF16PtrFromString("runas")
	exeW, _ := syscall.UTF16PtrFromString(exe)
	dirW, _ := syscall.UTF16PtrFromString(filepath.Dir(exe))
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verbW)),
		uintptr(unsafe.Pointer(exeW)), 0, uintptr(unsafe.Pointer(dirW)), 1) // SW_SHOWNORMAL=1
	os.Exit(0)
}

var (
	procShellExecuteW = shell32.NewProc("ShellExecuteW")
)

func standaloneMain() {
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
	registerRoutes()
	loadTabs()
	loadSettings()
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
