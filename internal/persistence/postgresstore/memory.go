package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/memory"
)

var _ memory.Store = (*Store)(nil)

func (s *Store) UpsertMemory(ctx context.Context, item memory.Memory) (memory.Memory, bool, error) {
	arbitrationJSON, err := json.Marshal(item.Arbitration)
	if err != nil {
		return item, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return item, false, err
	}
	defer tx.Rollback()
	if item.SupersedesID != "" {
		result, replaceErr := tx.ExecContext(ctx, `UPDATE app.long_term_memories SET status='superseded',valid_to=$1,updated_at=$1 WHERE id=$2 AND user_id=$3 AND status='active'`, item.ValidFrom, item.SupersedesID, item.UserID)
		if replaceErr != nil {
			return item, false, replaceErr
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return item, false, memory.ErrNotFound
		}
	}
	existing, err := scanMemory(tx.QueryRowContext(ctx, memorySelect+`
		WHERE user_id=$1 AND normalized_hash=$2 AND status='active'
		LIMIT 1
		FOR UPDATE`,
		item.UserID, item.NormalizedHash,
	))
	if err == nil {
		if item.SupersedesID != "" {
			return item, false, memory.ErrValidation
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return item, false, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO app.long_term_memories (
			id,user_id,memory_type,content,normalized_hash,source_conversation_id,
			source_message_id,confidence,importance,sensitivity,pinned,status,
			valid_from,valid_to,supersedes_id,created_at,updated_at,arbitration
		) VALUES (
			$1,$2,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,'')::uuid,$8,$9,
			$10,$11,$12,$13,$14,NULLIF($15,'')::uuid,$16,$17,$18
		)`,
		item.ID, item.UserID, item.Type, item.Content, item.NormalizedHash,
		item.SourceConversationID, item.SourceMessageID, item.Confidence,
		item.Importance, item.Sensitivity, item.Pinned, item.Status,
		item.ValidFrom, item.ValidTo, item.SupersedesID, item.CreatedAt,
		item.UpdatedAt, string(arbitrationJSON),
	)
	if err != nil {
		return item, false, err
	}
	return item, true, tx.Commit()
}

func (s *Store) ListMemories(ctx context.Context, userID string, limit int) ([]memory.Memory, error) {
	rows, err := s.db.QueryContext(ctx, memorySelect+`
		WHERE user_id=$1 AND status='active'
		ORDER BY pinned DESC,updated_at DESC
		LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]memory.Memory, 0)
	for rows.Next() {
		item, scanErr := scanMemory(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetMemory(ctx context.Context, userID, memoryID string) (memory.Memory, error) {
	item, err := scanMemory(s.db.QueryRowContext(ctx, memorySelect+`
		WHERE id=$1 AND user_id=$2 AND status='active'`,
		memoryID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Memory{}, memory.ErrNotFound
	}
	return item, err
}

func (s *Store) UpdateMemory(ctx context.Context, item memory.Memory) error {
	arbitrationJSON, err := json.Marshal(item.Arbitration)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.long_term_memories SET
			memory_type=$1,content=$2,normalized_hash=$3,importance=$4,
			sensitivity=$5,pinned=$6,updated_at=$7,arbitration=$10
		WHERE id=$8 AND user_id=$9 AND status='active'`,
		item.Type, item.Content, item.NormalizedHash, item.Importance,
		item.Sensitivity, item.Pinned, item.UpdatedAt, item.ID, item.UserID, string(arbitrationJSON),
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return memory.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteMemory(ctx context.Context, userID, memoryID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.long_term_memories SET
			status='deleted',valid_to=$1,updated_at=$1
		WHERE id=$2 AND user_id=$3 AND status='active'`,
		now, memoryID, userID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return memory.ErrNotFound
	}
	return nil
}

func (s *Store) ClearMemories(ctx context.Context, userID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE app.long_term_memories SET
			status='deleted',valid_to=$1,updated_at=$1
		WHERE user_id=$2 AND status='active'`,
		now, userID,
	)
	return err
}

const memorySelect = `
	SELECT id::text,user_id::text,memory_type,content,normalized_hash,
		COALESCE(source_conversation_id::text,''),COALESCE(source_message_id::text,''),
		confidence,importance,sensitivity,pinned,status,valid_from,valid_to,
		COALESCE(supersedes_id::text,''),created_at,updated_at,arbitration
	FROM app.long_term_memories`

func scanMemory(row rowScanner) (memory.Memory, error) {
	var item memory.Memory
	var validTo sql.NullTime
	var arbitrationJSON []byte
	err := row.Scan(
		&item.ID, &item.UserID, &item.Type, &item.Content, &item.NormalizedHash,
		&item.SourceConversationID, &item.SourceMessageID, &item.Confidence,
		&item.Importance, &item.Sensitivity, &item.Pinned, &item.Status,
		&item.ValidFrom, &validTo, &item.SupersedesID, &item.CreatedAt,
		&item.UpdatedAt, &arbitrationJSON,
	)
	if err == nil && len(arbitrationJSON) > 0 {
		err = json.Unmarshal(arbitrationJSON, &item.Arbitration)
	}
	if validTo.Valid {
		value := validTo.Time
		item.ValidTo = &value
	}
	return item, err
}
