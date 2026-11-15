package httpserver

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/config"
)

func (s *Server) mountHealthAssistant(mux *http.ServeMux, cfg config.Config) {
	target, _ := url.Parse(cfg.HealthAssistantURL)
	var proxy *httputil.ReverseProxy
	if target != nil && target.Host != "" && len(cfg.HealthAssistantToken) >= 32 {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.ResponseHeaderTimeout = 120 * time.Second
		proxy = &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(req *httputil.ProxyRequest) {
				req.SetURL(target)
				// The gateway authenticates each request. Never forward browser
				// cookies, tokens, spoofed identities, or other request headers.
				req.Out.Header = make(http.Header)
				for _, name := range []string{"Content-Type", "Last-Event-ID"} {
					if v := req.In.Header.Get(name); v != "" {
						req.Out.Header.Set(name, v)
					}
				}
				user := currentAuth(req.In).User
				req.Out.Header.Set("Authorization", "Bearer "+cfg.HealthAssistantToken)
				req.Out.Header.Set("X-Companion-User-ID", user.ID)
				req.Out.Header.Set("X-Companion-Timezone", user.Timezone)
			},
			ModifyResponse: func(response *http.Response) error {
				headers := make(http.Header)
				for _, name := range []string{"Content-Type", "Content-Disposition", "X-Request-ID", "X-Accel-Buffering"} {
					if v := response.Header.Get(name); v != "" {
						headers.Set(name, v)
					}
				}
				response.Header = headers
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				writeJSON(w, http.StatusBadGateway, apiError{Code: "health_unavailable", Message: "健康服务暂时无法连接，请稍后重试。"})
			},
		}
	}

	for _, route := range []string{
		"POST /v1/health/connect",
		"GET /v1/health/system/status",
		"GET /v1/health/state",
		"GET /v1/health/daily-summary",
		"GET /v1/health/signals",
		"GET /v1/health/conversation",
		"POST /v1/health/conversations/{id}/messages",
		"GET /v1/health/runs/{id}",
		"GET /v1/health/runs/{id}/events",
		"POST /v1/health/runs/{id}/cancel",
		"POST /v1/health/runs/{id}/retry",
		"POST /v1/health/actions/{id}/confirm",
		"POST /v1/health/actions/{id}/reject",
		"GET /v1/health/attachments/{id}",
		"POST /v1/health/attachments",
		"GET /v1/health/export",
		"POST /v1/health/records/clear",
	} {
		mux.Handle(route, s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if proxy == nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "health_disabled", Message: "健康助手尚未启用。"})
				return
			}
			if r.Method == http.MethodPost {
				limit := int64(2 << 20)
				if r.URL.Path == "/v1/health/attachments" {
					limit = 9 << 20
				}
				if r.ContentLength > limit {
					writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Code: "payload_too_large", Message: "请求内容过大。"})
					return
				}
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			if r.Pattern == "GET /v1/health/runs/{id}/events" {
				// The private stream can wait up to 110 seconds for a result.
				_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(130 * time.Second))
			}
			proxy.ServeHTTP(w, r)
		})))
	}
}
