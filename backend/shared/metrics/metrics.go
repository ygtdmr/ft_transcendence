// Package metrics exposes the Prometheus request metrics every service shares.
//
// The names and labels are a contract with the dashboards and alerts:
//
//	http_requests_total{service,method,route,status}
//	http_request_duration_seconds{service,route}
//	http_errors_total{service,code}
//
// route is Gin's route pattern (/api/v1/auth/login), never the raw path, and
// code is the REST error code, so label cardinality stays bounded.
package metrics

import (
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
)

var (
	requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests processed by the service, by route and status.",
	}, []string{"service", "method", "route", "status"})

	durations = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latencies in seconds.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"service", "route"})

	errorsByCode = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_errors_total",
		Help: "REST error responses by error code.",
	}, []string{"service", "code"})

	codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

func init() {
	prometheus.MustRegister(requests, durations, errorsByCode)
}

// Handler serves the default registry for GET /metrics. Keep /metrics off the
// public gateway; only Prometheus should reach it.
func Handler() gin.HandlerFunc { return gin.WrapH(promhttp.Handler()) }

// Middleware records request count, latency and error-code metrics for service.
// Register it after ginhttp.Trace so error codes are visible.
func Middleware(service string) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		durations.WithLabelValues(service, route).Observe(time.Since(start).Seconds())
		requests.WithLabelValues(service, c.Request.Method, route, strconv.Itoa(c.Writer.Status())).Inc()

		if code := ginhttp.ErrorCode(c); code != "" {
			if !codePattern.MatchString(code) {
				code = "UNKNOWN" // never let a malformed code explode cardinality
			}
			errorsByCode.WithLabelValues(service, code).Inc()
		}
	}
}
