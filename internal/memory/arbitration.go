package memory

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"github.com/windcry1/ai-companion/internal/arbitration"
)

// Candidate recall is deterministic and bounded. A judgment annotates a new
// memory; only the existing explicit correction API may replace an old memory.
func (s *Service) annotateMemory(ctx context.Context, item *Memory) error {
	items, err := s.store.ListMemories(ctx, item.UserID, 500)
	if err != nil {
		return err
	}
	type match struct {
		item  Memory
		score float64
	}
	matches := []match{}
	for _, old := range items {
		if old.ValidFrom.After(s.now()) || (old.ValidTo != nil && !old.ValidTo.After(s.now())) {
			continue
		}
		if old.NormalizedHash == item.NormalizedHash && old.ID != item.ID {
			item.Arbitration = nil
			return nil // The existing idempotent upsert keeps the original record.
		}
		if old.ID == item.ID {
			continue
		}
		if score := memorySimilarity(old.Content, item.Content); score >= .25 {
			matches = append(matches, match{old, score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			return matches[i].item.ID < matches[j].item.ID
		}
		return matches[i].score > matches[j].score
	})
	if len(matches) > 8 {
		matches = matches[:8]
	}
	if len(matches) == 0 {
		item.Arbitration = nil
		return nil
	}
	input := arbitration.Input{Kind: "memory", Issues: []arbitration.Issue{}, Evidence: []arbitration.Evidence{{ID: item.ID, Text: item.Content, Version: hash(item.Content)}}}
	for _, m := range matches {
		old := m.item
		input.Evidence = append(input.Evidence, arbitration.Evidence{ID: old.ID, Text: old.Content, Version: hash(old.Content)})
		input.Issues = append(input.Issues, arbitration.Issue{ID: old.ID, Label: "新旧记忆关系", Candidates: []arbitration.Candidate{{ID: old.ID, Text: old.Content, EvidenceIDs: []string{old.ID}}, {ID: item.ID, Text: item.Content, EvidenceIDs: []string{item.ID}}}})
	}
	record := arbitration.Judge(ctx, s.semantic, input)
	item.Arbitration = &record
	return nil
}

func memorySimilarity(a, b string) float64 {
	bigrams := func(text string) map[string]bool {
		runes := []rune(strings.ToLower(text))
		clean := []rune{}
		for _, r := range runes {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				clean = append(clean, r)
			}
		}
		out := map[string]bool{}
		for i := 1; i < len(clean); i++ {
			out[string(clean[i-1:i+1])] = true
		}
		return out
	}
	x, y := bigrams(a), bigrams(b)
	common := 0
	for k := range x {
		if y[k] {
			common++
		}
	}
	if common < 2 {
		return 0
	}
	return float64(common) / float64(len(x)+len(y)-common)
}

// Stale/deleted references invalidate the whole annotation. A model judgment
// never revives deleted memory text through its reasons or evidence quotes.
func (s *Service) visibleArbitration(ctx context.Context, item Memory) *arbitration.Record {
	if item.Arbitration == nil {
		return nil
	}
	for _, ref := range item.Arbitration.Inputs {
		if ref.ID == item.ID {
			if ref.Version != hash(item.Content) {
				return nil
			}
			continue
		}
		old, err := s.store.GetMemory(ctx, item.UserID, ref.ID)
		if err != nil || hash(old.Content) != ref.Version {
			return nil
		}
	}
	return item.Arbitration
}

func visibleArbitrationIn(item Memory, active map[string]Memory) *arbitration.Record {
	if item.Arbitration == nil {
		return nil
	}
	for _, ref := range item.Arbitration.Inputs {
		old, ok := active[ref.ID]
		if !ok || old.UserID != item.UserID || hash(old.Content) != ref.Version {
			return nil
		}
	}
	return item.Arbitration
}

func arbitrationNote(record *arbitration.Record) string {
	if record == nil {
		return ""
	}
	for _, d := range record.Decisions {
		if d.Action == "retain" || d.Action == "need_evidence" || d.Classification == "contradiction" || d.Classification == "version_update" || d.Action == "reject" {
			return "（该记忆与用户的其他记录可能存在差异，尚未通过明确纠正确认替代；请保留不确定性，不要把它视为唯一事实。）"
		}
	}
	return ""
}
