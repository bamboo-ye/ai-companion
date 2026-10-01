package httpserver

import (
	"context"
	"errors"
	"github.com/windcry1/ai-companion/internal/knowledge"
	"github.com/windcry1/ai-companion/internal/knowledgebridge"
	"github.com/windcry1/ai-companion/internal/semantic"
	"net/http"
)

func (s *Server) configureKnowledge(client *semantic.Client) {
	s.knowledgeClient = client
	s.knowledge = knowledgebridge.New(s.documents, s.memories, client, func(ctx context.Context, scope knowledge.Scope) error {
		if scope.WorkspaceID != "" {
			if _, err := s.teams.RequireActiveMember(ctx, scope.UserID, scope.WorkspaceID); err != nil {
				return knowledge.ErrForbidden
			}
		}
		return nil
	})
}
func (s *Server) knowledgeScope(r *http.Request, workspace string) knowledge.Scope {
	return knowledge.Scope{UserID: currentAuth(r).User.ID, WorkspaceID: workspace, Module: "work"}
}
func writeKnowledgeError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	message := "知识服务暂时不可用，请稍后重试"
	switch {
	case errors.Is(err, knowledge.ErrValidation):
		status = 422
		message = "知识查询参数无效或模式尚不支持"
	case errors.Is(err, knowledge.ErrForbidden):
		status = 403
		message = "没有此知识范围的访问权限"
	case errors.Is(err, knowledge.ErrNotFound):
		status = 404
		message = "来源已不可用"
	case errors.Is(err, knowledge.ErrConflict):
		status = 409
		message = "来源已经更新，请重新搜索"
	}
	writeKnowledgeJSON(w, status, apiError{Code: knowledgebridge.ErrorCode(err), Message: message})
}
func (s *Server) searchKnowledge(w http.ResponseWriter, r *http.Request) {
	var in knowledge.Query
	if !decodeJSON(w, r, &in) {
		return
	}
	if !s.reliability.Policy().UseFullRAG {
		// Do not silently change an explicitly selected knowledge scope.
		writeKnowledgeJSON(w, http.StatusServiceUnavailable, apiError{Code: "knowledge_degraded", Message: "知识检索暂处于降级保护，请稍后重试"})
		return
	}
	result, err := s.knowledge.Search(r.Context(), s.knowledgeScope(r, in.WorkspaceID), in)
	if err != nil {
		writeKnowledgeError(w, err)
		return
	}
	writeKnowledgeJSON(w, 200, result)
}
func (s *Server) readKnowledge(w http.ResponseWriter, r *http.Request) {
	var in knowledge.ReadInput
	if !decodeJSON(w, r, &in) {
		return
	}
	result, err := s.knowledge.Read(r.Context(), s.knowledgeScope(r, in.WorkspaceID), in)
	if err != nil {
		writeKnowledgeError(w, err)
		return
	}
	writeKnowledgeJSON(w, 200, result)
}
func (s *Server) validateKnowledge(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WorkspaceID string               `json:"workspace_id"`
		Evidence    []knowledge.Evidence `json:"evidence"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.knowledge.Validate(r.Context(), s.knowledgeScope(r, in.WorkspaceID), in.Evidence); err != nil {
		writeKnowledgeError(w, err)
		return
	}
	writeKnowledgeJSON(w, 200, map[string]any{"valid": true, "version": knowledge.Version})
}
func (s *Server) traceKnowledge(w http.ResponseWriter, r *http.Request) {
	var in knowledge.ReadInput
	if !decodeJSON(w, r, &in) {
		return
	}
	refs, err := s.knowledge.Trace(r.Context(), s.knowledgeScope(r, in.WorkspaceID), in.Ref)
	if err != nil {
		writeKnowledgeError(w, err)
		return
	}
	writeKnowledgeJSON(w, 200, map[string]any{"sources": refs, "limit": 49})
}
func (s *Server) knowledgeMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := s.documents.ContextMetrics(r.Context())
	if err != nil {
		writeKnowledgeError(w, err)
		return
	}
	stats, err := s.knowledgeClient.ProjectionStore().Stats(r.Context())
	if err != nil {
		writeKnowledgeError(w, err)
		return
	}
	metrics["projections"] = stats
	metrics["index_spec"] = s.knowledgeClient.IndexSpec()
	metrics["contract"] = knowledge.Version
	writeKnowledgeJSON(w, 200, metrics)
}
func (s *Server) retryKnowledge(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	n, err := s.knowledgeClient.ProjectionStore().Retry(r.Context())
	if err != nil {
		writeKnowledgeError(w, err)
		return
	}
	writeKnowledgeJSON(w, 202, map[string]any{"retried": n})
}
func (s *Server) rebuildKnowledge(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") {
		return
	}
	n, err := s.knowledgeClient.ProjectionStore().Rebuild(r.Context(), []string{s.knowledgeClient.ProjectionSpec(), s.documents.DocumentIndexSpec()})
	if err != nil {
		writeKnowledgeError(w, err)
		return
	}
	writeKnowledgeJSON(w, 202, map[string]any{"requeued": n})
}

func (s *Server) feedbackKnowledge(w http.ResponseWriter, r *http.Request) {
	var in knowledge.Feedback
	if !decodeJSON(w, r, &in) {
		return
	}
	saved, err := s.knowledge.Feedback(r.Context(), s.knowledgeScope(r, in.WorkspaceID), in)
	if err != nil {
		writeKnowledgeError(w, err)
		return
	}
	writeKnowledgeJSON(w, 201, saved)
}

func (s *Server) pendingKnowledgeFeedback(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	items, err := s.knowledge.PendingFeedback(r.Context())
	if err != nil {
		writeKnowledgeError(w, err)
		return
	}
	writeKnowledgeJSON(w, 200, map[string]any{"items": items, "limit": 100})
}
func (s *Server) resolveKnowledgeFeedback(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	var in struct {
		Resolution string `json:"resolution"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.knowledge.ResolveFeedback(r.Context(), r.PathValue("feedback_id"), currentOperator(r).Actor, in.Resolution); err != nil {
		writeKnowledgeError(w, err)
		return
	}
	writeKnowledgeJSON(w, 200, map[string]bool{"resolved": true})
}

func writeKnowledgeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, value)
}
