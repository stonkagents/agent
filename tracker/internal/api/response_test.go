// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: Tests for JSON response helpers

package api

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendJSON_StatusAndContentType(t *testing.T) {
	w := httptest.NewRecorder()
	SendJSON(w, http.StatusCreated, map[string]string{"id": "123"})

	if w.Code != http.StatusCreated {
		t.Errorf("SendJSON() status = %d, want %d", w.Code, http.StatusCreated)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("SendJSON() Content-Type = %q, want %q", ct, "application/json")
	}

	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["id"] != "123" {
		t.Errorf("SendJSON() body id = %q, want %q", body["id"], "123")
	}
}

func TestSendError_Envelope(t *testing.T) {
	w := httptest.NewRecorder()
	SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing field")

	if w.Code != http.StatusBadRequest {
		t.Errorf("SendError() status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var body ErrorEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error.Code != "VALIDATION_ERROR" {
		t.Errorf("SendError() code = %q, want %q", body.Error.Code, "VALIDATION_ERROR")
	}
	if body.Error.Message != "missing field" {
		t.Errorf("SendError() message = %q, want %q", body.Error.Message, "missing field")
	}
}

func TestSendList_WithTotal(t *testing.T) {
	w := httptest.NewRecorder()
	items := []string{"a", "b", "c"}
	SendList(w, items, 10)

	if w.Code != http.StatusOK {
		t.Errorf("SendList() status = %d, want %d", w.Code, http.StatusOK)
	}

	var body ListResponse
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Total != 10 {
		t.Errorf("SendList() total = %d, want 10", body.Total)
	}
}

// TestSendJSON_LogsEncodingError verifies that encoding failures are logged, not silently dropped.
// TD-093: response.go silent encode error.
func TestSendJSON_LogsEncodingError(t *testing.T) {
	// Capture log output
	var logBuf bytes.Buffer
	origOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOutput)

	w := httptest.NewRecorder()
	// Channels are not JSON-encodable — will trigger encoding error
	SendJSON(w, http.StatusOK, make(chan int))

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "SendJSON") || !strings.Contains(logOutput, "error") {
		t.Errorf("Expected log output to contain encoding error, got: %q", logOutput)
	}
}

// --- F-032: SendDataWithMeta tests ---

func TestSendDataWithMeta_FullResponse(t *testing.T) {
	w := httptest.NewRecorder()
	items := []string{"alpha", "beta"}
	SendDataWithMeta(w, items, 42, 20, 0)

	if w.Code != http.StatusOK {
		t.Errorf("SendDataWithMeta() status = %d, want %d", w.Code, http.StatusOK)
	}

	var body struct {
		Data []string `json:"data"`
		Meta struct {
			Total  int `json:"total"`
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("SendDataWithMeta() unmarshal error: %v", err)
	}
	if len(body.Data) != 2 {
		t.Errorf("SendDataWithMeta() data length = %d, want 2", len(body.Data))
	}
	if body.Meta.Total != 42 {
		t.Errorf("SendDataWithMeta() meta.total = %d, want 42", body.Meta.Total)
	}
	if body.Meta.Limit != 20 {
		t.Errorf("SendDataWithMeta() meta.limit = %d, want 20", body.Meta.Limit)
	}
	if body.Meta.Offset != 0 {
		t.Errorf("SendDataWithMeta() meta.offset = %d, want 0", body.Meta.Offset)
	}
}

func TestSendDataWithMeta_EmptyData(t *testing.T) {
	w := httptest.NewRecorder()
	SendDataWithMeta(w, []string{}, 0, 20, 0)

	var body struct {
		Data []interface{} `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("SendDataWithMeta() unmarshal error: %v", err)
	}
	// Must be empty array [], not null
	if body.Data == nil {
		t.Error("SendDataWithMeta() empty data should be [], not null")
	}
	if body.Meta.Total != 0 {
		t.Errorf("SendDataWithMeta() meta.total = %d, want 0", body.Meta.Total)
	}
}
