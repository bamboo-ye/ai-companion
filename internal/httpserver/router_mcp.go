package httpserver

import (
	"net/http"

	"github.com/windcry1/ai-companion/internal/router"
)

func (s *Server) routeIntent(w http.ResponseWriter, r *http.Request) {
	var input router.Input
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := s.intentRouter.Route(input)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listMCPServers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.mcp.Servers()})
}
