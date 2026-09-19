//go:build windows

// Package main: Windows service wrapper for StonkAgents. Registers with SCM,
// reports SERVICE_RUNNING promptly, then runs stonkagents-daemon.exe with daemon.env.
// Fixes Error 1053 (service did not respond in time) when using PowerShell as binPath.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/svc"
)

// serviceName is only the dispatcher table entry: for a SERVICE_WIN32_OWN_PROCESS
// service the SCM ignores it, so the registered name (per environment, see
// internal/installenv and installer/helper) does not have to match it.
const serviceName = "StonkAgentsDaemon"

func main() {
	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintf(os.Stderr, "IsWindowsService: %v\n", err)
		os.Exit(1)
	}
	if !isService {
		// Run in console for debugging (same as service logic but no SCM)
		runDaemon()
		return
	}
	if err := svc.Run(serviceName, &daemonService{}); err != nil {
		fmt.Fprintf(os.Stderr, "svc.Run: %v\n", err)
		os.Exit(1)
	}
}

type daemonService struct{}

func (d *daemonService) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (svcSpecificEC bool, exitCode uint32) {
	const accept = svc.AcceptStop | svc.AcceptShutdown
	s <- svc.Status{State: svc.StartPending, Accepts: accept}

	installDir, err := installDir()
	if err != nil {
		return true, 1
	}
	daemonExe := filepath.Join(installDir, "stonkagents-daemon.exe")
	envPath := filepath.Join(installDir, "daemon.env")
	env, err := loadDaemonEnv(envPath)
	if err != nil {
		return true, 2
	}

	// Capture daemon stdout/stderr so "started then stopped" can be diagnosed (e.g. config/secrets/log dir).
	var logFile *os.File
	logPath := filepath.Join(installDir, "daemon-service.log")
	logFile, err = os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		defer logFile.Close()
	}

	cmd := exec.Command(daemonExe)
	cmd.Dir = installDir
	cmd.Env = env
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		return true, 3
	}
	childDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(childDone)
	}()

	s <- svc.Status{State: svc.Running, Accepts: accept}

	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending, Accepts: accept}
				_ = cmd.Process.Kill()
				<-childDone
				return false, 0
			case svc.Interrogate:
				s <- req.CurrentStatus
			}
		case <-childDone:
			// Daemon exited on its own; report stopped and exit handler
			s <- svc.Status{State: svc.Stopped, Accepts: accept}
			return false, 0
		}
	}
}

func runDaemon() {
	// Debug/console mode: run daemon in foreground (no SCM)
	installDir, err := installDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	envPath := filepath.Join(installDir, "daemon.env")
	env, err := loadDaemonEnv(envPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	daemonExe := filepath.Join(installDir, "stonkagents-daemon.exe")
	cmd := exec.Command(daemonExe)
	cmd.Dir = installDir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			os.Exit(e.ExitCode())
		}
		os.Exit(1)
	}
}

func installDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	return filepath.Dir(exe), nil
}

// loadDaemonEnv reads daemon.env (KEY=VALUE per line), returns env slice for exec.Cmd (current env + file).
func loadDaemonEnv(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open daemon.env: %w", err)
	}
	defer f.Close()
	base := os.Environ()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		val = strings.Trim(val, `"'`)
		if key == "" {
			continue
		}
		// Override or append (env format KEY=VALUE)
		prefix := key + "="
		found := false
		for j, e := range base {
			if strings.HasPrefix(e, prefix) {
				base[j] = prefix + val
				found = true
				break
			}
		}
		if !found {
			base = append(base, prefix+val)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read daemon.env: %w", err)
	}
	return base, nil
}
