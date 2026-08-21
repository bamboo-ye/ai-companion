package document

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

var ErrParsedContentUnavailable = errors.New("parsed document content unavailable")

type ParsedStore interface {
	ListDocumentChunks(context.Context, string, string, int) ([]Chunk, error)
}

type DocumentContext struct {
	DocumentID        string `json:"document_id"`
	SourceFilename    string `json:"source_filename"`
	MediaType         string `json:"media_type"`
	Format            string `json:"format"`
	ParserVersion     string `json:"parser_version"`
	PageCount         int    `json:"page_count"`
	SelectedChunks    int    `json:"selected_chunk_count"`
	TotalChunks       int    `json:"total_chunk_count"`
	EstimatedTokens   int    `json:"estimated_token_count"`
	Text              string `json:"text"`
	Truncated         bool   `json:"truncated"`
	SourceOverwritten bool   `json:"source_overwritten"`
}

func (s *Service) ReadParsedContext(
	ctx context.Context,
	userID string,
	documentID string,
	maxTokens int,
) (DocumentContext, error) {
	item, err := s.store.GetDocument(ctx, userID, documentID)
	if err != nil {
		return DocumentContext{}, err
	}
	if item.Status != "ready" {
		return DocumentContext{}, ErrParsedContentUnavailable
	}
	store, ok := s.store.(ParsedStore)
	if !ok {
		return DocumentContext{}, ErrParsedContentUnavailable
	}
	chunks, err := store.ListDocumentChunks(ctx, userID, documentID, 10_000)
	if err != nil {
		return DocumentContext{}, err
	}
	if len(chunks) == 0 {
		return DocumentContext{}, ErrParsedContentUnavailable
	}
	if maxTokens <= 0 {
		maxTokens = 4_000
	}
	selected := selectRepresentativeChunks(chunks, maxTokens)
	if len(selected) == 0 {
		return DocumentContext{}, ErrParsedContentUnavailable
	}
	blocks := make([]string, 0, len(selected))
	estimatedTokens := 0
	for _, chunk := range selected {
		marker := fmt.Sprintf("[[PAGE %d]]", chunk.PageStart)
		if chunk.PageEnd > chunk.PageStart {
			marker = fmt.Sprintf("[[PAGES %d-%d]]", chunk.PageStart, chunk.PageEnd)
		}
		section := ""
		if strings.TrimSpace(chunk.SectionPath) != "" {
			section = "<!-- section: " + strings.TrimSpace(chunk.SectionPath) + " -->\n"
		}
		blocks = append(blocks, strings.TrimSpace(marker+"\n"+section+chunk.Content))
		estimatedTokens += contextChunkTokens(chunk) + 16
	}
	return DocumentContext{
		DocumentID: item.ID, SourceFilename: item.Name, MediaType: item.MediaType,
		Format: "markdown", ParserVersion: item.ParserVersion, PageCount: item.PageCount,
		SelectedChunks: len(selected), TotalChunks: len(chunks), EstimatedTokens: estimatedTokens,
		Text: strings.Join(blocks, "\n\n"), Truncated: len(selected) < len(chunks),
		SourceOverwritten: false,
	}, nil
}

func selectRepresentativeChunks(chunks []Chunk, maxTokens int) []Chunk {
	if len(chunks) == 0 || maxTokens <= 0 {
		return nil
	}
	total := 0
	for _, chunk := range chunks {
		total += contextChunkTokens(chunk) + 16
	}
	if total <= maxTokens {
		return append([]Chunk(nil), chunks...)
	}
	slots := max(2, min(len(chunks), maxTokens/650))
	indices := make([]int, 0, slots)
	seen := map[int]bool{}
	positions := []int{0, len(chunks) - 1}
	for index := 1; index < slots-1; index++ {
		positions = append(positions, index*(len(chunks)-1)/(slots-1))
	}
	for _, position := range positions {
		if !seen[position] {
			seen[position] = true
			indices = append(indices, position)
		}
	}
	selected := make([]Chunk, 0, slots)
	usedTokens := 0
	add := func(chunk Chunk) bool {
		cost := contextChunkTokens(chunk) + 16
		if len(selected) > 0 && usedTokens+cost > maxTokens {
			return false
		}
		selected = append(selected, chunk)
		usedTokens += cost
		return true
	}
	for _, index := range indices {
		_ = add(chunks[index])
	}
	selectedOrdinals := map[int]bool{}
	for _, chunk := range selected {
		selectedOrdinals[chunk.Ordinal] = true
	}
	for _, chunk := range chunks {
		if selectedOrdinals[chunk.Ordinal] {
			continue
		}
		if add(chunk) {
			selectedOrdinals[chunk.Ordinal] = true
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Ordinal < selected[j].Ordinal })
	return selected
}

func contextChunkTokens(chunk Chunk) int {
	estimate := chunk.TokenCount
	if estimate < 1 {
		estimate = 1
	}
	if strings.Contains(chunk.ParserVersion, "structural-v1") {
		estimate = max(estimate, conservativeTextTokens(chunk.Content))
	}
	return estimate
}

func conservativeTextTokens(value string) int {
	total, asciiRun := 0, 0
	flushASCII := func() {
		if asciiRun > 0 {
			total += (asciiRun + 3) / 4
			asciiRun = 0
		}
	}
	for _, char := range value {
		switch {
		case unicode.IsSpace(char):
			flushASCII()
		case char <= unicode.MaxASCII && (unicode.IsLetter(char) || unicode.IsDigit(char)):
			asciiRun++
		default:
			flushASCII()
			total++
		}
	}
	flushASCII()
	return max(1, total)
}
