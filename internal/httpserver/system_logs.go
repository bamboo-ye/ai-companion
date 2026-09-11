package httpserver

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/opslog"
)

func (s *Server) listOperatorSystemLogs(w http.ResponseWriter, r *http.Request) {
	filter, ok := operatorSystemLogFilter(w, r)
	if !ok {
		return
	}
	page, err := s.systemLogs.QuerySystemLogs(r.Context(), filter)
	if errors.Is(err, opslog.ErrValidation) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "日志筛选条件无效"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "system_logs_unavailable", Message: "系统日志暂时不可用"})
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func operatorSystemLogFilter(w http.ResponseWriter, r *http.Request) (opslog.Filter, bool) {
	query := r.URL.Query()
	filter := opslog.Filter{
		Service: strings.TrimSpace(query.Get("service")),
		Level:   strings.TrimSpace(query.Get("level")),
		Query:   strings.TrimSpace(query.Get("q")),
		TraceID: strings.TrimSpace(query.Get("trace_id")),
		RunID:   strings.TrimSpace(query.Get("run_id")),
		Limit:   queryLimit(r, 100),
	}
	var err error
	if value := strings.TrimSpace(query.Get("since")); value != "" {
		filter.Since, err = time.Parse(time.RFC3339, value)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "since 必须是 RFC3339 时间"})
			return opslog.Filter{}, false
		}
	}
	if value := strings.TrimSpace(query.Get("until")); value != "" {
		filter.Until, err = time.Parse(time.RFC3339, value)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "until 必须是 RFC3339 时间"})
			return opslog.Filter{}, false
		}
	}
	if value := strings.TrimSpace(query.Get("before_id")); value != "" {
		filter.BeforeID, err = strconv.ParseUint(value, 10, 64)
		if err != nil || filter.BeforeID == 0 {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "before_id 必须是正整数"})
			return opslog.Filter{}, false
		}
	}
	filter, err = opslog.NormalizeFilter(filter, time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "日志时间范围、级别或搜索条件无效"})
		return opslog.Filter{}, false
	}
	return filter, true
}
