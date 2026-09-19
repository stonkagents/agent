// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: JSON response envelope helpers for tracker API

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
)

// ErrorEnvelope wraps API errors in a consistent shape.
type ErrorEnvelope struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains error code and message.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ListResponse wraps paginated list results.
type ListResponse struct {
	Data  interface{} `json:"data"`
	Total int         `json:"total"`
}

// SendJSON writes a JSON response with the given status code.
func SendJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("[SendJSON] failed to encode response", "error", err)
	}
}

// SendError writes a JSON error response.
func SendError(w http.ResponseWriter, status int, code, message string) {
	SendJSON(w, status, ErrorEnvelope{
		Error: ErrorDetail{Code: code, Message: message},
	})
}

// SendList writes a JSON list response with total count.
func SendList(w http.ResponseWriter, data interface{}, total int) {
	SendJSON(w, http.StatusOK, ListResponse{Data: data, Total: total})
}

type DataEnvelope struct {
	Data interface{} `json:"data"`
}

// SendData writes a JSON response with { data: T } envelope.
func SendData(w http.ResponseWriter, data interface{}) {
	SendJSON(w, http.StatusOK, DataEnvelope{Data: data})
}

// PaginationMeta holds ADR-001 pagination metadata.
type PaginationMeta struct {
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// PaginatedResponse wraps data with ADR-001 pagination meta.
type PaginatedResponse struct {
	Data interface{}    `json:"data"`
	Meta PaginationMeta `json:"meta"`
}

// SendDataWithMeta writes a JSON response with { data: T[], meta: { total, limit, offset } }.
func SendDataWithMeta(w http.ResponseWriter, data interface{}, total, limit, offset int) {
	SendJSON(w, http.StatusOK, PaginatedResponse{
		Data: data,
		Meta: PaginationMeta{Total: total, Limit: limit, Offset: offset},
	})
}

// parsePagination reads limit and offset from query params with defaults and clamping.
func parsePagination(r *http.Request, defaultLimit, maxLimit int) (limit, offset int) {
	limit = defaultLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	return limit, offset
}
