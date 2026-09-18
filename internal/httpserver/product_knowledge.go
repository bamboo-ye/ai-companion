package httpserver

import (
	"errors"
	"net/http"

	"github.com/windcry1/ai-companion/internal/productknowledge"
)

func (s *Server) listBuiltinKnowledge(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"revision": productknowledge.Revision(), "items": productknowledge.List()})
}

func (s *Server) readBuiltinKnowledge(w http.ResponseWriter, r *http.Request) {
	p, err := productknowledge.Read(r.PathValue("page_id"))
	if err != nil {
		writeBuiltinKnowledgeError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+p.Version+`"`)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) searchBuiltinKnowledge(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Limit == 0 {
		input.Limit = 8
	}
	hits, err := productknowledge.Search(input.Query, input.Limit)
	if err != nil {
		writeBuiltinKnowledgeError(w, err)
		return
	}
	for i := range hits {
		body := []rune(hits[i].Page.Body)
		if len(body) > 240 {
			hits[i].Page.Body = string(body[:240]) + "…"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": productknowledge.Revision(), "hits": hits})
}

func writeBuiltinKnowledgeError(w http.ResponseWriter, err error) {
	if errors.Is(err, productknowledge.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, apiError{Code: "builtin_page_not_found", Message: "内置知识章节不存在，请重新搜索"})
		return
	}
	writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_failed", Message: "请输入 2–1000 字的问题，结果数量为 1–12"})
}
