package httpserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/eventbus"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

func (s *Server) listDeadLetterOutboxEvents(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireOperationsStore(w)
	if !ok {
		return
	}
	records, err := store.ListDeadLetterOutboxEvents(r.Context(), queryLimit(r, 100))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "operations_unavailable", Message: "运维事件暂时不可用"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": records})
}

func (s *Server) getOutboxEvent(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireOperationsStore(w)
	if !ok {
		return
	}
	record, err := store.GetOutboxEvent(r.Context(), r.PathValue("event_id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "事件不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": record})
}

func (s *Server) replayOutboxEvent(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireOperationsStore(w)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" || len(reason) > 512 {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "reason 必填且不能超过 512 字符"})
		return
	}
	eventID := r.PathValue("event_id")
	now := time.Now().UTC()
	if err := store.ReplayOutboxEvent(r.Context(), eventID, now); err != nil {
		writeJSON(w, http.StatusConflict, apiError{Code: "conflict", Message: "只有 dead_letter 事件可以重放"})
		return
	}
	compensation, err := s.recordCompensation(r, store, eventbus.CompensationInput{
		SourceType: "outbox_event", SourceID: eventID, Action: "outbox.replay", Reason: reason, Status: "recorded",
		Metadata: json.RawMessage(`{"result":"replayed_to_pending"}`), CreatedAt: now,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "compensation_record_failed", Message: "事件已重放，但补偿记录写入失败"})
		return
	}
	record, _ := store.GetOutboxEvent(r.Context(), eventID)
	writeJSON(w, http.StatusAccepted, map[string]any{"event": record, "compensation": compensation})
}

func (s *Server) listCompensationRecords(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireOperationsStore(w)
	if !ok {
		return
	}
	records, err := store.ListCompensationRecords(r.Context(), queryLimit(r, 100))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "operations_unavailable", Message: "补偿记录暂时不可用"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"compensations": records})
}

func (s *Server) createCompensationRecord(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireOperationsStore(w)
	if !ok {
		return
	}
	var input struct {
		SourceType string          `json:"source_type"`
		SourceID   string          `json:"source_id"`
		Action     string          `json:"action"`
		Reason     string          `json:"reason"`
		Status     string          `json:"status"`
		Metadata   json.RawMessage `json:"metadata"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Status == "" {
		input.Status = "recorded"
	}
	if !validCompensationInput(input.SourceType, input.SourceID, input.Action, input.Reason, input.Status, input.Metadata) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "补偿记录字段无效"})
		return
	}
	record, err := s.recordCompensation(r, store, eventbus.CompensationInput{
		SourceType: strings.TrimSpace(input.SourceType), SourceID: strings.TrimSpace(input.SourceID), Action: strings.TrimSpace(input.Action),
		Reason: strings.TrimSpace(input.Reason), Status: input.Status, Metadata: input.Metadata, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "operations_unavailable", Message: "补偿记录写入失败"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"compensation": record})
}

func (s *Server) requireOperationsStore(w http.ResponseWriter) (eventbus.OperationsStore, bool) {
	if s.operations == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "operations_store_unavailable", Message: "运维存储未启用"})
		return nil, false
	}
	return s.operations, true
}

func (s *Server) recordCompensation(r *http.Request, store eventbus.OperationsStore, input eventbus.CompensationInput) (eventbus.CompensationRecord, error) {
	compensationID, err := id.New()
	if err != nil {
		return eventbus.CompensationRecord{}, err
	}
	input.ID = compensationID
	input.Actor = currentOperator(r).Actor
	if len(input.Metadata) == 0 {
		input.Metadata = json.RawMessage(`{}`)
	}
	return store.CreateCompensationRecord(r.Context(), input)
}

func queryLimit(r *http.Request, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > 500 {
		return 500
	}
	return value
}

func validCompensationInput(sourceType, sourceID, action, reason, status string, metadata json.RawMessage) bool {
	sourceType, sourceID, action, reason, status = strings.TrimSpace(sourceType), strings.TrimSpace(sourceID), strings.TrimSpace(action), strings.TrimSpace(reason), strings.TrimSpace(status)
	if sourceType == "" || sourceID == "" || action == "" || reason == "" || len(sourceType) > 64 || len(sourceID) > 191 || len(action) > 128 || len(reason) > 512 {
		return false
	}
	if status != "recorded" && status != "completed" && status != "failed" {
		return false
	}
	if len(metadata) == 0 {
		return true
	}
	var decoded any
	return json.Unmarshal(metadata, &decoded) == nil
}
