// Package metrics Prometheus metriklerini üretir (GET /metrics): istek sayısı ve süre dağılımı.
//
// Etiket olarak ham URL değil chi'nin yol deseni ("/accounts/{id}") kullanılır; aksi halde her kimlik yeni bir seri doğurur
// ve metrik sayısı sınırsız büyür.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HTTP istek metriklerini tutar; kendi kayıt defteri vardır (global değişken yok).
type HTTP struct {
	reg      *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// New yeni bir HTTP metrik kümesi oluşturur; Go çalışma zamanı ve süreç metriklerini de ekler.
func New() *HTTP {
	h := &HTTP{
		reg: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "parakopru_http_requests_total", Help: "HTTP istek sayısı (yöntem, yol deseni, durum).",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "parakopru_http_request_duration_seconds", Help: "HTTP istek süresi (saniye).",
			Buckets: []float64{.001, .002, .005, .01, .02, .03, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"method", "route"}),
	}
	h.reg.MustRegister(h.requests, h.duration, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return h
}

// Handler GET /metrics yanıtıdır.
func (h *HTTP) Handler() http.Handler { return promhttp.HandlerFor(h.reg, promhttp.HandlerOpts{}) }

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Middleware her isteği sayar ve süresini ölçer. Yol deseni, istek işlendikten sonra (yönlendirme bittiğinde) okunur.
func (h *HTTP) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		h.requests.WithLabelValues(r.Method, route, strconv.Itoa(sw.status)).Inc()
		h.duration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}
