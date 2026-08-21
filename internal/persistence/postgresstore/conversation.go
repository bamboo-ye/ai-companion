package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

var _ conversation.Store = (*Store)(nil)

func (s *Store) CreateConversation(ctx context.Context, item conversation.Conversation) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app.conversations (
			id,user_id,character_id,title,status,next_sequence,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		item.ID, item.UserID, item.CharacterID, item.Title, item.Status,
		item.NextSequence, item.CreatedAt, item.UpdatedAt,
	)
	return err
}

func (s *Store) ListConversations(ctx context.Context, userID string) ([]conversation.Conversation, error) {
	rows, err := s.db.QueryContext(ctx, conversationSelect+` WHERE user_id=$1 AND status='active' ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]conversation.Conversation, 0)
	for rows.Next() {
		item, scanErr := scanConversation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetConversation(ctx context.Context, userID, conversationID string) (conversation.Conversation, error) {
	item, err := scanConversation(s.db.QueryRowContext(ctx, conversationSelect+` WHERE id=$1 AND user_id=$2 AND status='active'`, conversationID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		err = conversation.ErrNotFound
	}
	return item, err
}

func (s *Store) DeleteConversation(ctx context.Context, userID, conversationID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE app.conversations
		SET status='deleted',updated_at=$1
		WHERE id=$2 AND user_id=$3 AND status='active'`,
		now, conversationID, userID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return conversation.ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE app.generation_jobs
		SET
			status='cancelled',
			error_code='conversation_deleted',
			error_message='conversation was deleted',
			worker_id=NULL,
			lease_expires_at=NULL,
			completed_at=$1
		WHERE conversation_id=$2
			AND status IN ('accepted','running','cancel_requested')`,
		now, conversationID,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AcceptMessage(ctx context.Context, userID, conversationID string, message conversation.Message, job conversation.Job) (conversation.Message, conversation.Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return message, job, err
	}
	defer tx.Rollback()
	var sequence uint64
	err = tx.QueryRowContext(ctx, `
		SELECT next_sequence
		FROM app.conversations
		WHERE id=$1 AND user_id=$2 AND status='active'
		FOR UPDATE`,
		conversationID, userID,
	).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return message, job, conversation.ErrNotFound
	}
	if err != nil {
		return message, job, err
	}
	message.Sequence = sequence
	_, err = tx.ExecContext(ctx, `
		INSERT INTO app.messages (
			id,conversation_id,user_id,role,sequence_no,bubble_no,content,status,
			created_at,completed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		message.ID, message.ConversationID, message.UserID, message.Role,
		message.Sequence, message.Bubble, message.Content, message.Status,
		message.CreatedAt, message.CompletedAt,
	)
	if err != nil {
		return message, job, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO app.generation_jobs (
			id,conversation_id,user_message_id,status,attempt,deadline_at,
			available_at,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`,
		job.ID, job.ConversationID, job.UserMessageID, job.Status, job.Attempt,
		job.DeadlineAt, job.CreatedAt,
	)
	if err != nil {
		return message, job, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE app.conversations
		SET next_sequence=$1,last_message_at=$2,updated_at=$2
		WHERE id=$3`,
		sequence+2, message.CreatedAt, conversationID,
	)
	if err != nil {
		return message, job, err
	}
	if err = insertChatCommand(ctx, tx, job.ID, userID, conversationID, message.CreatedAt); err != nil {
		return message, job, err
	}
	return message, job, tx.Commit()
}

func (s *Store) ListMessages(ctx context.Context, userID, conversationID string, after uint64, afterBubble, limit int) ([]conversation.Message, error) {
	if _, err := s.GetConversation(ctx, userID, conversationID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, messageSelect+`
		WHERE conversation_id=$1
			AND (sequence_no>$2 OR (sequence_no=$2 AND bubble_no>$3))
		ORDER BY sequence_no,bubble_no
		LIMIT $4`,
		conversationID, after, afterBubble, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]conversation.Message, 0)
	for rows.Next() {
		item, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetLatestSummary(ctx context.Context, userID, conversationID string) (conversation.ConversationSummary, error) {
	item, err := scanConversationSummary(s.db.QueryRowContext(ctx, conversationSummarySelect+`
		JOIN app.conversations c ON c.id=s.conversation_id
		WHERE s.conversation_id=$1 AND c.user_id=$2
		ORDER BY s.version DESC
		LIMIT 1`,
		conversationID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		err = conversation.ErrNotFound
	}
	return item, err
}

func (s *Store) SaveSummary(ctx context.Context, item conversation.ConversationSummary) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	err = tx.QueryRowContext(ctx, `
		SELECT user_id::text
		FROM app.conversations
		WHERE id=$1
		FOR UPDATE`,
		item.ConversationID,
	).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return conversation.ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != item.UserID {
		return conversation.ErrNotFound
	}
	version := 1
	var latestVersion int
	var latestEnd uint64
	err = tx.QueryRowContext(ctx, `
		SELECT version,end_sequence
		FROM app.conversation_summaries
		WHERE conversation_id=$1
		ORDER BY version DESC
		LIMIT 1
		FOR UPDATE`,
		item.ConversationID,
	).Scan(&latestVersion, &latestEnd)
	if err == nil {
		if latestEnd >= item.EndSequence {
			return nil
		}
		version = latestVersion + 1
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO app.conversation_summaries (
			id,conversation_id,user_id,version,start_sequence,end_sequence,
			range_started_at,range_ended_at,content,token_count,
			summarizer_version,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		item.ID, item.ConversationID, item.UserID, version, item.StartSequence,
		item.EndSequence, item.RangeStartedAt, item.RangeEndedAt, item.Content,
		item.TokenCount, item.SummarizerVersion, item.CreatedAt,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetJob(ctx context.Context, userID, jobID string) (conversation.Job, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx, jobSelect+`
		JOIN app.conversations c ON c.id=j.conversation_id
		WHERE j.id=$1 AND c.user_id=$2`,
		jobID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		err = conversation.ErrNotFound
	}
	return job, err
}

func (s *Store) MarkJobRunning(ctx context.Context, userID, jobID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.generation_jobs AS j SET
			status='running',
			started_at=$1,
			model_provider='development',
			model_name='deterministic-persona-v1'
		FROM app.conversations AS c
		WHERE j.conversation_id=c.id
			AND j.id=$2
			AND c.user_id=$3
			AND j.status='accepted'`,
		now, jobID, userID,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return conversation.ErrConflict
	}
	return nil
}

func (s *Store) AppendAssistantBubble(ctx context.Context, userID, jobID string, _ int, content string, now time.Time) (conversation.Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conversation.Message{}, err
	}
	defer tx.Rollback()
	var conversationID, userMessageID string
	var sequence uint64
	err = tx.QueryRowContext(ctx, `
		SELECT j.conversation_id::text,j.user_message_id::text,m.sequence_no
		FROM app.generation_jobs j
		JOIN app.conversations c ON c.id=j.conversation_id
		JOIN app.messages m ON m.id=j.user_message_id
		WHERE j.id=$1 AND c.user_id=$2
			AND j.status IN ('running','failed','timed_out','cancelled')
		FOR UPDATE OF j,m`,
		jobID, userID,
	).Scan(&conversationID, &userMessageID, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return conversation.Message{}, conversation.ErrConflict
	}
	if err != nil {
		return conversation.Message{}, err
	}
	var bubble int
	if err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(bubble_no),0)+1
		FROM app.messages
		WHERE conversation_id=$1 AND sequence_no=$2`,
		conversationID, sequence+1,
	).Scan(&bubble); err != nil {
		return conversation.Message{}, err
	}
	messageID, err := id.New()
	if err != nil {
		return conversation.Message{}, err
	}
	message := conversation.Message{
		ID: messageID, ConversationID: conversationID, UserID: userID,
		Role: "assistant", Sequence: sequence + 1, Bubble: bubble, Content: content,
		Status: "completed", ReplyToID: userMessageID, CreatedAt: now,
		CompletedAt: &now,
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO app.messages (
			id,conversation_id,user_id,role,sequence_no,bubble_no,content,status,
			reply_to_id,created_at,completed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		message.ID, message.ConversationID, message.UserID, message.Role,
		message.Sequence, message.Bubble, message.Content, message.Status,
		message.ReplyToID, message.CreatedAt, message.CompletedAt,
	)
	if err != nil {
		return message, err
	}
	return message, tx.Commit()
}

func (s *Store) FinishJob(ctx context.Context, userID, jobID, status, code, message string, usage conversation.Usage, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE app.generation_jobs AS j SET
			error_code=CASE
				WHEN j.status='cancel_requested' THEN 'cancelled'
				ELSE NULLIF($1,'')
			END,
			error_message=CASE
				WHEN j.status='cancel_requested' THEN 'generation was cancelled'
				ELSE NULLIF($2,'')
			END,
			status=CASE
				WHEN j.status='cancel_requested' THEN 'cancelled'
				ELSE $3
			END,
			completed_at=$4,
			worker_id=NULL,
			lease_expires_at=NULL
		FROM app.conversations AS c
		WHERE j.conversation_id=c.id
			AND j.id=$5
			AND c.user_id=$6
			AND j.status IN ('running','cancel_requested')`,
		code, message, status, now, jobID, userID,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return conversation.ErrNotFound
	}
	if usage.Provider != "" {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO app.model_usage_records (
				user_id,generation_job_id,provider,model,input_tokens,
				output_tokens,estimated_cost_micros,latency_ms
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			userID, jobID, usage.Provider, usage.Model, usage.InputTokens,
			usage.OutputTokens, usage.EstimatedCostMicros, usage.Latency.Milliseconds(),
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RequestCancel(ctx context.Context, userID, jobID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.generation_jobs AS j SET
			error_code='cancelled',
			error_message='generation was cancelled',
			completed_at=$1,
			worker_id=NULL,
			lease_expires_at=NULL,
			status='cancelled'
		FROM app.conversations AS c
		WHERE j.conversation_id=c.id
			AND j.id=$2
			AND c.user_id=$3
			AND j.status IN ('accepted','running')`,
		now, jobID, userID,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return conversation.ErrConflict
	}
	return nil
}

func (s *Store) CreateRetry(ctx context.Context, userID, oldID string, next conversation.Job) (conversation.Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return next, err
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT j.conversation_id::text,j.user_message_id::text,j.attempt,j.status
		FROM app.generation_jobs j
		JOIN app.conversations c ON c.id=j.conversation_id
		WHERE j.id=$1 AND c.user_id=$2
		FOR UPDATE OF j`,
		oldID, userID,
	).Scan(&next.ConversationID, &next.UserMessageID, &next.Attempt, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return next, conversation.ErrNotFound
	}
	if err != nil {
		return next, err
	}
	if status == "completed" || !conversation.IsTerminal(status) {
		return next, conversation.ErrConflict
	}
	next.Attempt++
	_, err = tx.ExecContext(ctx, `
		INSERT INTO app.generation_jobs (
			id,conversation_id,user_message_id,status,attempt,deadline_at,
			available_at,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`,
		next.ID, next.ConversationID, next.UserMessageID, next.Status, next.Attempt,
		next.DeadlineAt, next.CreatedAt,
	)
	if err != nil {
		return next, err
	}
	if err = insertChatCommand(ctx, tx, next.ID, userID, next.ConversationID, next.CreatedAt); err != nil {
		return next, err
	}
	return next, tx.Commit()
}

func (s *Store) DurableChatDispatch() bool {
	return true
}

func (s *Store) ClaimGenerationJobByID(ctx context.Context, jobID, workerID string, now time.Time, lease, executionTimeout time.Duration) (conversation.Job, string, error) {
	return s.claimGenerationJob(ctx, jobID, workerID, now, lease, executionTimeout)
}

func (s *Store) ClaimGenerationJob(ctx context.Context, workerID string, now time.Time, lease, executionTimeout time.Duration) (conversation.Job, string, error) {
	return s.claimGenerationJob(ctx, "", workerID, now, lease, executionTimeout)
}

func (s *Store) DeferGenerationJob(ctx context.Context, userID, jobID, code, message string, availableAt, _ time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.generation_jobs AS j SET
			status='accepted',
			available_at=$1,
			deadline_at=$2,
			error_code=$3,
			error_message=$4,
			worker_id=NULL,
			lease_expires_at=NULL,
			completed_at=NULL
		FROM app.conversations AS c
		WHERE j.conversation_id=c.id
			AND j.id=$5
			AND c.user_id=$6
			AND j.status IN ('accepted','running')`,
		availableAt, availableAt.Add(30*time.Second), code, message, jobID, userID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return conversation.ErrConflict
	}
	return nil
}

func (s *Store) claimGenerationJob(ctx context.Context, jobID, workerID string, now time.Time, lease, executionTimeout time.Duration) (conversation.Job, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conversation.Job{}, "", err
	}
	defer tx.Rollback()
	var jobFilter any
	if jobID != "" {
		jobFilter = jobID
	}
	var claimedID, userID string
	err = tx.QueryRowContext(ctx, `
		SELECT j.id::text,c.user_id::text
		FROM app.generation_jobs j
		JOIN app.conversations c ON c.id=j.conversation_id
		WHERE ($1::uuid IS NULL OR j.id=$1::uuid)
			AND j.available_at<=$2
			AND (
				j.status='accepted'
				OR (j.status='running' AND j.lease_expires_at<=$3)
			)
			AND NOT EXISTS (
				SELECT 1
				FROM app.generation_jobs prior
				WHERE prior.conversation_id=j.conversation_id
					AND prior.id<>j.id
					AND prior.status IN ('accepted','running','cancel_requested')
					AND (
						prior.created_at<j.created_at
						OR (prior.created_at=j.created_at AND prior.id<j.id)
					)
			)
		ORDER BY j.available_at,j.created_at
		LIMIT 1
		FOR UPDATE OF j SKIP LOCKED`,
		jobFilter, now, now,
	).Scan(&claimedID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return conversation.Job{}, "", conversation.ErrNoRunnableJob
	}
	if err != nil {
		return conversation.Job{}, "", err
	}
	deadline := now.Add(executionTimeout)
	result, err := tx.ExecContext(ctx, `
		UPDATE app.generation_jobs SET
			status='running',
			started_at=COALESCE(started_at,$1),
			deadline_at=$2,
			worker_id=$3,
			lease_expires_at=$4,
			error_code=NULL,
			error_message=NULL
		WHERE id=$5`,
		now, deadline, workerID, now.Add(lease), claimedID,
	)
	if err != nil {
		return conversation.Job{}, "", err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return conversation.Job{}, "", conversation.ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return conversation.Job{}, "", err
	}
	job, err := s.GetJob(ctx, userID, claimedID)
	return job, userID, err
}

func (s *Store) LoadMessageForMemory(ctx context.Context, userID, conversationID, messageID string) (string, error) {
	var content string
	err := s.db.QueryRowContext(ctx, `
		SELECT content
		FROM app.messages
		WHERE id=$1 AND user_id=$2 AND conversation_id=$3 AND status='completed'`,
		messageID, userID, conversationID,
	).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", conversation.ErrNotFound
	}
	return content, err
}

func (s *Store) AppendEvent(ctx context.Context, jobID, eventType string, data any, now time.Time) (conversation.Event, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return conversation.Event{}, err
	}
	var eventID uint64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO app.generation_job_events (
			generation_job_id,event_type,event_data,created_at
		) VALUES ($1,$2,$3,$4)
		RETURNING id`,
		jobID, eventType, payload, now,
	).Scan(&eventID)
	if err != nil {
		return conversation.Event{}, err
	}
	return conversation.Event{
		ID: eventID, JobID: jobID, Type: eventType, Data: data, CreatedAt: now,
	}, nil
}

func (s *Store) ListEvents(ctx context.Context, userID, jobID string, after uint64, limit int) ([]conversation.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id,e.generation_job_id::text,e.event_type,e.event_data,e.created_at
		FROM app.generation_job_events e
		JOIN app.generation_jobs j ON j.id=e.generation_job_id
		JOIN app.conversations c ON c.id=j.conversation_id
		WHERE e.generation_job_id=$1 AND c.user_id=$2 AND e.id>$3
		ORDER BY e.id
		LIMIT $4`,
		jobID, userID, after, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]conversation.Event, 0)
	for rows.Next() {
		var item conversation.Event
		var data []byte
		if err := rows.Scan(&item.ID, &item.JobID, &item.Type, &data, &item.CreatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &item.Data); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) RecoverInterrupted(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text,status
		FROM app.generation_jobs
		WHERE status IN ('accepted','running','cancel_requested')
		FOR UPDATE`)
	if err != nil {
		return err
	}
	type interrupted struct {
		id     string
		status string
	}
	items := make([]interrupted, 0)
	for rows.Next() {
		var item interrupted
		if err = rows.Scan(&item.id, &item.status); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, item := range items {
		status, code := "failed", "process_interrupted"
		if item.status == "cancel_requested" {
			status, code = "cancelled", "cancelled"
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE app.generation_jobs SET
				status=$1,
				error_code=$2,
				error_message='generation was interrupted and can be retried',
				completed_at=$3,
				worker_id=NULL,
				lease_expires_at=NULL
			WHERE id=$4`,
			status, code, now, item.id,
		)
		if err != nil {
			return err
		}
		payload, marshalErr := json.Marshal(map[string]string{
			"status": status, "error_code": code,
		})
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO app.generation_job_events (
				generation_job_id,event_type,event_data,created_at
			) VALUES ($1,$2,$3,$4)`,
			item.id, status, payload, now,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertChatCommand(ctx context.Context, tx *sql.Tx, jobID, userID, conversationID string, occurredAt time.Time) error {
	eventID, err := id.New()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{
		"job_id": jobID, "user_id": userID, "conversation_id": conversationID,
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events (
			id,aggregate_type,aggregate_id,event_type,event_version,payload,
			occurred_at,available_at
		) VALUES ($1,'conversation',$2,'chat.command.v1',1,$3,$4,$4)`,
		eventID, conversationID, payload, occurredAt,
	)
	return err
}

