//go:build windows

package controller

import (
	"fmt"
	"net/http"
	"time"

	"github.com/stonkagents/agent/internal/installenv"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// daemonServiceName is the SCM service name for the current environment
// (STONKAGENTS_ENV from daemon.env; "StonkAgentsDaemon" for production, so
func daemonServiceName() string { return installenv.Current().DaemonService }

// daemonHealthURL is the daemon's /health on this environment's port.
func daemonHealthURL() string { return installenv.Current().DaemonHealthURL() }

const healthTimeout = 3 * time.Second

// StartDaemon starts the StonkAgentsDaemon Windows service via SCM.
func StartDaemon() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	service, err := m.OpenService(daemonServiceName())
	if err != nil {
		return fmt.Errorf("open service %s: %w", daemonServiceName(), err)
	}
	defer service.Close()

	return service.Start()
}

// StopDaemon stops the StonkAgentsDaemon Windows service via SCM.
func StopDaemon() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	service, err := m.OpenService(daemonServiceName())
	if err != nil {
		return fmt.Errorf("open service %s: %w", daemonServiceName(), err)
	}
	defer service.Close()

	_, err = service.Control(svc.Stop)
	return err
}

// IsDaemonRunning returns true if the daemon service state is Running.
func IsDaemonRunning() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()

	service, err := m.OpenService(daemonServiceName())
	if err != nil {
		return false
	}
	defer service.Close()

	status, err := service.Query()
	if err != nil {
		return false
	}
	return status.State == svc.Running
}

// IsDaemonHealthy returns true if GET http://localhost:7841/health returns 200.
func IsDaemonHealthy() bool {
	client := &http.Client{Timeout: healthTimeout}
	resp, err := client.Get(daemonHealthURL())
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
