//go:build windows && portable

package main

import (
	_ "embed"

	"vair/internal/engines"
)

// The engines, geo data and rule-set baselines that ship inside the PORTABLE
// Windows binary — one file that needs nothing beside it, and pays for that
// with 110 MB of payload and a first run that writes all of it into the user's
// profile. The installed build has none of this: its engines are files the
// installer put beside the exe (see embed_windows_packaged.go).
//
// These directives are the reason this file exists at the repository root
// rather than inside internal/engines: //go:embed can only name paths under the
// directory of the file that declares it, and the payload lives in bin/. So the
// data stays where the build system puts it, the logic lives in a package, and
// this file is the seam between them.
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
	// Bypass-countries baselines (routing mode "everything except selected
	// countries"): small .srs sets for CN/IR/KZ, embedded like the RU pair so
	// TUN mode works even when the upstream (GitHub) is unreachable. KZ has no
	// geosite (IP-only bypass).
	//go:embed bin/geosite-cn.srs
	embeddedGeositeCnSrs []byte
	//go:embed bin/geoip-cn.srs
	embeddedGeoipCnSrs []byte
	//go:embed bin/geosite-ir.srs
	embeddedGeositeIrSrs []byte
	//go:embed bin/geoip-ir.srs
	embeddedGeoipIrSrs []byte
	//go:embed bin/geoip-kz.srs
	embeddedGeoipKzSrs []byte
	// Blocked-in-China baseline (routing mode "only blocked in China"): the
	// community GFW list as plain text, emitted straight into engine configs
	// (no .dat/.srs involved). Refreshed from upstream at runtime, best-effort.
	//go:embed bin/gfw.txt
	embeddedGfwTxt []byte
)

// embeddedEngineFiles is what gets written out at startup. The engines and geo
// data are refreshed whenever the embedded copy differs; the rule-sets are
// baselines and must not overwrite a refreshed file.
func embeddedEngineFiles() []engines.File {
	return []engines.File{
		{Name: "xray.exe", Data: embeddedXray, Perm: 0o755},
		{Name: "sing-box.exe", Data: embeddedSingbox, Perm: 0o755},
		{Name: "geoip.dat", Data: embeddedGeoIP, Perm: 0o644},
		{Name: "geosite.dat", Data: embeddedGeosite, Perm: 0o644},

		{Name: "geoip-ru.srs", Data: embeddedGeoipRuSrs, Perm: 0o644, KeepExisting: true},
		{Name: "geosite-ru.srs", Data: embeddedGeositeRuSrs, Perm: 0o644, KeepExisting: true},
		{Name: "geosite-ru-blocked.srs", Data: embeddedGeositeRuBlockedSrs, Perm: 0o644, KeepExisting: true},
		{Name: "geoip-ru-blocked.srs", Data: embeddedGeoipRuBlockedSrs, Perm: 0o644, KeepExisting: true},
		{Name: "geosite-ru-blocked.dat", Data: embeddedGeositeRuBlockedDat, Perm: 0o644, KeepExisting: true},
		{Name: "geoip-ru-blocked.dat", Data: embeddedGeoipRuBlockedDat, Perm: 0o644, KeepExisting: true},
		{Name: "geosite-cn.srs", Data: embeddedGeositeCnSrs, Perm: 0o644, KeepExisting: true},
		{Name: "geoip-cn.srs", Data: embeddedGeoipCnSrs, Perm: 0o644, KeepExisting: true},
		{Name: "geosite-ir.srs", Data: embeddedGeositeIrSrs, Perm: 0o644, KeepExisting: true},
		{Name: "geoip-ir.srs", Data: embeddedGeoipIrSrs, Perm: 0o644, KeepExisting: true},
		{Name: "geoip-kz.srs", Data: embeddedGeoipKzSrs, Perm: 0o644, KeepExisting: true},
		{Name: "gfw.txt", Data: embeddedGfwTxt, Perm: 0o644, KeepExisting: true},
	}
}