const conversationSelect = `
	SELECT
		id::text,user_id::text,character_id::text,title,status,next_sequence,
		last_message_at,created_at,updated_at
	FROM app.conversations`

const messageSelect = `
	SELECT
		id::text,conversation_id::text,user_id::text,role,sequence_no,bubble_no,
		content,status,COALESCE(reply_to_id::text,''),created_at,completed_at
	FROM app.messages`

const jobSelect = `
	SELECT
		j.id::text,j.conversation_id::text,j.user_message_id::text,j.status,
		j.attempt,COALESCE(j.model_provider,''),COALESCE(j.model_name,''),
		j.deadline_at,COALESCE(j.error_code,''),COALESCE(j.error_message,''),
		j.created_at,j.started_at,j.completed_at
	FROM app.generation_jobs j`

const conversationSummarySelect = `
	SELECT
		s.id::text,s.conversation_id::text,s.user_id::text,s.version,
		s.start_sequence,s.end_sequence,s.range_started_at,s.range_ended_at,
		s.content,s.token_count,s.summarizer_version,s.created_at
	FROM app.conversation_summaries s`

func scanConversation(row rowScanner) (conversation.Conversation, error) {
	var item conversation.Conversation
	var last sql.NullTime
	err := row.Scan(
		&item.ID, &item.UserID, &item.CharacterID, &item.Title, &item.Status,
		&item.NextSequence, &last, &item.CreatedAt, &item.UpdatedAt,
	)
	if last.Valid {
		item.LastMessageAt = &last.Time
	}
	return item, err
}

