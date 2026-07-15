//go:build windows

package main

// Process enumeration for the Settings "Browse running processes" picker —
// ported verbatim from the 1.10 standalone_windows.go.

import (
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

const (
	th32csSnapProcess = 0x00000002
	winMaxPath        = 260
)

type processEntry32W struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [winMaxPath]uint16
}

var (
	winKernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procCreateToolhelp32Snapshot = winKernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = winKernel32.NewProc("Process32FirstW")
	procProcess32NextW           = winKernel32.NewProc("Process32NextW")
	procCloseHandleW             = winKernel32.NewProc("CloseHandle")
)

// listRunningProcessNames returns a sorted, deduplicated list of executable
// names of currently running processes (e.g. "chrome.exe"). Names are
// lowercased so the chip-input's case-insensitive matching stays consistent.
func listRunningProcessNames() []string {
	hSnap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	// INVALID_HANDLE_VALUE is -1 on x64; on a uintptr that's all-bits-set.
	if hSnap == 0 || hSnap == ^uintptr(0) {
		return nil
	}
	defer procCloseHandleW.Call(hSnap)

	var entry processEntry32W
	entry.Size = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32FirstW.Call(hSnap, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return nil
	}

	seen := make(map[string]bool, 256)
	for {
		name := syscall.UTF16ToString(entry.ExeFile[:])
		if name != "" {
			seen[strings.ToLower(name)] = true
		}
		ret, _, _ := procProcess32NextW.Call(hSnap, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}

	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
