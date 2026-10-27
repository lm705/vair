package core

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows/registry"
)

// Desktop notifications, Windows.
//
// The one thing worth interrupting someone for is a tunnel that stopped
// carrying traffic while the window was minimised to the tray — which is
// exactly when nothing on screen can say so.
//
// Done through PowerShell and the WinRT toast API rather than a toast library.
// The libraries register a COM activator and a CLSID under HKCU so that
// clicking the toast can start the app; that machinery would run our executable
// again on a click, and a second instance is a worse problem than a missed
// click. Nothing here is registered and nothing is left behind.

// notifyScript builds the script with the text already in it.
//
// Passed as one -EncodedCommand rather than as arguments, for two reasons: a
// script given to -Command cannot take parameters at all (PowerShell appends
// the extra arguments to the command line instead of binding them, which is how
// this arrived silently broken the first time), and the encoding is UTF-16, so
// a Russian message survives a console code page that would otherwise mangle
// it.
func notifyScript(title, body string) string {
	// Single-quoted PowerShell literals: nothing inside is expanded, and the
	// only escape needed is a doubled quote.
	q := func(s string) string {
		s = strings.ReplaceAll(s, "\n", " ")
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return fmt.Sprintf(`try{
[void][Windows.UI.Notifications.ToastNotificationManager,Windows.UI.Notifications,ContentType=WindowsRuntime]
$x=[Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$t=$x.GetElementsByTagName('text')
[void]$t.Item(0).AppendChild($x.CreateTextNode(%s))
[void]$t.Item(1).AppendChild($x.CreateTextNode(%s))
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('%s').Show([Windows.UI.Notifications.ToastNotification]::new($x))
}catch{}`, q(title), q(body), notifyAppID)
}

// encodePS is the UTF-16LE base64 that -EncodedCommand expects.
func encodePS(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, 0, len(units)*2)
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// notifyAppID is the identity Windows files the toast under. A toast raised for
// an app id Windows has never heard of is accepted by the API and then shown to
// nobody — which is exactly how this arrived broken the second time.
const notifyAppID = "Vair"

var aumidOnce sync.Once

// ensureAUMID registers the app id so toasts are actually displayed, and so the
// notification carries our name rather than a blank.
//
// One value under the user's own HKCU\Software\Classes — the same place every
// unpackaged desktop application registers itself for this, and the only way to
// have toasts without a Start-menu shortcut or a packaged identity. Best-effort:
// a machine where the write fails simply keeps the previous behaviour.
func ensureAUMID() {
	aumidOnce.Do(func() {
		key, _, err := registry.CreateKey(registry.CURRENT_USER,
			`Software\Classes\AppUserModelId\`+notifyAppID, registry.SET_VALUE)
		if err != nil {
			return
		}
		defer key.Close()
		_ = key.SetStringValue("DisplayName", "Vair")
		if exe, err := os.Executable(); err == nil {
			_ = key.SetStringValue("IconUri", exe)
		}
	})
}

// toastsAllowed reports whether Windows will show a toast at all.
//
// The master switch in Settings → System → Notifications writes ToastEnabled=0,
// and with it off the API accepts everything and displays nothing — no error,
// no trace. Worth reading, because "the app is broken" and "notifications are
// turned off on this machine" look identical from the outside.
func toastsAllowed() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\PushNotifications`, registry.QUERY_VALUE)
	if err != nil {
		return true // no key, no opinion
	}
	defer key.Close()
	v, _, err := key.GetIntegerValue("ToastEnabled")
	if err != nil {
		return true
	}
	return v != 0
}

// NotificationsBlockedReason returns a sentence to show the user when the
// system will swallow our notifications, or "" when it will not.
func NotificationsBlockedReason(lang string) string {
	if toastsAllowed() {
		return ""
	}
	if lang == "ru" {
		return "Уведомления выключены в самой Windows (Параметры → Система → Уведомления) — сообщение о молчащем сервере не появится, пока они выключены"
	}
	return "Notifications are switched off in Windows itself (Settings → System → Notifications) — nothing will be shown until they are turned back on"
}

// desktopNotify shows a system notification. Never blocks the caller and never
// reports failure: a machine with notifications turned off is not a fault.
func desktopNotify(title, body string) {
	if notifyMuted.Load() {
		return
	}
	go func() {
		ensureAUMID()
		cmd := runHidden("powershell.exe", "-NoProfile", "-NonInteractive",
			"-ExecutionPolicy", "Bypass", "-EncodedCommand", encodePS(notifyScript(title, body)))
		if err := cmd.Start(); err != nil {
			return
		}
		// PowerShell's startup is most of the cost; the toast is shown before the
		// process exits. Reap it so it does not linger.
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }() //nolint:errcheck
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			killTree(cmd)
		}
	}()
}
