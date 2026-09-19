// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: HTTP middleware for correlation ID, content type, logging

package api

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
)

type contextKey string

const correlationIDKey contextKey = "correlation_id"

// CorrelationIDHeader is the HTTP header for request correlation.
const CorrelationIDHeader = "X-Correlation-ID"

// Default CORS origins used when CORS_ALLOWED_ORIGINS is unset: local dev (frontend on
// port 3000, daemon UI on 7841). Deployed hosts are configured through the env var.
const defaultCORSOrigins = "http://localhost:3000,http://127.0.0.1:3000,http://localhost:7841,http://127.0.0.1:7841"

// CORSMiddleware sets CORS headers so the browser allows requests from the portal (e.g. localhost:3000).
// Reads CORS_ALLOWED_ORIGINS from env (comma-separated); if empty, uses default localhost origins.
func CORSMiddleware(next http.Handler) http.Handler {
	originsEnv := os.Getenv("CORS_ALLOWED_ORIGINS")
	if originsEnv == "" {
		originsEnv = defaultCORSOrigins
	}
	allowed := make(map[string]bool)
	for _, o := range strings.Split(originsEnv, ",") {
		o = strings.TrimSpace(o)
		if o != "" {
			allowed[o] = true
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			} else if allowed["*"] {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization, "+AdminKeyHeader+", "+TurnstileTokenHeader)
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CorrelationIDMiddleware ensures every request has a correlation ID.
func CorrelationIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationIDHeader)
		if id == "" {
			id = uuid.New().String()
		}
		w.Header().Set(CorrelationIDHeader, id)
		ctx := context.WithValue(r.Context(), correlationIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// multipartAllowedPaths lists the only routes permitted to POST multipart/form-data
// (file uploads). Everything else stays JSON-only.
var multipartAllowedPaths = map[string]bool{
	LaunchMetadataPath: true,
}

// ContentTypeMiddleware enforces application/json on POST/PUT/PATCH requests.
// Routes in multipartAllowedPaths may alternatively send multipart/form-data.
func ContentTypeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			ct := r.Header.Get("Content-Type")
			isJSON := strings.HasPrefix(ct, "application/json")
			isAllowedMultipart := r.Method == http.MethodPost && multipartAllowedPaths[r.URL.Path] && strings.HasPrefix(ct, "multipart/form-data")
			if !isJSON && !isAllowedMultipart {
				SendError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// GetCorrelationID retrieves the correlation ID from the request context.
func GetCorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDKey).(string)
	return id
}
