package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/memory"
)

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
		result, replaceErr := tx.ExecContext(ctx, `UPDATE long_term_memories SET status='superseded',valid_to=?,updated_at=? WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, item.ValidFrom, item.ValidFrom, item.SupersedesID, item.UserID)
		if replaceErr != nil {
			return item, false, replaceErr
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return item, false, memory.ErrNotFound
		}
	}
	existing, err := scanMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE user_id=UUID_TO_BIN(?) AND normalized_hash=? AND status='active' LIMIT 1 FOR UPDATE`, item.UserID, item.NormalizedHash))
	if err == nil {
		if item.SupersedesID != "" {
			return item, false, memory.ErrValidation
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return item, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO long_term_memories (id,user_id,memory_type,content,normalized_hash,source_conversation_id,source_message_id,confidence,importance,sensitivity,pinned,status,valid_from,supersedes_id,created_at,updated_at,arbitration) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,UUID_TO_BIN(NULLIF(?,'')),UUID_TO_BIN(NULLIF(?,'')),?,?,?,?,?,?,UUID_TO_BIN(NULLIF(?,'')),?,?,?)`, item.ID, item.UserID, item.Type, item.Content, item.NormalizedHash, item.SourceConversationID, item.SourceMessageID, item.Confidence, item.Importance, item.Sensitivity, item.Pinned, item.Status, item.ValidFrom, item.SupersedesID, item.CreatedAt, item.UpdatedAt, string(arbitrationJSON))
	if err != nil {
		return item, false, err
	}
	return item, true, tx.Commit()
}
func (s *Store) ListMemories(ctx context.Context, userID string, limit int) ([]memory.Memory, error) {
	rows, err := s.db.QueryContext(ctx, memorySelect+` WHERE user_id=UUID_TO_BIN(?) AND status='active' ORDER BY pinned DESC,updated_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []memory.Memory{}
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *Store) GetMemory(ctx context.Context, userID, memoryID string) (memory.Memory, error) {
	item, err := scanMemory(s.db.QueryRowContext(ctx, memorySelect+` WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, memoryID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		err = memory.ErrNotFound
	}
	return item, err
}
func (s *Store) UpdateMemory(ctx context.Context, item memory.Memory) error {
	arbitrationJSON, err := json.Marshal(item.Arbitration)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE long_term_memories SET memory_type=?,content=?,normalized_hash=?,importance=?,sensitivity=?,pinned=?,updated_at=?,arbitration=? WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, item.Type, item.Content, item.NormalizedHash, item.Importance, item.Sensitivity, item.Pinned, item.UpdatedAt, string(arbitrationJSON), item.ID, item.UserID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return memory.ErrNotFound
	}
	return nil
}
func (s *Store) DeleteMemory(ctx context.Context, userID, memoryID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE long_term_memories SET status='deleted',valid_to=?,updated_at=? WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, now, now, memoryID, userID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return memory.ErrNotFound
	}
	return nil
}
func (s *Store) ClearMemories(ctx context.Context, userID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE long_term_memories SET status='deleted',valid_to=?,updated_at=? WHERE user_id=UUID_TO_BIN(?) AND status='active'`, now, now, userID)
	return err
}

const memorySelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),memory_type,content,normalized_hash,COALESCE(BIN_TO_UUID(source_conversation_id),''),COALESCE(BIN_TO_UUID(source_message_id),''),confidence,importance,sensitivity,pinned,status,valid_from,valid_to,COALESCE(BIN_TO_UUID(supersedes_id),''),created_at,updated_at,arbitration FROM long_term_memories`

func scanMemory(row rowScanner) (memory.Memory, error) {
	var item memory.Memory
	var validTo sql.NullTime
	var arbitrationJSON []byte
	err := row.Scan(&item.ID, &item.UserID, &item.Type, &item.Content, &item.NormalizedHash, &item.SourceConversationID, &item.SourceMessageID, &item.Confidence, &item.Importance, &item.Sensitivity, &item.Pinned, &item.Status, &item.ValidFrom, &validTo, &item.SupersedesID, &item.CreatedAt, &item.UpdatedAt, &arbitrationJSON)
	if err == nil && len(arbitrationJSON) > 0 {
		err = json.Unmarshal(arbitrationJSON, &item.Arbitration)
	}
	if validTo.Valid {
		item.ValidTo = &validTo.Time
	}
	return item, err
}
