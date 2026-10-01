package postgresstore

import (
	"context"
	"github.com/windcry1/ai-companion/internal/knowledge"
	"github.com/windcry1/ai-companion/internal/semantic"
)

func (s *Store) KnowledgeProjectionStore() semantic.ProjectionStore {
	return semantic.NewSQLProjectionStore(s.db, "postgres")
}
func (s *Store) ListKnowledgeOwners(ctx context.Context, cursor string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT owner FROM (SELECT user_id::text AS owner FROM app.long_term_memories UNION SELECT owner_id FROM app.wiki_pages UNION SELECT user_id::text AS owner FROM app.documents) AS owners WHERE owner>$1 ORDER BY owner LIMIT $2`, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var owner string
		if err = rows.Scan(&owner); err != nil {
			return nil, err
		}
		out = append(out, owner)
	}
	return out, rows.Err()
}

func (s *Store) KnowledgeFeedbackStore() knowledge.FeedbackStore {
	return knowledge.NewSQLFeedbackStore(s.db, true)
}
