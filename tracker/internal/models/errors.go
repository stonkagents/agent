// Package: tracker/internal/models
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: Domain error types for tracker operations

package models

import "errors"

var (
	// ErrNotFound indicates the requested entity was not found.
	ErrNotFound = errors.New("not found")

	// ErrAlreadyExists indicates the entity already exists.
	ErrAlreadyExists = errors.New("already exists")

	// ErrInvalidInput indicates the input failed validation.
	ErrInvalidInput = errors.New("invalid input")

	// ErrInsufficientCredits indicates the account does not have enough credits.
	ErrInsufficientCredits = errors.New("insufficient credits")

	// ErrPaidCreditsRequired indicates the operation requires paid credits and the
	// account has neither paid balance nor remaining trial. Free credits cannot pay
	// for detailed (gpt-5.4) mode.
	ErrPaidCreditsRequired = errors.New("paid credits required")

	// ErrInsufficientPaidCredits indicates the account has some paid balance but
	// not enough to cover the requested operation (e.g. detailed mode call).
	ErrInsufficientPaidCredits = errors.New("insufficient paid credits")

	// ErrTrialExhausted indicates the detailed-mode trial has been used up or expired.
	ErrTrialExhausted = errors.New("detailed trial exhausted")

	// ErrNonceExpired indicates the registration nonce has expired.
	ErrNonceExpired = errors.New("nonce expired")
)
