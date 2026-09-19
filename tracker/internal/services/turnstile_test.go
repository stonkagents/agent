// Package: tracker/internal/services
// Feature: StonkAgents portal feedback
// Purpose: Turnstile siteverify client — request shape, success, rejection, secret errors and
//          unavailability map to the three sentinel errors.

package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNewTurnstileVerifier_NilWithoutSecret(t *testing.T) {
	if v := NewTurnstileVerifier("  ", "", nil); v != nil {
		t.Fatal("empty secret must disable verification (nil verifier)")
	}
}

func TestTurnstileVerify(t *testing.T) {
	var gotForm url.Values
	var reply string
	var status = http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", ct)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	defer srv.Close()
	v := NewTurnstileVerifier("secret-1", srv.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))

	reply = `{"success":true,"hostname":"dev.stonkagents.com"}`
	if err := v.Verify(context.Background(), " tok-abc ", "203.0.113.9"); err != nil {
		t.Fatalf("Verify(ok) = %v", err)
	}
	if gotForm.Get("secret") != "secret-1" || gotForm.Get("response") != "tok-abc" || gotForm.Get("remoteip") != "203.0.113.9" {
		t.Errorf("siteverify form = %v", gotForm)
	}

	if err := v.Verify(context.Background(), "", ""); !errors.Is(err, ErrTurnstileMissing) {
		t.Errorf("empty token err = %v, want ErrTurnstileMissing", err)
	}
	if err := v.Verify(context.Background(), strings.Repeat("x", 2049), ""); !errors.Is(err, ErrTurnstileRejected) {
		t.Errorf("oversized token err = %v, want ErrTurnstileRejected", err)
	}

	reply = `{"success":false,"error-codes":["invalid-input-response"]}`
	if err := v.Verify(context.Background(), "bad", ""); !errors.Is(err, ErrTurnstileRejected) {
		t.Errorf("rejected err = %v, want ErrTurnstileRejected", err)
	}
	// A bad server secret is an operator error, not a bot: unavailable, never a silent 403.
	reply = `{"success":false,"error-codes":["invalid-input-secret"]}`
	if err := v.Verify(context.Background(), "tok", ""); !errors.Is(err, ErrTurnstileUnavailable) {
		t.Errorf("bad secret err = %v, want ErrTurnstileUnavailable", err)
	}
	reply, status = `{"success":true}`, http.StatusBadGateway
	if err := v.Verify(context.Background(), "tok", ""); !errors.Is(err, ErrTurnstileUnavailable) {
		t.Errorf("HTTP 502 err = %v, want ErrTurnstileUnavailable", err)
	}
	reply, status = `not json`, http.StatusOK
	if err := v.Verify(context.Background(), "tok", ""); !errors.Is(err, ErrTurnstileUnavailable) {
		t.Errorf("bad body err = %v, want ErrTurnstileUnavailable", err)
	}

	srv.Close()
	if err := v.Verify(context.Background(), "tok", ""); !errors.Is(err, ErrTurnstileUnavailable) {
		t.Errorf("server down err = %v, want ErrTurnstileUnavailable", err)
	}
}
