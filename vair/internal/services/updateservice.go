package services

import "vair/core"

// UpdateService exposes the self-updater. Progress streams via the
// "update_status" Wails event (checking/downloading/verifying/ready/error/
// uptodate + pct).
type UpdateService struct{}

// Check fetches the published version.json and reports whether a newer build
// is available (Notify additionally honours "don't show again").
func (u *UpdateService) Check() core.UpdateInfo { return core.CheckForUpdate() }

// Pending is what the automatic check found — it runs once per start, through
// the first session's tunnel — for a window opened after it ran.
func (u *UpdateService) Pending() core.UpdateInfo { return core.PendingUpdate() }

// Apply downloads, verifies (SHA-256) and installs the update, then restarts.
func (u *UpdateService) Apply() { go core.RunUpdate() }

// Dismiss records "don't show again" for the given version.
func (u *UpdateService) Dismiss(version string) { core.DismissUpdateVersion(version) }
