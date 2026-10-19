package main

import "vair/internal/engines"

// Nothing is embedded on Linux: the packages install the engines under
// /usr/lib/vair/bin, root-owned, because that is the only place the privileged
// helper is allowed to run them from. See internal/engines/engines_linux.go.
func embeddedEngineFiles() []engines.File { return nil }
