package setup

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ControllerStatusTimeout bounds the GET /status probe.
const ControllerStatusTimeout = 2 * time.Second

// ControllerCheck probes GET {controllerURL}/status; ok when it answers 200.
func ControllerCheck(controllerURL string) Check {
	detail := map[string]any{"url": controllerURL}
	client := &http.Client{Timeout: ControllerStatusTimeout}
	resp, err := client.Get(controllerURL + "/status")
	if err != nil {
		return Failed(IDController, "controller not reachable on "+controllerURL, detail)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Failed(IDController, fmt.Sprintf("controller /status returned HTTP %d", resp.StatusCode), detail)
	}
	var body struct {
		Daemon  string `json:"daemon"`
		Healthy bool   `json:"healthy"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil {
		detail["daemon"] = body.Daemon
		detail["healthy"] = body.Healthy
		detail["version"] = body.Version
	}
	return OK(IDController, detail)
}
