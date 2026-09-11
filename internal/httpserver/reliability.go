package httpserver

import (
	"context"
	"net/http"

	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

func traceRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := tracectx.ExtractHTTP(r.Context(), r.Header)
		tracectx.InjectHTTP(ctx, w.Header())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validTraceID(value string) bool {
	return tracectx.ValidInput(value)
}

func requestTraceID(ctx context.Context) string {
	return tracectx.ID(ctx)
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
