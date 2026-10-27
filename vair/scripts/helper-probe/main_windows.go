// Command helper-probe drives the Windows helper service the way the app does,
// one step per argument, and prints every answer. A development tool: it is
// how the service is checked on a test machine before the app speaks to it.
//
//	helper-probe hello status blockstatus
//	helper-probe hello start=C:\vair\tun.json,ks wait=5 status stop
//	helper-probe -anon hello            (must be refused: no identity)
//
// Steps: hello, status, blockstatus, ping, start=<config file>[,ks][,lan],
// stop, stopkeep, ks=on|off, session=on|off, persist=on|off,
// firewall=<remote>,<http>,<socks>, ruleset=<file>, wait=<seconds>.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"

	"vair/helper/api"
)

func main() {
	anon := flag.Bool("anon", false, "connect anonymously (the service must refuse)")
	noCheck := flag.Bool("no-server-check", false, "skip checking that the pipe's server is the service")
	squat := flag.Int("squat", 0, "take the pipe's name for this many seconds, as an impostor would while the service is stopped")
	flag.Parse()
	if *squat > 0 {
		ln, err := winio.ListenPipe(api.PipeName, nil)
		if err != nil {
			fmt.Println("squat: could not take the name:", err)
			os.Exit(1)
		}
		fmt.Println("squat: holding", api.PipeName)
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				fmt.Println("squat: a client connected")
				c.Close()
			}
		}()
		time.Sleep(time.Duration(*squat) * time.Second)
		ln.Close()
		return
	}
	if err := run(*anon, *noCheck, flag.Args()); err != nil {
		fmt.Println("ERROR:", err)
		os.Exit(1)
	}
}

func run(anon, noCheck bool, steps []string) error {
	level := winio.PipeImpLevelIdentification
	if anon {
		level = winio.PipeImpLevelAnonymous
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	nc, err := winio.DialPipeAccessImpLevel(ctx, api.PipeName, uint32(windows.GENERIC_READ|windows.FILE_WRITE_DATA), level)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer nc.Close()
	if !noCheck {
		if err := checkServer(nc); err != nil {
			return err
		}
		fmt.Println("server: the pipe's server is the service")
	}
	c := api.NewConn(nc)
	frames := make(chan *api.Response, 256)
	go func() {
		for {
			r, err := c.ReadResponse()
			if err != nil {
				close(frames)
				return
			}
			frames <- r
		}
	}()
	id := uint64(0)
	call := func(req *api.Request) (*api.Response, error) {
		id++
		req.ID = id
		if err := c.WriteRequest(req); err != nil {
			return nil, err
		}
		for {
			select {
			case r, ok := <-frames:
				if !ok {
					return nil, errors.New("the service closed the connection")
				}
				if r.ID == 0 {
					printFrame(r)
					continue
				}
				return r, nil
			case <-time.After(60 * time.Second):
				return nil, errors.New("no answer")
			}
		}
	}

	for _, step := range steps {
		name, arg, _ := strings.Cut(step, "=")
		var req *api.Request
		switch name {
		case "hello":
			req = &api.Request{Op: api.OpHello, Protocol: api.ProtocolVersion}
		case "status":
			req = &api.Request{Op: api.OpStatus}
		case "blockstatus":
			req = &api.Request{Op: api.OpBlockStatus}
		case "ping":
			req = &api.Request{Op: api.OpPing}
		case "start":
			parts := strings.Split(arg, ",")
			cfg, err := os.ReadFile(parts[0])
			if err != nil {
				return err
			}
			req = &api.Request{Op: api.OpStartTUN, Config: cfg, LeaseSec: 60}
			for _, p := range parts[1:] {
				switch p {
				case "ks":
					req.KillSwitch = true
				case "lan":
					req.AllowLAN = true
				}
			}
		case "stop":
			req = &api.Request{Op: api.OpStop}
		case "stopkeep":
			req = &api.Request{Op: api.OpStop, KeepBlock: true}
		case "ks":
			req = &api.Request{Op: api.OpKillSwitch, KillSwitch: arg == "on"}
		case "session":
			req = &api.Request{Op: api.OpSessionBlock, Enable: arg == "on"}
		case "persist":
			req = &api.Request{Op: api.OpPersistentBlock, Enable: arg == "on"}
		case "firewall":
			n := strings.Split(arg, ",")
			req = &api.Request{Op: api.OpFirewall}
			if len(n) == 3 {
				req.RemotePort, _ = strconv.Atoi(n[0])
				req.LANHTTPPort, _ = strconv.Atoi(n[1])
				req.LANSocksPort, _ = strconv.Atoi(n[2])
			}
		case "ruleset":
			data, err := os.ReadFile(arg)
			if err != nil {
				return err
			}
			req = &api.Request{Op: api.OpPutRuleSet, Name: filepath.Base(arg), Data: data}
		case "wait":
			sec, _ := strconv.Atoi(arg)
			end := time.Now().Add(time.Duration(sec) * time.Second)
			for time.Now().Before(end) {
				select {
				case r, ok := <-frames:
					if !ok {
						return errors.New("the service closed the connection")
					}
					printFrame(r)
				case <-time.After(time.Second):
				}
				if _, err := call(&api.Request{Op: api.OpPing}); err != nil {
					return err
				}
			}
			fmt.Printf("wait %ds: done\n", sec)
			continue
		default:
			return fmt.Errorf("unknown step %q", step)
		}
		r, err := call(req)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		out, _ := json.Marshal(r)
		fmt.Printf("%s: %s\n", name, out)
	}
	return nil
}

func printFrame(r *api.Response) {
	if r.Log != nil {
		fmt.Printf("  [%s %s] %s\n", r.Log.Src, r.Log.Lvl, r.Log.Text)
	}
	if r.Status != nil {
		fmt.Printf("  [status] running=%v\n", r.Status.Running)
	}
}

var procGetNamedPipeServerProcessId = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetNamedPipeServerProcessId")

// checkServer: the process at the other end of the pipe is the service the
// service manager knows — not a program that took the name while the service
// was stopped.
func checkServer(nc interface{}) error {
	f, ok := nc.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("not a pipe")
	}
	var pid uint32
	if r, _, err := procGetNamedPipeServerProcessId.Call(f.Fd(), uintptr(unsafe.Pointer(&pid))); r == 0 {
		return fmt.Errorf("pipe server process: %w", err)
	}
	spid, err := servicePID()
	if err != nil {
		return err
	}
	if spid != pid {
		return fmt.Errorf("the pipe is served by process %d, the service is process %d — refusing", pid, spid)
	}
	return nil
}

// servicePID asks the service manager for the service's process, with the
// least access that answers: connect, and query status — what an ordinary
// user may do (mgr.Connect asks for everything and would be refused).
func servicePID() (uint32, error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, fmt.Errorf("service manager: %w", err)
	}
	defer windows.CloseServiceHandle(scm) //nolint:errcheck
	name, err := windows.UTF16PtrFromString(api.ServiceName)
	if err != nil {
		return 0, err
	}
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return 0, fmt.Errorf("the %s service: %w", api.ServiceName, err)
	}
	s := &mgr.Service{Name: api.ServiceName, Handle: h}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return 0, err
	}
	if st.ProcessId == 0 {
		return 0, errors.New("the service is not running")
	}
	return st.ProcessId, nil
}
