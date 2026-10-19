//go:build windows

package engines

import (
	"fmt"
	"os"
	"path/filepath"

	"vair/core"
)

// engineDir is %LOCALAPPDATA%\vair\bin — where engines + rule-sets extract
// (same root as the app data since 2.0 took over the 1.10 "vair" folder;
// engines are re-extracted each run, so any 1.10 leftovers are overwritten).
func engineDir() (string, error) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		local = os.Getenv("APPDATA")
	}
	if local == "" {
		return os.MkdirTemp("", "vair-")
	}
	dir := filepath.Join(local, "vair", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}
	return dir, nil
}

// needsExtract reports whether dst is missing or differs from src (by size, then
// the first 512 bytes — catches same-size updates).
func needsExtract(dst string, src []byte) bool {
	info, err := os.Stat(dst)
	if err != nil || info.Size() != int64(len(src)) {
		return true
	}
	f, err := os.Open(dst)
	if err != nil {
		return true
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n < 512 || n > len(src) {
		return true
	}
	for i := 0; i < n; i++ {
		if buf[i] != src[i] {
			return true
		}
	}
	return false
}

// RegisterPaths records the engine binary paths with core SYNCHRONOUSLY,
// before the (slow) byte extraction runs in the background. Without this, the
// frontend's first ConnService.AppInfo() call races the extraction goroutine and
// sees state.singboxBin == "" → the TUN pill shows "sing-box not found". The
// paths are valid the instant the dir exists; the actual .exe files land a moment
// later (extractEngines), well before the user could click connect.
func RegisterPaths() {
	dir, err := engineDir()
	if err != nil {
		return
	}
	core.SetBinDir(dir)
	core.SetEngines(filepath.Join(dir, "xray.exe"), filepath.Join(dir, "sing-box.exe"))
}

// Extract writes the engines, geo data and rule-set baselines it is handed into
// the engine directory, then points core at them.
//
// The bytes arrive as an argument rather than being embedded here: //go:embed
// cannot reach outside its own directory and the data lives in bin/ at the
// repository root, so the directives stay beside the data while the logic lives
// in a package of its own.
func Extract(files []File) error {
	dir, err := engineDir()
	if err != nil {
		return err
	}
	for _, f := range files {
		dst := filepath.Join(dir, f.Name)
		if f.KeepExisting {
			// A rule-set baseline: written only when MISSING, so a runtime
			// refresh is never clobbered by the copy that shipped with the
			// release.
			if _, statErr := os.Stat(dst); statErr == nil {
				continue
			}
			os.WriteFile(dst, f.Data, os.FileMode(f.Perm)) //nolint:errcheck
			continue
		}
		if needsExtract(dst, f.Data) {
			if err := os.WriteFile(dst, f.Data, os.FileMode(f.Perm)); err != nil {
				return fmt.Errorf("extract %s: %w", f.Name, err)
			}
		}
	}
	core.SetBinDir(dir)
	core.SetEngines(filepath.Join(dir, "xray.exe"), filepath.Join(dir, "sing-box.exe"))
	return nil
}