func scanMessage(row rowScanner) (conversation.Message, error) {
	var item conversation.Message
	var completed sql.NullTime
	err := row.Scan(
		&item.ID, &item.ConversationID, &item.UserID, &item.Role, &item.Sequence,
		&item.Bubble, &item.Content, &item.Status, &item.ReplyToID,
		&item.CreatedAt, &completed,
	)
	if completed.Valid {
		item.CompletedAt = &completed.Time
	}
	return item, err
}

func scanJob(row rowScanner) (conversation.Job, error) {
	var item conversation.Job
	var started, completed sql.NullTime
	err := row.Scan(
		&item.ID, &item.ConversationID, &item.UserMessageID, &item.Status,
		&item.Attempt, &item.Provider, &item.Model, &item.DeadlineAt,
		&item.ErrorCode, &item.ErrorMessage, &item.CreatedAt, &started, &completed,
	)
	if started.Valid {
		item.StartedAt = &started.Time
	}
	if completed.Valid {
		item.CompletedAt = &completed.Time
	}
	return item, err
}

func scanConversationSummary(row rowScanner) (conversation.ConversationSummary, error) {
	var item conversation.ConversationSummary
	err := row.Scan(
		&item.ID, &item.ConversationID, &item.UserID, &item.Version,
		&item.StartSequence, &item.EndSequence, &item.RangeStartedAt,
		&item.RangeEndedAt, &item.Content, &item.TokenCount,
		&item.SummarizerVersion, &item.CreatedAt,
	)
	return item, err
}
