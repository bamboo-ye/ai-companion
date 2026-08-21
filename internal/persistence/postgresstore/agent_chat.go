package postgresstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/conversation"
)

var _ agent.ChatStore = (*Store)(nil)

func (s *Store) AcceptAgentMessage(ctx context.Context, message conversation.Message, run agent.Run) (conversation.Message, agent.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return message, run, err
	}
	defer tx.Rollback()
	var sequence uint64
	var characterID string
	err = tx.QueryRowContext(ctx, `
		SELECT next_sequence,character_id::text
		FROM app.conversations
		WHERE id=$1 AND user_id=$2 AND status='active'
		FOR UPDATE`,
		message.ConversationID, message.UserID,
	).Scan(&sequence, &characterID)
	if errors.Is(err, sql.ErrNoRows) {
		return message, run, conversation.ErrNotFound
	}
	if err != nil {
		return message, run, err
	}
	if characterID != run.CharacterID || run.ConversationID != message.ConversationID ||
		run.UserID != message.UserID {
		return message, run, agent.ErrValidation
	}
	message.Sequence = sequence
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.messages (
			id,conversation_id,user_id,role,sequence_no,bubble_no,content,status,
			created_at,completed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		message.ID, message.ConversationID, message.UserID, message.Role,
		message.Sequence, message.Bubble, message.Content, message.Status,
		message.CreatedAt, message.CompletedAt,
	); err != nil {
		return message, run, err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO agent.runs (
			id,thread_id,user_id,conversation_id,character_id,module_key,
			graph_name,graph_version,status,idempotency_key,input,available_at,
			deadline_at,revision,created_at,updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16
		)`,
		run.ID, run.ThreadID, run.UserID, run.ConversationID, run.CharacterID,
		run.Module, run.GraphName, run.GraphVersion, run.Status,
		run.IdempotencyKey, run.Input, run.AvailableAt, run.DeadlineAt,
		run.Revision, run.CreatedAt, run.UpdatedAt,
	); err != nil {
		return message, run, err
	}
	if _, err = appendAgentEvent(ctx, tx, run.ID, "accepted", map[string]any{
		"graph_name": run.GraphName, "graph_version": run.GraphVersion,
	}); err != nil {
		return message, run, err
	}
	if err = appendAgentRunRequested(ctx, tx, run); err != nil {
		return message, run, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.conversations
		SET next_sequence=$1,last_message_at=$2,updated_at=$2
		WHERE id=$3`,
		sequence+2, message.CreatedAt, message.ConversationID,
	); err != nil {
		return message, run, err
	}
	if err = tx.Commit(); err != nil {
		return message, run, err
	}
	return message, run, nil
}
