package semantic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
)

// IndexSpec is immutable and safe to expose in diagnostics. Never include keys
// or provider URLs with credentials in the public index identity.
type IndexSpec struct {
	Model         string `json:"model"`
	Revision      string `json:"revision"`
	Dimensions    int    `json:"dimensions"`
	ProviderHash  string `json:"provider_hash"`
	QueryHash     string `json:"query_hash"`
	Preprocessing string `json:"preprocessing"`
}

func digest(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func (s IndexSpec) ID() string {
	b, _ := json.Marshal(s)
	return "embedding-v2-" + digest(string(b))[:24]
}
func (c *Client) IndexSpec() IndexSpec {
	if c == nil {
		return IndexSpec{}
	}
	provider := c.config.BaseURL
	if u, err := url.Parse(provider); err == nil {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		provider = strings.TrimRight(u.String(), "/")
	}
	revision := c.config.EmbeddingRevision
	if revision == "" {
		revision = "unversioned"
	}
	prep := c.config.PreprocessingVersion
	if prep == "" {
		prep = "source-title-section-v2"
	}
	return IndexSpec{Model: c.config.EmbeddingModel, Revision: revision, Dimensions: c.config.Dimensions, ProviderHash: digest(provider), QueryHash: digest(c.config.QueryInstruction), Preprocessing: prep}
}
func (c *Client) EmbedQuery(ctx context.Context, query string) ([][]float32, error) {
	if c != nil && c.config.QueryInstruction != "" {
		query = c.config.QueryInstruction + "\n" + query
	}
	return c.Embed(ctx, []string{query})
}
