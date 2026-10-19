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
