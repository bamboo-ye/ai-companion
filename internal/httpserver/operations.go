package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/eventbus"
	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/opsauth"
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

func (s *Server) listPoisonMessages(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireOperationsStore(w)
	if !ok {
		return
	}
	records, err := store.ListPoisonMessages(r.Context(), queryLimit(r, 100))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "operations_unavailable", Message: "Kafka 脏消息暂时不可用"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": records})
}

func (s *Server) listEmailDeliveries(w http.ResponseWriter, r *http.Request) {
	items, err := s.emails.ListDeliveries(r.Context(), strings.TrimSpace(r.URL.Query().Get("status")), queryLimit(r, 100))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "邮件状态筛选无效"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": items})
}

func (s *Server) getEmailDelivery(w http.ResponseWriter, r *http.Request) {
	item, err := s.emails.GetDelivery(r.Context(), r.PathValue("delivery_id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "邮件发送任务不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivery": item})
}

func (s *Server) replayEmailDelivery(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
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
	deliveryID := r.PathValue("delivery_id")
	delivery, err := s.emails.ReplayDelivery(r.Context(), deliveryID)
	if err != nil {
		writeJSON(w, http.StatusConflict, apiError{Code: "conflict", Message: "只有 failed 邮件任务可以重放"})
		return
	}
	compensation, err := s.recordCompensation(r, store, eventbus.CompensationInput{
		SourceType: "email_delivery", SourceID: deliveryID, Action: "email.delivery.replay", Reason: reason, Status: "recorded",
		Metadata: json.RawMessage(`{"result":"replayed_to_queued"}`), CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "compensation_record_failed", Message: "邮件任务已重放，但补偿记录写入失败"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"delivery": delivery, "compensation": compensation})
}

func (s *Server) replayOutboxEvent(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
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
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
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

func (s *Server) listOperatorUsers(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireIdentityAdmin(w)
	if !ok {
		return
	}
	items, err := admin.ListUsers(r.Context(), identity.UserAccountFilter{
		Query: strings.TrimSpace(r.URL.Query().Get("q")), Status: strings.TrimSpace(r.URL.Query().Get("status")), Limit: queryLimit(r, 100),
	})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "用户筛选条件无效"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": items})
}

func (s *Server) getOperatorUser(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireIdentityAdmin(w)
	if !ok {
		return
	}
	user, err := admin.GetUser(r.Context(), r.PathValue("user_id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "用户不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (s *Server) disableOperatorUser(w http.ResponseWriter, r *http.Request) {
	s.moderateOperatorUser(w, r, "disable")
}

func (s *Server) enableOperatorUser(w http.ResponseWriter, r *http.Request) {
	s.moderateOperatorUser(w, r, "enable")
}

func (s *Server) moderateOperatorUser(w http.ResponseWriter, r *http.Request, action string) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	admin, ok := s.requireIdentityAdmin(w)
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
	var (
		user identity.User
		err  error
	)
	switch action {
	case "disable":
		user, err = admin.DisableUser(r.Context(), r.PathValue("user_id"), currentOperator(r).Actor, reason)
	case "enable":
		user, err = admin.EnableUser(r.Context(), r.PathValue("user_id"), currentOperator(r).Actor, reason)
	default:
		err = identity.ErrValidation
	}
	if err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "用户不存在"})
			return
		}
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (s *Server) listOperatorAccounts(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireOperatorAccountAdmin(w, r)
	if !ok {
		return
	}
	items, err := service.List(r.Context(), opsauth.OperatorFilter{
		Role: strings.TrimSpace(r.URL.Query().Get("role")), Status: strings.TrimSpace(r.URL.Query().Get("status")), Limit: queryLimit(r, 100),
	})
	if err != nil {
		writeOperatorAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operators": items})
}

func (s *Server) createOperatorAccount(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireOperatorAccountAdmin(w, r)
	if !ok {
		return
	}
	var input struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
		Token       string `json:"token"`
		TOTPSecret  string `json:"totp_secret"`
		MFAEnabled  bool   `json:"mfa_enabled"`
		Reason      string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := service.Create(r.Context(), opsauth.CreateInput{
		ID: input.ID, DisplayName: input.DisplayName, Role: input.Role, Token: input.Token, TOTPSecret: input.TOTPSecret, MFAEnabled: input.MFAEnabled,
		Actor: currentOperator(r).Actor, Reason: input.Reason,
	})
	if err != nil {
		writeOperatorAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) getOperatorAccount(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireOperatorAccountAdmin(w, r)
	if !ok {
		return
	}
	account, err := service.Get(r.Context(), r.PathValue("operator_id"))
	if err != nil {
		writeOperatorAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operator": account})
}

func (s *Server) disableOperatorAccount(w http.ResponseWriter, r *http.Request) {
	s.setOperatorAccountStatus(w, r, "disable")
}

func (s *Server) enableOperatorAccount(w http.ResponseWriter, r *http.Request) {
	s.setOperatorAccountStatus(w, r, "enable")
}

func (s *Server) setOperatorAccountStatus(w http.ResponseWriter, r *http.Request, action string) {
	service, ok := s.requireOperatorAccountAdmin(w, r)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var (
		account opsauth.Account
		err     error
	)
	switch action {
	case "disable":
		account, err = service.Disable(r.Context(), r.PathValue("operator_id"), currentOperator(r).Actor, input.Reason)
	case "enable":
		account, err = service.Enable(r.Context(), r.PathValue("operator_id"), currentOperator(r).Actor, input.Reason)
	default:
		err = opsauth.ErrValidation
	}
	if err != nil {
		writeOperatorAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operator": account})
}

func (s *Server) resetOperatorAccountToken(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireOperatorAccountAdmin(w, r)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := service.ResetToken(r.Context(), r.PathValue("operator_id"), currentOperator(r).Actor, input.Reason)
	if err != nil {
		writeOperatorAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) resetOperatorAccountMFA(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireOperatorAccountAdmin(w, r)
	if !ok {
		return
	}
	var input struct {
		Reason     string `json:"reason"`
		MFAEnabled *bool  `json:"mfa_enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	enabled := true
	if input.MFAEnabled != nil {
		enabled = *input.MFAEnabled
	}
	result, err := service.ResetMFA(r.Context(), r.PathValue("operator_id"), currentOperator(r).Actor, input.Reason, enabled)
	if err != nil {
		writeOperatorAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireIdentityAdmin(w)
	if !ok {
		return
	}
	records, err := admin.ListAuditLogs(r.Context(), identity.AuditLogFilter{
		ResourceType: strings.TrimSpace(r.URL.Query().Get("resource_type")),
		ResourceID:   strings.TrimSpace(r.URL.Query().Get("resource_id")),
		Limit:        queryLimit(r, 100),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "audit_unavailable", Message: "审计日志暂时不可用"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit_logs": records})
}

func (s *Server) requireIdentityAdmin(w http.ResponseWriter) (*identity.AdminService, bool) {
	if s.identityAdmin == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "identity_admin_unavailable", Message: "用户运维存储未启用"})
		return nil, false
	}
	return s.identityAdmin, true
}

func (s *Server) requireOperatorAccountAdmin(w http.ResponseWriter, r *http.Request) (*opsauth.Service, bool) {
	if !s.requireOperatorRole(w, r, "admin") {
		return nil, false
	}
	if s.operatorAuth == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "operator_accounts_unavailable", Message: "运维账号存储未启用"})
		return nil, false
	}
	return s.operatorAuth, true
}

func writeOperatorAccountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, opsauth.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "运维账号不存在"})
	case errors.Is(err, opsauth.ErrConflict):
		writeJSON(w, http.StatusConflict, apiError{Code: "conflict", Message: "运维账号或令牌已存在"})
	case errors.Is(err, opsauth.ErrForbidden):
		writeJSON(w, http.StatusForbidden, apiError{Code: "operator_forbidden", Message: "当前运维角色无权执行该操作"})
	case errors.Is(err, opsauth.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "运维账号字段无效，reason 必填且不能超过 512 字符"})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "operator_accounts_unavailable", Message: "运维账号服务暂时不可用"})
	}
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
