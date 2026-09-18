package document

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var wikiClaim = regexp.MustCompile(`(?m)^([^：:\n]{1,40})[：:]([^\n]{1,120})$`)

// Recompute topic/entity/decision pages affected by a source job. Jobs are
// serialized per owner, so parallel documents cannot publish partial unions.
func (s *Service) aggregateWiki(ctx context.Context, user, documentID string, fresh []WikiPage) ([]WikiPage, error) {
	existing, err := s.ListWiki(ctx, user, "")
	if err != nil {
		return nil, err
	}
	candidates := append([]WikiPage{}, fresh...)
	for _, p := range existing {
		if p.Stale || strings.HasSuffix(p.Compiler, ":aggregate") {
			continue
		}
		replaced := false
		for _, source := range p.Sources {
			if source.DocumentID == documentID {
				replaced = true
			}
		}
		if !replaced {
			candidates = append(candidates, p)
		}
	}
	groups := map[string][]WikiPage{}
	for _, p := range candidates {
		if p.Kind == "source" {
			continue
		}
		key := p.Kind + ":" + strings.ToLower(strings.TrimSpace(p.Title))
		groups[key] = append(groups[key], p)
	}
	out := []WikiPage{}
	for key, pages := range groups {
		bySource := map[string]WikiSource{}
		for _, p := range pages {
			for _, source := range p.Sources {
				bySource[source.DocumentID] = source
			}
		}
		if len(bySource) < 2 {
			continue
		}
		sort.Slice(pages, func(i, j int) bool { return pages[i].ID < pages[j].ID })
		p := WikiPage{ID: deterministicUUID("wiki:aggregate:" + user + ":" + key), OwnerID: user, Kind: pages[0].Kind, Title: pages[0].Title, Compiler: WikiCompilerVersion + ":aggregate", UpdatedAt: s.now().UTC(), Sources: []WikiSource{}, Evidence: []WikiEvidence{}, Links: []string{}, Conflicts: []string{}}
		for _, source := range bySource {
			p.Sources = append(p.Sources, source)
		}
		sort.Slice(p.Sources, func(i, j int) bool { return p.Sources[i].DocumentID < p.Sources[j].DocumentID })
		claims := map[string]map[string]string{}
		for _, child := range pages {
			p.Links = append(p.Links, child.ID)
			for _, e := range child.Evidence {
				for _, match := range wikiClaim.FindAllStringSubmatch(e.Quote, -1) {
					claim := strings.TrimSpace(match[1])
					value := strings.TrimSpace(match[2])
					if claims[claim] == nil {
						claims[claim] = map[string]string{}
					}
					claims[claim][value] = e.ChunkID
				}
				if len(p.Evidence) < 12 {
					e.Quote = truncateRunes(e.Quote, 400)
					p.Evidence = append(p.Evidence, e)
					p.Body += fmt.Sprintf("- %s [chunk:%s]\n", e.Quote, e.ChunkID)
				}
			}
		}
		keys := []string{}
		for claim, values := range claims {
			if len(values) > 1 {
				keys = append(keys, claim)
			}
		}
		sort.Strings(keys)
		for _, claim := range keys {
			values := []string{}
			for value, chunk := range claims[claim] {
				values = append(values, fmt.Sprintf("%s [chunk:%s]", value, chunk))
			}
			sort.Strings(values)
			p.Conflicts = append(p.Conflicts, claim+" 的来源表述不同，需核对适用时间和范围："+strings.Join(values, "；"))
		}
		p.Body = "以下为各来源的记录，存在差异时应核对原文。\n\n" + p.Body
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
