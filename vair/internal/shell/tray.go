package shell

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"

	"vair/core"
)

// ── System tray menu (state-aware, ported from 1.10 showTrayMenu) ───────────
// The menu reflects the live connection / auto / block state, and so do the
// tooltip and a dot on the icon. With the window hidden the tray is all there
// is to look at: it used to show nothing while a connection was being made
// (and offered no way to stop it), nothing about a kill-switch block holding
// the machine offline, and "Vair" as its tooltip whatever was going on.
// Rebuilt on conn/auto changes (RefreshTrayMenu via the emitter). Labels
// follow the app language.
var (
	theTray     *application.SystemTray
	trayMenuSig string
	trayIconRaw []byte
	trayIcons   = map[string][]byte{} // state → the icon with its dot, built once
)

// trayT picks the label for the current app language (Russian when settings say
// so, English otherwise — matching the rest of the UI's i18n default).
func trayT(en, ru string) string {
	if core.GetSettings().Language == "ru" {
		return ru
	}
	return en
}

// trayState names what the tray shows: "connected", "connecting" (which
// includes the check that a new session carries traffic), "disconnecting",
// "blocked" (a kill-switch block with nothing connected), or "" for idle.
func trayState(cs core.ConnState) string {
	switch {
	case cs.Status == core.ConnConnected && !cs.Checking:
		return "connected"
	case cs.Status == core.ConnConnecting || (cs.Status == core.ConnConnected && cs.Checking):
		return "connecting"
	case cs.Status == core.ConnDisconnecting:
		return "disconnecting"
	case cs.Blocked:
		return "blocked"
	}
	return ""
}

func trayModeName(cs core.ConnState) string {
	if cs.Mode == core.ModeTUN {
		return "TUN"
	}
	return "Proxy"
}

// trayEntryName is what the session is, as the bar says it: a chain by its hops.
func trayEntryName(cs core.ConnState) string {
	if len(cs.Chain) > 1 {
		return strings.Join(cs.Chain, " → ")
	}
	return cs.EntryName
}

func buildTrayMenu() *application.Menu {
	m := theApp.NewMenu()
	cs := core.ConnSnapshot()
	st := trayState(cs)
	autoOn := core.AutoEnabled()

	switch st {
	case "connected":
		m.Add(fmt.Sprintf("● %s [%s]", trayEntryName(cs), trayModeName(cs))).SetEnabled(false)
	case "connecting":
		m.Add("◌ " + trayT("Connecting: ", "Подключение: ") + trayEntryName(cs)).SetEnabled(false)
	case "disconnecting":
		m.Add("◌ " + trayT("Disconnecting…", "Отключение…")).SetEnabled(false)
	case "blocked":
		m.Add("⛔ " + trayT("Traffic blocked (kill-switch)", "Трафик заблокирован (kill-switch)")).SetEnabled(false)
	}
	if autoOn {
		m.Add("⟳ " + trayT("Auto-connect: ON", "Авто-подключение: ВКЛ")).SetEnabled(false)
	}
	if st != "" || autoOn {
		m.AddSeparator()
	}
	m.Add(trayT("Show Vair", "Показать Vair")).OnClick(func(_ *application.Context) { ShowMain() })
	if autoOn {
		m.Add(trayT("Switch now", "Переключить сейчас")).OnClick(func(_ *application.Context) { core.AutoSwitch() })
	}
	switch st {
	case "connected":
		m.Add(trayT("Disconnect", "Отключить")).OnClick(func(_ *application.Context) { core.Disconnect() })
	case "connecting":
		m.Add(trayT("Cancel", "Отменить")).OnClick(func(_ *application.Context) { core.Disconnect() })
	case "blocked":
		// The same as the red chip on the bar. Opening the firewall can take a
		// second or more, so it does not hold up the menu.
		m.Add(trayT("Release the block", "Снять блокировку")).OnClick(func(_ *application.Context) {
			go core.ForceReleaseBlock()
		})
	}
	m.AddSeparator()
	m.Add(trayT("Exit", "Выход")).OnClick(func(_ *application.Context) {
		core.Shutdown() // stop any connection + clear system proxy before exit
		theApp.Quit()
	})
	return m
}

