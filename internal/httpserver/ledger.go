package httpserver

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/ledger"
)

func (s *Server) createLedgerCandidate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Text     string `json:"text"`
		Timezone string `json:"timezone"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	auth := currentAuth(r)
	if strings.TrimSpace(input.Timezone) == "" {
		input.Timezone = auth.User.Timezone
	}
	item, err := s.ledger.ParseCandidate(r.Context(), auth.User.ID, "", input.Text, input.Timezone)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) confirmLedgerCandidate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Note string `json:"note"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	item, created, err := s.ledger.Confirm(r.Context(), currentAuth(r).User.ID, r.PathValue("candidate_id"), r.Header.Get("Idempotency-Key"), input.Note)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"entry": item, "deduplicated": !created})
}

func (s *Server) listLedgerEntries(w http.ResponseWriter, r *http.Request) {
	filter := ledger.EntryFilter{Direction: r.URL.Query().Get("direction"), Category: r.URL.Query().Get("category")}
	if value := r.URL.Query().Get("start"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeLedgerError(w, ledger.ErrValidation)
			return
		}
		filter.Start = &parsed
	}
	if value := r.URL.Query().Get("end"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeLedgerError(w, ledger.ErrValidation)
			return
		}
		filter.End = &parsed
	}
	if value := r.URL.Query().Get("limit"); value != "" {
		filter.Limit, _ = strconv.Atoi(value)
	}
	items, err := s.ledger.List(r.Context(), currentAuth(r).User.ID, filter)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getLedgerEntry(w http.ResponseWriter, r *http.Request) {
	item, err := s.ledger.Get(r.Context(), currentAuth(r).User.ID, r.PathValue("entry_id"))
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) updateLedgerEntry(w http.ResponseWriter, r *http.Request) {
	var input ledger.UpdateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.ledger.Update(r.Context(), currentAuth(r).User.ID, r.PathValue("entry_id"), input)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteLedgerEntry(w http.ResponseWriter, r *http.Request) {
	if err := s.ledger.Delete(r.Context(), currentAuth(r).User.ID, r.PathValue("entry_id")); err != nil {
		writeLedgerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ledgerMonthlySummary(w http.ResponseWriter, r *http.Request) {
	auth := currentAuth(r)
	timezone := r.URL.Query().Get("timezone")
	if timezone == "" {
		timezone = auth.User.Timezone
	}
	month := r.URL.Query().Get("month")
	if month == "" {
		location, err := time.LoadLocation(timezone)
		if err != nil {
			writeLedgerError(w, ledger.ErrValidation)
			return
		}
		month = time.Now().In(location).Format("2006-01")
	}
	result, err := s.ledger.Summary(r.Context(), auth.User.ID, month, timezone, r.URL.Query().Get("currency"))
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) exportLedger(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Month    string `json:"month"`
		Timezone string `json:"timezone"`
		Currency string `json:"currency"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	auth := currentAuth(r)
	if input.Timezone == "" {
		input.Timezone = auth.User.Timezone
	}
	job, created, err := s.ledger.QueueExport(r.Context(), auth.User.ID, r.Header.Get("Idempotency-Key"), input.Month, input.Timezone, input.Currency)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"export": job, "deduplicated": !created})
}

func (s *Server) getLedgerExport(w http.ResponseWriter, r *http.Request) {
	job, err := s.ledger.GetExport(r.Context(), currentAuth(r).User.ID, r.PathValue("export_id"))
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) downloadLedgerExport(w http.ResponseWriter, r *http.Request) {
	job, data, err := s.ledger.DownloadExport(r.Context(), currentAuth(r).User.ID, r.PathValue("export_id"))
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	w.Header().Set("Content-Type", job.MediaType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+job.FileName+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func writeLedgerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "账单或候选不存在"})
	case errors.Is(err, ledger.ErrConfirmation):
		writeJSON(w, http.StatusConflict, apiError{Code: "clarification_required", Message: "金额、方向和绝对日期确认完整后才能记账"})
	case errors.Is(err, ledger.ErrIdempotencyKey):
		writeJSON(w, http.StatusBadRequest, apiError{Code: "idempotency_key_required", Message: "该操作必须提供 Idempotency-Key"})
	case errors.Is(err, ledger.ErrIdempotencyReuse):
		writeJSON(w, http.StatusConflict, apiError{Code: "idempotency_key_reused", Message: "该 Idempotency-Key 已用于其他操作"})
	case errors.Is(err, ledger.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	case errors.Is(err, ledger.ErrExporterUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "ledger_export_unavailable", Message: "Excel 导出服务暂时不可用"})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "账本服务暂时不可用"})
	}
}
