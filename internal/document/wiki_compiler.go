package document

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

func (s *Service) RunWikiCompiler(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		_, err := s.CompileNextWiki(ctx)
		if err != nil && ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
	}
}
func (s *Service) CompileNextWiki(ctx context.Context) (bool, error) {
	store, ok := s.store.(WikiStore)
	if !ok {
		return false, nil
	}
	job, err := store.ClaimWiki(ctx, s.now().UTC())
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A lease is ten minutes; bounded model requests and the deadline finish first.
	compileCtx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	pages, err := s.compileWiki(compileCtx, job)
	if err != nil {
		s.wikiRuntime.failures.Add(1)
		finishErr := store.FinishWiki(ctx, job, nil, "compile_failed", s.now().UTC())
		return true, errors.Join(err, finishErr)
	}
	err = store.FinishWiki(ctx, job, pages, "", s.now().UTC())
	if err == nil {
		s.wikiRuntime.compiles.Add(1)
	}
	return true, err
}
func (s *Service) compileWiki(ctx context.Context, job WikiJob) ([]WikiPage, error) {
	if !s.wikiEnabled {
		if _, ok := s.index.(*RolloutIndex); ok {
			err := s.Reindex(ctx, job.UserID, job.DocumentID)
			if errors.Is(err, ErrNotFound) {
				err = nil
			}
			return nil, err
		}
		return nil, nil
	}
	d, err := s.Get(ctx, job.UserID, job.DocumentID)
	if errors.Is(err, ErrNotFound) {
		return s.aggregateWiki(ctx, job.UserID, job.DocumentID, nil)
	}
	if err != nil {
		return nil, err
	}
	if d.Status != "ready" {
		return nil, ErrValidation
	}
	parsed, ok := s.store.(ParsedStore)
	if !ok {
		return nil, ErrValidation
	}
	chunks, err := parsed.ListDocumentChunks(ctx, job.UserID, d.ID, 100000)
	if err != nil {
		return nil, err
	}
	if len(chunks) != d.ChunkCount {
		return nil, fmt.Errorf("incomplete wiki source: %d/%d", len(chunks), d.ChunkCount)
	}
	if _, ok := s.index.(*RolloutIndex); ok {
		if err := s.Reindex(ctx, job.UserID, d.ID); err != nil {
			return nil, err
		}
	}
	source := WikiSource{d.ID, SourceVersion(d), d.Name}
	pages := []WikiPage{}
	sourceID := deterministicUUID("wiki:source:" + d.ID)
	makePage := func(kind, title, body, key string, evidence []WikiEvidence) WikiPage {
		return WikiPage{ID: deterministicUUID("wiki:" + d.ID + ":" + key), OwnerID: d.UserID, Kind: kind, Title: title, Body: body, Sources: []WikiSource{source}, Evidence: evidence, Links: []string{sourceID}, Conflicts: []string{}, Compiler: WikiCompilerVersion + ":extractive", UpdatedAt: s.now().UTC()}
	}
	// Every chunk receives a navigable page. Exhaustive coverage lives here and
	// in Source IR, independently of the summary/model input budget.
	for _, chunk := range chunks {
		title := chunk.SectionPath
		if title == "" {
			title = fmt.Sprintf("%s · 第 %d 页 · 片段 %d", d.Name, chunk.PageStart, chunk.Ordinal+1)
		}
		kind := "topic"
		if containsWiki(title, "决策", "决定", "Decision", "ADR") {
			kind = "decision"
		}
		if containsWiki(title, "人物", "组织", "实体", "Entity", "术语") {
			kind = "entity"
		}
		evidence := []WikiEvidence{{d.ID, chunk.ID, chunk.Content, chunk.PageStart}}
		pages = append(pages, makePage(kind, title, chunk.Content, "chunk:"+chunk.ID, evidence))
	}
	links := []string{}
	summaryEvidence := []WikiEvidence{}
	body := "# " + d.Name + "\n\n"
	for _, p := range pages {
		links = append(links, p.ID)
		if len(summaryEvidence) < 8 {
			ev := p.Evidence[0]
			ev.Quote = truncateRunes(ev.Quote, 360)
			summaryEvidence = append(summaryEvidence, ev)
			body += "- " + ev.Quote + "\n"
		}
	}
	sourcePage := makePage("source", d.Name, body, "source", summaryEvidence)
	sourcePage.ID = sourceID
	sourcePage.Links = links
	if s.semantic.SummariesEnabled() && len(chunks) > 0 {
		// Semantic synthesis uses a clearly delimited subset; source pages still link
		// every chunk. Additional generated pages cannot claim unseen citations.
		supplied := []WikiEvidence{}
		size := 0
		for _, chunk := range chunks {
			if size+len(chunk.Content) > 24000 {
				break
			}
			supplied = append(supplied, WikiEvidence{d.ID, chunk.ID, chunk.Content, chunk.PageStart})
			size += len(chunk.Content)
		}
		var output struct {
			Pages []struct {
				Kind      string   `json:"kind"`
				Title     string   `json:"title"`
				Body      string   `json:"body"`
				ChunkIDs  []string `json:"chunk_ids"`
				Conflicts []string `json:"conflicts"`
			} `json:"pages"`
		}
		err := s.semantic.JSON(ctx, `Create a source summary and useful topic/entity/decision wiki pages from supplied evidence. Return {"pages":[{"kind":"source|topic|entity|decision","title":"...","body":"Markdown with [chunk:ID] citations for every factual paragraph","chunk_ids":["ID"],"conflicts":["contradictory source claims, with chunk citations"]}]}. At most 8 pages. Only use supplied facts and exact chunk IDs. Clearly label unresolved conflicting claims. Do not infer which source is correct.`, supplied, 4000, &output)
		if err == nil && len(output.Pages) <= 8 {
			evidenceByID := map[string]WikiEvidence{}
			for _, e := range supplied {
				evidenceByID[e.ChunkID] = e
			}
			for _, generated := range output.Pages {
				if generated.Kind != "source" && generated.Kind != "topic" && generated.Kind != "entity" && generated.Kind != "decision" {
					continue
				}
				if len(generated.Title) == 0 || len([]rune(generated.Title)) > 160 || len(generated.Body) > 16000 || len(generated.ChunkIDs) == 0 {
					continue
				}
				evidence := []WikiEvidence{}
				valid := true
				for _, chunkID := range generated.ChunkIDs {
					e, ok := evidenceByID[chunkID]
					if !ok || !strings.Contains(generated.Body, "[chunk:"+chunkID+"]") {
						valid = false
						break
					}
					e.Quote = truncateRunes(e.Quote, 1000)
					evidence = append(evidence, e)
				}
				if !valid || !groundedParagraphs(generated.Body, generated.ChunkIDs) {
					continue
				}
				for _, conflict := range generated.Conflicts {
					if !groundedParagraphs(conflict, generated.ChunkIDs) {
						valid = false
					}
				}
				if !valid {
					continue
				}
				page := makePage(generated.Kind, generated.Title, generated.Body, "semantic:"+generated.Kind+":"+generated.Title, evidence)
				page.Conflicts = generated.Conflicts
				page.Compiler = WikiCompilerVersion + ":semantic"
				if generated.Kind == "source" {
					page.ID = sourceID
					page.Links = links
					sourcePage = page
				} else {
					pages = append(pages, page)
					sourcePage.Links = append(sourcePage.Links, page.ID)
				}
			}
		}
	}
	sourcePage.Links = []string{}
	for _, page := range pages {
		sourcePage.Links = append(sourcePage.Links, page.ID)
	}
	pages = append(pages, sourcePage)
	// The source can be deleted/reparsed during compilation. Re-read before commit;
	// every subsequent read also validates this dependency version.
	latest, err := s.Get(ctx, job.UserID, d.ID)
	if errors.Is(err, ErrNotFound) {
		return []WikiPage{}, nil
	}
	if err != nil {
		return nil, err
	}
	if latest.Status != "ready" || SourceVersion(latest) != source.Version {
		return nil, ErrWikiConflict
	}
	aggregates, err := s.aggregateWiki(ctx, job.UserID, job.DocumentID, pages)
	if err != nil {
		return nil, err
	}
	return append(pages, aggregates...), nil
}
func containsWiki(s string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(s, term) {
			return true
		}
	}
	return false
}

var wikiCitationPattern = regexp.MustCompile(`\[chunk:([^\]]+)\]`)

func groundedParagraphs(body string, ids []string) bool {
	allowed := map[string]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	for _, match := range wikiCitationPattern.FindAllStringSubmatch(body, -1) {
		if !allowed[match[1]] {
			return false
		}
	}
	for _, p := range strings.Split(body, "\n") {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		has := false
		for _, id := range ids {
			if strings.Contains(p, "[chunk:"+id+"]") {
				has = true
			}
		}
		if !has {
			return false
		}
	}
	return true
}
func sameWikiContent(a, b WikiPage) bool {
	a.Version = 0
	b.Version = 0
	a.UpdatedAt = time.Time{}
	b.UpdatedAt = time.Time{}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
