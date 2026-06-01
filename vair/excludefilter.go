package main

import "strings"

// excludeColumns is the canonical set of columns the exclude-filter UI can
// target. Anything else in the column position of a "col:val" rule is
// treated as legacy (name-only) data. Keep this in sync with the dropdown
// options in the tab-settings modal.
var excludeColumns = map[string]struct{}{
	"name":      {},
	"type":      {},
	"host":      {},
	"transport": {},
	"security":  {},
}

// parseExcludeRule splits a stored exclude-filter entry into (column, value).
//
// Encoding:
//   - New form:   "column:value"  where column is one of excludeColumns.
//   - Legacy:     "value"         (no colon, or colon-prefix isn't a known
//     column) — defaults to column="name" so
//     pre-existing user data keeps working
//     without a migration step.
//
// The colon split is on the FIRST ':' only — values can contain colons
// without escaping ("name:foo:bar" filters names containing "foo:bar").
func parseExcludeRule(s string) (column, value string) {
	if i := strings.Index(s, ":"); i > 0 {
		col := strings.ToLower(s[:i])
		if _, ok := excludeColumns[col]; ok {
			return col, s[i+1:]
		}
	}
	return "name", s
}

// displayProtocol returns the protocol label as the user sees it in the UI:
// "ss2022" is split out from the backend's unified "ss" Kind by inspecting
// the cipher. Keep in sync with chipProto() in the JS layer.
func displayProtocol(kind, security string) string {
	if kind == "ss" && strings.HasPrefix(security, "2022-blake3-") {
		return "ss2022"
	}
	return kind
}

// shouldSkip reports whether a config row should be hidden because it
// matches any of the per-tab exclude rules. Each rule is "column:value"
// (or bare "value" for legacy name-only rules — see parseExcludeRule).
// A row is skipped if ANY rule's value is a (case-insensitive) substring
// of the corresponding column's value on the row.
//
// The `kind` parameter is the backend protocol id ("vless", "ss", …); the
// type-column match uses displayProtocol so a "ss2022" filter actually
// targets only the SS2022 ciphers, not legacy SS.
func shouldSkip(name, kind, host, network, security string, rules []string) bool {
	if len(rules) == 0 {
		return false
	}
	lowName := strings.ToLower(name)
	lowType := strings.ToLower(displayProtocol(kind, security))
	lowHost := strings.ToLower(host)
	lowNet := strings.ToLower(network)
	lowSec := strings.ToLower(security)
	for _, r := range rules {
		col, val := parseExcludeRule(r)
		val = strings.ToLower(strings.TrimSpace(val))
		if val == "" {
			continue
		}
		var hay string
		switch col {
		case "type":
			hay = lowType
		case "host":
			hay = lowHost
		case "transport":
			hay = lowNet
		case "security":
			hay = lowSec
		default: // "name" + legacy
			hay = lowName
		}
		if strings.Contains(hay, val) {
			return true
		}
	}
	return false
}
