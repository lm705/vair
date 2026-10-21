// Package validate checks a sing-box configuration before a process running as
// root is asked to execute it.
//
// It exists because of what the helper's security boundary actually is: any
// process running as the user who installed the helper can reach its socket.
// That makes them authenticated, not trusted — a compromised browser extension,
// a malicious dependency in some unrelated project, anything running as that
// user, can send a start_tun. If the helper ran whatever arrived, "member of
// the user's group" would silently mean "root".
//
// So this is a WHITELIST, and it rejects by default. A config that a future
// version of the app legitimately needs will be rejected until this file learns
// about it, and that is the correct direction to fail in.
//
// What it does NOT do, stated plainly rather than implied: it does not
// understand sing-box semantics, and it does not stop a caller who is allowed to
// start a tunnel from routing traffic somewhere unpleasant. It stops the config
// from being a lever on the root process itself — writing files, opening
// privileged ports, reading paths outside the two directories the helper owns.
package validate

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Options are the limits the caller imposes: the helper passes the directories
// it owns.
type Options struct {
	// AllowedPathPrefixes are the directories any path in the config may point
	// into, after cleaning. Empty means no path is allowed anywhere.
	AllowedPathPrefixes []string
	// MinListenPort is the lowest port a non-TUN inbound may bind. Root can
	// bind anything; a config that asks it to listen on 80 or 443 is not a
	// tunnel configuration.
	MinListenPort int
}

// DefaultMinListenPort keeps inbounds out of the privileged range.
const DefaultMinListenPort = 1024

// ifaceName is the interface names the helper will create. Fixed, short and
// unmistakable: an arbitrary name lets a caller collide with an existing
// interface and have root reconfigure it.
var ifaceName = regexp.MustCompile(`^vair[0-9]{0,2}$`)

// topLevel is every key allowed at the root of the document.
//
// "experimental" is deliberately absent. It is where sing-box keeps its HTTP
// control API and the cache file — a control surface and a file write, both of
// which would be running as root.
var topLevel = map[string]bool{
	"log":       true,
	"dns":       true,
	"inbounds":  true,
	"outbounds": true,
	"route":     true,
	// endpoints carries protocols that own an interface rather than dialling
	// out — WireGuard, and whatever sing-box adds there later. Allowed, but not
	// blindly: checkEndpoints below admits one type and refuses the rest,
	// because "a top-level key we now permit" is exactly how a whitelist stops
	// being one.
	"endpoints": true,
}

// Config returns nil when raw is safe for the helper to run, and an error
// naming the first problem otherwise.
func Config(raw []byte, opt Options) error {
	if opt.MinListenPort == 0 {
		opt.MinListenPort = DefaultMinListenPort
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("config is not a JSON object: %w", err)
	}

	for k := range doc {
		if !topLevel[k] {
			return fmt.Errorf("unsupported top-level key %q", k)
		}
	}

	if err := checkLog(doc["log"]); err != nil {
		return err
	}
	if err := checkInbounds(doc["inbounds"], opt); err != nil {
		return err
	}
	if err := checkEndpoints(doc["endpoints"]); err != nil {
		return err
	}
	// Paths are checked over the WHOLE document rather than at the places we
	// expect them: rule_set today, something else after the next sing-box
	// release. A whitelist that only looks where it remembers to look is not one.
	var anyDoc interface{}
	if err := json.Unmarshal(raw, &anyDoc); err != nil {
		return fmt.Errorf("config is not valid JSON: %w", err)
	}
	return checkPaths(anyDoc, "", false, opt)
}

// checkLog rejects log.output, which is an arbitrary file write as root — the
// single most direct escalation available in an otherwise innocuous block.
func checkLog(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var log map[string]interface{}
	if err := json.Unmarshal(raw, &log); err != nil {
		return fmt.Errorf("log: %w", err)
	}
	if _, ok := log["output"]; ok {
		return fmt.Errorf("log.output is not allowed: it would write a file as root")
	}
	return nil
}

// allowedEndpointTypes is what may appear in the endpoints list.
//
// One entry, and it should stay short. sing-box's other endpoint type is
// Tailscale, which authenticates, keeps state on disk and can expose an SSH
// server — none of which an unprivileged caller should be able to ask a root
// service for.
var allowedEndpointTypes = map[string]bool{"wireguard": true}

// checkEndpoints admits WireGuard and refuses everything else by name.
func checkEndpoints(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var eps []map[string]interface{}
	if err := json.Unmarshal(raw, &eps); err != nil {
		return fmt.Errorf("endpoints: %w", err)
	}
	for i, ep := range eps {
		t, _ := ep["type"].(string)
		if !allowedEndpointTypes[t] {
			return fmt.Errorf("endpoints[%d]: unsupported type %q", i, t)
		}
	}
	return nil
}

