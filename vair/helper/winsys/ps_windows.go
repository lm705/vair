package winsys

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

// createNoWindow keeps a console program from flashing a window.
const createNoWindow = 0x08000000

// Hide prepares a child process to run without a window — every program this
// package starts is a console one.
func Hide(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

// NL is the newline used when joining a script — PowerShell reads carriage
// returns fine, but a bare \n keeps the encoded command shorter.
const NL = "\n"

// PSQuote escapes a value for a single-quoted PowerShell string.
func PSQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// PSTimeout bounds every PowerShell run. None of ours takes more than a few
// seconds; one that does is stuck — on a firewall cmdlet waiting for a service
// that is stopping, as during a shutdown — and was waited on for ever.
const PSTimeout = time.Minute

// PS runs a PowerShell snippet and returns its output. Errors carry the output,
// because "the command failed" without what it said is not a report.
//
// The progress stream is silenced for every script, and that is a bug fix
// rather than tidiness. PowerShell writes progress records — "Preparing modules
// for first use", the per-command bar — to a stream that a non-interactive host
// renders as CLIXML on stderr, and CombinedOutput hands them back mixed into the
// output we parse. One of those records was read as a firewall profile called
// `<Objs Version` and written into the marker; the release then tried to
// restore a profile by that name, the command failed, and a real profile was
// left blocking behind it — a machine whose disconnect left the firewall half
// shut.
func PS(script string) (string, error) { return PSFor(script, PSTimeout) }

// PSFor is PS with a time limit of its own: the elevated release waits on a UAC
// prompt, which waits on a person.
func PSFor(script string, limit time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-EncodedCommand",
		EncodePS("$ProgressPreference = 'SilentlyContinue'"+NL+script))
	Hide(cmd)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("PowerShell did not finish within %s", limit)
	}
	if err != nil {
		return string(out), fmt.Errorf("%v: %s", err, PSErrorText(string(out)))
	}
	return string(out), nil
}

// EncodePS is the UTF-16LE base64 that -EncodedCommand expects.
func EncodePS(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, 0, len(units)*2)
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// PSErrorText pulls the readable part out of what PowerShell writes when its
// error stream is redirected: a CLIXML document, several kilobytes of it, with
// the actual message spread across <S S="Error"> fragments and the newlines
// spelled out as _x000D__x000A_. Logged raw, one failed firewall rule filled the
// screen and said nothing; this is the same failure in one line.
func PSErrorText(out string) string {
	out = strings.TrimSpace(out)
	if !strings.Contains(out, "#< CLIXML") {
		return out
	}
	var b strings.Builder
	for _, part := range strings.Split(out, `<S S="Error">`)[1:] {
		frag, _, ok := strings.Cut(part, "</S>")
		if !ok {
			continue
		}
		frag = strings.ReplaceAll(frag, "_x000D__x000A_", " ")
		frag = strings.ReplaceAll(frag, "_x000D_", " ")
		frag = strings.ReplaceAll(frag, "_x000A_", " ")
		if frag = strings.TrimSpace(frag); frag != "" {
			if b.Len() > 0 {
				b.WriteString(" ")
			}
			b.WriteString(frag)
		}
	}
	if b.Len() == 0 {
		return out
	}
	// The tail is PowerShell describing its own stack; the first sentence is
	// what went wrong.
	msg := b.String()
	if i := strings.Index(msg, "+ CategoryInfo"); i > 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	return msg
}
