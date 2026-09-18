package config

import "testing"

func TestKnowledgeConfiguration(t *testing.T) {
	for _, key := range []string{"CONTEXT_MODEL_BASE_URL", "CONTEXT_EMBEDDING_MODEL", "CONTEXT_SUMMARY_MODEL", "CONTEXT_RERANK_MODEL"} {
		t.Setenv(key, "")
	}
	t.Setenv("CONTEXT_INDEX_MODE", "legacy")
	t.Setenv("CONTEXT_WIKI_ENABLED", "true")
	t.Setenv("CONTEXT_EMBEDDING_DIMENSIONS", "1024")
	t.Setenv("CONTEXT_MODEL_TIMEOUT", "30s")
	defaults, err := loadKnowledge("production")
	if err != nil || !defaults.WikiEnabled || defaults.EmbeddingModel != "" {
		t.Fatalf("defaults %+v %v", defaults, err)
	}
	t.Setenv("CONTEXT_INDEX_MODE", "shadow")
	if _, err = loadKnowledge("development"); err == nil {
		t.Fatal("shadow accepted without model")
	}
	t.Setenv("CONTEXT_EMBEDDING_MODEL", "multilingual-v1")
	t.Setenv("CONTEXT_MODEL_BASE_URL", "https://models.example/v1")
	if _, err = loadKnowledge("production"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTEXT_MODEL_BASE_URL", "http://models.example/v1")
	if _, err = loadKnowledge("production"); err == nil {
		t.Fatal("production accepted insecure model endpoint")
	}
	t.Setenv("CONTEXT_MODEL_BASE_URL", "https://models.example/v1")
	t.Setenv("CONTEXT_EMBEDDING_DIMENSIONS", "0")
	if _, err = loadKnowledge("development"); err == nil {
		t.Fatal("invalid dimensions accepted")
	}
}
