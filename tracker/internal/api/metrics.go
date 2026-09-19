// Package: tracker/internal/api
// Feature: F-012 (Observability)
// Story: US-012-01 (Prometheus Metrics Export)
// Purpose: Prometheus metrics collection and export

package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Request counter: total API requests by method, path, and status
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "at_api_requests_total",
			Help: "Total number of API requests",
		},
		[]string{"method", "path", "status"},
	)

	// Latency histogram: API request latency in seconds
	latencySeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "at_api_latency_seconds",
			Help:    "API request latency in seconds",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1.0}, // 10ms, 50ms, 100ms, 500ms, 1s
		},
		[]string{"method", "path"},
	)

	// Error counter: API errors by method, path, and error code
	errorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "at_api_errors_total",
			Help: "Total number of API errors",
		},
		[]string{"method", "path", "error_code"},
	)

	// Resource gauges: current counts of peers and assets
	peersTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "at_peers_total",
			Help: "Current number of registered peers",
		},
	)

	assetsTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "at_assets_total",
			Help: "Current number of announced assets",
		},
	)

	// profileDegradationTotal counts soft-degraded profile sub-calls (TD-025).
	// Label "component" identifies which sub-call degraded (reputation, presence, assets, trusts, library).
	profileDegradationTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "at_profile_degradation_total",
			Help: "Count of soft-degraded profile sub-calls",
		},
		[]string{"component"},
	)

	downloadRouteTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "at_download_route_total",
			Help: "Download route outcomes: p2p (peers returned) or no_peers (503)",
		},
		[]string{"mode"},
	)
)

func init() {
	// Register metrics with Prometheus default registry
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(latencySeconds)
	prometheus.MustRegister(errorsTotal)
	prometheus.MustRegister(peersTotal)
	prometheus.MustRegister(assetsTotal)
	prometheus.MustRegister(profileDegradationTotal)
	prometheus.MustRegister(downloadRouteTotal)
}

// MetricsMiddleware records request metrics (counter, latency, errors)
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code
		ww := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		// Serve request
		next.ServeHTTP(ww, r)

		// Record metrics
		duration := time.Since(start).Seconds()
		path := getRoutePath(r)
		method := r.Method
		status := strconv.Itoa(ww.statusCode)

		requestsTotal.WithLabelValues(method, path, status).Inc()
		latencySeconds.WithLabelValues(method, path).Observe(duration)

		// Record error if status >= 400
		if ww.statusCode >= 400 {
			errorCode := status // Use status code as error code for now
			errorsTotal.WithLabelValues(method, path, errorCode).Inc()
		}
	})
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// getRoutePath extracts the route pattern from the request (using gorilla/mux)
func getRoutePath(r *http.Request) string {
	route := mux.CurrentRoute(r)
	if route == nil {
		return r.URL.Path
	}

	path, err := route.GetPathTemplate()
	if err != nil {
		return r.URL.Path
	}
	return path
}

// HandleMetrics returns the Prometheus metrics endpoint handler
func HandleMetrics() http.Handler {
	return promhttp.Handler()
}
