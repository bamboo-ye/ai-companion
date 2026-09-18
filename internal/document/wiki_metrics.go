package document

import "context"

type WikiStatsStore interface {
	WikiStats(context.Context) (map[string]int64, error)
}

func (s *Service) ContextMetrics(ctx context.Context) (map[string]any, error) {
	result := map[string]any{"runtime": s.WikiMetrics()}
	if rollout, ok := s.index.(*RolloutIndex); ok {
		result["index"] = map[string]any{"mode": rollout.Mode, "shadow_queries": rollout.ShadowQueries.Load(), "shadow_differences": rollout.ShadowDifferences.Load(), "candidate_failures": rollout.CandidateFailures.Load()}
	}
	if store, ok := s.store.(WikiStatsStore); ok {
		stats, err := store.WikiStats(ctx)
		if err != nil {
			return nil, err
		}
		result["durable"] = stats
	}
	return result, nil
}
func (s *WikiMemoryStore) WikiStats(_ context.Context) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := map[string]int64{"pages": int64(len(s.pages))}
	for _, j := range s.jobs {
		stats["jobs_"+j.status]++
	}
	for _, f := range s.feedback {
		stats["feedback_"+f.Rating]++
	}
	return stats, nil
}
func (s *WikiSQLStore) WikiStats(ctx context.Context) (map[string]int64, error) {
	stats := map[string]int64{}
	// Keep counts free of source/user text. Feedback is a quality signal, never an
	// automatic claim that the latest generated answer is correct.
	var count int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+s.table("wiki_pages")).Scan(&count); err != nil {
		return nil, err
	}
	stats["pages"] = count
	rating := "payload->>'rating'"
	if s.dialect != "postgres" {
		rating = "JSON_UNQUOTE(JSON_EXTRACT(payload,'$.rating'))"
	}
	for _, spec := range []struct{ table, key, prefix string }{{"wiki_jobs", "status", "jobs_"}, {"wiki_feedback", rating, "feedback_"}} {
		rows, err := s.db.QueryContext(ctx, "SELECT "+spec.key+",COUNT(*) FROM "+s.table(spec.table)+" GROUP BY "+spec.key)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			var n int64
			if err = rows.Scan(&key, &n); err != nil {
				rows.Close()
				return nil, err
			}
			stats[spec.prefix+key] = n
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return stats, nil
}
