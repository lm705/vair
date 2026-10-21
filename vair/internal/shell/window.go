package shell

// Window sizing that is pure arithmetic and identical on every platform.
// Only "how big is the usable screen" differs, and that lives in
// window_{windows,linux}.go.

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

// PanelWindowSize is the size for a secondary window that has a natural size of
// its own — the AUTO panel — never larger than the screen it has to fit on.
//
// The panel used to be created at a flat 620×760. On a 1366×768 laptop, or any
// machine whose taskbar leaves less than that, the window opened taller than
// the desktop: its own close button sat below the edge of the screen. There is
// no floor here on purpose — a panel smaller than it would like to be scrolls,
// which is what it already does when the settings inside it are expanded.
func PanelWindowSize(wantW, wantH int) (int, int) {
	workW, workH := workAreaDIP()
	// A little back from the very edge: a window exactly the height of the work
	// area looks like it is stuck to the taskbar, and on Windows the shadow is
	// then clipped.
	const margin = 40
	if int32(wantW) > workW-margin {
		wantW = int(workW - margin)
	}
	if int32(wantH) > workH-margin {
		wantH = int(workH - margin)
	}
	if wantW < 320 {
		wantW = 320
	}
	if wantH < 320 {
		wantH = 320
	}
	return wantW, wantH
}
