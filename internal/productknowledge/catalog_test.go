package productknowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/windcry1/ai-companion/internal/contextengine"
)

func TestEmbeddedSourcesAreCurrentAndComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, source := range builtin.catalog.Sources {
		data, err := os.ReadFile(filepath.Join("../..", source.Path))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != source.SHA256 {
			t.Fatalf("%s changed; run node scripts/build-product-knowledge.mjs", source.Path)
		}
	}
	for _, page := range builtin.catalog.Pages {
		if seen[page.ID] || page.Body == "" || page.Source == "" || page.Section == "" || len(page.Version) != 64 {
			t.Fatalf("invalid page %s", page.ID)
		}
		seen[page.ID] = true
		for _, id := range page.Links {
			if _, err := Read(id); err != nil {
				t.Fatal("broken link", id)
			}
		}
	}
	for _, id := range []string{"builtin-user-welcome", "builtin-user-documents", "builtin-user-profile", "builtin-admin-access", "builtin-admin-rollout"} {
		if !seen[id] {
			t.Fatal("missing guide", id)
		}
	}
	for _, p := range List() {
		if p.Body != "" {
			t.Fatal("list leaked full corpus")
		}
		if p.CitationURL != "/#knowledge="+p.ID {
			t.Fatal("missing safe citation URL")
		}
	}
}

func TestProductHelpRetrieval(t *testing.T) {
	cases := []struct{ query, id string }{
		{"伴AI是什么，有哪些功能", "builtin-user-welcome"},
		{"如何修改登录密码", "builtin-user-profile"},
		{"怎么把资料上传到知识库", "builtin-user-documents"},
		{"怎么纠正你记住的偏好", "builtin-user-memories"},
		{"如何生成PPT演示文稿", "builtin-user-presentations"},
		{"管理员如何用PIN登录后台", "builtin-admin-access"},
		{"如何记账和导出账本", "builtin-user-life"},
		{"提醒事项只有日期没有时间可以吗", "builtin-user-reminders"},
		{"How do I upload a document?", "builtin-user-documents"},
		{"Can I use this app offline?", "builtin-user-install"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			hits, err := Search(tc.query, 5)
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			found := false
			for _, hit := range hits {
				ids = append(ids, hit.Page.ID)
				if hit.Page.ID == tc.id {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s in %v", tc.id, ids)
			}
			items, tokens, _ := Context(tc.query, nil)
			found = false
			for _, item := range items {
				if item.Source.ID == tc.id {
					found = true
				}
			}
			if !found || tokens > ContextBudget {
				t.Fatalf("auto grounding missing %s: %v", tc.id, items)
			}
		})
	}
}

func TestFollowupScopeAndUnrelatedChat(t *testing.T) {
	history := []contextengine.Message{{Role: "user", Content: "怎么上传文档到知识库"}, {Role: "assistant", Content: "上传 PDF 或 TXT"}}
	if RetrievalQuery("具体有什么限制？", history) == "" {
		t.Fatal("followup lost topic")
	}
	history = append(history, contextengine.Message{Role: "user", Content: "今天心情很难受"})
	if RetrievalQuery("然后呢", history) != "" {
		t.Fatal("stale product topic leaked")
	}
	for _, query := range []string{"你好", "明天提醒我交报告", "午饭花了30元", "我们项目的预算是多少", "帮我写一个童话故事"} {
		items, _, _ := Context(query, nil)
		if len(items) != 0 {
			t.Fatalf("unrelated query grounded: %s", query)
		}
	}
	if hits, _ := Search("zyxneverdocumented", 5); len(hits) != 0 {
		t.Fatal("fabricated search evidence")
	}
}

func TestReadPaginationIsLosslessAndCatalogImmutable(t *testing.T) {
	for _, p := range builtin.catalog.Pages {
		var joined strings.Builder
		for offset := 0; ; {
			part, err := ReadPart(p.ID, offset)
			if err != nil {
				t.Fatal(err)
			}
			joined.WriteString(part.Page.Body)
			if !part.HasMore {
				break
			}
			if part.NextOffset <= offset {
				t.Fatal("cursor did not advance")
			}
			offset = part.NextOffset
		}
		if joined.String() != p.Body {
			t.Fatal("lost source content", p.ID)
		}
		copy, _ := Read(p.ID)
		if len(copy.Links) > 0 {
			copy.Links[0] = "corrupted"
			again, _ := Read(p.ID)
			if again.Links[0] == "corrupted" {
				t.Fatal("mutable global catalog")
			}
		}
	}
	if _, err := ReadPart("builtin-user-welcome", -1); err != ErrValidation {
		t.Fatal(err)
	}
	if _, err := ReadPart("private-document-id", 0); err != ErrNotFound {
		t.Fatal(err)
	}
}
