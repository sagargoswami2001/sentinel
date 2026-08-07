// Package metrics exposes a Prometheus /metrics endpoint.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// sentinel_target_up{name="...", url="..."} 1|0
	TargetUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sentinel_target_up",
		Help: "1 if the target is currently up, 0 if down.",
	}, []string{"name", "url"})

	// sentinel_target_latency_seconds{name="...", url="..."}
	TargetLatency = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sentinel_target_latency_seconds",
		Help: "Most recent check latency in seconds.",
	}, []string{"name", "url"})

	// sentinel_target_cert_days_left{name="...", url="..."}
	TargetCertDays = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sentinel_target_cert_days_left",
		Help: "Days until the TLS certificate expires (-1 when not HTTPS).",
	}, []string{"name", "url"})

	// sentinel_checks_total{name="...", url="...", status="up|down"}
	ChecksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sentinel_checks_total",
		Help: "Total number of checks performed.",
	}, []string{"name", "url", "status"})
)

// Handler returns the standard Prometheus metrics HTTP handler.
func Handler() http.Handler {
	return promhttp.Handler()
}
