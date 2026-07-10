package httpserver

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/windcry1/ai-companion/internal/billing"
	"github.com/windcry1/ai-companion/internal/document"
)

func (s *Server) uploadDocument(w http.ResponseWriter, r *http.Request) {
	if !s.requireQuota(w, r, billing.ResourceDocuments) {
		return
	}
	maxBytes := s.documents.MaxUploadBytes()
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Code: "file_too_large", Message: "文件超过上传大小限制"})
			return
		}
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_multipart", Message: "无法读取上传文件"})
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "file_required", Message: "请选择要上传的文件"})
		return
	}
	defer file.Close()
	data, err := readUpload(file, maxBytes)
	if err != nil {
		if errors.Is(err, errUploadTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Code: "file_too_large", Message: "文件超过上传大小限制"})
			return
		}
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_file", Message: "无法读取上传文件"})
		return
	}
	item, created, err := s.documents.Upload(r.Context(), currentAuth(r).User.ID, header.Filename, data)
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"document": item, "deduplicated": !created})
}

func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request) {
	items, err := s.documents.List(r.Context(), currentAuth(r).User.ID)
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getDocument(w http.ResponseWriter, r *http.Request) {
	item, err := s.documents.Get(r.Context(), currentAuth(r).User.ID, r.PathValue("document_id"))
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) queryDocuments(w http.ResponseWriter, r *http.Request) {
	var input document.QueryInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if !s.reliability.Policy().UseFullRAG {
		writeJSON(w, http.StatusOK, document.DegradedQueryResult("reliability_policy"))
		return
	}
	result, err := s.documents.Query(r.Context(), currentAuth(r).User.ID, input)
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request) {
	if err := s.documents.Delete(r.Context(), currentAuth(r).User.ID, r.PathValue("document_id")); err != nil {
		writeDocumentError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errUploadTooLarge = errors.New("upload too large")

func readUpload(file multipart.File, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errUploadTooLarge
	}
	return data, nil
}

func writeDocumentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, document.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "文档不存在"})
	case errors.Is(err, document.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	case errors.Is(err, document.ErrUnsupportedType):
		writeJSON(w, http.StatusUnsupportedMediaType, apiError{Code: "unsupported_file_type", Message: "首版仅支持文本 PDF 和 UTF-8 文本文件"})
	case errors.Is(err, document.ErrUnsafeContent):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "unsafe_file", Message: "文件未通过安全检查"})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "文档服务暂时不可用"})
	}
}
