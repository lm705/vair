//go:build !windows && !linux

package core

// BypassRow is the shape the panel expects. Windows and Linux have real
// implementations; this is the stub for anything else.
type BypassRow struct {
	Process string `json:"process"`
	Dest    string `json:"dest"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Count   int    `json:"count"`
}

// BypassConnections has nothing to read on a platform whose socket table we do
// not know how to enumerate.
func BypassConnections() []BypassRow { return nil }

// clearBypassSnapshot has nothing to clear where there is no snapshot.
func clearBypassSnapshot() {}
