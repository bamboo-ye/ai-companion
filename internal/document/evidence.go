package document

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/windcry1/ai-companion/internal/productknowledge"
)

// EvidenceReference is a raw excerpt previously returned by a read tool. This
// endpoint only validates it; it cannot retrieve text or expand access.
type EvidenceReference struct {
	SourceID string `json:"source_id"`
	Version  string `json:"version"`
	ChunkID  string `json:"chunk_id"`
	Text     string `json:"text"`
}

// ValidateEvidence rechecks current ownership, availability, version and original
// text at model/checkpoint boundaries. Unknown provenance fails closed.
func (s *Service) ValidateEvidence(ctx context.Context, user string, refs []EvidenceReference) (map[string]string, error) {
	encoded, err := json.Marshal(refs)
	if err != nil || len(refs) > 48 || len(encoded) > 96000 || user == "" {
		return nil, ErrValidation
	}
	versions := map[string]string{}
	chunks := map[string][]Chunk{}
	for _, ref := range refs {
		if ref.SourceID == "" || ref.Text == "" || len(ref.SourceID) > 160 {
			return nil, ErrValidation
		}
		if strings.HasPrefix(ref.SourceID, "product:") {
			page, err := productknowledge.Read(strings.TrimPrefix(ref.SourceID, "product:"))
			if err != nil || !strings.Contains(page.Body, ref.Text) || (ref.Version != "" && ref.Version != page.Version) {
				return nil, ErrNotFound
			}
			versions[ref.SourceID] = page.Version
			continue
		}
		version, loaded := versions[ref.SourceID]
		if !loaded {
			doc, err := s.store.GetDocument(ctx, user, ref.SourceID)
			if err != nil || doc.Status != "ready" {
				return nil, ErrNotFound
			}
			version = SourceVersion(doc)
			parsed, ok := s.store.(ParsedStore)
			if !ok {
				return nil, ErrParsedContentUnavailable
			}
			items, err := parsed.ListDocumentChunks(ctx, user, ref.SourceID, 10000)
			if err != nil {
				return nil, ErrNotFound
			}
			chunks[ref.SourceID], versions[ref.SourceID] = items, version
		}
		if ref.Version != "" && ref.Version != version {
			return nil, ErrNotFound
		}
		found := false
		for _, chunk := range chunks[ref.SourceID] {
			if (ref.ChunkID == "" || ref.ChunkID == chunk.ID) && strings.Contains(chunk.Content, ref.Text) {
				found = true
				break
			}
		}
		if !found {
			return nil, ErrNotFound
		}
	}
	return versions, nil
}
