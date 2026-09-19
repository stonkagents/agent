// Command controller runs the StonkAgents controller HTTP server (start/stop daemon, status).
// On Windows, the MSI installs stonkagents-controller-svc.exe (installer/controllersvc) as a
// Windows Service; this binary can be run in console for debugging. On macOS, this is the
// main controller binary run by launchd.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/stonkagents/agent/internal/controller"
)

// Version is the build version, can be overridden via ldflags:
// go build -ldflags "-X main.Version=1.0.0" ./cmd/controller
var Version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := controller.NewServer(Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "controller init: %v\n", err)
		os.Exit(1)
	}
	if err := srv.Run(ctx); err != nil && err != context.Canceled && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "controller: %v\n", err)
		os.Exit(1)
	}
}
