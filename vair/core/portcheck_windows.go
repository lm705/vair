package core

import (
	"errors"
	"syscall"
)

// listeningOwner names the program listening on port, over IPv4 or IPv6.
//
// Both tables: a program listening on [::] — most servers written in Node,
// Java or Python do — is in the IPv6 table only, and a trial bind of
// 127.0.0.1 succeeds beside it. The port looked free, and "localhost" then
// reached one program or the other depending on how it resolved.
func listeningOwner(port int) (string, int) {
	const listen = 2 // MIB_TCP_STATE_LISTEN
	// The port arrives with its bytes the other way round.
	local := func(p uint32) int { return int(uint16(p<<8) | uint16(p>>8&0xff)) }
	for _, r := range tableRows[tcpRowOwnerPID](tcpTable(2)) {
		if r.state == listen && local(r.localPort) == port {
			return processNameCached(r.owningPID), int(r.owningPID)
		}
	}
	for _, r := range tableRows[tcp6RowOwnerPID](tcpTable(23)) {
		if r.state == listen && local(r.localPort) == port {
			return processNameCached(r.owningPID), int(r.owningPID)
		}
	}
	return "", 0
}

// bindRefusedBySystem is WSAEACCES: a port in a range Windows has excluded
// (Hyper-V, WSL and Docker reserve such ranges) or otherwise forbids.
func bindRefusedBySystem(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == 10013
}
