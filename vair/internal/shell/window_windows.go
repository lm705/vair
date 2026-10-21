//go:build windows

package shell

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
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
// Handed in by main rather than embedded here: the .ico lives in build/windows/
// and //go:embed cannot name a path outside its own directory. Same seam as the
// engine payload — see embed_windows.go.
var windowIcon []byte

// SetWindowIcon gives this package the .ico to install on the native window.
func SetWindowIcon(ico []byte) { windowIcon = ico }

const (
	wmSeticon      = 0x0080
	iconSmall      = 0
	iconBig        = 1
	imageIcon      = 1
	lrLoadfromfile = 0x0010
	smCxicon       = 11 // SM_CXICON  — large-icon width, DPI-scaled (32 @100%, 48 @150%)
	smCxsmicon     = 49 // SM_CXSMICON — small-icon width (16 @100%, 20 @125%)
)

// ApplyWindowIcon waits for the native window to exist, then sets the
// window/taskbar/alt-tab icons. ICON_BIG is requested at SM_CXICON — the size
// the shell ACTUALLY draws the taskbar/Alt-Tab icon at for the current DPI (32px
// @100%, 48px @150%). That matches an exact entry in the .ico so the shell paints
// it 1:1 instead of down-scaling a fixed 48px source to 32px (the soft/blurry
// look). ICON_SMALL uses SM_CXSMICON (16px @100%, 20px @125%).
func ApplyWindowIcon() {
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

// InitialWindowSize returns the default window size in Wails DIPs: 80% of the
// primary monitor's work area (the historical default — the WindowSizePct
// setting was retired in 2.0), floored at 980×620, capped at the work area
// itself (a small laptop must never get a window pushed past its edges). Wails
// centers the window on the work area.
func InitialWindowSize() (int, int) {
	workW, workH := workAreaDIP()
	w, h := idealWindowSize(workW, workH, 80)
	return int(w), int(h)
}

// workAreaDIP is the primary monitor's usable area in Wails DIPs — what any
// window has to fit inside.
func workAreaDIP() (int32, int32) {
	// Physical work area of the primary monitor (excludes the taskbar).
	var r winRect
	workW, workH := int32(1920), int32(1040)
	if ret, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0); ret != 0 {
		workW, workH = r.Right-r.Left, r.Bottom-r.Top
	}
	// Wails v3 window sizes are DIPs (it scales by the screen DPI itself), so
	// convert the physical work area to DIPs first.
	//
	// Find() before Call(): GetDpiForSystem arrived in Windows 10 1607, and
	// LazyProc.Call panics when the procedure is not there — so on anything older
	// the program died at startup, before its first window, with a stack trace
	// nobody would see (it is a GUI binary; there is no console to print to).
	// Measured on Windows 10 build 10240. Below 1607 the work area stays in
	// physical pixels, which is exactly right for the 96-DPI displays those
	// machines have.
	if procGetDpiForSystem.Find() == nil {
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi > 0 {
			workW = workW * 96 / int32(dpi)
			workH = workH * 96 / int32(dpi)
		}
	}
	return workW, workH
}

// RaiseWindow brings the window back from the tray or the taskbar. Show covers
// the hidden case, Restore the minimised one, Focus the merely-obscured one;
// each is a no-op when it does not apply.
func RaiseWindow(w *application.WebviewWindow) {
	w.Show()
	w.Restore()
	w.Focus()
}

// WebviewDataPath gives Vair its own WebView2 user-data folder under the app's
// data root (%LOCALAPPDATA%\Vair). A dedicated folder (rather than the Wails
// default %APPDATA%\<BinaryName.exe>) keeps the WebView2 cache next to the rest
// of the app's data. It holds only browser cache — no user data to migrate.
func WebviewDataPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	p := filepath.Join(base, "Vair", "WebView2")
	_ = os.MkdirAll(p, 0o755)
	return p
}
