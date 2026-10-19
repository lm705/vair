package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"vair/helper/api"
)

const (
	unitName    = "vair-helper.service"
	dropInDir   = "/etc/systemd/system/" + unitName + ".d"
	dropInFile  = dropInDir + "/10-group.conf"
	installedTo = "/usr/lib/vair/vair-helper"
)

// minUID is the first id a real login account gets on every distribution we
// target. Below it are system accounts, several of which are members of groups
// with far more reach than a desktop user's.
const minUID = 1000

// adminGroups must never end up owning the socket.
//
// This is the crux of the whole design, so it is worth stating plainly: the
// helper's security boundary is membership of the group named in the drop-in.
// Anyone in that group can ask a root process to build a tunnel. Pointing it at
// a shared administrative group would quietly hand that to every account in it,
// which is exactly the mistake that turns "install once" into "install a
// backdoor for the whole machine".
var adminGroups = map[string]bool{
	"root": true, "wheel": true, "sudo": true, "admin": true, "adm": true, "staff": true,
}

func install(uninstall bool) error {
	if os.Geteuid() != 0 {
		return errors.New("must run as root — the GUI starts this through pkexec")
	}
	if uninstall {
		return doUninstall()
	}

	group, username, err := targetGroup()
	if err != nil {
		return err
	}
	if _, err := os.Stat(installedTo); err != nil {
		return fmt.Errorf("%s is missing — is the vair package installed?", installedTo)
	}

	if err := os.MkdirAll(dropInDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dropInDir, err)
	}
	// Only the site-specific line lives here. The unit itself belongs to the
	// package, so an upgrade replaces it without touching this decision.
	dropIn := fmt.Sprintf(`# Written by vair-helper-install for %s.
# The group here decides who may talk to the helper's socket, and therefore who
# may ask a root process to build a tunnel. Change it only if you know what that
# means.
[Service]
Group=%s
`, username, group)
	if err := os.WriteFile(dropInFile, []byte(dropIn), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dropInFile, err)
	}

	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", "--now", unitName},
	} {
		out, err := exec.Command("systemctl", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}

	// Waiting for the socket rather than declaring victory on exit code: the
	// unit is Type=simple, so systemctl returns as soon as the process is
	// spawned, and a helper that dies at startup would look like success.
	if err := waitForSocket(10 * time.Second); err != nil {
		return err
	}
	fmt.Printf("Vair helper installed and running (group %s).\n", group)
	return nil
}

// targetGroup resolves the account this install is for and its primary group.
//
// PKEXEC_UID is set by pkexec to the uid of whoever authorised the action, and
// it is the only trustworthy answer here: the process itself is root, so
// getuid() says nothing, and an argument would let a caller nominate any group
// it liked.
func targetGroup() (group, username string, err error) {
	raw := os.Getenv("PKEXEC_UID")
	if raw == "" {
		// Running under sudo rather than pkexec — accept SUDO_UID, since the
		// same check applies and it makes the installer usable from a terminal.
		raw = os.Getenv("SUDO_UID")
	}
	if raw == "" {
		return "", "", errors.New("cannot tell which account to install for: neither PKEXEC_UID nor SUDO_UID is set")
	}
	uid, err := strconv.Atoi(raw)
	if err != nil {
		return "", "", fmt.Errorf("invalid uid %q", raw)
	}
	if uid < minUID {
		return "", "", fmt.Errorf("uid %d is a system account; the helper is meant for a login user", uid)
	}
	u, err := user.LookupId(raw)
	if err != nil {
		return "", "", fmt.Errorf("no such user %s: %w", raw, err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		return "", "", fmt.Errorf("cannot resolve group %s: %w", u.Gid, err)
	}
	if adminGroups[g.Name] {
		return "", "", fmt.Errorf(
			"%s's primary group is %q, which is shared with other administrators — "+
				"give the account a personal primary group before installing the helper",
			u.Username, g.Name)
	}
	return g.Name, u.Username, nil
}

func waitForSocket(d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(api.SocketPath); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The journal is where the reason will be, so say so instead of guessing.
	return fmt.Errorf("the service did not open %s within %s — check: journalctl -u %s",
		api.SocketPath, d, unitName)
}

func doUninstall() error {
	// Errors are reported but not fatal: a partially-installed state must still
	// be removable, and "already gone" is the outcome we want anyway.
	var problems []string
	for _, args := range [][]string{
		{"disable", "--now", unitName},
		{"daemon-reload"},
	} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			problems = append(problems, fmt.Sprintf("systemctl %s: %s",
				strings.Join(args, " "), strings.TrimSpace(string(out))))
		}
	}
	if err := os.Remove(dropInFile); err != nil && !os.IsNotExist(err) {
		problems = append(problems, err.Error())
	}
	os.Remove(filepath.Clean(dropInDir)) //nolint:errcheck — only if now empty
	if len(problems) > 0 {
		return fmt.Errorf("uninstalled with problems: %s", strings.Join(problems, "; "))
	}
	fmt.Println("Vair helper stopped and disabled.")
	return nil
}
