package document

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestReadParsedContextUsesRepresentativeTokenBudget(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store, NewMemoryBlobStore(), 1<<20)
	item, created, err := service.Upload(ctx, "user-1", "report.txt", []byte("source"))
	if err != nil || !created {
		t.Fatalf("Upload() = %#v, %v, %v", item, created, err)
	}
	job, err := store.ClaimIngestJobByID(ctx, item.JobID, "worker-1", time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]Chunk, 10)
	pages := make([]Page, 10)
	for index := range chunks {
		page := index + 1
		pages[index] = Page{PageNo: page, Text: fmt.Sprintf("page %d", page), Quality: 1}
		chunks[index] = Chunk{
			Ordinal: page, PageStart: page, PageEnd: page, SectionPath: "Report",
			Content: fmt.Sprintf("Evidence from page %d", page), TokenCount: 700,
			ParserVersion: "pypdf-6.14.2-markdown-v2",
		}
	}
	result := ParseResult{ParserVersion: "pypdf-6.14.2-markdown-v2", Pages: pages, Chunks: chunks}
	if err = store.SaveParsedDocument(ctx, job, result, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteIngestJob(ctx, item.JobID, time.Now()); err != nil {
		t.Fatal(err)
	}

	parsed, err := service.ReadParsedContext(ctx, "user-1", item.ID, 2_000)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Truncated || parsed.SelectedChunks != 2 || parsed.TotalChunks != 10 {
		t.Fatalf("parsed context = %#v", parsed)
	}
	if parsed.EstimatedTokens > 2_000 {
		t.Fatalf("estimated tokens = %d", parsed.EstimatedTokens)
	}
	if !strings.Contains(parsed.Text, "[[PAGE 1]]") || !strings.Contains(parsed.Text, "[[PAGE 10]]") {
		t.Fatalf("representative pages missing: %s", parsed.Text)
	}
}

func TestOldChineseChunksUseConservativeContextEstimate(t *testing.T) {
	chunk := Chunk{
		Content: strings.Repeat("证据", 100), TokenCount: 50,
		ParserVersion: "pypdf-6.14.2-structural-v1",
	}
	if got := contextChunkTokens(chunk); got < 200 {
		t.Fatalf("contextChunkTokens() = %d", got)
	}
}

func TestReadParsedContextRoundsMergesSequentialCoverageAndCleaning(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store, NewMemoryBlobStore(), 1<<20)
	item, created, err := service.Upload(ctx, "user-1", "large-report.txt", []byte("source"))
	if err != nil || !created {
		t.Fatalf("Upload() = %#v, %v, %v", item, created, err)
	}
	job, err := store.ClaimIngestJobByID(ctx, item.JobID, "worker-1", time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]Chunk, 6)
	pages := make([]Page, 6)
	for index := range chunks {
		page := index + 1
		pages[index] = Page{PageNo: page, Text: fmt.Sprintf("page %d", page), Quality: 1}
		chunks[index] = Chunk{
			Ordinal: index, PageStart: page, PageEnd: page, SectionPath: "Rows",
			Content:    fmt.Sprintf("header\nheader\nrow-%d\n\n\nvalue-%d", page, page),
			TokenCount: 700, ParserVersion: "pypdf-6.14.2-markdown-v2",
		}
	}
	if err = store.SaveParsedDocument(ctx, job, ParseResult{
		ParserVersion: "pypdf-6.14.2-markdown-v2", Pages: pages, Chunks: chunks,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteIngestJob(ctx, item.JobID, time.Now()); err != nil {
		t.Fatal(err)
	}

	parsed, err := service.ReadParsedContextRounds(ctx, "user-1", item.ID, 1_500, 2)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.RoundCount != 3 || parsed.CompletedRounds != 2 || parsed.SelectedChunks != 4 || !parsed.Truncated {
		t.Fatalf("parsed context = %#v", parsed)
	}
	if parsed.CoverageRatio != 4.0/6.0 || len(parsed.Rounds) != 2 {
		t.Fatalf("coverage = %v rounds=%#v", parsed.CoverageRatio, parsed.Rounds)
	}
	if !strings.Contains(parsed.Text, "row-1") || !strings.Contains(parsed.Text, "row-4") || strings.Contains(parsed.Text, "row-5") {
		t.Fatalf("unexpected merged text: %s", parsed.Text)
	}
	if parsed.CleaningReport.DuplicateLinesRemoved != 4 || parsed.CleaningReport.BlankLinesCollapsed != 4 {
		t.Fatalf("cleaning report = %#v", parsed.CleaningReport)
	}
}