func checkInbounds(raw json.RawMessage, opt Options) error {
	if len(raw) == 0 {
		return fmt.Errorf("no inbounds: a TUN configuration needs exactly one tun inbound")
	}
	var inbounds []map[string]interface{}
	if err := json.Unmarshal(raw, &inbounds); err != nil {
		return fmt.Errorf("inbounds: %w", err)
	}

	tunCount := 0
	for i, in := range inbounds {
		typ, _ := in["type"].(string)
		switch typ {
		case "tun":
			tunCount++
			name, _ := in["interface_name"].(string)
			if !ifaceName.MatchString(name) {
				return fmt.Errorf("inbound %d: interface_name %q is not one of ours (want vair, vair0, vair12…)", i, name)
			}
		case "mixed", "socks", "http":
			// The LAN-sharing inbounds. Bounded by port only; what they carry is
			// the caller's business, what they bind is ours.
			port, ok := numberField(in, "listen_port")
			if !ok {
				return fmt.Errorf("inbound %d (%s): listen_port is required", i, typ)
			}
			if port < opt.MinListenPort {
				return fmt.Errorf("inbound %d (%s): listen_port %d is below %d — root must not bind a privileged port", i, typ, port, opt.MinListenPort)
			}
		default:
			return fmt.Errorf("inbound %d: unsupported type %q", i, typ)
		}
	}
	if tunCount != 1 {
		return fmt.Errorf("expected exactly one tun inbound, found %d", tunCount)
	}
	return nil
}

func numberField(m map[string]interface{}, key string) (int, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64) // encoding/json gives every number as float64
	if !ok {
		return 0, false
	}
	return int(f), true
}

// checkPaths walks the decoded document and constrains every string that names
// a file.
//
// Anything ending in "_path" is a file wherever it appears — certificate_path,
// key_path, cache_file's path — so that test is schema-agnostic and stays right
// as sing-box grows fields this package has never heard of.
//
// A bare "path" is not. sing-box uses that name for two unrelated things, and
// treating them alike was wrong: a DoH server's `"path": "/dns-query"` is a URL
// component, not a file, and the blanket check rejected the app's own
// configuration for it. So a bare "path" counts only inside route.rule_set,
// which is the one place it names a file — found by validating what the app
// actually generates rather than by reading the schema.
func checkPaths(node interface{}, key string, inRuleSet bool, opt Options) error {
	switch v := node.(type) {
	case map[string]interface{}:
		for k, child := range v {
			if err := checkPaths(child, k, inRuleSet || k == "rule_set", opt); err != nil {
				return err
			}
		}
	case []interface{}:
		for _, child := range v {
			if err := checkPaths(child, key, inRuleSet, opt); err != nil {
				return err
			}
		}
	case string:
		if strings.HasSuffix(key, "_path") || (key == "path" && inRuleSet) {
			return checkPath(key, v, opt)
		}
	}
	return nil
}

// checkPath uses slash semantics from "path", not "path/filepath".
//
// The configs being validated are always Linux ones, whatever machine this runs
// on, and filepath would answer questions about the HOST — on Windows
// "/etc/shadow" is not even an absolute path, so a check written with filepath
// passes or fails for reasons that have nothing to do with the config. The
// developer machine here is Windows, so this is the difference between a test
// that means something and one that does not.
//
// The comparison is deliberately lexical: no stat, no symlink resolution. That
// is sound only because the allowed directories are not writable by the user
// who can talk to the helper — /usr/lib/vair belongs to the package manager and
// /run/vair is created by systemd as root with mode 0750. A user who cannot
// create a symlink inside them cannot use one to escape them. If an allowed
// prefix is ever added that the user CAN write to, this has to grow real
// symlink resolution, and the reason it did not need it disappears.
func checkPath(key, p string, opt Options) error {
	if p == "" {
		return fmt.Errorf("%s is empty", key)
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("%s %q must be an absolute path", key, p)
	}
	clean := path.Clean(p)
	for _, prefix := range opt.AllowedPathPrefixes {
		prefix = path.Clean(prefix)
		// The trailing separator matters: a plain string prefix would accept
		// /usr/lib/vair-evil for an allowed /usr/lib/vair.
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return nil
		}
	}
	return fmt.Errorf("%s %q is outside the allowed directories %v", key, p, opt.AllowedPathPrefixes)
}
