package cmd

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/config"
	"github.com/stonkagents/agent/internal/setup"
)

func TestLocalSetupChecks_OrderAndSemantics(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir(), TrackerURL: "https://tracker.test", UploadCapMbps: 10, DownloadCapMbps: 50}
	checks := localSetupChecks(cfg)
	if len(checks) != len(setup.CheckIDs) {
		t.Fatalf("checks = %d, want %d", len(checks), len(setup.CheckIDs))
	}
	byID := map[string]setup.Check{}
	for i, c := range checks {
		if c.ID != setup.CheckIDs[i] {
			t.Errorf("check[%d] = %s, want %s", i, c.ID, setup.CheckIDs[i])
		}
		byID[c.ID] = c
	}
	if byID[setup.IDService].Status != setup.StatusMissing || byID[setup.IDP2P].Status != setup.StatusMissing {
		t.Error("service/p2p must be missing without the daemon")
	}
	if c := byID[setup.IDStorage]; c.Status != setup.StatusOK {
		t.Errorf("storage = %+v", c)
	}
	if c := byID[setup.IDBandwidth]; c.Status != setup.StatusOK {
		t.Errorf("bandwidth = %+v", c)
	}
	if c := byID[setup.IDTracker]; c.Status != setup.StatusMissing || c.Detail["apiKeyPresent"] != false {
		t.Errorf("tracker = %+v", c)
	}
	if runtime.GOOS != "windows" {
		if c := byID[setup.IDFirewall]; c.Message != "Windows only" {
			t.Errorf("firewall = %+v", c)
		}
	}
	// nil config: storage/bandwidth degrade to missing, nothing panics
	for _, c := range localSetupChecks(nil) {
		if c.ID == setup.IDStorage && c.Status != setup.StatusMissing {
			t.Errorf("storage without config = %+v", c)
		}
	}
}

func TestPrintCheck_DoesNotPanic(t *testing.T) {
	printCheck(setup.OK(setup.IDService, nil))
	printCheck(setup.Missing(setup.IDFirewall, "Windows only", nil))
	printCheck(setup.Failed(setup.IDStorage, "no space", nil))
}

func TestFetchDaemonSetupStatus_Unreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	old := doctorDaemonURL
	doctorDaemonURL = srv.URL
	defer func() { doctorDaemonURL = old }()
	if _, ok := fetchDaemonSetupStatus(); ok {
		t.Error("expected daemon unreachable")
	}
}

func TestDoctorStatusTimeout_CoversDaemonBudget(t *testing.T) {
	// The daemon bounds GET /api/v1/setup/status at 8s; the CLI must wait
	// longer than that or a healthy daemon looks unreachable.
	if doctorStatusTimeout < 10*time.Second {
		t.Errorf("doctorStatusTimeout = %v, want >= 10s", doctorStatusTimeout)
	}
}

func TestFetchDaemonSetupStatus_ToleratesSlowDaemon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2500 * time.Millisecond) // longer than the old 2s client timeout
		_, _ = w.Write([]byte(`{"checks":[{"id":"service","status":"ok"}]}`))
	}))
	defer srv.Close()
	old := doctorDaemonURL
	doctorDaemonURL = srv.URL
	defer func() { doctorDaemonURL = old }()
	if checks, ok := fetchDaemonSetupStatus(); !ok || len(checks) != 1 {
		t.Errorf("slow daemon reported unreachable: ok=%v checks=%v", ok, checks)
	}
}

func TestApplySetupFix_SendsMutationHeaders(t *testing.T) {
	var gotHeaders bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = setup.HasMutationHeaders(r)
		if !gotHeaders {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"FORBIDDEN","message":"headers"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"check":{"id":"bandwidth","status":"ok"}}`))
	}))
	defer srv.Close()
	old := doctorDaemonURL
	doctorDaemonURL = srv.URL
	defer func() { doctorDaemonURL = old }()
	c, err := applySetupFix(setup.IDBandwidth)
	if err != nil || !gotHeaders || c.Status != setup.StatusOK {
		t.Errorf("fix = %+v err=%v headers=%v", c, err, gotHeaders)
	}
}

func TestFetchDaemonSetupStatus_ParsesChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/setup/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"checks":[{"id":"service","status":"ok"},{"id":"firewall","status":"missing","message":"Windows only"}]}`))
	}))
	defer srv.Close()
	old := doctorDaemonURL
	doctorDaemonURL = srv.URL
	defer func() { doctorDaemonURL = old }()
	checks, ok := fetchDaemonSetupStatus()
	if !ok || len(checks) != 2 || checks[1].Message != "Windows only" {
		t.Errorf("checks = %+v ok=%v", checks, ok)
	}
}
