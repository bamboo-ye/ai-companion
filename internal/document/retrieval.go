package document

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"unicode"
)

type SearchHit struct {
	ChunkID      string
	DocumentID   string
	DocumentName string
	PageStart    int
	PageEnd      int
	SectionPath  string
	Content      string
	Score        float64
}

type QueryInput struct {
	Query       string   `json:"query"`
	DocumentIDs []string `json:"document_ids"`
	Limit       int      `json:"limit"`
}

type Citation struct {
	ChunkID      string  `json:"chunk_id"`
	DocumentID   string  `json:"document_id"`
	DocumentName string  `json:"document_name"`
	PageStart    int     `json:"page_start"`
	PageEnd      int     `json:"page_end"`
	SectionPath  string  `json:"section_path,omitempty"`
	Quote        string  `json:"quote"`
	Score        float64 `json:"score"`
}

type QueryResult struct {
	Answer            string     `json:"answer"`
	Sufficient        bool       `json:"sufficient"`
	Citations         []Citation `json:"citations"`
	Degraded          bool       `json:"degraded,omitempty"`
	DegradationReason string     `json:"degradation_reason,omitempty"`
}

func (s *Service) Query(ctx context.Context, userID string, input QueryInput) (QueryResult, error) {
	query := strings.TrimSpace(input.Query)
	if len([]rune(query)) < 2 || len([]rune(query)) > 1000 {
		return QueryResult{}, fmt.Errorf("%w: query must contain 2-1000 characters", ErrValidation)
	}
	limit := input.Limit
	if limit <= 0 || limit > 8 {
		limit = 5
	}
	items, err := s.store.ListDocuments(ctx, userID, 200)
	if err != nil {
		return QueryResult{}, err
	}
	requested := map[string]bool{}
	for _, documentID := range input.DocumentIDs {
		requested[documentID] = true
	}
	readyIDs := make([]string, 0)
	for _, item := range items {
		if item.Status == "ready" && (len(requested) == 0 || requested[item.ID]) {
			readyIDs = append(readyIDs, item.ID)
		}
	}
	if len(readyIDs) == 0 {
		return insufficientQueryResult(), nil
	}
	hits, err := s.index.Search(ctx, userID, query, readyIDs, limit*3)
	if err != nil {
		return QueryResult{}, err
	}
	citations := make([]Citation, 0, limit)
	for _, hit := range hits {
		item, getErr := s.store.GetDocument(ctx, userID, hit.DocumentID)
		if getErr != nil || item.Status != "ready" || tokenOverlap(query, hit.Content) == 0 {
			continue
		}
		citations = append(citations, Citation{
			ChunkID: hit.ChunkID, DocumentID: hit.DocumentID, DocumentName: hit.DocumentName,
			PageStart: hit.PageStart, PageEnd: hit.PageEnd, SectionPath: hit.SectionPath,
			Quote: truncateRunes(hit.Content, 360), Score: hit.Score,
		})
		if len(citations) >= limit {
			break
		}
	}
	if len(citations) == 0 {
		return insufficientQueryResult(), nil
	}
	top := citations[0]
	page := fmt.Sprintf("第 %d 页", top.PageStart)
	if top.PageEnd > top.PageStart {
		page = fmt.Sprintf("第 %d–%d 页", top.PageStart, top.PageEnd)
	}
	answer := fmt.Sprintf("根据文档《%s》%s：%s", top.DocumentName, page, top.Quote)
	return QueryResult{Answer: answer, Sufficient: true, Citations: citations}, nil
}

func insufficientQueryResult() QueryResult {
	return QueryResult{Answer: "现有文档中没有足够证据回答这个问题。", Sufficient: false, Citations: []Citation{}}
}

func DegradedQueryResult(reason string) QueryResult {
	return QueryResult{Answer: "当前系统处于降级保护中，暂时跳过文档检索；你的问题没有丢失，请稍后重试。", Sufficient: false, Citations: []Citation{}, Degraded: true, DegradationReason: reason}
}

func vectorize(value string) ([]float32, []uint32, []float32) {
	tokens := retrievalTokens(value)
	dense := make([]float32, 256)
	sparseValues := map[uint32]float32{}
	for _, token := range tokens {
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(token))
		value := hash.Sum32()
		index := value % uint32(len(dense))
		sign := float32(1)
		if value&(1<<31) != 0 {
			sign = -1
		}
		dense[index] += sign
		sparseValues[value&0x7fffffff]++
	}
	var norm float64
	for _, value := range dense {
		norm += float64(value * value)
	}
	if norm > 0 {
		scale := float32(1 / math.Sqrt(norm))
		for index := range dense {
			dense[index] *= scale
		}
	}
	indices := make([]uint32, 0, len(sparseValues))
	for index := range sparseValues {
		indices = append(indices, index)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	values := make([]float32, len(indices))
	for position, index := range indices {
		values[position] = 1 + float32(math.Log(float64(sparseValues[index])))
	}
	return dense, indices, values
}

func retrievalTokens(value string) []string {
	value = strings.ToLower(value)
	words := strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	tokens := make([]string, 0)
	for _, word := range words {
		runes := []rune(word)
		hasHan := false
		for _, r := range runes {
			if unicode.Is(unicode.Han, r) {
				hasHan = true
				break
			}
		}
		if hasHan {
			if len(runes) > 1 {
				tokens = append(tokens, word)
			}
			for index := 0; index+1 < len(runes); index++ {
				tokens = append(tokens, string(runes[index:index+2]))
			}
			continue
		}
		if len(runes) >= 3 && !englishStopWords[word] {
			tokens = append(tokens, word)
		}
	}
	return tokens
}

var englishStopWords = map[string]bool{
	"and": true, "are": true, "but": true, "does": true, "for": true, "from": true,
	"has": true, "have": true, "how": true, "into": true, "not": true, "the": true,
	"this": true, "was": true, "what": true, "when": true, "where": true, "which": true,
	"who": true, "why": true, "with": true,
}

func tokenOverlap(left, right string) int {
	rightTokens := map[string]bool{}
	for _, token := range retrievalTokens(right) {
		rightTokens[token] = true
	}
	count := 0
	seen := map[string]bool{}
	for _, token := range retrievalTokens(left) {
		if rightTokens[token] && !seen[token] {
			seen[token] = true
			count++
		}
	}
	return count
}

func truncateRunes(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
