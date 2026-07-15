//go:build windows

package main

import (
	_ "embed"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

var (
	winUser32                 = syscall.NewLazyDLL("user32.dll")
	procSystemParametersInfoW = winUser32.NewProc("SystemParametersInfoW")
	procGetDpiForSystem       = winUser32.NewProc("GetDpiForSystem")
	procLoadImageW            = winUser32.NewProc("LoadImageW")
	procSendMessageW          = winUser32.NewProc("SendMessageW")
	procGetSystemMetricsW     = winUser32.NewProc("GetSystemMetrics")
)

const spiGetWorkArea = 0x0030

// windowIcon is the multi-size app icon (the 1.10 icon.ico, built from
// icon2.png). Extracted to the data dir at startup so setWindowIcon can load
// the exact pixel sizes Windows asks for — same pipeline as 1.10.
//
//go:embed build/windows/icon.ico
var windowIcon []byte

const (
	wmSeticon      = 0x0080
	iconSmall      = 0
	iconBig        = 1
	imageIcon      = 1
	lrLoadfromfile = 0x0010
	smCxicon       = 11 // SM_CXICON  — large-icon width, DPI-scaled (32 @100%, 48 @150%)
	smCxsmicon     = 49 // SM_CXSMICON — small-icon width (16 @100%, 20 @125%)
)

// applyWindowIcon waits for the native window to exist, then sets the
// window/taskbar/alt-tab icons. ICON_BIG is requested at SM_CXICON — the size
// the shell ACTUALLY draws the taskbar/Alt-Tab icon at for the current DPI (32px
// @100%, 48px @150%). That matches an exact entry in the .ico so the shell paints
// it 1:1 instead of down-scaling a fixed 48px source to 32px (the soft/blurry
// look). ICON_SMALL uses SM_CXSMICON (16px @100%, 20px @125%).
func applyWindowIcon() {
	// Write the embedded .ico next to the engines so LoadImageW can read it.
	iconPath := filepath.Join(os.Getenv("LOCALAPPDATA"), "vair", "bin", "icon.ico")
	_ = os.MkdirAll(filepath.Dir(iconPath), 0o755)
	_ = os.WriteFile(iconPath, windowIcon, 0o644)
	pathW, err := syscall.UTF16PtrFromString(iconPath)
	if err != nil {
		return
	}

	// The hwnd only exists once app.Run() has created the window — poll briefly.
	var hwnd uintptr
	for i := 0; i < 100; i++ {
		if mainWindow != nil {
			if p := mainWindow.NativeWindow(); p != nil {
				hwnd = uintptr(p)
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if hwnd == 0 {
		return
	}

	bigSz, _, _ := procGetSystemMetricsW.Call(smCxicon)
	if bigSz == 0 {
		bigSz = 32
	}
	smallSz, _, _ := procGetSystemMetricsW.Call(smCxsmicon)
	if smallSz == 0 {
		smallSz = 16
	}
	hBig, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(pathW)), imageIcon, bigSz, bigSz, lrLoadfromfile)
	hSmall, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(pathW)), imageIcon, smallSz, smallSz, lrLoadfromfile)
	if hBig != 0 {
		procSendMessageW.Call(hwnd, wmSeticon, iconBig, hBig)
	}
	if hSmall != 0 {
		procSendMessageW.Call(hwnd, wmSeticon, iconSmall, hSmall)
	}

}

type winRect struct{ Left, Top, Right, Bottom int32 }

// initialWindowSize returns the default window size in Wails DIPs: 80% of the
// primary monitor's work area (the historical default — the WindowSizePct
// setting was retired in 2.0), floored at 980×620, capped at the work area
// itself (a small laptop must never get a window pushed past its edges). Wails
// centers the window on the work area.
func initialWindowSize() (int, int) {
	// Physical work area of the primary monitor (excludes the taskbar).
	var r winRect
	workW, workH := int32(1920), int32(1040)
	if ret, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0); ret != 0 {
		workW, workH = r.Right-r.Left, r.Bottom-r.Top
	}
	// Wails v3 window sizes are DIPs (it scales by the screen DPI itself), so
	// convert the physical work area to DIPs first.
	if dpi, _, _ := procGetDpiForSystem.Call(); dpi > 0 {
		workW = workW * 96 / int32(dpi)
		workH = workH * 96 / int32(dpi)
	}
	w, h := idealWindowSize(workW, workH, 80)
	return int(w), int(h)
}

// idealWindowSize is the exact 1.10 sizing rule (standalone_windows.go).
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
