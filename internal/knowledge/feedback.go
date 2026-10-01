package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"strings"
	"sync"
	"time"
)

type Feedback struct {
	ID          string    `json:"id"`
	UserID      string    `json:"-"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	Ref         Ref       `json:"ref"`
	Rating      string    `json:"rating"`
	Note        string    `json:"note"`
	Strategy    string    `json:"strategy"`
	CreatedAt   time.Time `json:"created_at"`
	Status      string    `json:"status"`
	Resolution  string    `json:"resolution,omitempty"`
	ReviewedBy  string    `json:"reviewed_by,omitempty"`
}
type FeedbackStore interface {
	SaveKnowledgeFeedback(context.Context, Feedback) error
	ListKnowledgeFeedback(context.Context, int) ([]Feedback, error)
	ResolveKnowledgeFeedback(context.Context, string, string, string) error
}
type FeedbackStoreProvider interface{ KnowledgeFeedbackStore() FeedbackStore }
type MemoryFeedbackStore struct {
	mu    sync.Mutex
	items []Feedback
}

func (s *MemoryFeedbackStore) SaveKnowledgeFeedback(_ context.Context, f Feedback) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, f)
	return nil
}
func (s *Service) SetFeedbackStore(store FeedbackStore) { s.feedback = store }
func (s *Service) Feedback(ctx context.Context, scope Scope, in Feedback) (Feedback, error) {
	if in.WorkspaceID != scope.WorkspaceID || len([]rune(in.Note)) > 1000 || in.Ref.Revision == "" || in.Ref.ContentHash == "" {
		return Feedback{}, ErrValidation
	}
	switch in.Rating {
	case "helpful", "incorrect", "outdated", "missing", "unsupported", "permission":
	default:
		return Feedback{}, ErrValidation
	}
	if _, err := s.read(ctx, scope, in.Ref); err != nil {
		return Feedback{}, err
	}
	key, err := id.New()
	if err != nil {
		return Feedback{}, err
	}
	in.ID = key
	in.UserID = scope.UserID
	in.CreatedAt = time.Now().UTC()
	in.Strategy = "scoped-rrf-v1"
	in.Status, in.Resolution, in.ReviewedBy = "pending", "", ""
	if s.feedback == nil {
		return Feedback{}, ErrValidation
	}
	err = s.feedback.SaveKnowledgeFeedback(ctx, in)
	return in, err
}

// Review operations are invoked only by the authenticated operator boundary.
func (s *Service) PendingFeedback(ctx context.Context) ([]Feedback, error) {
	return s.feedback.ListKnowledgeFeedback(ctx, 100)
}
func (s *Service) ResolveFeedback(ctx context.Context, key, actor, resolution string) error {
	if key == "" || actor == "" || len([]rune(strings.TrimSpace(resolution))) < 2 || len([]rune(resolution)) > 1000 {
		return ErrValidation
	}
	return s.feedback.ResolveKnowledgeFeedback(ctx, key, actor, resolution)
}
func (s *MemoryFeedbackStore) ListKnowledgeFeedback(_ context.Context, limit int) ([]Feedback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Feedback{}
	for _, f := range s.items {
		if f.Status == "pending" {
			out = append(out, f)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func (s *MemoryFeedbackStore) ResolveKnowledgeFeedback(_ context.Context, key, actor, resolution string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == key && s.items[i].Status == "pending" {
			s.items[i].Status = "resolved"
			s.items[i].ReviewedBy = actor
			s.items[i].Resolution = resolution
			return nil
		}
	}
	return ErrConflict
}

type SQLFeedbackStore struct {
	db       *sql.DB
	postgres bool
}

func NewSQLFeedbackStore(db *sql.DB, postgres bool) *SQLFeedbackStore {
	return &SQLFeedbackStore{db, postgres}
}
func (s *SQLFeedbackStore) SaveKnowledgeFeedback(ctx context.Context, f Feedback) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	q := "INSERT INTO knowledge_feedback (id,user_id,rating,payload,created_at,status) VALUES (?,?,?,?,?,'pending')"
	if s.postgres {
		q = strings.Replace(q, "INTO knowledge_feedback", "INTO app.knowledge_feedback", 1)
		for _, p := range []string{"$1", "$2", "$3", "$4", "$5"} {
			q = strings.Replace(q, "?", p, 1)
		}
	}
	_, err = s.db.ExecContext(ctx, q, f.ID, f.UserID, f.Rating, string(data), f.CreatedAt)
	return err
}
func (s *SQLFeedbackStore) query(q string) string {
	if !s.postgres {
		return q
	}
	q = strings.ReplaceAll(q, "knowledge_feedback", "app.knowledge_feedback")
	for i := 1; strings.Contains(q, "?"); i++ {
		q = strings.Replace(q, "?", fmt.Sprintf("$%d", i), 1)
	}
	return q
}
func (s *SQLFeedbackStore) ListKnowledgeFeedback(ctx context.Context, limit int) ([]Feedback, error) {
	rows, err := s.db.QueryContext(ctx, s.query("SELECT payload FROM knowledge_feedback WHERE status='pending' ORDER BY created_at,id LIMIT ?"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Feedback{}
	for rows.Next() {
		var b []byte
		var f Feedback
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (s *SQLFeedbackStore) ResolveKnowledgeFeedback(ctx context.Context, key, actor, resolution string) error {
	var b []byte
	if err := s.db.QueryRowContext(ctx, s.query("SELECT payload FROM knowledge_feedback WHERE id=? AND status='pending'"), key).Scan(&b); err != nil {
		if err == sql.ErrNoRows {
			return ErrConflict
		}
		return err
	}
	var f Feedback
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	f.Status, f.ReviewedBy, f.Resolution = "resolved", actor, resolution
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	r, err := s.db.ExecContext(ctx, s.query("UPDATE knowledge_feedback SET status='resolved',payload=? WHERE id=? AND status='pending'"), string(b), key)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
