package main

import (
	"encoding/json"
	"os"
	"time"

	"vair/helper/wfp"
	"vair/helper/winsys"
)

// system is what the service asks of Windows besides running the engine:
// filters, firewall rules, adapters. Behind an interface so the request
// handling can be tested without touching the machine.
type system interface {
	openSessionBlock(o wfp.BlockOptions) (func(), error)
	installPersistent(o wfp.BlockOptions) error
	removePersistent() error
	persistentPresent() (present, known bool)
	luid(iface string) (uint64, error)
	legacyPresent() bool
	releaseLegacy() error

	engineRule(singbox, iface string) error
	removeEngineRule(singbox string)
	sweepAdapters(except string)
	ghostSweep(engines []string)

	remoteRule(port int, program string)
	removeRemoteRule()
	lanRule(httpPort, socksPort int, programs ...string)
	removeLANRule()
	removeProgramRules(programs ...string) []string
}

type realSystem struct{}

func (realSystem) openSessionBlock(o wfp.BlockOptions) (func(), error) { return wfp.OpenSession(o) }
func (realSystem) installPersistent(o wfp.BlockOptions) error          { return wfp.InstallPersistent(o) }
func (realSystem) removePersistent() error                             { return wfp.RemovePersistent() }
func (realSystem) persistentPresent() (bool, bool)                     { return wfp.PersistentPresent() }
func (realSystem) luid(iface string) (uint64, error)                   { return wfp.InterfaceLUID(iface) }

// legacyPresent: a Windows Firewall block of the kind builds before 2.7.0 put
// up — the default outbound action at Block — with our rule group still there
// to say it is ours. Without the group the firewall's setting is somebody
// else's business.
func (realSystem) legacyPresent() bool {
	ours, _ := winsys.LegacyRulesPresent()
	if !ours {
		return false
	}
	blocking, _ := winsys.AnyProfileBlocking()
	return blocking
}

func (realSystem) releaseLegacy() error {
	winsys.RemoveLegacyRules()
	return winsys.SetOutbound("Allow")
}

func (realSystem) engineRule(singbox, iface string) error {
	return winsys.EnsureEngineRule(singbox, iface)
}
func (realSystem) removeEngineRule(singbox string) { winsys.RemoveEngineRule(singbox) }
func (realSystem) sweepAdapters(except string)     { winsys.RunSweep(winsys.ReapScript(except)) }
func (realSystem) ghostSweep(engines []string)     { winsys.RunSweep(winsys.GhostSweepScript(engines)) }

func (realSystem) remoteRule(port int, program string) { winsys.EnsureRemoteRule(port, program) }
func (realSystem) removeRemoteRule()                   { winsys.RemoveRemoteRule() }
func (realSystem) lanRule(httpPort, socksPort int, programs ...string) {
	winsys.EnsureLANProxyRule(httpPort, socksPort, programs...)
}
func (realSystem) removeLANRule() { winsys.RemoveLANProxyRule() }
func (realSystem) removeProgramRules(programs ...string) []string {
	return winsys.RemoveProgramRules(programs...)
}

// persisted is what the service remembers about the persistent block across
// its own restarts and the machine's: whether it was asked for, and by whom.
// The filters themselves are persistent and need no record to keep working;
// this is what decides who may take them down.
type persisted struct {
	Persistent bool      `json:"persistent"`
	OwnerSID   string    `json:"owner_sid,omitempty"`
	Owner      string    `json:"owner,omitempty"`
	LAN        bool      `json:"lan"`
	Since      time.Time `json:"since"`
}

func loadPersisted(path string) persisted {
	var p persisted
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	return p
}

func savePersisted(path string, p persisted) {
	if !p.Persistent {
		os.Remove(path) //nolint:errcheck
		return
	}
	if data, err := json.MarshalIndent(p, "", "  "); err == nil {
		tmp := path + ".tmp"
		if os.WriteFile(tmp, data, 0o600) == nil {
			os.Rename(tmp, path) //nolint:errcheck
		}
	}
}
