package services

import "vair/core"

// UnlockService drives the "does this server open X" check.
//
// The work is one HTTP request per service per configuration, through the same
// throwaway engine the ping and speed tests use, so a whole list can be asked
// at once — which is the point: every other client checks the one server you
// are already connected to.
type UnlockService struct{}

// Services lists what can be checked.
func (u *UnlockService) Services() []core.UnlockService { return core.UnlockServices() }

// Start begins a check over the given rows, or over everything the table is
// showing when none are named — hence the view parameters, which are the same
// three PingAll takes. Returns "" or a reason.
func (u *UnlockService) Start(tabId, sort, filter string, proto []string, indices []int, serviceIds []string) string {
	return core.UnlockCheck(tabId, sort, filter, proto, indices, serviceIds)
}

// Cancel stops a running check.
func (u *UnlockService) Cancel() { core.UnlockCancel() }

// State is the progress and whatever has been answered so far. Polled while the
// panel is open.
func (u *UnlockService) State() core.UnlockProgress { return core.UnlockState() }

// Services lists what can be checked, with the rule each entry uses to read an
// answer — the editor shows all of it.
func (u *UnlockService) SetServices(list []core.UnlockService) { core.SetUnlockServices(list) }

// ResetServices forgets the user's list, restoring the one that shipped.
func (u *UnlockService) ResetServices() { core.SetUnlockServices(nil) }
