package document

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/windcry1/ai-companion/internal/arbitration"
	"github.com/windcry1/ai-companion/internal/semantic"
)

// Separate model contexts by exact source ACL/version dependencies. At most
// three groups receive model calls in one compile; remaining groups retain
// their disputes. Complete original records are never rewritten or removed.
func (s *Service) arbitrateWiki(ctx context.Context, pages []WikiPage, raw map[string][]WikiEvidence) {
	groups := map[string][]int{}
	for i, page := range pages {
		if len(page.Conflicts) == 0 {
			continue
		}
		dependencies := []string{}
		for _, source := range page.Sources {
			dependencies = append(dependencies, source.DocumentID+":"+source.Version)
		}
		sort.Strings(dependencies)
		key := strings.Join(dependencies, ",")
		groups[key] = append(groups[key], i)
	}
	keys := []string{}
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for n, key := range keys {
		batch := []WikiPage{}
		for _, i := range groups[key] {
			batch = append(batch, pages[i])
		}
		client := s.semantic
		if n >= 3 {
			client = nil
		}
		arbitrateWikiGroup(ctx, batch, raw, client)
		for j, i := range groups[key] {
			if n >= 3 && batch[j].Arbitration != nil {
				batch[j].Arbitration.Reason = "arbitration_group_budget"
			}
			pages[i] = batch[j]
		}
	}
}

func arbitrateWikiGroup(ctx context.Context, pages []WikiPage, raw map[string][]WikiEvidence, client *semantic.Client) {
	input := arbitration.Input{Kind: "wiki", Issues: []arbitration.Issue{}, Evidence: []arbitration.Evidence{}}
	byPage := map[string][]string{}
	evidence := map[string]arbitration.Evidence{}
	for _, page := range pages {
		versions := map[string]string{}
		for _, source := range page.Sources {
			versions[source.DocumentID] = source.Version
		}
		claims := map[string]map[string]arbitration.Candidate{}
		for _, e := range raw[page.ID] {
			if old, ok := evidence[e.ChunkID]; !ok || len(e.Quote) > len(old.Text) {
				evidence[e.ChunkID] = arbitration.Evidence{ID: e.ChunkID, Text: e.Quote, Version: versions[e.DocumentID]}
			}
			for _, m := range wikiClaim.FindAllStringSubmatch(e.Quote, -1) {
				field, value := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
				if claims[field] == nil {
					claims[field] = map[string]arbitration.Candidate{}
				}
				candidateID := deterministicUUID(page.ID + ":" + field + ":" + value + ":" + e.ChunkID)
				claims[field][value+":"+e.ChunkID] = arbitration.Candidate{ID: candidateID, Text: field + "：" + value, EvidenceIDs: []string{e.ChunkID}}
			}
		}
		fields := []string{}
		for field, candidates := range claims {
			values := map[string]bool{}
			for _, c := range candidates {
				values[c.Text] = true
			}
			if len(values) > 1 {
				fields = append(fields, field)
			}
		}
		sort.Strings(fields)
		for _, field := range fields {
			issue := arbitration.Issue{ID: deterministicUUID(page.ID + ":" + field), Label: field, Candidates: []arbitration.Candidate{}}
			for _, c := range claims[field] {
				issue.Candidates = append(issue.Candidates, c)
			}
			sort.Slice(issue.Candidates, func(i, j int) bool { return issue.Candidates[i].ID < issue.Candidates[j].ID })
			input.Issues = append(input.Issues, issue)
			byPage[page.ID] = append(byPage[page.ID], issue.ID)
		}
	}
	if len(input.Issues) == 0 {
		return
	}
	needed := map[string]bool{}
	for _, issue := range input.Issues {
		for _, c := range issue.Candidates {
			for _, id := range c.EvidenceIDs {
				needed[id] = true
			}
		}
	}
	for id := range needed {
		input.Evidence = append(input.Evidence, evidence[id])
	}
	sort.Slice(input.Evidence, func(i, j int) bool { return input.Evidence[i].ID < input.Evidence[j].ID })
	record := arbitration.Judge(ctx, client, input)
	for i := range pages {
		ids := map[string]bool{}
		for _, id := range byPage[pages[i].ID] {
			ids[id] = true
		}
		if len(ids) == 0 {
			continue
		}
		filtered := record
		filtered.Decisions = []arbitration.Decision{}
		filtered.Inputs = []arbitration.Reference{}
		refs := map[string]bool{}
		for _, issue := range input.Issues {
			if ids[issue.ID] {
				for _, c := range issue.Candidates {
					for _, id := range c.EvidenceIDs {
						refs[id] = true
					}
				}
			}
		}
		for _, ref := range record.Inputs {
			if refs[ref.ID] {
				filtered.Inputs = append(filtered.Inputs, ref)
			}
		}
		notes := "来源差异核对（保留各来源原始记录）：\n"
		for _, d := range record.Decisions {
			if !ids[d.IssueID] {
				continue
			}
			filtered.Decisions = append(filtered.Decisions, d)
			label := map[string]string{"equivalent": "等价表述", "scope_difference": "适用范围不同", "version_update": "来源明确了版本替代关系", "contradiction": "来源存在矛盾", "unresolved": "尚未解决"}[d.Classification]
			if d.Action == "retain" || d.Action == "need_evidence" {
				label = "保留争议，需补充核对"
			}
			notes += fmt.Sprintf("- %s：%s", label, d.Reason)
			for _, ref := range d.Evidence {
				notes += " [chunk:" + ref.ID + "]"
			}
			notes += "\n"
		}
		pages[i].Arbitration = &filtered
		pages[i].Body = notes + "\n" + pages[i].Body
	}
}
