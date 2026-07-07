package document

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestQdrantIndexLifecycleAndTenantFilter(t *testing.T) {
	collectionExists := false
	requests := map[string]map[string]any{}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		key := request.Method + " " + request.URL.Path
		var body map[string]any
		if request.Body != nil {
			_ = json.NewDecoder(request.Body).Decode(&body)
		}
		requests[key] = body
		status := http.StatusOK
		response := `{"status":"ok","result":true}`
		switch {
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/collections/"):
			if !collectionExists {
				status = http.StatusNotFound
				response = `{"status":"error"}`
			}
		case request.Method == http.MethodPut && request.URL.Path == "/collections/test_chunks":
			collectionExists = true
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/points/query"):
			response = `{"status":"ok","result":{"points":[{"score":0.9,"payload":{"chunk_id":"chunk-1","document_id":"doc-1","document_name":"report.pdf","page_start":2,"page_end":2,"section_path":"结论","content":"火星计划在四月启动。"}}]}}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})

	index := NewQdrantIndex("http://qdrant.test", "test_chunks", "", time.Second)
	index.client.Transport = transport
	ctx := context.Background()
	if err := index.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	chunks := []Chunk{{ID: "chunk-1", PointID: "2c1743a3-9130-5fbf-b67d-f8e4f069f9f9", PageStart: 2, PageEnd: 2, SectionPath: "结论", Content: "火星计划在四月启动。", ParserVersion: "p1", EmbeddingVersion: EmbeddingVersion}}
	if err := index.Upsert(ctx, Document{ID: "doc-1", UserID: "user-1", Name: "report.pdf"}, chunks); err != nil {
		t.Fatal(err)
	}
	hits, err := index.Search(ctx, "user-1", "火星计划何时启动", []string{"doc-1"}, 5)
	if err != nil || len(hits) != 1 || hits[0].PageStart != 2 {
		t.Fatalf("search = %#v, %v", hits, err)
	}
	if err = index.DeleteDocument(ctx, "user-1", "doc-1"); err != nil {
		t.Fatal(err)
	}

	queryBody := requests["POST /collections/test_chunks/points/query"]
	upsertBody := requests["PUT /collections/test_chunks/points"]
	deleteBody := requests["POST /collections/test_chunks/points/delete"]
	encodedQuery, _ := json.Marshal(queryBody)
	encodedUpsert, _ := json.Marshal(upsertBody)
	encodedDelete, _ := json.Marshal(deleteBody)
	for label, encoded := range map[string][]byte{"query": encodedQuery, "upsert": encodedUpsert, "delete": encodedDelete} {
		text := string(encoded)
		if !strings.Contains(text, "user-1") {
			t.Fatalf("%s request lacks tenant filter/payload: %s", label, text)
		}
	}
	if !strings.Contains(string(encodedQuery), `"rrf"`) || !strings.Contains(string(encodedQuery), `"sparse"`) || !strings.Contains(string(encodedQuery), `"dense"`) {
		t.Fatalf("query is not dense/sparse RRF: %s", encodedQuery)
	}
}
