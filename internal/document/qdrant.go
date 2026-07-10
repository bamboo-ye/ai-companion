package document

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type QdrantIndex struct {
	baseURL    string
	collection string
	apiKey     string
	client     *http.Client
}

func NewQdrantIndex(baseURL, collection, apiKey string, timeout time.Duration) *QdrantIndex {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &QdrantIndex{
		baseURL: strings.TrimRight(baseURL, "/"), collection: collection, apiKey: apiKey,
		client: &http.Client{Timeout: timeout},
	}
}

func (q *QdrantIndex) Ensure(ctx context.Context) error {
	path := "/collections/" + url.PathEscape(q.collection)
	status, _, err := q.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("qdrant collection check status %d", status)
	}
	body := map[string]any{
		"vectors":        map[string]any{"dense": map[string]any{"size": 256, "distance": "Cosine"}},
		"sparse_vectors": map[string]any{"sparse": map[string]any{}},
		"metadata":       map[string]any{"embedding_version": EmbeddingVersion},
	}
	status, response, err := q.do(ctx, http.MethodPut, path, body)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusConflict {
		return fmt.Errorf("create qdrant collection status %d: %s", status, response)
	}
	return nil
}

func (q *QdrantIndex) Upsert(ctx context.Context, item Document, chunks []Chunk) error {
	points := make([]map[string]any, 0, len(chunks))
	for _, chunk := range chunks {
		dense, indices, values := vectorize(chunk.Content)
		points = append(points, map[string]any{
			"id": chunk.PointID,
			"vector": map[string]any{
				"dense":  dense,
				"sparse": map[string]any{"indices": indices, "values": values},
			},
			"payload": map[string]any{
				"user_id": item.UserID, "source_type": "document", "source_id": chunk.ID,
				"document_id": item.ID, "document_name": item.Name, "chunk_id": chunk.ID,
				"page_start": chunk.PageStart, "page_end": chunk.PageEnd,
				"section_path": chunk.SectionPath, "content": chunk.Content,
				"parser_version": chunk.ParserVersion, "embedding_version": chunk.EmbeddingVersion,
			},
		})
	}
	path := "/collections/" + url.PathEscape(q.collection) + "/points?wait=true"
	status, response, err := q.do(ctx, http.MethodPut, path, map[string]any{"points": points})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("upsert qdrant points status %d: %s", status, response)
	}
	return nil
}

func (q *QdrantIndex) Search(ctx context.Context, userID, query string, documentIDs []string, limit int) ([]SearchHit, error) {
	filter := map[string]any{"must": []any{map[string]any{"key": "user_id", "match": map[string]any{"value": userID}}}}
	if len(documentIDs) > 0 {
		filter["must"] = append(filter["must"].([]any), map[string]any{"key": "document_id", "match": map[string]any{"any": documentIDs}})
	}
	return q.searchWithFilter(ctx, query, filter, limit)
}

func (q *QdrantIndex) SearchDocuments(ctx context.Context, query string, documentIDs []string, limit int) ([]SearchHit, error) {
	if len(documentIDs) == 0 {
		return nil, nil
	}
	filter := map[string]any{"must": []any{map[string]any{"key": "document_id", "match": map[string]any{"any": documentIDs}}}}
	return q.searchWithFilter(ctx, query, filter, limit)
}

func (q *QdrantIndex) searchWithFilter(ctx context.Context, query string, filter map[string]any, limit int) ([]SearchHit, error) {
	dense, indices, values := vectorize(query)
	if len(indices) == 0 {
		return nil, nil
	}
	prefetchLimit := limit * 4
	if prefetchLimit < 20 {
		prefetchLimit = 20
	}
	body := map[string]any{
		"prefetch": []any{
			map[string]any{"query": map[string]any{"indices": indices, "values": values}, "using": "sparse", "filter": filter, "limit": prefetchLimit},
			map[string]any{"query": dense, "using": "dense", "filter": filter, "limit": prefetchLimit},
		},
		"query": map[string]any{"rrf": map[string]any{}},
		"limit": limit, "with_payload": true,
	}
	path := "/collections/" + url.PathEscape(q.collection) + "/points/query"
	status, response, err := q.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("query qdrant points status %d: %s", status, response)
	}
	var payload struct {
		Result struct {
			Points []struct {
				Score   float64 `json:"score"`
				Payload struct {
					ChunkID      string `json:"chunk_id"`
					DocumentID   string `json:"document_id"`
					DocumentName string `json:"document_name"`
					PageStart    int    `json:"page_start"`
					PageEnd      int    `json:"page_end"`
					SectionPath  string `json:"section_path"`
					Content      string `json:"content"`
				} `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}
	if err = json.Unmarshal([]byte(response), &payload); err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, len(payload.Result.Points))
	for _, point := range payload.Result.Points {
		hits = append(hits, SearchHit{
			ChunkID: point.Payload.ChunkID, DocumentID: point.Payload.DocumentID,
			DocumentName: point.Payload.DocumentName, PageStart: point.Payload.PageStart,
			PageEnd: point.Payload.PageEnd, SectionPath: point.Payload.SectionPath,
			Content: point.Payload.Content, Score: point.Score,
		})
	}
	return hits, nil
}

func (q *QdrantIndex) DeleteDocument(ctx context.Context, userID, documentID string) error {
	filter := map[string]any{"must": []any{
		map[string]any{"key": "user_id", "match": map[string]any{"value": userID}},
		map[string]any{"key": "document_id", "match": map[string]any{"value": documentID}},
	}}
	path := "/collections/" + url.PathEscape(q.collection) + "/points/delete?wait=true"
	status, response, err := q.do(ctx, http.MethodPost, path, map[string]any{"filter": filter})
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	if status != http.StatusOK {
		return fmt.Errorf("delete qdrant points status %d: %s", status, response)
	}
	return nil
}

func (q *QdrantIndex) do(ctx context.Context, method, path string, body any) (int, string, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, q.baseURL+path, reader)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if q.apiKey != "" {
		request.Header.Set("api-key", q.apiKey)
	}
	response, err := q.client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, "", err
	}
	return response.StatusCode, string(data), nil
}
