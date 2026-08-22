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
	DocumentID        string         `json:"document_id"`
	SourceFilename    string         `json:"source_filename"`
	MediaType         string         `json:"media_type"`
	Format            string         `json:"format"`
	ParserVersion     string         `json:"parser_version"`
	PageCount         int            `json:"page_count"`
	SelectedChunks    int            `json:"selected_chunk_count"`
	TotalChunks       int            `json:"total_chunk_count"`
	EstimatedTokens   int            `json:"estimated_token_count"`
	Text              string         `json:"text"`
	Truncated         bool           `json:"truncated"`
	RoundCount        int            `json:"round_count"`
	CompletedRounds   int            `json:"completed_rounds"`
	RoundStart        int            `json:"round_start"`
	NextRound         int            `json:"next_round"`
	HasMore           bool           `json:"has_more"`
	CoverageRatio     float64        `json:"coverage_ratio"`
	CleaningReport    CleaningReport `json:"cleaning_report"`
	Rounds            []ContextRound `json:"rounds"`
	SourceOverwritten bool           `json:"source_overwritten"`
}

// ContextRound is a lossless, ordered unit of document work. It deliberately
// carries only manifest data: the merged Text remains the single model-facing
// payload so round metadata does not duplicate large source content.
type ContextRound struct {
	RoundNo    int `json:"round_no"`
	ChunkStart int `json:"chunk_start"`
	ChunkEnd   int `json:"chunk_end"`
	ChunkCount int `json:"chunk_count"`
	TokenCount int `json:"token_count"`
}

type CleaningReport struct {
	PolicyVersion         string `json:"policy_version"`
	DuplicateLinesRemoved int    `json:"duplicate_lines_removed"`
	BlankLinesCollapsed   int    `json:"blank_lines_collapsed"`
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
		RoundCount: 1, CompletedRounds: 1,
		CoverageRatio:     float64(len(selected)) / float64(len(chunks)),
		CleaningReport:    CleaningReport{PolicyVersion: "document-cleaning-v1"},
		Rounds:            []ContextRound{{RoundNo: 1, ChunkStart: selected[0].Ordinal, ChunkEnd: selected[len(selected)-1].Ordinal, ChunkCount: len(selected), TokenCount: estimatedTokens}},
		SourceOverwritten: false,
	}, nil
}

// ReadParsedContextRounds processes an oversized document as ordered bounded
// rounds and merges the completed rounds without representative sampling. This
// is the correct retrieval mode for exhaustive downstream tasks such as
// "include every row in a presentation". maxRounds bounds the model-facing
// payload; callers must treat Truncated as an incomplete hard requirement.
func (s *Service) ReadParsedContextRounds(
	ctx context.Context,
	userID string,
	documentID string,
	maxTokensPerRound int,
	maxRounds int,
) (DocumentContext, error) {
	return s.ReadParsedContextRoundWindow(
		ctx, userID, documentID, maxTokensPerRound, 1, maxRounds,
	)
}

func (s *Service) ReadParsedContextRoundWindow(
	ctx context.Context,
	userID string,
	documentID string,
	maxTokensPerRound int,
	roundStart int,
	maxRounds int,
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
	if maxTokensPerRound <= 0 {
		maxTokensPerRound = 4_000
	}
	if maxRounds <= 0 {
		maxRounds = 4
	}
	if roundStart <= 0 {
		roundStart = 1
	}

	roundChunks := partitionContextRounds(chunks, maxTokensPerRound)
	startIndex := min(roundStart-1, len(roundChunks))
	if startIndex >= len(roundChunks) {
		return DocumentContext{}, fmt.Errorf("document context round %d is out of range", roundStart)
	}
	endIndex := min(len(roundChunks), startIndex+maxRounds)
	completedRounds := endIndex - startIndex
	selected := make([]Chunk, 0, len(chunks))
	manifest := make([]ContextRound, 0, completedRounds)
	blocks := make([]string, 0, completedRounds)
	report := CleaningReport{PolicyVersion: "document-cleaning-v1"}
	estimatedTokens := 0
	for index, items := range roundChunks[startIndex:endIndex] {
		roundBlocks := make([]string, 0, len(items))
		roundTokens := 0
		for _, chunk := range items {
			cleaned, duplicateLines, blankLines := cleanContextText(chunk.Content)
			report.DuplicateLinesRemoved += duplicateLines
			report.BlankLinesCollapsed += blankLines
			marker := fmt.Sprintf("[[PAGE %d]]", chunk.PageStart)
			if chunk.PageEnd > chunk.PageStart {
				marker = fmt.Sprintf("[[PAGES %d-%d]]", chunk.PageStart, chunk.PageEnd)
			}
			section := ""
			if strings.TrimSpace(chunk.SectionPath) != "" {
				section = "<!-- section: " + strings.TrimSpace(chunk.SectionPath) + " -->\n"
			}
			roundBlocks = append(roundBlocks, strings.TrimSpace(marker+"\n"+section+cleaned))
			cost := contextChunkTokens(chunk) + 16
			roundTokens += cost
			estimatedTokens += cost
			selected = append(selected, chunk)
		}
		blocks = append(blocks, strings.Join(roundBlocks, "\n\n"))
		manifest = append(manifest, ContextRound{
			RoundNo: startIndex + index + 1, ChunkStart: items[0].Ordinal,
			ChunkEnd: items[len(items)-1].Ordinal, ChunkCount: len(items),
			TokenCount: roundTokens,
		})
	}
	coverage := float64(len(selected)) / float64(len(chunks))
	return DocumentContext{
		DocumentID: item.ID, SourceFilename: item.Name, MediaType: item.MediaType,
		Format: "markdown", ParserVersion: item.ParserVersion, PageCount: item.PageCount,
		SelectedChunks: len(selected), TotalChunks: len(chunks), EstimatedTokens: estimatedTokens,
		Text: strings.Join(blocks, "\n\n"), Truncated: len(selected) < len(chunks),
		RoundCount: len(roundChunks), CompletedRounds: completedRounds,
		RoundStart: roundStart, NextRound: endIndex + 1, HasMore: endIndex < len(roundChunks),
		CoverageRatio:  coverage,
		CleaningReport: report, Rounds: manifest, SourceOverwritten: false,
	}, nil
}

func partitionContextRounds(chunks []Chunk, maxTokens int) [][]Chunk {
	result := make([][]Chunk, 0)
	current := make([]Chunk, 0)
	currentTokens := 0
	for _, chunk := range chunks {
		cost := contextChunkTokens(chunk) + 16
		if len(current) > 0 && currentTokens+cost > maxTokens {
			result = append(result, current)
			current = make([]Chunk, 0)
			currentTokens = 0
		}
		current = append(current, chunk)
		currentTokens += cost
	}
	if len(current) > 0 {
		result = append(result, current)
	}
	return result
}

func cleanContextText(value string) (string, int, int) {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	cleaned := make([]string, 0, len(lines))
	duplicateLines, blankLines := 0, 0
	previous, previousBlank := "", false
	for _, line := range lines {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		blank := strings.TrimSpace(line) == ""
		if blank && previousBlank {
			blankLines++
			continue
		}
		if !blank && line == previous {
			duplicateLines++
			continue
		}
		cleaned = append(cleaned, line)
		previous, previousBlank = line, blank
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n")), duplicateLines, blankLines
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
