package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"golang.org/x/sys/windows/svc"

	"vair/helper/api"
)

// run is the Windows entry point: one of the setup program's commands, or the
// service itself when the service manager starts it.
func run(o options) error {
	switch {
	case o.cleanup:
		return errors.New("-cleanup is Linux's; on Windows the service clears up as it starts")
	case o.install:
		return installService()
	case o.uninstall:
		return uninstallService()
	case o.repair:
		return installService()
	case o.stop:
		return stopService()
	}
	inService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !inService {
		return fmt.Errorf("vair-helper is the %s service; the setup program installs it (vair-helper -install-service)", api.ServiceName)
	}
	return svc.Run(api.ServiceName, &service{})
}

// service is the handler the service manager talks to.
type service struct{}

// Execute runs the service until the manager asks it to stop.
//
// Stop, shutdown and pre-shutdown all end the same way: the tunnel and the
// session block go, the persistent block stays. Pre-shutdown is the one that
// matters at a restart: it arrives while the network stack is still there,
// before Windows starts tearing services down in no particular order.
func (*service) Execute(_ []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	p, err := servicePaths()
	if err != nil {
		return true, 1
	}
	if err := p.ensureDataDir(); err != nil {
		return true, 2
	}
	useLogFile(p.logPath)
	log.Printf("starting (protocol %d, version %s, installed in %s)", api.ProtocolVersion, version, p.installDir)

	srv := newServer(p, &engineProc{path: p.engine, dir: p.dataDir, config: p.configPath}, realSystem{})
	srv.startup()

	// Running before the pipe is open: opening it can take a while when the
	// name is still held (listenPatiently), and the manager must be able to
	// stop the service meanwhile.
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPreShutdown}

	opened := make(chan net.Listener, 1)
	failed := make(chan error, 1)
	stopping := make(chan struct{})
	go func() {
		ln, err := listenPatiently(stopping)
		if err != nil {
			failed <- err
			return
		}
		opened <- ln
	}()

	var ln net.Listener
	var wg sync.WaitGroup
	for {
		select {
		case l := <-opened:
			ln = l
			log.Printf("listening on %s", api.PipeName)
			wg.Add(1)
			go func() {
				defer wg.Done()
				acceptLoop(ln, srv)
			}()
		case err := <-failed:
			log.Printf("listen on %s: %v — giving up; the service manager starts the service again", api.PipeName, err)
			srv.shutdown()
			return true, 3
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown, svc.PreShutdown:
				status <- svc.Status{State: svc.StopPending}
				log.Printf("stopping (%v): the tunnel goes, a persistent block stays", c.Cmd)
				close(stopping)
				if ln != nil {
					ln.Close()
				}
				srv.shutdown()
				wg.Wait()
				return false, 0
			}
		}
	}
}

// acceptLoop identifies each client before a single request is read. One that
// cannot be identified, or runs below medium integrity, is closed unheard.
func acceptLoop(ln net.Listener, srv *server) {
	for {
		nc, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("accept: %v", err)
			continue
		}
		who, err := identify(nc)
		if err != nil {
			log.Printf("refused a client: %v", err)
			nc.Close()
			continue
		}
		if who.integrity < integrityMedium {
			log.Printf("refused a client below medium integrity (%s)", who)
			nc.Close()
			continue
		}
		go srv.serve(nc, who)
	}
}

// listenPatiently opens the pipe, trying again for a while when the name is
// taken. It is taken while the previous instance of the service is still
// going away — and while some other program holds it, which the app refuses to
// talk to (it checks that the pipe's server is the service) but which keeps
// the service from listening until it lets go.
func listenPatiently(stopping <-chan struct{}) (net.Listener, error) {
	var err error
	for attempt := 0; attempt < 30; attempt++ {
		var ln net.Listener
		if ln, err = listen(); err == nil {
			return ln, nil
		}
		if attempt == 0 {
			log.Printf("listen on %s: %v — trying again", api.PipeName, err)
		}
		select {
		case <-stopping:
			return nil, errors.New("stopped while waiting for the pipe")
		case <-time.After(2 * time.Second):
		}
	}
	return nil, err
}
