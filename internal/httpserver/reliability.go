package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

type traceContextKey struct{}

func traceRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := strings.TrimSpace(r.Header.Get("X-Trace-ID"))
		if !validTraceID(traceID) {
			traceID, _ = id.New()
			if traceID == "" {
				traceID = fmt.Sprintf("fallback-%d", time.Now().UnixNano())
			}
		}
		w.Header().Set("X-Trace-ID", traceID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), traceContextKey{}, traceID)))
	})
}

func validTraceID(value string) bool {
	if len(value) < 16 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func requestTraceID(ctx context.Context) string {
	value, _ := ctx.Value(traceContextKey{}).(string)
	return value
}

type statusResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status, w.wroteHeader = status, true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) prometheusMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(s.metrics.Prometheus(s.reliability.Snapshot())))
}

func (s *Server) getReliability(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.reliability.Snapshot())
}
