package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

func (s *Store) CreateConversation(ctx context.Context, item conversation.Conversation) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversations (id,user_id,character_id,title,status,next_sequence,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?)`, item.ID, item.UserID, item.CharacterID, item.Title, item.Status, item.NextSequence, item.CreatedAt, item.UpdatedAt)
	return err
}
func (s *Store) ListConversations(ctx context.Context, userID string) ([]conversation.Conversation, error) {
	rows, err := s.db.QueryContext(ctx, conversationSelect+` WHERE user_id=UUID_TO_BIN(?) AND status='active' ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []conversation.Conversation{}
	for rows.Next() {
		item, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *Store) GetConversation(ctx context.Context, userID, conversationID string) (conversation.Conversation, error) {
	item, err := scanConversation(s.db.QueryRowContext(ctx, conversationSelect+` WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, conversationID, userID))
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
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET status='deleted',updated_at=? WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, now, conversationID, userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return conversation.ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `UPDATE generation_jobs SET status='cancelled',error_code='conversation_deleted',error_message='conversation was deleted',worker_id=NULL,lease_expires_at=NULL,completed_at=? WHERE conversation_id=UUID_TO_BIN(?) AND status IN ('accepted','running','cancel_requested')`, now, conversationID)
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
	if err = tx.QueryRowContext(ctx, `SELECT next_sequence FROM conversations WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active' FOR UPDATE`, conversationID, userID).Scan(&sequence); errors.Is(err, sql.ErrNoRows) {
		return message, job, conversation.ErrNotFound
	} else if err != nil {
		return message, job, err
	}
	message.Sequence = sequence
	_, err = tx.ExecContext(ctx, `INSERT INTO messages (id,conversation_id,user_id,role,sequence_no,bubble_no,content,status,created_at,completed_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,?,?)`, message.ID, message.ConversationID, message.UserID, message.Role, message.Sequence, message.Bubble, message.Content, message.Status, message.CreatedAt, message.CompletedAt)
	if err != nil {
		return message, job, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO generation_jobs (id,conversation_id,user_message_id,status,attempt,deadline_at,created_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?)`, job.ID, job.ConversationID, job.UserMessageID, job.Status, job.Attempt, job.DeadlineAt, job.CreatedAt)
	if err != nil {
		return message, job, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE conversations SET next_sequence=?,last_message_at=?,updated_at=? WHERE id=UUID_TO_BIN(?)`, sequence+2, message.CreatedAt, message.CreatedAt, conversationID)
	if err != nil {
		return message, job, err
	}
	chatEventID, err := id.New()
	if err != nil {
		return message, job, err
	}
	chatPayload, _ := json.Marshal(map[string]string{"job_id": job.ID, "user_id": userID, "conversation_id": conversationID})
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at) VALUES (UUID_TO_BIN(?),'conversation',UUID_TO_BIN(?),'chat.command.v1',1,?,?)`, chatEventID, conversationID, chatPayload, message.CreatedAt); err != nil {
		return message, job, err
	}
	return message, job, tx.Commit()
}
func (s *Store) ListMessages(ctx context.Context, userID, conversationID string, after uint64, afterBubble, limit int) ([]conversation.Message, error) {
	if _, err := s.GetConversation(ctx, userID, conversationID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, messageSelect+` WHERE conversation_id=UUID_TO_BIN(?) AND (sequence_no>? OR (sequence_no=? AND bubble_no>?)) ORDER BY sequence_no,bubble_no LIMIT ?`, conversationID, after, after, afterBubble, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []conversation.Message{}
	for rows.Next() {
		item, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetLatestSummary(ctx context.Context, userID, conversationID string) (conversation.ConversationSummary, error) {
	item, err := scanConversationSummary(s.db.QueryRowContext(ctx, conversationSummarySelect+` JOIN conversations c ON c.id=s.conversation_id WHERE s.conversation_id=UUID_TO_BIN(?) AND c.user_id=UUID_TO_BIN(?) ORDER BY s.version DESC LIMIT 1`, conversationID, userID))
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
	if err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(user_id) FROM conversations WHERE id=UUID_TO_BIN(?) FOR UPDATE`, item.ConversationID).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return conversation.ErrNotFound
	} else if err != nil {
		return err
	}
	if owner != item.UserID {
		return conversation.ErrNotFound
	}
	version := 1
	var latestVersion int
	var latestEnd uint64
	err = tx.QueryRowContext(ctx, `SELECT version,end_sequence FROM conversation_summaries WHERE conversation_id=UUID_TO_BIN(?) ORDER BY version DESC LIMIT 1 FOR UPDATE`, item.ConversationID).Scan(&latestVersion, &latestEnd)
	if err == nil {
		if latestEnd >= item.EndSequence {
			return nil
		}
		version = latestVersion + 1
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO conversation_summaries (id,conversation_id,user_id,version,start_sequence,end_sequence,range_started_at,range_ended_at,content,token_count,summarizer_version,created_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,?,?,?,?)`, item.ID, item.ConversationID, item.UserID, version, item.StartSequence, item.EndSequence, item.RangeStartedAt, item.RangeEndedAt, item.Content, item.TokenCount, item.SummarizerVersion, item.CreatedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) GetJob(ctx context.Context, userID, jobID string) (conversation.Job, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx, jobSelect+` JOIN conversations c ON c.id=j.conversation_id WHERE j.id=UUID_TO_BIN(?) AND c.user_id=UUID_TO_BIN(?)`, jobID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		err = conversation.ErrNotFound
	}
	return job, err
}
func (s *Store) MarkJobRunning(ctx context.Context, userID, jobID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE generation_jobs j JOIN conversations c ON c.id=j.conversation_id SET j.status='running',j.started_at=?,j.model_provider='development',j.model_name='deterministic-persona-v1' WHERE j.id=UUID_TO_BIN(?) AND c.user_id=UUID_TO_BIN(?) AND j.status='accepted'`, now, jobID, userID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return conversation.ErrConflict
	}
	return nil
}
func (s *Store) AppendAssistantBubble(ctx context.Context, userID, jobID string, bubble int, content string, now time.Time) (conversation.Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conversation.Message{}, err
	}
	defer tx.Rollback()
	var convID, userMessageID string
	var sequence uint64
	err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(j.conversation_id),BIN_TO_UUID(j.user_message_id),m.sequence_no FROM generation_jobs j JOIN conversations c ON c.id=j.conversation_id JOIN messages m ON m.id=j.user_message_id WHERE j.id=UUID_TO_BIN(?) AND c.user_id=UUID_TO_BIN(?) AND j.status='running' FOR UPDATE`, jobID, userID).Scan(&convID, &userMessageID, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return conversation.Message{}, conversation.ErrConflict
	} else if err != nil {
		return conversation.Message{}, err
	}
	messageID, err := id.New()
	if err != nil {
		return conversation.Message{}, err
	}
	message := conversation.Message{ID: messageID, ConversationID: convID, UserID: userID, Role: "assistant", Sequence: sequence + 1, Bubble: bubble, Content: content, Status: "completed", ReplyToID: userMessageID, CreatedAt: now, CompletedAt: &now}
	_, err = tx.ExecContext(ctx, `INSERT INTO messages (id,conversation_id,user_id,role,sequence_no,bubble_no,content,status,reply_to_id,created_at,completed_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,UUID_TO_BIN(?),?,?)`, message.ID, message.ConversationID, message.UserID, message.Role, message.Sequence, message.Bubble, message.Content, message.Status, message.ReplyToID, message.CreatedAt, message.CompletedAt)
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
	result, err := tx.ExecContext(ctx, `UPDATE generation_jobs j JOIN conversations c ON c.id=j.conversation_id SET j.error_code=IF(j.status='cancel_requested','cancelled',NULLIF(?,'')),j.error_message=IF(j.status='cancel_requested','generation was cancelled',NULLIF(?,'')),j.status=IF(j.status='cancel_requested','cancelled',?),j.completed_at=?,j.worker_id=NULL,j.lease_expires_at=NULL WHERE j.id=UUID_TO_BIN(?) AND c.user_id=UUID_TO_BIN(?) AND j.status IN ('running','cancel_requested')`, code, message, status, now, jobID, userID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return conversation.ErrNotFound
	}
	if usage.Provider != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO model_usage_records (user_id,generation_job_id,provider,model,input_tokens,output_tokens,estimated_cost_micros,latency_ms) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,?)`, userID, jobID, usage.Provider, usage.Model, usage.InputTokens, usage.OutputTokens, usage.EstimatedCostMicros, usage.Latency.Milliseconds())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) RequestCancel(ctx context.Context, userID, jobID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE generation_jobs j JOIN conversations c ON c.id=j.conversation_id SET j.error_code='cancelled',j.error_message='generation was cancelled',j.completed_at=?,j.worker_id=NULL,j.lease_expires_at=NULL,j.status='cancelled' WHERE j.id=UUID_TO_BIN(?) AND c.user_id=UUID_TO_BIN(?) AND j.status IN ('accepted','running')`, now, jobID, userID)
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
	err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(j.conversation_id),BIN_TO_UUID(j.user_message_id),j.attempt,j.status FROM generation_jobs j JOIN conversations c ON c.id=j.conversation_id WHERE j.id=UUID_TO_BIN(?) AND c.user_id=UUID_TO_BIN(?) FOR UPDATE`, oldID, userID).Scan(&next.ConversationID, &next.UserMessageID, &next.Attempt, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return next, conversation.ErrNotFound
	} else if err != nil {
		return next, err
	}
	if status == "completed" || !conversation.IsTerminal(status) {
		return next, conversation.ErrConflict
	}
	next.Attempt++
	_, err = tx.ExecContext(ctx, `INSERT INTO generation_jobs (id,conversation_id,user_message_id,status,attempt,deadline_at,created_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?)`, next.ID, next.ConversationID, next.UserMessageID, next.Status, next.Attempt, next.DeadlineAt, next.CreatedAt)
	if err != nil {
		return next, err
	}
	eventID, err := id.New()
	if err != nil {
		return next, err
	}
	payload, _ := json.Marshal(map[string]string{"job_id": next.ID, "user_id": userID, "conversation_id": next.ConversationID})
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at) VALUES (UUID_TO_BIN(?),'conversation',UUID_TO_BIN(?),'chat.command.v1',1,?,?)`, eventID, next.ConversationID, payload, next.CreatedAt); err != nil {
		return next, err
	}
	return next, tx.Commit()
}

func (s *Store) DurableChatDispatch() bool { return true }

func (s *Store) ClaimGenerationJobByID(ctx context.Context, jobID, workerID string, now time.Time, lease, executionTimeout time.Duration) (conversation.Job, string, error) {
	return s.claimGenerationJob(ctx, `j.id=UUID_TO_BIN(?)`, []any{jobID}, workerID, now, lease, executionTimeout)
}

func (s *Store) ClaimGenerationJob(ctx context.Context, workerID string, now time.Time, lease, executionTimeout time.Duration) (conversation.Job, string, error) {
	return s.claimGenerationJob(ctx, `1=1`, nil, workerID, now, lease, executionTimeout)
}

func (s *Store) DeferGenerationJob(ctx context.Context, userID, jobID, code, message string, availableAt, _ time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE generation_jobs j JOIN conversations c ON c.id=j.conversation_id SET j.status='accepted',j.available_at=?,j.deadline_at=?,j.error_code=?,j.error_message=?,j.worker_id=NULL,j.lease_expires_at=NULL,j.completed_at=NULL WHERE j.id=UUID_TO_BIN(?) AND c.user_id=UUID_TO_BIN(?) AND j.status IN ('accepted','running')`, availableAt, availableAt.Add(30*time.Second), code, message, jobID, userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return conversation.ErrConflict
	}
	return nil
}

