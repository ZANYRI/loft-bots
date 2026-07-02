// Package metrics defines the Prometheus metrics exported by the project
// and serves them on /metrics for Prometheus/Grafana to scrape.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests handled by the admin web server.",
	}, []string{"method", "path", "status"})

	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	BotUpdatesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "bot_updates_total",
		Help: "Total Telegram updates processed, by bot.",
	}, []string{"bot"})

	BotErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "bot_errors_total",
		Help: "Total errors encountered while processing bot updates, by bot.",
	}, []string{"bot"})

	OrdersCreatedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "orders_created_total",
		Help: "Total orders created, by type (ticket/reservation/menu).",
	}, []string{"type"})

	OrdersResolvedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "orders_resolved_total",
		Help: "Total orders resolved by an admin, by outcome (confirmed/rejected).",
	}, []string{"outcome"})
)

// Handler exposes the /metrics endpoint for Prometheus to scrape.
func Handler() http.Handler {
	return promhttp.Handler()
}

// ObserveHTTP records request count and latency for one HTTP request.
func ObserveHTTP(method, path string, status int, start time.Time) {
	HTTPRequestDuration.WithLabelValues(method, path).Observe(time.Since(start).Seconds())
	HTTPRequestsTotal.WithLabelValues(method, path, http.StatusText(status)).Inc()
}
