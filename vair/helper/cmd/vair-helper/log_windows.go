package main

import (
	"log"
	"os"
	"sync"
)

// The service's own log: a file in its data directory, because a service has no
// console and the event log is a poor place to read a session from. The app
// gets the lines that concern it as frames; this is the record for everything
// else — who connected, what was refused, what was torn down and why.

// logMaxBytes is when the file is set aside as helper.log.1 and a new one
// started. Two files of this size are the whole history kept.
const logMaxBytes = 1 << 20

type rotatingFile struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func openLog(path string) *rotatingFile {
	r := &rotatingFile{path: path}
	r.open()
	return r
}

func (r *rotatingFile) open() {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	r.f = f
	if st, err := f.Stat(); err == nil {
		r.size = st.Size()
	}
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return len(p), nil
	}
	if r.size+int64(len(p)) > logMaxBytes {
		r.f.Close()
		os.Remove(r.path + ".1")       //nolint:errcheck
		os.Rename(r.path, r.path+".1") //nolint:errcheck
		r.size = 0
		r.open()
		if r.f == nil {
			return len(p), nil
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// useLogFile sends the standard logger to the file.
func useLogFile(path string) {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(openLog(path))
}
