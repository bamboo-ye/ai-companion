package postgresstore

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
)

const concurrentRetryRuns = 24

type concurrentRetryExecutor struct {
	mu       sync.Mutex
	attempts map[string]int
}

func (e *concurrentRetryExecutor) Execute(_ context.Context, run agent.Run) (agent.ExecutionResult, error) {
	e.mu.Lock()
	e.attempts[run.ID]++
	attempt := e.attempts[run.ID]
	e.mu.Unlock()
	if attempt == 1 {
		return agent.ExecutionResult{}, &agent.ExecutorError{
			ErrorCode: "model_unavailable", ErrorMessage: "controlled concurrent 503",
			ShouldRetry: true, ProviderStatus: 503,
		}
	}
	return agent.ExecutionResult{
		Status: "completed",
		Output: map[string]any{"response": "并发持久化重试恢复成功。"},
	}, nil
}

func (e *concurrentRetryExecutor) attemptCounts() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make(map[string]int, len(e.attempts))
	for runID, count := range e.attempts {
		result[runID] = count
	}
	return result
}

func TestAgentRetryConcurrentPersistence(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	userID := mustID(t)
	characterID := mustID(t)
	conversationID := mustID(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.db.ExecContext(cleanupCtx, `
			DELETE FROM eventing.outbox_events
			WHERE aggregate_type='agent_run'
				AND aggregate_id IN (
					SELECT id::text FROM agent.runs WHERE conversation_id=$1
				)`, conversationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM agent.runs WHERE conversation_id=$1`, conversationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.messages WHERE conversation_id=$1`, conversationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.conversations WHERE id=$1`, conversationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.characters WHERE id=$1`, characterID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.users WHERE id=$1`, userID)
	})
	if _, err = store.db.ExecContext(ctx, `
		INSERT INTO app.users (
			id,email,password_hash,display_name,timezone,locale,status,created_at,updated_at
		) VALUES ($1,$2,'hash','并发重试验收','Asia/Shanghai','zh-CN','active',$3,$3)`,
		userID, fmt.Sprintf("agent-retry-concurrency-%s@example.com", userID), now,
	); err != nil {
		t.Fatalf("insert concurrent retry user: %v", err)
	}
	if _, err = store.db.ExecContext(ctx, `
		INSERT INTO app.characters (
			id,user_id,module_key,name,relationship_label,personality,speech_style,
			hobbies,boundaries,initiative,reply_length,sticker_style,raw_prompt,
			persona_version,status,created_at,updated_at
		) VALUES (
			$1,$2,'life','并发重试角色','测试','稳定','简洁',
			'[]'::jsonb,'[]'::jsonb,'balanced','short','','测试并发重试',
			1,'active',$3,$3
		)`, characterID, userID, now,
	); err != nil {
		t.Fatalf("insert concurrent retry character: %v", err)
	}
	if _, err = store.db.ExecContext(ctx, `
		INSERT INTO app.conversations (
			id,user_id,character_id,title,status,next_sequence,created_at,updated_at
		) VALUES ($1,$2,$3,'并发重试验收','active',1,$4,$4)`,
		conversationID, userID, characterID, now,
	); err != nil {
		t.Fatalf("insert concurrent retry conversation: %v", err)
	}

	clockNow := now
	service := agent.NewServiceWithClock(store, func() time.Time { return clockNow })
	runs := make([]agent.Run, 0, concurrentRetryRuns)
	for index := 0; index < concurrentRetryRuns; index++ {
		_, run, acceptErr := service.AcceptChat(ctx, agent.AcceptChatInput{
			UserID: userID, ConversationID: conversationID, CharacterID: characterID,
			Module: "life", Content: fmt.Sprintf("并发重试请求 %02d", index+1),
		})
		if acceptErr != nil {
			t.Fatalf("AcceptChat(%d) error = %v", index, acceptErr)
		}
		runs = append(runs, run)
	}

	executor := &concurrentRetryExecutor{attempts: make(map[string]int)}
	worker := agent.NewRuntimeWorker(service, executor, "concurrent-retry-worker", time.Minute, 100*time.Millisecond)
	worker.SetRetryPolicy(100*time.Millisecond, 0)
	worker.SetMaxConcurrency(8)
	retryObservations := make(chan agent.RuntimeRetryObservation, concurrentRetryRuns)
	worker.SetRetryObservationHandler(func(observation agent.RuntimeRetryObservation) {
		retryObservations <- observation
	})
	dispatcher := agent.NewRunDispatcher(worker, 8, concurrentRetryRuns*2)
	completions := make(chan agent.RunDispatchObservation, concurrentRetryRuns*2)
	dispatcher.SetCompleteHandler(func(observation agent.RunDispatchObservation) {
		completions <- observation
	})
	dispatchCtx, stopDispatcher := context.WithCancel(ctx)
	dispatchDone := make(chan error, 1)
	go func() { dispatchDone <- dispatcher.Run(dispatchCtx) }()
	defer func() {
		stopDispatcher()
		<-dispatchDone
	}()

	for _, run := range runs {
		if err = dispatcher.Dispatch(ctx, run.ID); err != nil {
			t.Fatalf("first Dispatch(%s) error = %v", run.ID, err)
		}
	}
	for index := 0; index < concurrentRetryRuns; index++ {
		observation := <-completions
		if observation.Err != nil || !observation.Processed {
			t.Fatalf("first dispatch observation = %#v", observation)
		}
	}
	if len(retryObservations) != concurrentRetryRuns {
		t.Fatalf("retry observations = %d, want %d", len(retryObservations), concurrentRetryRuns)
	}

	clockNow = clockNow.Add(100 * time.Millisecond)
	for _, run := range runs {
		if err = dispatcher.Dispatch(ctx, run.ID); err != nil {
			t.Fatalf("recovery Dispatch(%s) error = %v", run.ID, err)
		}
		if err = dispatcher.Dispatch(ctx, run.ID); err != nil {
			t.Fatalf("duplicate Dispatch(%s) error = %v", run.ID, err)
		}
	}
	processed, deduplicated := 0, 0
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for processed < concurrentRetryRuns {
		select {
		case observation := <-completions:
			if observation.Err != nil {
				t.Fatalf("recovery dispatch observation = %#v", observation)
			}
			if observation.Processed {
				processed++
			} else {
				deduplicated++
			}
		case <-deadline.C:
			t.Fatalf("recovery dispatch timed out at processed/deduplicated = %d/%d", processed, deduplicated)
		}
	}
	// Duplicates already waiting in the bounded queue are coalesced without an
	// execution callback. A duplicate arriving during execution retains one
	// trailing replay and is rejected by the durable revision fence.
	quiet := time.NewTimer(200 * time.Millisecond)
	defer quiet.Stop()
	for {
		select {
		case observation := <-completions:
			if observation.Err != nil || observation.Processed {
				t.Fatalf("trailing recovery dispatch observation = %#v", observation)
			}
			deduplicated++
			if !quiet.Stop() {
				<-quiet.C
			}
			quiet.Reset(200 * time.Millisecond)
		case <-quiet.C:
			coalesced := concurrentRetryRuns*2 - processed - deduplicated
			if coalesced < 0 || coalesced+deduplicated != concurrentRetryRuns {
				t.Fatalf("recovery dispatch processed/deduplicated/coalesced = %d/%d/%d", processed, deduplicated, coalesced)
			}
			goto recoverySettled
		}
	}

recoverySettled:

	attempts := executor.attemptCounts()
	if len(attempts) != concurrentRetryRuns {
		t.Fatalf("executor runs = %d, want %d", len(attempts), concurrentRetryRuns)
	}
	for runID, count := range attempts {
		if count != 2 {
			t.Fatalf("executor attempts for %s = %d", runID, count)
		}
	}
	var runCount, completedCount, minRevision, maxRevision int
	if err = store.db.QueryRowContext(ctx, `
		SELECT COUNT(*),COUNT(*) FILTER (WHERE status='completed'),
			MIN(revision),MAX(revision)
		FROM agent.runs
		WHERE conversation_id=$1`, conversationID,
	).Scan(&runCount, &completedCount, &minRevision, &maxRevision); err != nil ||
		runCount != concurrentRetryRuns || completedCount != concurrentRetryRuns ||
		minRevision != 5 || maxRevision != 5 {
		t.Fatalf("persisted runs = %d/%d revisions=%d/%d, %v", runCount, completedCount, minRevision, maxRevision, err)
	}
	var userMessages, assistantMessages int
	if err = store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE role='user'),
			COUNT(*) FILTER (WHERE role='assistant')
		FROM app.messages
		WHERE conversation_id=$1`, conversationID,
	).Scan(&userMessages, &assistantMessages); err != nil ||
		userMessages != concurrentRetryRuns || assistantMessages != concurrentRetryRuns {
		t.Fatalf("persisted messages = %d/%d, %v", userMessages, assistantMessages, err)
	}
	var eventRuns, minEvents, maxEvents int
	if err = store.db.QueryRowContext(ctx, `
		SELECT COUNT(*),MIN(event_count),MAX(event_count)
		FROM (
			SELECT e.run_id,COUNT(*)::int AS event_count
			FROM agent.run_events e
			JOIN agent.runs r ON r.id=e.run_id
			WHERE r.conversation_id=$1
			GROUP BY e.run_id
		) counts`, conversationID,
	).Scan(&eventRuns, &minEvents, &maxEvents); err != nil ||
		eventRuns != concurrentRetryRuns || minEvents != 5 || maxEvents != 5 {
		t.Fatalf("persisted events = %d min/max=%d/%d, %v", eventRuns, minEvents, maxEvents, err)
	}
	sample, err := store.ReliabilitySample(ctx, time.Now().UTC())
	if err != nil || sample.AgentRetries.ScheduledRecent < concurrentRetryRuns ||
		sample.AgentRetries.RecoveredRecent < concurrentRetryRuns {
		t.Fatalf("ReliabilitySample(concurrent retry) = %#v, %v", sample.AgentRetries, err)
	}
}
