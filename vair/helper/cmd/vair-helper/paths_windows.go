package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// paths is everything on disk the service uses. All of it is decided here,
// from where the service itself was installed — never from a request.
type paths struct {
	// installDir is Program Files\Vair: the directory this exe is in, which
	// only administrators can write. The engines run from its bin\.
	installDir string
	engine     string // installDir\bin\sing-box.exe — the one program the service runs
	xray       string // installDir\bin\xray.exe — the app runs it; the blocks let it out
	app        string // installDir\Vair.exe — the app; the blocks and rules name it

	// dataDir is %ProgramData%\Vair, readable and writable by SYSTEM and
	// administrators only (dataDirSDDL). What a client sends that becomes a
	// file — a configuration, a rule-set — lands here.
	dataDir    string
	ruleSetDir string
	configPath string
	logPath    string
	statePath  string
}

// dataDirSDDL protects dataDir: SYSTEM and administrators, inherited by
// everything inside, and nothing inherited from ProgramData, which ordinary
// users can write to.
const dataDirSDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

func servicePaths() (paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return paths{}, err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	pd, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return paths{}, fmt.Errorf("find ProgramData: %w", err)
	}
	dir := filepath.Dir(exe)
	data := filepath.Join(pd, "Vair")
	return paths{
		installDir: dir,
		engine:     filepath.Join(dir, "bin", "sing-box.exe"),
		xray:       filepath.Join(dir, "bin", "xray.exe"),
		app:        filepath.Join(dir, "Vair.exe"),
		dataDir:    data,
		ruleSetDir: filepath.Join(data, "rulesets"),
		configPath: filepath.Join(data, "tun.json"),
		logPath:    filepath.Join(data, "helper.log"),
		statePath:  filepath.Join(data, "block.json"),
	}, nil
}

// blockApps are the programs the blocks let out on their own: the engines, to
// reach the servers, and the app, for what it does itself (tests, updates
// through the tunnel). Only those that exist: WFP identifies a program by its
// file, and a filter for a missing one cannot be made.
func (p paths) blockApps() []string {
	var out []string
	for _, f := range []string{p.engine, p.xray, p.app} {
		if st, err := os.Stat(f); err == nil && !st.IsDir() {
			out = append(out, f)
		}
	}
	return out
}

// ensureDataDir makes dataDir and sets its protection — every start, not only
// at install: a directory somebody recreated with ProgramData's inherited
// permissions would hand every user a file the service later reads.
func (p paths) ensureDataDir() error {
	if err := os.MkdirAll(p.ruleSetDir, 0o700); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(dataDirSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(p.dataDir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
