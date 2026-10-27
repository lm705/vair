// Package wfp is Vair's kill-switch in the Windows Filtering Platform: the
// calls into fwpuclnt.dll, the policy that decides what may pass, and the three
// shapes the block takes — the persistent set at fixed keys, the session's own
// set in a dynamic session, and the presence check.
//
// It imports nothing of vair/core, and that is its reason to be a package of
// its own. The same code runs in two places: inside the app, when a portable
// copy is run as administrator, and inside the Windows helper service, which
// runs as SYSTEM — and code running as SYSTEM links nothing it does not need,
// least of all the app's config store, parsers and HTTP clients.
//
// Everything that decides WHEN to block — settings, markers, the hand-over
// between sessions, what the user is told — stays with the caller. This package
// is told what to put up and does exactly that.
package wfp