// trayTooltip says the state in words, for a hover over the icon.
func trayTooltip(cs core.ConnState, st string) string {
	switch st {
	case "connected":
		return "Vair — " + trayT("connected: ", "подключено: ") + trayEntryName(cs) + " (" + trayModeName(cs) + ")"
	case "connecting":
		return "Vair — " + trayT("connecting: ", "подключение: ") + trayEntryName(cs)
	case "disconnecting":
		return "Vair — " + trayT("disconnecting", "отключение")
	case "blocked":
		return "Vair — " + trayT("traffic blocked (kill-switch)", "трафик заблокирован (kill-switch)")
	}
	return "Vair — " + trayT("not connected", "не подключено")
}

// RefreshTrayMenu rebuilds the tray menu, tooltip and icon when the state that
// shapes them actually changed (cheap signature guard, since auto_update fires
// often).
func RefreshTrayMenu() {
	if theTray == nil {
		return
	}
	cs := core.ConnSnapshot()
	st := trayState(cs)
	sig := fmt.Sprintf("%s|%s|%s|%v|%s",
		st, trayEntryName(cs), cs.Mode, core.AutoEnabled(), core.GetSettings().Language)
	if sig == trayMenuSig {
		return
	}
	trayMenuSig = sig
	theTray.SetMenu(buildTrayMenu())
	theTray.SetTooltip(trayTooltip(cs, st))
	if icon := trayIconFor(st); icon != nil {
		theTray.SetIcon(icon)
	}
}

// trayIconFor is the tray icon with a dot in its corner for the state: green
// connected, amber on the way, red blocked; the plain icon when idle.
func trayIconFor(st string) []byte {
	if trayIconRaw == nil {
		return nil
	}
	dot := map[string]color.RGBA{
		"connected":     {63, 185, 80, 255},
		"connecting":    {210, 153, 34, 255},
		"disconnecting": {210, 153, 34, 255},
		"blocked":       {248, 81, 73, 255},
	}
	c, ok := dot[st]
	if !ok {
		return trayIconRaw
	}
	if b, ok := trayIcons[st]; ok {
		return b
	}
	b := withDot(trayIconRaw, c)
	trayIcons[st] = b
	return b
}

// withDot draws a filled circle, ringed in dark so it reads on any taskbar, in
// the lower right of a PNG icon. The plain icon comes back if anything fails.
func withDot(icon []byte, c color.RGBA) []byte {
	src, err := png.Decode(bytes.NewReader(icon))
	if err != nil {
		return icon
	}
	b := src.Bounds()
	img := image.NewRGBA(b)
	draw.Draw(img, b, src, b.Min, draw.Src)
	w := float64(b.Dx())
	r := w * 0.22
	cx, cy := float64(b.Max.X)-r-1, float64(b.Max.Y)-r-1
	ring := color.RGBA{20, 20, 20, 255}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			d := math.Sqrt(dx*dx + dy*dy)
			switch {
			case d <= r-1.2:
				img.SetRGBA(x, y, c)
			case d <= r+0.4:
				img.SetRGBA(x, y, ring)
			}
		}
	}
	var out bytes.Buffer
	if png.Encode(&out, img) != nil {
		return icon
	}
	return out.Bytes()
}

// CreateTray installs the system-tray icon and its menu.
//
// Only called where something will display it: registering an icon into a
// desktop with no StatusNotifierItem host produces a log line nobody reads and
// an application the user cannot reach.
func CreateTray(icon []byte) {
	if theApp == nil {
		return
	}
	trayIconRaw = icon
	theTray = theApp.SystemTray.New()
	theTray.SetTooltip("Vair")
	theTray.SetIcon(icon)
	theTray.OnClick(func() { ShowMain() })
	RefreshTrayMenu() // build the initial (state-aware) menu
}

// pendingDeepLink holds a vair:// URL the app was LAUNCHED with until the
// frontend mounts and pulls it (SettingsService.TakePendingDeepLink) — the
// frontend asks rather than being told, so there is no startup race.
var pendingDeepLink atomic.Value

// SetPendingDeepLink stores a link the app was started with.
func SetPendingDeepLink(url string) { pendingDeepLink.Store(url) }

// TakePendingDeepLink returns that link once and forgets it.
func TakePendingDeepLink() string {
	if v := pendingDeepLink.Swap(""); v != nil {
		if dl, ok := v.(string); ok {
			return dl
		}
	}
	return ""
}
