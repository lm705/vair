package core

// tunRunsInHelper is false here: on Windows the app is already elevated when
// TUN mode is available, so it runs the engine as its own child and there is no
// helper to hand it to.
func tunRunsInHelper() bool { return false }

// startTUNViaHelper is never called on Windows; the declaration exists so the
// shared connect path compiles on both platforms.
func startTUNViaHelper(*connManager, *ConfigEntry, string, string, string, map[string]interface{}, []string, []string) {
	panic("unreachable")
}

// tunPreconditionError — TUN needs elevation here, and the app can get it by
// relaunching itself.
func tunPreconditionError() string {
	if !isPrivileged() {
		return "TUN mode requires administrator/root. Run the program as admin."
	}
	return ""
}

// startHybridTUNViaHelper is never called on Windows.
func startHybridTUNViaHelper(*connManager, *ConfigEntry, *Node, string, string, map[string]interface{}, hybridParts) {
	panic("unreachable")
}
