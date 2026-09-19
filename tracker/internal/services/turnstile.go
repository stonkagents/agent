// Package: tracker/internal/services
// Feature: StonkAgents portal feedback
// Purpose: Server-side Cloudflare Turnstile verification (siteverify) for the portal's
//          x-turnstile-token header. Enabled by TURNSTILE_SECRET_KEY; a nil verifier skips it.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TurnstileSiteverifyURL is Cloudflare's verification endpoint.
const TurnstileSiteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

const (
	turnstileTimeout   = 5 * time.Second
	turnstileBodyLimit = 64 * 1024
	// turnstileMaxTokenBytes is Cloudflare's documented token length cap (2048).
	turnstileMaxTokenBytes = 2048
)

// Turnstile errors. Handlers map the first two to 403 and the third to 503.
var (
	ErrTurnstileMissing     = errors.New("turnstile token missing")
	ErrTurnstileRejected    = errors.New("turnstile token rejected")
	ErrTurnstileUnavailable = errors.New("turnstile verification unavailable")
)

// TurnstileVerifier calls siteverify with the server-side secret.
type TurnstileVerifier struct {
	secret string
	url    string
	client *http.Client
	logger *slog.Logger
}

// NewTurnstileVerifier returns a verifier for secret, or nil when secret is empty (feature off).
// verifyURL overrides the siteverify endpoint (tests); empty = TurnstileSiteverifyURL.
func NewTurnstileVerifier(secret, verifyURL string, logger *slog.Logger) *TurnstileVerifier {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil
	}
	if verifyURL == "" {
		verifyURL = TurnstileSiteverifyURL
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &TurnstileVerifier{secret: secret, url: verifyURL, client: &http.Client{Timeout: turnstileTimeout}, logger: logger}
}

type turnstileResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
	Hostname   string   `json:"hostname"`
	Action     string   `json:"action"`
}

// Verify checks token (the widget response) for remoteIP. A missing or rejected token is
// ErrTurnstileMissing / ErrTurnstileRejected; a siteverify failure is ErrTurnstileUnavailable
// (fail closed: the caller answers 503, not 200).
func (v *TurnstileVerifier) Verify(ctx context.Context, token, remoteIP string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return ErrTurnstileMissing
	}
	if len(token) > turnstileMaxTokenBytes {
		return ErrTurnstileRejected
	}
	form := url.Values{"secret": {v.secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.url, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTurnstileUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTurnstileUnavailable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, turnstileBodyLimit))
	if err != nil {
		return fmt.Errorf("%w: read: %v", ErrTurnstileUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: siteverify HTTP %d", ErrTurnstileUnavailable, resp.StatusCode)
	}
	var out turnstileResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrTurnstileUnavailable, err)
	}
	if !out.Success {
		// A misconfigured secret is an operator problem, not a bot: surface it as unavailable.
		for _, code := range out.ErrorCodes {
			if code == "invalid-input-secret" || code == "missing-input-secret" || code == "internal-error" {
				v.logger.Error("[turnstile] siteverify rejected the server secret", "error_codes", out.ErrorCodes)
				return fmt.Errorf("%w: %s", ErrTurnstileUnavailable, code)
			}
		}
		return fmt.Errorf("%w: %s", ErrTurnstileRejected, strings.Join(out.ErrorCodes, ","))
	}
	return nil
}
