package httpserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
)

var operatorAgentRunStatuses = map[string]bool{
	"accepted": true, "queued": true, "running": true,
	"waiting_approval": true, "waiting_tool": true,
	"completed": true, "failed": true, "cancel_requested": true,
	"cancelled": true, "timed_out": true,
}

var operatorAgentModules = map[string]bool{"companion": true, "life": true, "work": true}

type operatorAgentRunCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func (s *Server) listOperatorAgentRuns(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireAgentOperationsStore(w)
	if !ok {
		return
	}
	filter, ok := operatorAgentRunFilter(w, r)
	if !ok {
		return
	}
	items, err := store.ListAgentRuns(r.Context(), filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "agent_operations_unavailable", Message: "Agent 运行数据暂时不可用"})
		return
	}
	for index := range items {
		items[index].ErrorMessage = truncateOperatorText(items[index].ErrorMessage, 300)
	}
	nextCursor := ""
	if len(items) == filter.Limit && len(items) > 0 {
		last := items[len(items)-1]
		nextCursor = encodeOperatorAgentRunCursor(operatorAgentRunCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "next_cursor": nextCursor,
	})
}

func (s *Server) getOperatorAgentRun(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireAgentOperationsStore(w)
	if !ok {
		return
	}
	runID := strings.TrimSpace(r.PathValue("run_id"))
	if !validOperatorUUID(runID) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "run_id 格式无效"})
		return
	}
	run, err := store.GetAgentRun(r.Context(), runID)
	if err != nil {
		writeOperatorAgentRunError(w, err)
		return
	}
	events, err := store.ListAgentRunEvents(r.Context(), runID, 0, 500)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "agent_operations_unavailable", Message: "Agent 事件数据暂时不可用"})
		return
	}
	tools, err := store.ListAgentToolCalls(r.Context(), runID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "agent_operations_unavailable", Message: "Agent 工具调用数据暂时不可用"})
		return
	}
	summaries, err := store.ListAgentRuns(r.Context(), agent.RunOperationsFilter{Query: runID, Limit: 1})
	if err != nil || len(summaries) == 0 {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "agent_operations_unavailable", Message: "Agent 运行摘要暂时不可用"})
		return
	}
	summary := summaries[0]
	summary.ErrorMessage = truncateOperatorText(summary.ErrorMessage, 300)
	for index := range tools {
		tools[index].ErrorMessage = truncateOperatorText(tools[index].ErrorMessage, 300)
	}
	detail := operatorAgentOutput(run.Output)
	detail["run"] = summary
	detail["events"] = operatorAgentEvents(events)
	detail["tool_calls"] = tools
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) requireAgentOperationsStore(w http.ResponseWriter) (agent.OperationsStore, bool) {
	if s.agentOperations == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_operations_unavailable", Message: "Agent 运维存储未启用"})
		return nil, false
	}
	return s.agentOperations, true
}

func operatorAgentRunFilter(w http.ResponseWriter, r *http.Request) (agent.RunOperationsFilter, bool) {
	query := r.URL.Query()
	status := strings.TrimSpace(query.Get("status"))
	module := strings.TrimSpace(query.Get("module"))
	errorCode := strings.TrimSpace(query.Get("error_code"))
	search := strings.TrimSpace(query.Get("q"))
	if status != "" && !operatorAgentRunStatuses[status] {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "status 筛选值无效"})
		return agent.RunOperationsFilter{}, false
	}
	if module != "" && !operatorAgentModules[module] {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "module 筛选值无效"})
		return agent.RunOperationsFilter{}, false
	}
	if len(errorCode) > 128 || len(search) > 128 {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "筛选值过长"})
		return agent.RunOperationsFilter{}, false
	}
	from, ok := operatorOptionalTime(w, query.Get("from"), "from")
	if !ok {
		return agent.RunOperationsFilter{}, false
	}
	to, ok := operatorOptionalTime(w, query.Get("to"), "to")
	if !ok {
		return agent.RunOperationsFilter{}, false
	}
	if from != nil && to != nil && from.After(*to) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "from 不能晚于 to"})
		return agent.RunOperationsFilter{}, false
	}
	filter := agent.RunOperationsFilter{
		Status: status, Module: module, ErrorCode: errorCode, Query: search,
		CreatedFrom: from, CreatedTo: to, Limit: queryLimitMax(r, 50, 200),
	}
	if encoded := strings.TrimSpace(query.Get("cursor")); encoded != "" {
		cursor, valid := decodeOperatorAgentRunCursor(encoded)
		if !valid {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "cursor 格式无效"})
			return agent.RunOperationsFilter{}, false
		}
		filter.CursorCreatedAt, filter.CursorID = &cursor.CreatedAt, cursor.ID
	}
	return filter, true
}

func operatorOptionalTime(w http.ResponseWriter, raw, field string) (*time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: field + " 必须是 RFC3339 时间"})
		return nil, false
	}
	parsed = parsed.UTC()
	return &parsed, true
}

