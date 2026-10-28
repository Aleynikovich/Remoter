//go:build windows

// Package wdesktop runs work on the Windows desktop that currently has
// input. Windows keeps the sign-in screen, the lock screen and a running
// UAC prompt each on their own desktop (Winlogon, the secure desktop),
// separate from the ordinary "Default" desktop where programs run. A thread
// may only capture or send input to the desktop it is attached to, so to
// follow the user from the sign-in screen through to their session the host
// has to move its capture and input threads to whichever desktop has input
// at the moment. A Worker owns one such thread and re-attaches it whenever
// the input desktop changes.
package wdesktop

import (
	"log"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	procOpenInputDesktop         = user32.NewProc("OpenInputDesktop")
	procSetThreadDesktop         = user32.NewProc("SetThreadDesktop")
	procCloseDesktop             = user32.NewProc("CloseDesktop")
	procGetUserObjectInformation = user32.NewProc("GetUserObjectInformationW")
)

const (
	genericAll = 0x10000000
	uoiName    = 2
)

// Worker runs functions on a dedicated thread kept on the active input
// desktop.
type Worker struct {
	name string
	reqs chan func()
}

// NewWorker starts a worker thread. name appears in its log lines.
func NewWorker(name string) *Worker {
	w := &Worker{name: name, reqs: make(chan func())}
	go w.run()
	return w
}

// Do runs fn on the worker's thread, after making sure that thread is on the
// desktop that currently has input, and waits for fn to return.
func (w *Worker) Do(fn func()) {
	done := make(chan struct{})
	w.reqs <- func() {
		defer close(done)
		fn()
	}
	<-done
}

func (w *Worker) run() {
	// The desktop a thread is attached to is thread state, so this
	// goroutine must stay on its own OS thread for the life of the process.
	runtime.LockOSThread()
	var cur uintptr    // the desktop handle this thread is on
	var curName string // its name, so a no-op re-attach is skipped
	for fn := range w.reqs {
		if h, name, ok := openInputDesktop(); ok {
			if name != curName {
				if r, _, _ := procSetThreadDesktop.Call(h); r != 0 {
					if cur != 0 {
						// Safe now: the thread has moved off the old one.
						procCloseDesktop.Call(cur)
					}
					cur, curName = h, name
					log.Printf("%s: following input desktop %q", w.name, name)
				} else {
					// Could not switch (another thread may hold it); keep
					// the handle only if it is the one we are already on.
					if h != cur {
						procCloseDesktop.Call(h)
					}
				}
			} else if h != cur {
				procCloseDesktop.Call(h)
			}
		}
		fn()
	}
}

// openInputDesktop opens the desktop that currently receives input and
// returns its handle and name.
func openInputDesktop() (uintptr, string, bool) {
	h, _, _ := procOpenInputDesktop.Call(0, 0, genericAll)
	if h == 0 {
		return 0, "", false
	}
	var buf [256]uint16
	var needed uint32
	r, _, _ := procGetUserObjectInformation.Call(h, uoiName,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&needed)))
	name := ""
	if r != 0 {
		name = syscall.UTF16ToString(buf[:])
	}
	return h, name, true
}
