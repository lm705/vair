//go:build windows

package core

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WinINET's per-connection proxy options: the documented way to read and write
// the whole proxy configuration of the LAN connection — the manual proxy, the
// bypass list, the PAC script and automatic detection — in one call that also
// keeps the registry values in step. The three registry values alone cannot
// switch off a PAC script or automatic detection, and those come first.

const (
	internetOptionPerConnectionOption = 75

	perConnFlags         = 1
	perConnProxyServer   = 2
	perConnProxyBypass   = 3
	perConnAutoconfigURL = 4

	proxyTypeDirect       = 1
	proxyTypeProxy        = 2
	proxyTypeAutoProxyURL = 4
	proxyTypeAutoDetect   = 8
)

// perConnOption is INTERNET_PER_CONN_OPTIONW: a DWORD, then a union of a DWORD,
// a string pointer and a FILETIME. uint64 gives the union its size, and its
// alignment matches the C layout on both 386 (4) and amd64 (8).
type perConnOption struct {
	option uint32
	value  uint64
}

// perConnOptionList is INTERNET_PER_CONN_OPTION_LISTW.
type perConnOptionList struct {
	size        uint32
	connection  *uint16 // nil: the LAN connection
	optionCount uint32
	optionError uint32
	options     *perConnOption
}

// perConnSettings is the part of the configuration Vair takes over.
type perConnSettings struct {
	flags  uint32
	server string
	bypass string
	pac    string
}

var (
	procInternetQueryOptionW = windows.NewLazySystemDLL("wininet.dll").NewProc("InternetQueryOptionW")
	procGlobalFree           = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
)

// queryPerConn reads the LAN connection's proxy configuration.
func queryPerConn() (perConnSettings, error) {
	opts := []perConnOption{{option: perConnFlags}, {option: perConnProxyServer}, {option: perConnProxyBypass}, {option: perConnAutoconfigURL}}
	list := perConnOptionList{optionCount: uint32(len(opts)), options: &opts[0]}
	list.size = uint32(unsafe.Sizeof(list))
	size := list.size
	r, _, err := procInternetQueryOptionW.Call(0, internetOptionPerConnectionOption, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
	runtime.KeepAlive(opts)
	if r == 0 {
		return perConnSettings{}, fmt.Errorf("InternetQueryOption: %w", err)
	}
	// The strings are WinINET's, allocated for the caller to free. The pointer
	// is read from the union's own memory: on 386 it is the low half of it.
	str := func(o *perConnOption) string {
		p := *(**uint16)(unsafe.Pointer(&o.value))
		if p == nil {
			return ""
		}
		s := windows.UTF16PtrToString(p)
		procGlobalFree.Call(uintptr(unsafe.Pointer(p))) //nolint:errcheck
		return s
	}
	return perConnSettings{
		flags:  uint32(opts[0].value),
		server: str(&opts[1]),
		bypass: str(&opts[2]),
		pac:    str(&opts[3]),
	}, nil
}

// setPerConn writes the LAN connection's proxy configuration.
func setPerConn(s perConnSettings) error {
	opts := []perConnOption{
		{option: perConnFlags, value: uint64(s.flags)},
		{option: perConnProxyServer},
		{option: perConnProxyBypass},
		{option: perConnAutoconfigURL},
	}
	// Each string is written into its union as a pointer, in the union's own
	// memory, and kept alive below: the uint64 does not keep it alive itself.
	var keep [][]uint16
	for i, v := range []string{s.server, s.bypass, s.pac} {
		u, err := windows.UTF16FromString(v)
		if err != nil {
			u = []uint16{0}
		}
		keep = append(keep, u)
		*(**uint16)(unsafe.Pointer(&opts[i+1].value)) = &u[0]
	}
	list := perConnOptionList{optionCount: uint32(len(opts)), options: &opts[0]}
	list.size = uint32(unsafe.Sizeof(list))
	r, _, err := procInternetSetOptionW.Call(0, internetOptionPerConnectionOption, uintptr(unsafe.Pointer(&list)), uintptr(list.size))
	// The strings are referred to only through integers in opts; keep them
	// alive until the call has read them.
	runtime.KeepAlive(opts)
	runtime.KeepAlive(keep)
	if r == 0 {
		return fmt.Errorf("InternetSetOption: %w", err)
	}
	return nil
}
