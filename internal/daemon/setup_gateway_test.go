package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayDashboardURL(t *testing.T) {
	if got := gatewayDashboardURL("http://127.0.0.1:19002", "abc 123"); got != "http://127.0.0.1:19002/#token=abc+123" {
		t.Fatalf("with token = %q", got)
	}
	if got := gatewayDashboardURL("http://127.0.0.1:19002/", ""); got != "http://127.0.0.1:19002/" {
		t.Fatalf("without token = %q", got)
	}
}

func TestSetupGateway_LoopbackOnlyAndRunningProbe(t *testing.T) {
	trk, _ := newHeartbeatRecorder(t, http.StatusOK)
	s := newIdentityTestServer(t, trk.URL)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer gw.Close()
	s.config.GatewayURL = gw.URL
	s.config.GatewayToken = "tok"

	rec := do(s, setupRequest(http.MethodGet, "/api/v1/setup/gateway", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out gatewayLinkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.URL != gw.URL || out.Data.DashboardURL != gw.URL+"/#token=tok" || !out.Data.Running {
		t.Fatalf("unexpected %+v", out.Data)
	}

	gw.Close()
	rec = do(s, setupRequest(http.MethodGet, "/api/v1/setup/gateway", ""))
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Running {
		t.Fatalf("running should be false once the gateway is gone")
	}

	remote := setupRequest(http.MethodGet, "/api/v1/setup/gateway", "")
	remote.RemoteAddr = "203.0.113.5:4444"
	if rec := do(s, remote); rec.Code != http.StatusForbidden {
		t.Fatalf("remote status %d", rec.Code)
	}
}
