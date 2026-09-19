// Package: tracker/internal/solana
// Feature: StonkAgents (Raydium LaunchLab metrics)
// Purpose: Tests for getAccountInfo / getMultipleAccounts base64 account reads

package solana

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_GetAccountInfo_Base64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "getAccountInfo" {
			t.Errorf("method = %s, want getAccountInfo", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","result":{"context":{"slot":1},"value":{"data":["AQID","base64"],"owner":"Prog","lamports":42}},"id":1}`)
	}))
	defer srv.Close()

	info, err := NewClient(srv.URL).GetAccountInfo(context.Background(), "Addr")
	if err != nil {
		t.Fatalf("GetAccountInfo: %v", err)
	}
	if info == nil || info.Owner != "Prog" || info.Lamports != 42 || string(info.Data) != "\x01\x02\x03" {
		t.Errorf("unexpected account info: %+v", info)
	}
}

func TestClient_GetAccountInfo_Missing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","result":{"context":{"slot":1},"value":null},"id":1}`)
	}))
	defer srv.Close()
	info, err := NewClient(srv.URL).GetAccountInfo(context.Background(), "Addr")
	if err != nil || info != nil {
		t.Errorf("missing account: info=%v err=%v, want nil,nil", info, err)
	}
}

func TestClient_GetMultipleAccounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","result":{"context":{"slot":1},"value":[null,{"data":["AQ==","base64"],"owner":"P","lamports":1}]},"id":1}`)
	}))
	defer srv.Close()
	infos, err := NewClient(srv.URL).GetMultipleAccounts(context.Background(), []string{"A", "B"})
	if err != nil {
		t.Fatalf("GetMultipleAccounts: %v", err)
	}
	if len(infos) != 2 || infos[0] != nil || infos[1] == nil || infos[1].Owner != "P" {
		t.Errorf("unexpected: %+v", infos)
	}
	if _, err := NewClient(srv.URL).GetMultipleAccounts(context.Background(), make([]string, 101)); err == nil {
		t.Error("expected error for >100 addresses")
	}
}
