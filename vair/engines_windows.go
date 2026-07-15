//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"vair/core"
)

// Embedded engines + geo data + RU rule-set baselines (ported from 1.10
// standalone_windows.go). Embedded from root/bin/ at compile time.
var (
	//go:embed bin/xray.exe
	embeddedXray []byte
	//go:embed bin/sing-box.exe
	embeddedSingbox []byte
	//go:embed bin/geoip.dat
	embeddedGeoIP []byte
	//go:embed bin/geosite.dat
	embeddedGeosite []byte
	//go:embed bin/geoip-ru.srs
	embeddedGeoipRuSrs []byte
	//go:embed bin/geosite-ru.srs
	embeddedGeositeRuSrs []byte
	//go:embed bin/geosite-ru-blocked.srs
	embeddedGeositeRuBlockedSrs []byte
	//go:embed bin/geoip-ru-blocked.srs
	embeddedGeoipRuBlockedSrs []byte
	//go:embed bin/geosite-ru-blocked.dat
	embeddedGeositeRuBlockedDat []byte
	//go:embed bin/geoip-ru-blocked.dat
	embeddedGeoipRuBlockedDat []byte
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

// registerEnginePaths records the engine binary paths with core SYNCHRONOUSLY,
// before the (slow) byte extraction runs in the background. Without this, the
// frontend's first ConnService.AppInfo() call races the extraction goroutine and
// sees state.singboxBin == "" → the TUN pill shows "sing-box not found". The
// paths are valid the instant the dir exists; the actual .exe files land a moment
// later (extractEngines), well before the user could click connect.
func registerEnginePaths() {
	dir, err := engineDir()
	if err != nil {
		return
	}
	core.SetBinDir(dir)
	core.SetEngines(filepath.Join(dir, "xray.exe"), filepath.Join(dir, "sing-box.exe"))
}

// extractEngines writes the embedded xray/sing-box + geo data + RU rule-sets to
// vair2\bin and points core at them. Ported from 1.10 extractBinaries.
func extractEngines() error {
	dir, err := engineDir()
	if err != nil {
		return err
	}
	// Engines + geo data: re-extract when size/head differs (covers updates).
	for _, f := range []struct {
		name string
		data []byte
		perm os.FileMode
	}{
		{"xray.exe", embeddedXray, 0o755},
		{"sing-box.exe", embeddedSingbox, 0o755},
		{"geoip.dat", embeddedGeoIP, 0o644},
		{"geosite.dat", embeddedGeosite, 0o644},
	} {
		dst := filepath.Join(dir, f.name)
		if needsExtract(dst, f.data) {
			if err := os.WriteFile(dst, f.data, f.perm); err != nil {
				return fmt.Errorf("extract %s: %w", f.name, err)
			}
		}
	}
	// RU rule-set baselines: only if MISSING, so a runtime refresh isn't clobbered.
	for _, rs := range []struct {
		name string
		data []byte
	}{
		{"geoip-ru.srs", embeddedGeoipRuSrs},
		{"geosite-ru.srs", embeddedGeositeRuSrs},
		{"geosite-ru-blocked.srs", embeddedGeositeRuBlockedSrs},
		{"geoip-ru-blocked.srs", embeddedGeoipRuBlockedSrs},
		{"geosite-ru-blocked.dat", embeddedGeositeRuBlockedDat},
		{"geoip-ru-blocked.dat", embeddedGeoipRuBlockedDat},
	} {
		dst := filepath.Join(dir, rs.name)
		if _, statErr := os.Stat(dst); statErr != nil {
			_ = os.WriteFile(dst, rs.data, 0o644)
		}
	}
	core.SetBinDir(dir)
	core.SetEngines(filepath.Join(dir, "xray.exe"), filepath.Join(dir, "sing-box.exe"))
	return nil
}
