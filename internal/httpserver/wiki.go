package httpserver

import (
	"errors"
	"fmt"
	"github.com/windcry1/ai-companion/internal/document"
	"net/http"
)

func (s *Server) wikiScope(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	user := currentAuth(r).User.ID
	workspace := r.PathValue("workspace_id")
	if workspace != "" {
		if _, err := s.teams.RequireActiveMember(r.Context(), user, workspace); err != nil {
			writeTeamError(w, err)
			return "", "", false
		}
	}
	return user, workspace, true
}
func writeWikiError(w http.ResponseWriter, err error) {
	if errors.Is(err, document.ErrWikiConflict) {
		writeJSON(w, http.StatusConflict, apiError{Code: "wiki_version_conflict", Message: "页面或来源版本已变化，请刷新后重试"})
		return
	}
	writeDocumentError(w, err)
}
func (s *Server) listWiki(w http.ResponseWriter, r *http.Request) {
	user, workspace, ok := s.wikiScope(w, r)
	if !ok {
		return
	}
	pages, err := s.documents.ListWiki(r.Context(), user, workspace)
	if err != nil {
		writeWikiError(w, err)
		return
	}
	for i := range pages {
		pages[i].Body = ""
		pages[i].Evidence = nil
		if pages[i].Stale {
			pages[i].Title = "来源已更新，等待重新编译"
			pages[i].Links = nil
			pages[i].Conflicts = nil
		}
	}
	writeJSON(w, 200, map[string]any{"items": pages})
}
func (s *Server) readWiki(w http.ResponseWriter, r *http.Request) {
	user, workspace, ok := s.wikiScope(w, r)
	if !ok {
		return
	}
	p, err := s.documents.ReadWiki(r.Context(), user, workspace, r.PathValue("page_id"))
	if err != nil {
		writeWikiError(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) searchWiki(w http.ResponseWriter, r *http.Request) {
	user, workspace, ok := s.wikiScope(w, r)
	if !ok {
		return
	}
	var input struct {
		Query       string `json:"query"`
		Limit       int    `json:"limit"`
		TokenBudget int    `json:"token_budget"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Limit == 0 {
		input.Limit = 5
	}
	if input.TokenBudget == 0 {
		input.TokenBudget = 3000
	}
	if !s.reliability.Policy().UseFullRAG {
		writeJSON(w, 200, document.WikiSearchResult{Hits: []document.WikiSearchHit{}, Degraded: true})
		return
	}
	result, err := s.documents.SearchWiki(r.Context(), user, workspace, input.Query, input.Limit, input.TokenBudget)
	if err != nil {
		writeWikiError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) followWiki(w http.ResponseWriter, r *http.Request) {
	user, workspace, ok := s.wikiScope(w, r)
	if !ok {
		return
	}
	pages, err := s.documents.FollowWikiLinks(r.Context(), user, workspace, r.PathValue("page_id"), 4, 6000)
	if err != nil {
		writeWikiError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": pages})
}
func (s *Server) updateWiki(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version int    `json:"version"`
		Title   string `json:"title"`
		Body    string `json:"body"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	p, err := s.documents.UpdateWiki(r.Context(), currentAuth(r).User.ID, r.PathValue("page_id"), input.Version, input.Title, input.Body)
	if err != nil {
		writeWikiError(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) wikiHistory(w http.ResponseWriter, r *http.Request) {
	pages, err := s.documents.WikiHistory(r.Context(), currentAuth(r).User.ID, r.PathValue("page_id"))
	if err != nil {
		writeWikiError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": pages})
}
func (s *Server) rebuildWiki(w http.ResponseWriter, r *http.Request) {
	if err := s.documents.RebuildWiki(r.Context(), currentAuth(r).User.ID, r.PathValue("document_id")); err != nil {
		writeWikiError(w, err)
		return
	}
	writeJSON(w, 202, map[string]string{"status": "queued"})
}
func (s *Server) reindexDocument(w http.ResponseWriter, r *http.Request) {
	if err := s.documents.Reindex(r.Context(), currentAuth(r).User.ID, r.PathValue("document_id")); err != nil {
		writeWikiError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) wikiFeedback(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version int    `json:"version"`
		Rating  string `json:"rating"`
		Note    string `json:"note"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.documents.FeedbackWiki(r.Context(), currentAuth(r).User.ID, r.PathValue("page_id"), input.Version, input.Rating, input.Note); err != nil {
		writeWikiError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) exportWiki(w http.ResponseWriter, r *http.Request) {
	p, err := s.documents.ReadWiki(r.Context(), currentAuth(r).User.ID, "", r.PathValue("page_id"))
	if err != nil {
		writeWikiError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="wiki.md"`)
	fmt.Fprintf(w, "# %s\n\n%s\n\n## 来源\n", p.Title, p.Body)
	for _, e := range p.Evidence {
		fmt.Fprintf(w, "\n- [chunk:%s] 文档 %s，第 %d 页：%s\n", e.ChunkID, e.DocumentID, e.Page, e.Quote)
	}
}
func (s *Server) correctMemory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.memories.Correct(r.Context(), currentAuth(r).User.ID, r.PathValue("memory_id"), input.Content)
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, 201, item)
}

func (s *Server) contextMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := s.documents.ContextMetrics(r.Context())
	if err != nil {
		writeWikiError(w, err)
		return
	}
	writeJSON(w, 200, metrics)
}

func (s *Server) backfillContext(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Cursor string `json:"cursor"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := s.documents.BackfillContext(r.Context(), input.Cursor)
	if err != nil {
		writeWikiError(w, err)
		return
	}
	writeJSON(w, 202, result)
}

func (s *Server) resetWiki(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version int `json:"version"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.documents.ResetWiki(r.Context(), currentAuth(r).User.ID, r.PathValue("page_id"), input.Version); err != nil {
		writeWikiError(w, err)
		return
	}
	writeJSON(w, 202, map[string]string{"status": "queued"})
}
