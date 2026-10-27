package core

import (
	"os"
	"path/filepath"
	"strconv"
)

// writeFileDurable replaces path with data so that a reader — this process,
// another one, or the next start after a crash — only ever sees the old file or
// the new one, never something in between.
//
// os.WriteFile does not give that. It truncates the file and then writes it,
// and anything that ends the process between the two — Windows restarting for
// an update, a crash, the task manager, a second copy of the program exiting on
// its own — leaves an empty or half-written file behind. For settings.json that
// is not a small loss: the next start cannot read it, runs on defaults, and the
// first change of any kind writes those defaults over it.
//
// So the data goes to a temporary file in the same folder first, is flushed to
// disk, and is then renamed over the target. A rename within one volume is a
// single step on NTFS and on every Linux filesystem the app supports. The
// temporary name carries the process ID, so two processes can never write into
// each other's half-finished file.
func writeFileDurable(path string, data []byte, perm os.FileMode) error {
	tmp := filepath.Join(filepath.Dir(path),
		"."+filepath.Base(path)+".tmp-"+strconv.Itoa(os.Getpid()))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()      //nolint:errcheck
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()      //nolint:errcheck
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	return nil
}