func (s *Store) claimGenerationJob(ctx context.Context, predicate string, args []any, workerID string, now time.Time, lease, executionTimeout time.Duration) (conversation.Job, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conversation.Job{}, "", err
	}
	defer tx.Rollback()
	var userID string
	queryArgs := append(append([]any{}, args...), now, now)
	var jobID string
	err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(j.id),BIN_TO_UUID(c.user_id) FROM generation_jobs j JOIN conversations c ON c.id=j.conversation_id WHERE `+predicate+` AND j.available_at<=? AND (j.status='accepted' OR (j.status='running' AND j.lease_expires_at<=?)) AND NOT EXISTS (SELECT 1 FROM generation_jobs prior WHERE prior.conversation_id=j.conversation_id AND prior.id<>j.id AND prior.status IN ('accepted','running','cancel_requested') AND (prior.created_at<j.created_at OR (prior.created_at=j.created_at AND prior.id<j.id))) ORDER BY j.available_at,j.created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, queryArgs...).Scan(&jobID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return conversation.Job{}, "", conversation.ErrNoRunnableJob
	}
	if err != nil {
		return conversation.Job{}, "", err
	}
	deadline := now.Add(executionTimeout)
	result, err := tx.ExecContext(ctx, `UPDATE generation_jobs SET status='running',started_at=COALESCE(started_at,?),deadline_at=?,worker_id=?,lease_expires_at=?,error_code=NULL,error_message=NULL WHERE id=UUID_TO_BIN(?)`, now, deadline, workerID, now.Add(lease), jobID)
	if err != nil {
		return conversation.Job{}, "", err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return conversation.Job{}, "", conversation.ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return conversation.Job{}, "", err
	}
	job, err := s.GetJob(ctx, userID, jobID)
	return job, userID, err
}

