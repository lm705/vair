package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"vair/helper/api"
	"vair/helper/wfp"
	"vair/helper/winsys"
)

// Installing, repairing, stopping and removing the service. The setup program
// runs these, elevated; each prints what it did, one line per step, which is
// what ends up in the setup log.

// serviceSDDL is the service object's own permissions: the defaults, plus
// starting it for interactive users — so the app can bring a stopped service
// back without asking for administrator rights. Stopping, changing or deleting
// it stays with administrators.
const serviceSDDL = "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWRPLORC;;;IU)"

func installService() error {
	p, err := servicePaths()
	if err != nil {
		return err
	}
	if err := p.ensureDataDir(); err != nil {
		return fmt.Errorf("data directory %s: %w", p.dataDir, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %w", err)
	}
	defer m.Disconnect() //nolint:errcheck

	s, err := m.OpenService(api.ServiceName)
	if err == nil {
		// Already registered — an upgrade, or a repair. Point it at this exe:
		// the setup program may have installed somewhere else this time.
		cfg, err := s.Config()
		if err == nil {
			cfg.BinaryPathName = `"` + exe + `"`
			cfg.StartType = mgr.StartAutomatic
			cfg.Dependencies = []string{"BFE"}
			if err := s.UpdateConfig(cfg); err != nil {
				s.Close()
				return fmt.Errorf("update the service: %w", err)
			}
		}
		fmt.Println("service: already registered, updated")
	} else {
		s, err = m.CreateService(api.ServiceName, exe, mgr.Config{
			DisplayName:  "Vair helper",
			Description:  "Creates Vair's tunnel and holds its kill-switch, so the app itself never runs with administrator rights.",
			StartType:    mgr.StartAutomatic,
			ErrorControl: mgr.ErrorNormal,
			// The filtering engine: the kill-switch is WFP filters.
			Dependencies: []string{"BFE"},
		})
		if err != nil {
			return fmt.Errorf("create the service: %w", err)
		}
		fmt.Println("service: registered")
	}
	defer s.Close()

	// As Mullvad does: back after 3 s, then 30 s, then 10 min; the count
	// starts over after a day without a failure.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 3 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Minute},
	}, 24*60*60); err != nil {
		fmt.Println("service: recovery actions not set:", err)
	}
	// And when it stops with an error of its own, not only when it crashes:
	// a service that could not open its pipe exits cleanly with a code, and
	// without this flag the manager leaves it stopped for good.
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		fmt.Println("service: recovery on errors not set:", err)
	}
	if err := setServiceDACL(s.Handle); err != nil {
		fmt.Println("service: permissions not set:", err)
	}

	st, err := s.Query()
	if err == nil && st.State == svc.Running {
		// A repair of a running service, or an upgrade whose files were the
		// same: restart it, so the exe on disk is the one running.
		if err := waitState(s, svc.Stop, svc.Stopped); err != nil {
			return err
		}
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start the service: %w", err)
	}
	if err := waitFor(s, svc.Running, 20*time.Second); err != nil {
		return err
	}
	fmt.Println("service: running")
	return nil
}

func setServiceDACL(h windows.Handle) error {
	sd, err := windows.SecurityDescriptorFromString(serviceSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func stopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %w", err)
	}
	defer m.Disconnect() //nolint:errcheck
	s, err := m.OpenService(api.ServiceName)
	if err != nil {
		fmt.Println("service: not installed")
		return nil
	}
	defer s.Close()
	if err := waitState(s, svc.Stop, svc.Stopped); err != nil {
		return err
	}
	fmt.Println("service: stopped")
	return nil
}

// uninstallService stops the service, takes down everything of Vair's that
// holds traffic or opens a door, and removes the service and its data.
// Failing to take the block down is the one failure that is reported as one:
// the setup program calls the uninstall off on it, so the program that can
// take the block down is still there.
func uninstallService() error {
	p, err := servicePaths()
	if err != nil {
		return err
	}
	if err := stopService(); err != nil {
		fmt.Println("service: did not stop:", err)
	}

	blockErr := wfp.RemovePersistent()
	if blockErr == nil {
		if present, known := wfp.PersistentPresent(); known && present {
			blockErr = errors.New("the filters are still in place")
		}
	}
	if blockErr != nil {
		fmt.Println("kill switch: COULD NOT RELEASE —", blockErr)
	} else {
		fmt.Println("kill switch: released")
	}
	if ours, _ := winsys.LegacyRulesPresent(); ours {
		winsys.RemoveLegacyRules()
		if err := winsys.SetOutbound("Allow"); err != nil {
			fmt.Println("firewall (2.6.0 block): COULD NOT RESTORE —", err)
			if blockErr == nil {
				blockErr = err
			}
		} else {
			fmt.Println("firewall (2.6.0 block): restored")
		}
	}
	winsys.RemoveEngineRule(p.engine)
	winsys.RemoveRemoteRule()
	winsys.RemoveLANProxyRule()
	for _, line := range winsys.RemoveProgramRules(p.app, p.engine, p.xray) {
		fmt.Println(line)
	}
	winsys.RunSweep(winsys.GhostSweepScript([]string{p.engine, p.xray}))
	fmt.Println("TUN adapters: swept")

	if m, err := mgr.Connect(); err == nil {
		if s, err := m.OpenService(api.ServiceName); err == nil {
			if err := s.Delete(); err != nil {
				fmt.Println("service: COULD NOT REMOVE —", err)
			} else {
				fmt.Println("service: removed")
			}
			s.Close()
		}
		m.Disconnect() //nolint:errcheck
	}
	if err := os.RemoveAll(p.dataDir); err != nil {
		fmt.Println("data:", err)
	}
	return blockErr
}

// waitState sends a control and waits for the state it leads to.
func waitState(s *mgr.Service, c svc.Cmd, want svc.State) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == want || st.State == svc.Stopped && want == svc.Stopped {
		return nil
	}
	if _, err := s.Control(c); err != nil {
		return fmt.Errorf("control the service: %w", err)
	}
	return waitFor(s, want, 30*time.Second)
}

func waitFor(s *mgr.Service, want svc.State, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("the service did not reach state %d within %s", want, limit)
}
