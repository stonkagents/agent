//go:build windows

// Package main: Windows service wrapper for the StonkAgents controller. Registers with SCM
// as StonkAgentsController, reports SERVICE_RUNNING, and runs the controller HTTP server
// (start/stop daemon, status) in a goroutine.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stonkagents/agent/internal/controller"
	"golang.org/x/sys/windows/svc"
)

// loadDaemonEnv applies KEY=VALUE lines from daemon.env (written by setuphelper
// next to this executable) to the process environment without overriding
// variables that are already set. The controller needs STONKAGENTS_RELEASES_URL
// from it so a dev install checks the dev manifest, not production's.
func loadDaemonEnv() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	f, err := os.Open(filepath.Join(filepath.Dir(exe), "daemon.env"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || os.Getenv(key) != "" {
			continue
		}
		_ = os.Setenv(key, strings.Trim(strings.TrimSpace(val), `"`))
	}
}

// Version is the build version, can be overridden via ldflags:
// go build -ldflags "-X main.Version=1.0.0" ./installer/controllersvc
var Version = "dev"

// serviceName is only the dispatcher table entry: for a SERVICE_WIN32_OWN_PROCESS
// service the SCM ignores it, so the registered name (per environment, see
// internal/installenv and installer/helper) does not have to match it.
const serviceName = "StonkAgentsController"

func main() {
	loadDaemonEnv()
	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintf(os.Stderr, "IsWindowsService: %v\n", err)
		os.Exit(1)
	}
	if !isService {
		// Console mode: run controller HTTP server directly (for debugging)
		srv, initErr := controller.NewServer(Version)
		if initErr != nil {
			fmt.Fprintf(os.Stderr, "controller init: %v\n", initErr)
			os.Exit(1)
		}
		if runErr := srv.Run(context.Background()); runErr != nil {
			fmt.Fprintf(os.Stderr, "controller: %v\n", runErr)
			os.Exit(1)
		}
		return
	}
	if err := svc.Run(serviceName, &controllerService{}); err != nil {
		fmt.Fprintf(os.Stderr, "svc.Run: %v\n", err)
		os.Exit(1)
	}
}

type controllerService struct{}

func (c *controllerService) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (svcSpecificEC bool, exitCode uint32) {
	const accept = svc.AcceptStop | svc.AcceptShutdown
	s <- svc.Status{State: svc.StartPending, Accepts: accept}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	srv, initErr := controller.NewServer(Version)
	if initErr != nil {
		return false, 1
	}
	go func() {
		defer close(done)
		_ = srv.Run(ctx)
	}()

	s <- svc.Status{State: svc.Running, Accepts: accept}

	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending, Accepts: accept}
				cancel()
				<-done
				return false, 0
			case svc.Interrogate:
				s <- req.CurrentStatus
			}
		case <-done:
			s <- svc.Status{State: svc.Stopped, Accepts: accept}
			return false, 0
		}
	}
}
