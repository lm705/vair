package shell

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ListRunningProcessNames feeds the "apps without VPN" picker: the set of
// process names a user could plausibly want to exclude.
//
// /proc is the whole API. Each numeric directory is a PID and its `comm` holds
// the executable name — the same string sing-box matches its process_name rules
// against, which is what makes this list useful rather than merely decorative.
//
// Kernel threads are skipped. They show up in /proc like everything else, they
// have no executable, and they would bury the applications the user is looking
// for under a hundred entries named kworker/3:1H. The test is an empty cmdline,
// which is what the kernel reports for them.
func ListRunningProcessNames() []string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{}, 128)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid := e.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue // /proc/self, /proc/meminfo, …
		}
		// A process that exits mid-scan makes every read here fail. That is
		// normal rather than exceptional, and it simply means the process does
		// not belong in the list.
		cmdline, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
		if err != nil || len(cmdline) == 0 {
			continue // gone, or a kernel thread
		}
		comm, err := os.ReadFile(filepath.Join("/proc", pid, "comm"))
		if err != nil {
			continue
		}
		if name := strings.TrimSpace(string(comm)); name != "" {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	// Case-insensitive: this is a list a human reads, and having "Discord" sort
	// far away from "code" helps nobody.
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}
