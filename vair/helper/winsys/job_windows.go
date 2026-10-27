package winsys

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The engines end with their owner.
//
// Every engine goes into one job object that kills its members when it is
// closed — and Windows closes it when the owning process ends, however it ends.
// Before, a crash of Vair left sing-box and xray running on their own: the TUN
// adapter and its routes stayed, the kill-switch's session filters were gone,
// DNS stopped answering, and the machine sat in a half-tunnel nobody was
// driving. The owner is the app for the engines it starts, and the helper
// service for the one it runs.
var job struct {
	once sync.Once
	h    windows.Handle
	err  error
}

// jobHandle is the job, made on first use. Its handle is never closed: it
// closes with the process, and that is the moment it exists for.
func jobHandle() (windows.Handle, error) {
	job.once.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			job.err = err
			return
		}
		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(h) //nolint:errcheck
			job.err = err
			return
		}
		job.h = h
	})
	return job.h, job.err
}

// TieToOwner puts a started process into the job, so it ends when this process
// does.
func TieToOwner(pid int) error {
	j, err := jobHandle()
	if err != nil {
		return err
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ph) //nolint:errcheck
	return windows.AssignProcessToJobObject(j, ph)
}
