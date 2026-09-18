// context-eval replays labelled retrieval cases against the real configured
// embedding service, or an explicitly selected lexical baseline.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/semantic"
	"os"
	"strconv"
	"time"
)

type replayCase struct {
	Name           string   `json:"name"`
	Facts          []string `json:"facts"`
	Query          string   `json:"query"`
	Expected       string   `json:"expected"`
	DeleteExpected bool     `json:"delete_expected"`
}

func main() {
	path := flag.String("cases", "evals/context/retrieval.v1.json", "labelled replay file")
	offline := flag.Bool("offline", false, "run lexical baseline without model calls")
	minimum := flag.Float64("min-recall", 0, "fail below this Recall@5 threshold")
	flag.Parse()
	data, err := os.ReadFile(*path)
	if err != nil {
		fail(err)
	}
	var fixture struct {
		Cases []replayCase `json:"cases"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		fail(err)
	}
	if len(fixture.Cases) == 0 {
		fail(fmt.Errorf("empty replay"))
	}
	dimensions := 1024
	if v := os.Getenv("CONTEXT_EMBEDDING_DIMENSIONS"); v != "" {
		dimensions, err = strconv.Atoi(v)
		if err != nil {
			fail(err)
		}
	}
	c := semantic.Config{BaseURL: os.Getenv("CONTEXT_MODEL_BASE_URL"), APIKey: os.Getenv("CONTEXT_MODEL_API_KEY"), EmbeddingModel: os.Getenv("CONTEXT_EMBEDDING_MODEL"), Dimensions: dimensions, Timeout: 30 * time.Second}
	mode := "live_embedding"
	if *offline {
		c.EmbeddingModel = ""
		mode = "lexical_baseline"
	} else if c.EmbeddingModel == "" || c.BaseURL == "" {
		fail(fmt.Errorf("configure CONTEXT_MODEL_BASE_URL and CONTEXT_EMBEDDING_MODEL, or explicitly select --offline"))
	}
	client := semantic.New(c)
	results := []map[string]any{}
	correct := 0
	reciprocal := 0.0
	positives, found := 0, 0
	invariantsOK := true
	for index, test := range fixture.Cases {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		service := memory.NewService(memory.NewMemoryStore())
		service.SetSemanticClient(client)
		user := fmt.Sprintf("replay-%d", index)
		for _, fact := range test.Facts {
			item, e := service.Create(ctx, user, fact)
			if e != nil {
				fail(e)
			}
			if test.DeleteExpected && fact == test.Expected {
				if e = service.Delete(ctx, user, item.ID); e != nil {
					fail(e)
				}
			}
		}
		start := time.Now()
		hits, e := service.RecallContext(ctx, user, test.Query, 5)
		cancel()
		if e != nil {
			fail(e)
		}
		rank := 0
		for i, hit := range hits {
			if hit.Content == test.Expected {
				rank = i + 1
				break
			}
		}
		passed := rank > 0
		if test.DeleteExpected {
			passed = rank == 0
			invariantsOK = invariantsOK && passed
		} else if test.Expected == "" {
			passed = len(hits) == 0
			invariantsOK = invariantsOK && passed
		} else {
			positives++
			if rank > 0 {
				found++
				reciprocal += 1 / float64(rank)
			}
		}
		if passed {
			correct++
		}
		results = append(results, map[string]any{"name": test.Name, "passed": passed, "rank": rank, "latency_ms": time.Since(start).Milliseconds()})
	}
	passRate := float64(correct) / float64(len(results))
	recall := float64(found) / float64(max(positives, 1))
	report := map[string]any{"mode": mode, "model": c.EmbeddingModel, "cases": results, "pass_rate": passRate, "recall_at_5": recall, "mrr": reciprocal / float64(max(positives, 1)), "usage": client.Metrics(), "recorded_at": time.Now().UTC()}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(encoded))
	if recall < *minimum || !invariantsOK || (!*offline && client.Metrics().Failures > 0) {
		os.Exit(1)
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