func (s *Store) LoadMessageForMemory(ctx context.Context, userID, conversationID, messageID string) (string, error) {
	var content string
	err := s.db.QueryRowContext(ctx, `SELECT content FROM messages WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND conversation_id=UUID_TO_BIN(?) AND status='completed'`, messageID, userID, conversationID).Scan(&content)
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
	result, err := s.db.ExecContext(ctx, `INSERT INTO generation_job_events (generation_job_id,event_type,event_data,created_at) VALUES (UUID_TO_BIN(?),?,?,?)`, jobID, eventType, payload, now)
	if err != nil {
		return conversation.Event{}, err
	}
	eventID, _ := result.LastInsertId()
	return conversation.Event{ID: uint64(eventID), JobID: jobID, Type: eventType, Data: data, CreatedAt: now}, nil
}
func (s *Store) ListEvents(ctx context.Context, userID, jobID string, after uint64, limit int) ([]conversation.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,BIN_TO_UUID(e.generation_job_id),e.event_type,e.event_data,e.created_at FROM generation_job_events e JOIN generation_jobs j ON j.id=e.generation_job_id JOIN conversations c ON c.id=j.conversation_id WHERE e.generation_job_id=UUID_TO_BIN(?) AND c.user_id=UUID_TO_BIN(?) AND e.id>? ORDER BY e.id LIMIT ?`, jobID, userID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []conversation.Event{}
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
	rows, err := tx.QueryContext(ctx, `SELECT BIN_TO_UUID(id),status FROM generation_jobs WHERE status IN ('accepted','running','cancel_requested') FOR UPDATE`)
	if err != nil {
		return err
	}
	type interrupted struct{ id, status string }
	items := []interrupted{}
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
		_, err = tx.ExecContext(ctx, `UPDATE generation_jobs SET status=?,error_code=?,error_message='generation was interrupted and can be retried',completed_at=? WHERE id=UUID_TO_BIN(?)`, status, code, now, item.id)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"status": status, "error_code": code})
		_, err = tx.ExecContext(ctx, `INSERT INTO generation_job_events (generation_job_id,event_type,event_data,created_at) VALUES (UUID_TO_BIN(?),?,?,?)`, item.id, status, payload, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

const conversationSelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),BIN_TO_UUID(character_id),title,status,next_sequence,last_message_at,created_at,updated_at FROM conversations`
const messageSelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(conversation_id),BIN_TO_UUID(user_id),role,sequence_no,bubble_no,content,status,COALESCE(BIN_TO_UUID(reply_to_id),''),created_at,completed_at FROM messages`
const jobSelect = `SELECT BIN_TO_UUID(j.id),BIN_TO_UUID(j.conversation_id),BIN_TO_UUID(j.user_message_id),j.status,j.attempt,COALESCE(j.model_provider,''),COALESCE(j.model_name,''),j.deadline_at,COALESCE(j.error_code,''),COALESCE(j.error_message,''),j.created_at,j.started_at,j.completed_at FROM generation_jobs j`
const conversationSummarySelect = `SELECT BIN_TO_UUID(s.id),BIN_TO_UUID(s.conversation_id),BIN_TO_UUID(s.user_id),s.version,s.start_sequence,s.end_sequence,s.range_started_at,s.range_ended_at,s.content,s.token_count,s.summarizer_version,s.created_at FROM conversation_summaries s`

func scanConversation(row rowScanner) (conversation.Conversation, error) {
	var item conversation.Conversation
	var last sql.NullTime
	err := row.Scan(&item.ID, &item.UserID, &item.CharacterID, &item.Title, &item.Status, &item.NextSequence, &last, &item.CreatedAt, &item.UpdatedAt)
	if last.Valid {
		item.LastMessageAt = &last.Time
	}
	return item, err
}
func scanMessage(row rowScanner) (conversation.Message, error) {
	var item conversation.Message
	var completed sql.NullTime
	err := row.Scan(&item.ID, &item.ConversationID, &item.UserID, &item.Role, &item.Sequence, &item.Bubble, &item.Content, &item.Status, &item.ReplyToID, &item.CreatedAt, &completed)
	if completed.Valid {
		item.CompletedAt = &completed.Time
	}
	return item, err
}
func scanJob(row rowScanner) (conversation.Job, error) {
	var item conversation.Job
	var started, completed sql.NullTime
	err := row.Scan(&item.ID, &item.ConversationID, &item.UserMessageID, &item.Status, &item.Attempt, &item.Provider, &item.Model, &item.DeadlineAt, &item.ErrorCode, &item.ErrorMessage, &item.CreatedAt, &started, &completed)
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
	err := row.Scan(&item.ID, &item.ConversationID, &item.UserID, &item.Version, &item.StartSequence, &item.EndSequence, &item.RangeStartedAt, &item.RangeEndedAt, &item.Content, &item.TokenCount, &item.SummarizerVersion, &item.CreatedAt)
	return item, err
}