func encodeOperatorAgentRunCursor(cursor operatorAgentRunCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeOperatorAgentRunCursor(value string) (operatorAgentRunCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) > 256 {
		return operatorAgentRunCursor{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cursor operatorAgentRunCursor
	if err = decoder.Decode(&cursor); err != nil || cursor.CreatedAt.IsZero() || !validOperatorUUID(cursor.ID) {
		return operatorAgentRunCursor{}, false
	}
	return cursor, true
}

func validOperatorUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func operatorAgentOutput(raw json.RawMessage) map[string]any {
	response := map[string]any{
		"graph": map[string]any{}, "budget": map[string]any{},
		"model_manifest": map[string]any{}, "model_calls": []any{}, "node_trace": []any{},
		"langfuse": map[string]any{"enabled": false},
	}
	var output map[string]any
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &output) != nil {
		return response
	}
	if graph, ok := output["graph"].(map[string]any); ok {
		response["graph"] = operatorSafeMap(graph, 0)
	}
	if budget, ok := output["budget"].(map[string]any); ok {
		response["budget"] = operatorSafeMap(budget, 0)
	}
	if model, ok := output["model"].(map[string]any); ok {
		if manifest, ok := model["manifest"].(map[string]any); ok {
			response["model_manifest"] = operatorSafeMap(manifest, 0)
		}
		if calls, ok := model["calls"].([]any); ok {
			response["model_calls"] = operatorSafeModelCalls(calls)
		}
	}
	if observability, ok := output["observability"].(map[string]any); ok {
		if trace, ok := observability["node_trace"].([]any); ok {
			response["node_trace"] = operatorSafeNodeTrace(trace)
		}
		if langfuse, ok := observability["langfuse"].(map[string]any); ok {
			clean := map[string]any{}
			for _, key := range []string{"enabled", "trace_id", "initialized", "capture_content", "environment"} {
				if value, exists := langfuse[key]; exists {
					clean[key] = operatorSafeValue(value, 0)
				}
			}
			response["langfuse"] = clean
		}
	}
	return response
}

func operatorSafeModelCalls(items []any) []map[string]any {
	allowed := map[string]bool{
		"graph_node": true, "role": true, "status": true, "provider": true,
		"requested_model": true, "returned_model": true, "prompt_tokens": true,
		"completion_tokens": true, "cached_tokens": true, "reasoning_tokens": true,
		"cost_micros": true, "latency_ms": true, "timeout_ms": true,
		"reasoning_effort": true, "error_status": true, "retryable": true,
		"retry_after": true, "contract_valid": true, "contract_error": true,
		"prompt_token_upper_bound": true,
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		value, ok := item.(map[string]any)
		if !ok {
			continue
		}
		clean := make(map[string]any)
		for key, entry := range value {
			if allowed[key] {
				clean[key] = operatorSafeValue(entry, 0)
			}
		}
		result = append(result, clean)
	}
	return result
}

func operatorSafeNodeTrace(items []any) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		value, ok := item.(map[string]any)
		if !ok {
			continue
		}
		clean := map[string]any{}
		for _, key := range []string{"node", "status", "duration_ms"} {
			if entry, exists := value[key]; exists {
				clean[key] = operatorSafeValue(entry, 0)
			}
		}
		if details, ok := value["details"].(map[string]any); ok {
			clean["details"] = operatorSafeMap(details, 0)
		}
		result = append(result, clean)
	}
	return result
}

func operatorAgentEvents(events []agent.Event) []map[string]any {
	allowed := map[string]bool{
		"revision": true, "source": true, "reason": true, "retry_kind": true,
		"error_code": true, "approved": true, "available_at": true, "at": true,
		"task_id": true, "status": true, "tool_name": true,
	}
	result := make([]map[string]any, 0, len(events))
	for _, event := range events {
		item := map[string]any{
			"id": event.ID, "sequence": event.Sequence, "type": event.Type,
			"created_at": event.CreatedAt,
		}
		var payload map[string]any
		if json.Unmarshal(event.Payload, &payload) == nil {
			metadata := make(map[string]any)
			for key, value := range payload {
				if allowed[key] {
					metadata[key] = operatorSafeValue(value, 0)
				}
			}
			if len(metadata) > 0 {
				item["metadata"] = metadata
			}
		}
		result = append(result, item)
	}
	return result
}

func operatorSafeMap(input map[string]any, depth int) map[string]any {
	if depth > 4 {
		return map[string]any{}
	}
	result := make(map[string]any)
	for key, value := range input {
		if operatorSensitiveKey(key) {
			continue
		}
		result[key] = operatorSafeValue(value, depth+1)
	}
	return result
}

func operatorSafeValue(value any, depth int) any {
	if depth > 4 {
		return nil
	}
	switch typed := value.(type) {
	case nil, bool, float64:
		return typed
	case string:
		return truncateOperatorText(typed, 300)
	case map[string]any:
		return operatorSafeMap(typed, depth+1)
	case []any:
		limit := len(typed)
		if limit > 50 {
			limit = 50
		}
		items := make([]any, 0, limit)
		for _, item := range typed[:limit] {
			items = append(items, operatorSafeValue(item, depth+1))
		}
		return items
	default:
		return nil
	}
}

func operatorSensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, fragment := range []string{"secret", "authorization", "api_key", "password", "confirmation_token", "user_message", "prompt", "response", "arguments", "result", "content", "email"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

func truncateOperatorText(value string, limit int) string {
	value = strings.TrimSpace(value)
	characters := []rune(value)
	if len(characters) <= limit {
		return value
	}
	return string(characters[:limit]) + "…"
}

func writeOperatorAgentRunError(w http.ResponseWriter, err error) {
	if errors.Is(err, agent.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, apiError{Code: "agent_run_not_found", Message: "Agent 运行不存在"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, apiError{Code: "agent_operations_unavailable", Message: "Agent 运行数据暂时不可用"})
}
