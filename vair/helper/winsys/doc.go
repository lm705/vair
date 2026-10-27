// Package winsys is the rest of what Vair asks of Windows with administrator
// rights, beside the filters in helper/wfp: PowerShell runs, Windows Firewall
// profiles and rules, the sweep of leftover TUN adapters, and the job object
// that ties an engine's life to its owner's.
//
// Like helper/wfp it imports nothing of vair/core, because the same code runs
// in the app (a portable copy run as administrator) and in the helper service
// (SYSTEM). It does what it is told and reports what happened; deciding when,
// and what to tell the user, stays with the caller.
package winsys
