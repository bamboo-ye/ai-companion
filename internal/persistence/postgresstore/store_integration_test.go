package postgresstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/billing"
	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/email"
	"github.com/windcry1/ai-companion/internal/eventbus"
	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/opsauth"
	"github.com/windcry1/ai-companion/internal/planner"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"github.com/windcry1/ai-companion/internal/safety"
	"github.com/windcry1/ai-companion/internal/skill"
	"github.com/windcry1/ai-companion/internal/team"
)

func TestCoreStores(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	userID := mustID(t)
	deviceID := mustID(t)
	characterID := mustID(t)
	conversationID := mustID(t)
	skillRunID := mustID(t)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.inbox_events WHERE consumer_name='migration-test'`)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.outbox_events WHERE aggregate_id=$1`, conversationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.model_usage_records WHERE generation_job_id IN (SELECT id FROM app.generation_jobs WHERE conversation_id=$1)`, conversationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.generation_job_events WHERE generation_job_id IN (SELECT id FROM app.generation_jobs WHERE conversation_id=$1)`, conversationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.conversation_summaries WHERE conversation_id=$1`, conversationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.long_term_memories WHERE user_id=$1`, userID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.skill_run_deliveries WHERE run_id=$1`, skillRunID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.skill_runs WHERE id=$1`, skillRunID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.outbox_events WHERE aggregate_id=$1`, skillRunID)
		_, _ = store.db.ExecContext(cleanupCtx, `
			DELETE FROM eventing.outbox_events
			WHERE aggregate_type='agent_run'
				AND aggregate_id IN (
					SELECT id::text FROM agent.runs WHERE conversation_id=$1
				)`,
			conversationID,
		)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM agent.runs WHERE conversation_id=$1`, conversationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.generation_jobs WHERE conversation_id=$1`, conversationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.messages WHERE conversation_id=$1`, conversationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.conversations WHERE id=$1`, conversationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.character_persona_versions WHERE character_id=$1`, characterID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.characters WHERE id=$1`, characterID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.refresh_sessions WHERE user_id=$1`, userID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.user_devices WHERE user_id=$1`, userID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.users WHERE id=$1`, userID)
	})

	// Keep this fixture earlier than application data so the outbox claim can
	// select only the test event from a shared migration database.
	now := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	user := identity.User{
		ID: userID, Email: "Postgres-Migration@example.com", PasswordHash: "hash",
		DisplayName: "迁移验收", Timezone: "Asia/Shanghai", Locale: "zh-CN",
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	found, err := store.FindUserByEmail(ctx, strings.ToUpper(user.Email))
	if err != nil || found.ID != userID {
		t.Fatalf("FindUserByEmail() = %#v, %v", found, err)
	}
	duplicate := user
	duplicate.ID = mustID(t)
	if err := store.CreateUser(ctx, duplicate); !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("CreateUser(duplicate) error = %v", err)
	}

	device, err := store.UpsertDevice(ctx, identity.Device{
		ID: deviceID, UserID: userID, DeviceKey: "migration-test",
		Name: "Browser", Platform: "web", Timezone: "Asia/Shanghai", LastSeen: now,
	})
	if err != nil || device.ID != deviceID {
		t.Fatalf("UpsertDevice() = %#v, %v", device, err)
	}
	sessionID := mustID(t)
	if err := store.CreateSession(ctx, identity.Session{
		ID: sessionID, UserID: userID, DeviceID: deviceID,
		TokenHash: strings.Repeat("a", 64), ExpiresAt: now.Add(time.Hour),
		CreatedAt: now, LastUsedAt: now,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if session, err := store.GetSession(ctx, sessionID); err != nil || session.UserID != userID {
		t.Fatalf("GetSession() = %#v, %v", session, err)
	}

	item := character.Character{
		ID: characterID, UserID: userID, Module: "life", Name: "小满",
		Relationship: "生活管家", Personality: "细心", SpeechStyle: "简洁",
		Hobbies: []string{"计划"}, Boundaries: []string{"不杜撰"}, Initiative: "balanced",
		ReplyLength: "short", StickerStyle: "", RawPrompt: "严格读取工具数据",
		PersonaVersion: 1, Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	persona := character.PersonaVersion{
		CharacterID: characterID, Version: 1, CompilerVersion: "migration-test",
		Compiled: map[string]any{"module": "life"}, CreatedAt: now,
	}
	if err := store.Create(ctx, item, persona); err != nil {
		t.Fatalf("Create(character) error = %v", err)
	}
	got, err := store.Get(ctx, userID, characterID)
	if err != nil || got.Module != "life" || len(got.Boundaries) != 1 {
		t.Fatalf("Get(character) = %#v, %v", got, err)
	}

	chat := conversation.Conversation{
		ID: conversationID, UserID: userID, CharacterID: characterID,
		Status: "active", NextSequence: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateConversation(ctx, chat); err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}
	messageID := mustID(t)
	jobID := mustID(t)
	userMessage := conversation.Message{
		ID: messageID, ConversationID: conversationID, UserID: userID,
		Role: "user", Bubble: 1, Content: "我今天的计划是什么",
		Status: "completed", CreatedAt: now, CompletedAt: &now,
	}
	job := conversation.Job{
		ID: jobID, ConversationID: conversationID, UserMessageID: messageID,
		Status: "accepted", Attempt: 1, DeadlineAt: now.Add(time.Minute), CreatedAt: now,
	}
	accepted, _, err := store.AcceptMessage(ctx, userID, conversationID, userMessage, job)
	if err != nil || accepted.Sequence != 1 {
		t.Fatalf("AcceptMessage() = %#v, %v", accepted, err)
	}

	claimTime := now.Add(time.Second)
	claimed, owner, err := store.ClaimGenerationJobByID(ctx, jobID, "migration-worker", claimTime, time.Minute, 2*time.Minute)
	if err != nil || owner != userID || claimed.Status != "running" {
		t.Fatalf("ClaimGenerationJobByID() = %#v, %q, %v", claimed, owner, err)
	}
	event, err := store.AppendEvent(ctx, jobID, "started", map[string]string{"status": "running"}, claimTime)
	if err != nil || event.ID == 0 {
		t.Fatalf("AppendEvent() = %#v, %v", event, err)
	}
	if _, err = store.AppendAssistantBubble(ctx, userID, jobID, 1, "已读取今日计划。", claimTime); err != nil {
		t.Fatalf("AppendAssistantBubble() error = %v", err)
	}
	if err = store.FinishJob(ctx, userID, jobID, "completed", "", "", conversation.Usage{
		Provider: "openrouter", Model: "test/free", InputTokens: 10,
		OutputTokens: 5, Latency: 25 * time.Millisecond,
	}, claimTime); err != nil {
		t.Fatalf("FinishJob() error = %v", err)
	}
	var legacyModel string
	var legacyPromptTokens int64
	if err = store.db.QueryRowContext(ctx, `
		SELECT returned_model,prompt_tokens
		FROM app.model_usage_observations
		WHERE source_type='legacy_chat' AND execution_id=$1 AND call_index=1`,
		jobID,
	).Scan(&legacyModel, &legacyPromptTokens); err != nil ||
		legacyModel != "test/free" || legacyPromptTokens != 10 {
		t.Fatalf("legacy model usage observation = %q/%d, %v", legacyModel, legacyPromptTokens, err)
	}
	messages, err := store.ListMessages(ctx, userID, conversationID, 0, 0, 10)
	if err != nil || len(messages) != 2 || messages[1].Role != "assistant" {
		t.Fatalf("ListMessages() = %#v, %v", messages, err)
	}
	if content, err := store.LoadMessageForMemory(ctx, userID, conversationID, messageID); err != nil || content != userMessage.Content {
		t.Fatalf("LoadMessageForMemory() = %q, %v", content, err)
	}

	skillInput, _ := json.Marshal(map[string]string{"value": "retry"})
	failedAt := claimTime.Add(time.Second)
	skillRun := skill.Run{
		ID: skillRunID, UserID: userID, SkillName: "test.retry_delivery", SkillVersion: "1",
		ExecutionMode: "inline", Status: "failed", CurrentState: "failed", RiskLevel: "none",
		Input: skillInput, ConversationID: conversationID, OriginMessageID: messageID,
		CreateKey: "retry-delivery-1", Attempt: 1, MaxSteps: 8, TimeoutMS: 1000,
		MaxInputBytes: 65536, Revision: 1, AvailableAt: now, CreatedAt: now,
		UpdatedAt: failedAt, CompletedAt: &failedAt, Steps: []skill.Step{}, Files: []skill.GeneratedFile{},
	}
	if err = store.CreateSkillRun(ctx, skillRun); err != nil {
		t.Fatalf("CreateSkillRun(retry delivery) error = %v", err)
	}
	skillCompletedAt := failedAt.Add(time.Second)
	skillRun.Status, skillRun.CurrentState, skillRun.Attempt = "succeeded", "deliver", 2
	skillRun.Revision, skillRun.UpdatedAt, skillRun.CompletedAt = 2, skillCompletedAt, &skillCompletedAt
	skillRun.Output = json.RawMessage(`{"value":"delivered"}`)
	skillRun.DeliveryContent = "任务重试已完成：test.retry_delivery\n\n<!--ai-skill-run:" + skillRunID + "|2|succeeded-->"
	if err = store.SaveSkillRun(ctx, skillRun, 1, nil, nil); err != nil {
		t.Fatalf("SaveSkillRun(retry delivery) error = %v", err)
	}
	var deliveredContent string
	var deliveries int
	if err = store.db.QueryRowContext(ctx, `
		SELECT m.content,COUNT(*) OVER ()
		FROM app.skill_run_deliveries d
		JOIN app.messages m ON m.id=d.message_id
		WHERE d.run_id=$1 AND d.attempt=2`, skillRunID,
	).Scan(&deliveredContent, &deliveries); err != nil || deliveries != 1 || deliveredContent != skillRun.DeliveryContent {
		t.Fatalf("retry delivery = %q/%d, %v", deliveredContent, deliveries, err)
	}
	var deliveredBubble, deliveredSequence int
	if err = store.db.QueryRowContext(ctx, `
		SELECT bubble_no,sequence_no
		FROM app.messages
		WHERE conversation_id=$1 AND content=$2`, conversationID, skillRun.DeliveryContent,
	).Scan(&deliveredBubble, &deliveredSequence); err != nil || deliveredBubble != 2 || deliveredSequence != 2 {
		t.Fatalf("retry delivery cursor = %d/%d, %v", deliveredSequence, deliveredBubble, err)
	}
	memoryID := mustID(t)
	memoryItem := memory.Memory{
		ID: memoryID, UserID: userID, Type: "preference", Content: "喜欢简洁回复",
		NormalizedHash: strings.Repeat("9", 64), SourceConversationID: conversationID,
		SourceMessageID: messageID, Confidence: 0.99, Importance: 0.8,
		Sensitivity: "normal", Status: "active", ValidFrom: now,
		CreatedAt: now, UpdatedAt: now,
	}
	savedMemory, created, err := store.UpsertMemory(ctx, memoryItem)
	if err != nil || !created || savedMemory.ID != memoryID {
		t.Fatalf("UpsertMemory() = %#v, %v, %v", savedMemory, created, err)
	}
	savedMemory, created, err = store.UpsertMemory(ctx, memoryItem)
	if err != nil || created || savedMemory.ID != memoryID {
		t.Fatalf("UpsertMemory(idempotent) = %#v, %v, %v", savedMemory, created, err)
	}
	memoryItem.Pinned = true
	memoryItem.UpdatedAt = claimTime
	if err = store.UpdateMemory(ctx, memoryItem); err != nil {
		t.Fatalf("UpdateMemory() error = %v", err)
	}
	if memories, listErr := store.ListMemories(ctx, userID, 10); listErr != nil || len(memories) != 1 || !memories[0].Pinned {
		t.Fatalf("ListMemories() = %#v, %v", memories, listErr)
	}

	agentNow := claimTime.Add(time.Second)
	agentService := agent.NewServiceWithClock(store, func() time.Time { return agentNow })
	agentRun, created, err := agentService.Create(ctx, agent.CreateInput{
		UserID: userID, ConversationID: conversationID, CharacterID: characterID,
		Module: "life", IdempotencyKey: "agent-migration-message",
		Payload: map[string]any{"message_id": messageID, "text": userMessage.Content},
	})
	if err != nil || !created || agentRun.ThreadID != agentRun.ID {
		t.Fatalf("Create(agent run) = %#v, %v, %v", agentRun, created, err)
	}
	duplicateAgentRun, created, err := agentService.Create(ctx, agent.CreateInput{
		UserID: userID, ConversationID: conversationID, CharacterID: characterID,
		Module: "life", IdempotencyKey: "agent-migration-message",
		Payload: map[string]any{"message_id": messageID, "text": userMessage.Content},
	})
	if err != nil || created || duplicateAgentRun.ID != agentRun.ID {
		t.Fatalf("Create(agent run duplicate) = %#v, %v, %v", duplicateAgentRun, created, err)
	}
	claimedAgentRun, err := agentService.Claim(ctx, agentRun.ID, "langgraph-worker", time.Minute)
	if err != nil || claimedAgentRun.Status != "running" || claimedAgentRun.Revision != 2 {
		t.Fatalf("Claim(agent run) = %#v, %v", claimedAgentRun, err)
	}
	if _, err = agentService.Complete(ctx, agentRun.ID, "langgraph-worker", 1, map[string]any{"response": "stale"}); !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("Complete(agent stale revision) error = %v", err)
	}
	completedAgentRun, err := agentService.Complete(ctx, agentRun.ID, "langgraph-worker", claimedAgentRun.Revision, map[string]any{
		"response": "已读取今日计划。",
		"model": map[string]any{
			"manifest": map[string]any{"config_version": "migration-test-v1"},
			"calls": []map[string]any{{
				"status": "succeeded", "provider": "openrouter",
				"requested_model": "openai/gpt-5-mini",
				"returned_model":  "openai/gpt-5-mini-2026-01-01",
				"role":            "responder", "graph_node": "respond",
				"prompt_tokens": 20, "completion_tokens": 7,
				"cost_micros": 3, "latency_ms": 30,
			}},
		},
	})
	if err != nil || completedAgentRun.Status != "completed" || completedAgentRun.Revision != 3 {
		t.Fatalf("Complete(agent run) = %#v, %v", completedAgentRun, err)
	}
	var graphConfigVersion, graphRole string
	var graphPromptTokens int64
	if err = store.db.QueryRowContext(ctx, `
		SELECT model_config_version,role,prompt_tokens
		FROM app.model_usage_observations
		WHERE source_type='agent_graph' AND execution_id=$1 AND call_index=1`,
		agentRun.ID,
	).Scan(&graphConfigVersion, &graphRole, &graphPromptTokens); err != nil ||
		graphConfigVersion != "migration-test-v1" || graphRole != "responder" ||
		graphPromptTokens != 20 {
		t.Fatalf("agent model usage observation = %q/%q/%d, %v", graphConfigVersion, graphRole, graphPromptTokens, err)
	}
	agentEvents, err := agentService.Events(ctx, agentRun.ID, 0, 10)
	if err != nil || len(agentEvents) != 3 ||
		agentEvents[0].Type != "accepted" ||
		agentEvents[1].Type != "running" ||
		agentEvents[2].Type != "completed" {
		t.Fatalf("Agent events = %#v, %v", agentEvents, err)
	}
	approvalRun, created, err := agentService.Create(ctx, agent.CreateInput{
		UserID: userID, ConversationID: conversationID, CharacterID: characterID,
		Module: "life", IdempotencyKey: "agent-migration-approval",
		Payload: map[string]any{"message_id": messageID, "text": "午饭花了50元"},
	})
	if err != nil || !created {
		t.Fatalf("Create(agent approval run) = %#v, %v, %v", approvalRun, created, err)
	}
	claimedApprovalRun, err := agentService.ClaimNext(ctx, "agent-reconciler", time.Minute)
	if err != nil || claimedApprovalRun.ID != approvalRun.ID ||
		claimedApprovalRun.Status != "running" || claimedApprovalRun.Revision != 2 {
		t.Fatalf("ClaimNext(agent run) = %#v, %v", claimedApprovalRun, err)
	}
	pausedAgentRun, err := agentService.PauseForApproval(
		ctx,
		approvalRun.ID,
		"agent-reconciler",
		claimedApprovalRun.Revision,
		map[string]any{
			"response": "",
			"interrupts": []map[string]any{{
				"type": "tool_approval", "summary": "餐饮支出 50 元",
				"confirmation_token": "integration-signed-token",
			}},
		},
	)
	if err != nil || pausedAgentRun.Status != "waiting_approval" ||
		pausedAgentRun.Revision != 3 || pausedAgentRun.LeaseOwner != "" {
		t.Fatalf("PauseForApproval(agent run) = %#v, %v", pausedAgentRun, err)
	}
	resolvedAgentRun, changed, err := agentService.ResolveApproval(
		ctx, userID, approvalRun.ID, true, "agent-approval-resolution",
	)
	var resumeResolution struct {
		Approved bool `json:"approved"`
	}
	resumeDecodeErr := json.Unmarshal(resolvedAgentRun.Resume, &resumeResolution)
	if err != nil || !changed || resolvedAgentRun.Status != "queued" ||
		resolvedAgentRun.Revision != 4 || resumeDecodeErr != nil ||
		!resumeResolution.Approved {
		t.Fatalf("ResolveApproval(agent run) = %#v, %v, %v, decode=%v", resolvedAgentRun, changed, err, resumeDecodeErr)
	}
	replayedAgentRun, changed, err := agentService.ResolveApproval(
		ctx, userID, approvalRun.ID, true, "agent-approval-resolution",
	)
	if err != nil || changed || replayedAgentRun.ID != approvalRun.ID {
		t.Fatalf("ResolveApproval(agent replay) = %#v, %v, %v", replayedAgentRun, changed, err)
	}
	resumedAgentRun, err := agentService.Claim(ctx, approvalRun.ID, "agent-reconciler", time.Minute)
	if err != nil || resumedAgentRun.Status != "running" || resumedAgentRun.Revision != 5 {
		t.Fatalf("Claim(agent resume) = %#v, %v", resumedAgentRun, err)
	}
	resumedAgentRun, err = agentService.Complete(
		ctx, approvalRun.ID, "agent-reconciler", resumedAgentRun.Revision,
		map[string]any{"response": "已写入生活账本。"},
	)
	if err != nil || resumedAgentRun.Status != "completed" || resumedAgentRun.Revision != 6 {
		t.Fatalf("Complete(agent resume) = %#v, %v", resumedAgentRun, err)
	}
	toolWaitRun, created, err := agentService.Create(ctx, agent.CreateInput{
		UserID: userID, ConversationID: conversationID, CharacterID: characterID,
		Module: "work", IdempotencyKey: "agent-migration-tool-wake",
		Payload: map[string]any{"message_id": messageID, "text": "生成文件"},
	})
	if err != nil || !created {
		t.Fatalf("Create(agent tool wait run) = %#v, %v, %v", toolWaitRun, created, err)
	}
	claimedToolWaitRun, err := agentService.Claim(ctx, toolWaitRun.ID, "agent-tool-worker", time.Minute)
	if err != nil {
		t.Fatalf("Claim(agent tool wait run) error = %v", err)
	}
	suspendedToolWaitRun, err := agentService.SuspendForTool(
		ctx, toolWaitRun.ID, "agent-tool-worker", claimedToolWaitRun.Revision,
		map[string]any{"interrupts": []any{map[string]any{
			"type": "tool_wait", "task_id": skillRunID, "interrupt_id": "tool-wake-integration",
			"poll_after_ms": float64(60_000),
		}}},
		time.Minute,
	)
	if err != nil || suspendedToolWaitRun.Status != "waiting_tool" || suspendedToolWaitRun.Revision != 3 {
		t.Fatalf("SuspendForTool(agent run) = %#v, %v", suspendedToolWaitRun, err)
	}
	wokenToolRuns, err := agentService.WakeForTool(ctx, userID, skillRunID, 100)
	if err != nil || len(wokenToolRuns) != 1 || wokenToolRuns[0].ID != toolWaitRun.ID ||
		wokenToolRuns[0].Status != "queued" || wokenToolRuns[0].Revision != 4 ||
		!wokenToolRuns[0].AvailableAt.Equal(agentNow) {
		t.Fatalf("WakeForTool(agent run) = %#v, %v", wokenToolRuns, err)
	}
	if duplicateWake, wakeErr := agentService.WakeForTool(ctx, userID, skillRunID, 100); wakeErr != nil || len(duplicateWake) != 0 {
		t.Fatalf("WakeForTool(agent duplicate) = %#v, %v", duplicateWake, wakeErr)
	}
	resumedToolRun, err := agentService.Claim(ctx, toolWaitRun.ID, "agent-tool-worker", time.Minute)
	if err != nil || resumedToolRun.Revision != 5 {
		t.Fatalf("Claim(agent tool wake) = %#v, %v", resumedToolRun, err)
	}
	if _, err = agentService.Complete(
		ctx, toolWaitRun.ID, "agent-tool-worker", resumedToolRun.Revision,
		map[string]any{"response": "文件已生成。"},
	); err != nil {
		t.Fatalf("Complete(agent tool wake) error = %v", err)
	}
	var toolResumeEvents int
	if err = store.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM eventing.outbox_events
		WHERE aggregate_id=$1 AND event_type='agent.run.resume.requested.v1'`,
		toolWaitRun.ID,
	).Scan(&toolResumeEvents); err != nil || toolResumeEvents != 2 {
		t.Fatalf("agent tool resume events = %d, %v", toolResumeEvents, err)
	}
	cancelRun, created, err := agentService.Create(ctx, agent.CreateInput{
		UserID: userID, ConversationID: conversationID, CharacterID: characterID,
		Module: "life", IdempotencyKey: "agent-migration-cancel",
		Payload: map[string]any{"message_id": messageID, "text": "停止这个任务"},
	})
	if err != nil || !created {
		t.Fatalf("Create(agent cancel run) = %#v, %v, %v", cancelRun, created, err)
	}
	claimedCancelRun, err := agentService.Claim(ctx, cancelRun.ID, "agent-cancel-worker", time.Minute)
	if err != nil {
		t.Fatalf("Claim(agent cancel run) error = %v", err)
	}
	requestedCancelRun, changed, err := agentService.Cancel(ctx, userID, cancelRun.ID)
	if err != nil || !changed || requestedCancelRun.Status != "cancel_requested" ||
		requestedCancelRun.Revision != claimedCancelRun.Revision+1 {
		t.Fatalf("Cancel(agent running) = %#v, %v, %v", requestedCancelRun, changed, err)
	}
	if _, err = agentService.Complete(
		ctx, cancelRun.ID, "agent-cancel-worker", claimedCancelRun.Revision,
		map[string]any{"response": "must not be persisted"},
	); !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("Complete(after cancellation) error = %v", err)
	}
	cancelledRun, err := agentService.FinalizeCancellation(
		ctx, cancelRun.ID, "agent-cancel-worker", requestedCancelRun.Revision,
	)
	if err != nil || cancelledRun.Status != "cancelled" {
		t.Fatalf("FinalizeCancellation() = %#v, %v", cancelledRun, err)
	}
	cancelledRun, changed, err = agentService.Cancel(ctx, userID, cancelRun.ID)
	if err != nil || changed || cancelledRun.Status != "cancelled" {
		t.Fatalf("Cancel(agent replay) = %#v, %v, %v", cancelledRun, changed, err)
	}

	timeoutNow := agentNow
	timeoutService := agent.NewServiceWithClock(store, func() time.Time { return timeoutNow })
	timeoutService.SetRunTimeout(time.Second)
	timeoutRun, created, err := timeoutService.Create(ctx, agent.CreateInput{
		UserID: userID, ConversationID: conversationID, CharacterID: characterID,
		Module: "life", IdempotencyKey: "agent-migration-timeout",
		Payload: map[string]any{"message_id": messageID, "text": "超时任务"},
	})
	if err != nil || !created {
		t.Fatalf("Create(agent timeout run) = %#v, %v, %v", timeoutRun, created, err)
	}
	timeoutNow = timeoutNow.Add(2 * time.Second)
	expired, err := timeoutService.ExpireDue(ctx, 100)
	if err != nil || expired != 1 {
		t.Fatalf("ExpireDue() = %d, %v", expired, err)
	}
	timeoutRun, err = timeoutService.Get(ctx, timeoutRun.ID)
	if err != nil || timeoutRun.Status != "timed_out" || timeoutRun.CompletedAt == nil {
		t.Fatalf("Get(agent timeout run) = %#v, %v", timeoutRun, err)
	}
	if _, err = timeoutService.Claim(ctx, timeoutRun.ID, "late-worker", time.Minute); !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("Claim(agent timed out) error = %v", err)
	}

	agentChatMessage, agentChatRun, err := agentService.AcceptChat(ctx, agent.AcceptChatInput{
		UserID: userID, ConversationID: conversationID, CharacterID: characterID,
		Module: "life", Content: "我今天的计划是什么",
		Context: map[string]any{"timezone": "Asia/Shanghai"},
	})
	if err != nil || agentChatMessage.Sequence != 3 || agentChatRun.ThreadID != agentChatRun.ID {
		t.Fatalf("AcceptChat(agent) = %#v, %#v, %v", agentChatMessage, agentChatRun, err)
	}
	var legacyJobsForAgentMessage int
	if err = store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM app.generation_jobs WHERE user_message_id=$1`,
		agentChatMessage.ID,
	).Scan(&legacyJobsForAgentMessage); err != nil || legacyJobsForAgentMessage != 0 {
		t.Fatalf("legacy jobs for Agent message = %d, %v", legacyJobsForAgentMessage, err)
	}
	claimedAgentChatRun, err := agentService.Claim(ctx, agentChatRun.ID, "agent-chat-worker", time.Minute)
	if err != nil {
		t.Fatalf("Claim(agent chat) error = %v", err)
	}
	completedAgentChatRun, err := agentService.Complete(
		ctx, agentChatRun.ID, "agent-chat-worker", claimedAgentChatRun.Revision,
		map[string]any{"response": "今天没有计划。"},
	)
	if err != nil || completedAgentChatRun.Status != "completed" {
		t.Fatalf("Complete(agent chat) = %#v, %v", completedAgentChatRun, err)
	}
	agentChatMessages, err := store.ListMessages(ctx, userID, conversationID, 2, 2, 10)
	if err != nil || len(agentChatMessages) != 2 ||
		agentChatMessages[0].ID != agentChatMessage.ID ||
		agentChatMessages[1].Role != "assistant" ||
		agentChatMessages[1].Content != "今天没有计划。" {
		t.Fatalf("Agent chat messages = %#v, %v", agentChatMessages, err)
	}

	retryMessage, retryRun, err := agentService.AcceptChat(ctx, agent.AcceptChatInput{
		UserID: userID, ConversationID: conversationID, CharacterID: characterID,
		Module: "life", Content: "执行持久化重试恢复验收",
	})
	if err != nil || retryRun.Revision != 1 || retryMessage.Role != "user" {
		t.Fatalf("AcceptChat(agent retry) = %#v, %#v, %v", retryMessage, retryRun, err)
	}
	claimedRetryRun, err := agentService.Claim(ctx, retryRun.ID, "agent-retry-worker", time.Minute)
	if err != nil || claimedRetryRun.Status != "running" || claimedRetryRun.Revision != 2 {
		t.Fatalf("Claim(agent retry) = %#v, %v", claimedRetryRun, err)
	}
	retryAvailableAt := agentNow.Add(17 * time.Second)
	deferredRetryRun, err := agentService.Defer(
		ctx, retryRun.ID, "agent-retry-worker", claimedRetryRun.Revision,
		17*time.Second, "controlled transient 503",
	)
	if err != nil || deferredRetryRun.Status != "queued" ||
		deferredRetryRun.Revision != 3 || deferredRetryRun.LeaseOwner != "" ||
		!deferredRetryRun.AvailableAt.Equal(retryAvailableAt) {
		t.Fatalf("Defer(agent retry) = %#v, %v", deferredRetryRun, err)
	}
	if _, err = agentService.Claim(ctx, retryRun.ID, "agent-retry-worker", time.Minute); !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("Claim(agent retry before available_at) error = %v", err)
	}
	agentNow = retryAvailableAt
	resumedRetryRun, err := agentService.Claim(ctx, retryRun.ID, "agent-retry-worker", time.Minute)
	if err != nil || resumedRetryRun.Status != "running" || resumedRetryRun.Revision != 4 {
		t.Fatalf("Claim(agent retry after available_at) = %#v, %v", resumedRetryRun, err)
	}
	completedRetryRun, err := agentService.Complete(
		ctx, retryRun.ID, "agent-retry-worker", resumedRetryRun.Revision,
		map[string]any{"response": "持久化重试恢复成功。"},
	)
	if err != nil || completedRetryRun.Status != "completed" || completedRetryRun.Revision != 5 {
		t.Fatalf("Complete(agent retry) = %#v, %v", completedRetryRun, err)
	}
	if _, err = agentService.Complete(
		ctx, retryRun.ID, "agent-retry-worker", resumedRetryRun.Revision,
		map[string]any{"response": "must not be duplicated"},
	); !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("Complete(agent retry replay) error = %v", err)
	}
	var retryAssistantMessages int
	if err = store.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM app.messages
		WHERE conversation_id=$1 AND role='assistant' AND content=$2`,
		conversationID, "持久化重试恢复成功。",
	).Scan(&retryAssistantMessages); err != nil || retryAssistantMessages != 1 {
		t.Fatalf("retry assistant messages = %d, %v", retryAssistantMessages, err)
	}
	retryEvents, err := agentService.Events(ctx, retryRun.ID, 0, 10)
	var retryEventPayload struct {
		RetryKind   string    `json:"retry_kind"`
		Reason      string    `json:"reason"`
		AvailableAt time.Time `json:"available_at"`
	}
	if err == nil && len(retryEvents) == 5 {
		err = json.Unmarshal(retryEvents[2].Payload, &retryEventPayload)
	}
	if err != nil || len(retryEvents) != 5 ||
		retryEvents[0].Type != "accepted" || retryEvents[1].Type != "running" ||
		retryEvents[2].Type != "queued" || retryEvents[3].Type != "running" ||
		retryEvents[4].Type != "completed" ||
		retryEventPayload.RetryKind != "execution" ||
		retryEventPayload.Reason != "controlled transient 503" ||
		!retryEventPayload.AvailableAt.Equal(retryAvailableAt) {
		t.Fatalf("Agent retry events = %#v, payload=%#v, %v", retryEvents, retryEventPayload, err)
	}

	budgetRun, created, err := agentService.Create(ctx, agent.CreateInput{
		UserID: userID, ConversationID: conversationID, CharacterID: characterID,
		Module: "life", IdempotencyKey: "agent-migration-retry-exhausted",
		Payload: map[string]any{"message_id": messageID, "text": "重试次数耗尽验收"},
	})
	if err != nil || !created {
		t.Fatalf("Create(agent retry exhausted) = %#v, %v, %v", budgetRun, created, err)
	}
	claimedBudgetRun, err := agentService.Claim(ctx, budgetRun.ID, "agent-retry-worker", time.Minute)
	if err != nil {
		t.Fatalf("Claim(agent retry exhausted) = %#v, %v", claimedBudgetRun, err)
	}
	deferredBudgetRun, err := agentService.Defer(
		ctx, budgetRun.ID, "agent-retry-worker", claimedBudgetRun.Revision,
		time.Second, "controlled transient 408",
	)
	if err != nil || deferredBudgetRun.Revision != 3 {
		t.Fatalf("Defer(agent retry exhausted) = %#v, %v", deferredBudgetRun, err)
	}
	agentNow = agentNow.Add(time.Second)
	claimedBudgetRun, err = agentService.Claim(ctx, budgetRun.ID, "agent-retry-worker", time.Minute)
	if err != nil || claimedBudgetRun.Revision != 4 {
		t.Fatalf("Claim(agent retry exhausted resume) = %#v, %v", claimedBudgetRun, err)
	}
	failedBudgetRun, err := agentService.Fail(
		ctx, budgetRun.ID, "agent-retry-worker", claimedBudgetRun.Revision,
		"execution_retry_budget_exhausted", "controlled retry budget exhausted",
	)
	if err != nil || failedBudgetRun.Status != "failed" || failedBudgetRun.Revision != 5 {
		t.Fatalf("Fail(agent retry exhausted) = %#v, %v", failedBudgetRun, err)
	}

	deadlineRun, created, err := agentService.Create(ctx, agent.CreateInput{
		UserID: userID, ConversationID: conversationID, CharacterID: characterID,
		Module: "life", IdempotencyKey: "agent-migration-retry-deadline",
		Payload: map[string]any{"message_id": messageID, "text": "重试截止时间验收"},
	})
	if err != nil || !created {
		t.Fatalf("Create(agent retry deadline) = %#v, %v, %v", deadlineRun, created, err)
	}
	claimedDeadlineRun, err := agentService.Claim(ctx, deadlineRun.ID, "agent-retry-worker", time.Minute)
	if err != nil || !agentNow.Before(claimedDeadlineRun.DeadlineAt) {
		t.Fatalf("Claim(agent retry deadline) = %#v, %v", claimedDeadlineRun, err)
	}
	timedOutRetryRun, err := agentService.TimeoutForRetryDeadline(
		ctx, deadlineRun.ID, "agent-retry-worker", claimedDeadlineRun.Revision,
	)
	if err != nil || timedOutRetryRun.Status != "timed_out" ||
		timedOutRetryRun.ErrorCode != "execution_retry_deadline_exhausted" ||
		timedOutRetryRun.Revision != 3 {
		t.Fatalf("TimeoutForRetryDeadline() = %#v, %v", timedOutRetryRun, err)
	}

	summary := conversation.ConversationSummary{
		ID: mustID(t), ConversationID: conversationID, UserID: userID,
		StartSequence: 1, EndSequence: 2, RangeStartedAt: now,
		RangeEndedAt: claimTime, Content: "用户查询今日计划。", TokenCount: 12,
		SummarizerVersion: "migration-test", CreatedAt: claimTime,
	}
	if err := store.SaveSummary(ctx, summary); err != nil {
		t.Fatalf("SaveSummary() error = %v", err)
	}
	if latest, err := store.GetLatestSummary(ctx, userID, conversationID); err != nil || latest.Version != 1 {
		t.Fatalf("GetLatestSummary() = %#v, %v", latest, err)
	}

	var chatEventID, chatEventType string
	if err = store.db.QueryRowContext(ctx, `
		SELECT id::text,event_type
		FROM eventing.outbox_events
		WHERE aggregate_id=$1 AND event_type='chat.command.v1'
		LIMIT 1`,
		conversationID,
	).Scan(&chatEventID, &chatEventType); err != nil ||
		chatEventType != "chat.command.v1" {
		t.Fatalf("chat outbox event = %q/%q, %v", chatEventID, chatEventType, err)
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT aggregate_id,event_type
		FROM eventing.outbox_events
		WHERE aggregate_type='agent_run'
			AND aggregate_id IN ($1,$2,$3)`,
		agentRun.ID, approvalRun.ID, agentChatRun.ID,
	)
	if err != nil {
		t.Fatalf("query Agent outbox events: %v", err)
	}
	defer rows.Close()
	agentEventIDs := map[string]bool{}
	resumeEventFound := false
	for rows.Next() {
		var aggregateID, eventType string
		if err = rows.Scan(&aggregateID, &eventType); err != nil {
			t.Fatalf("scan Agent outbox event: %v", err)
		}
		if eventType == "agent.run.requested.v1" {
			agentEventIDs[aggregateID] = true
		}
		if eventType == "agent.run.resume.requested.v1" &&
			aggregateID == approvalRun.ID {
			resumeEventFound = true
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatalf("iterate Agent outbox events: %v", err)
	}
	if !agentEventIDs[agentRun.ID] || !agentEventIDs[approvalRun.ID] ||
		!agentEventIDs[agentChatRun.ID] || !resumeEventFound {
		t.Fatalf("transactional Agent outbox events = %#v, resume=%v", agentEventIDs, resumeEventFound)
	}
	inserted, err := store.RecordInboxEvent(ctx, "migration-test", chatEventID, claimTime)
	if err != nil || !inserted {
		t.Fatalf("RecordInboxEvent(first) = %v, %v", inserted, err)
	}
	inserted, err = store.RecordInboxEvent(ctx, "migration-test", chatEventID, claimTime)
	if err != nil || inserted {
		t.Fatalf("RecordInboxEvent(duplicate) = %v, %v", inserted, err)
	}
	if sample, sampleErr := store.ReliabilitySample(ctx, agentNow); sampleErr != nil ||
		sample.QueueLag < 0 || sample.ModelUsage.Calls < 2 ||
		sample.ModelUsage.PromptTokens < 30 || sample.ModelUsage.CompletionTokens < 12 ||
		sample.ModelUsage.CostMicros < 3 || sample.AgentRetries.ScheduledRecent < 1 ||
		sample.AgentRetries.RecoveredRecent < 1 || sample.AgentRetries.ExhaustedRecent < 1 ||
		sample.AgentRetries.DeadlineExhaustedRecent < 1 ||
		sample.AgentRetries.RecoveryRatio <= 0 || sample.AgentRetries.RecoveryRatio >= 1 {
		t.Fatalf("ReliabilitySample() = %#v, %v", sample, sampleErr)
	}
}

func TestLifeStores(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	userID := mustID(t)
	candidateID := mustID(t)
	entryID := mustID(t)
	exportID := mustID(t)
	workspaceID := mustID(t)
	planID := mustID(t)
	planItemID := mustID(t)
	reminderID := mustID(t)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.workspace_ledger_export_shares WHERE workspace_id=$1`, workspaceID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.workspace_members WHERE workspace_id=$1`, workspaceID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.workspaces WHERE id=$1`, workspaceID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.notification_deliveries WHERE reminder_id=$1`, reminderID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.reminder_events WHERE reminder_id=$1`, reminderID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.external_reminder_links WHERE reminder_id=$1`, reminderID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.reminders WHERE id=$1`, reminderID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.plan_items WHERE plan_id=$1`, planID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.plans WHERE id=$1`, planID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.ledger_exports WHERE id=$1`, exportID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.ledger_entries WHERE id=$1`, entryID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.ledger_candidates WHERE id=$1`, candidateID)
		_, _ = store.db.ExecContext(cleanupCtx, `
			DELETE FROM eventing.outbox_events
			WHERE aggregate_id IN ($1,$2,$3,$4)`,
			entryID, exportID, reminderID, userID,
		)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.audit_logs WHERE actor_id=$1`, userID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.users WHERE id=$1`, userID)
	})

	now := time.Date(1999, time.January, 1, 8, 0, 0, 0, time.UTC)
	user := identity.User{
		ID: userID, Email: "life-migration@example.com", PasswordHash: "hash",
		DisplayName: "生活域迁移验收", Timezone: "Asia/Shanghai", Locale: "zh-CN",
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	if err = store.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if err = store.CreateWorkspace(ctx,
		team.Workspace{
			ID: workspaceID, Name: "生活域迁移工作区", OwnerID: userID,
			Status: "active", CreatedAt: now, UpdatedAt: now,
		},
		team.Member{
			WorkspaceID: workspaceID, UserID: userID, Role: "owner",
			Status: "active", JoinedAt: now, UpdatedAt: now,
		},
	); err != nil {
		t.Fatalf("CreateWorkspace(life) error = %v", err)
	}

	occurredAt := now.Add(time.Minute)
	candidate := ledger.Candidate{
		ID: candidateID, UserID: userID, RawText: "午饭花了50元",
		Direction: "expense", Currency: "CNY", AmountMinor: 5000,
		Category: "餐饮", OccurredAt: &occurredAt, Timezone: "Asia/Shanghai",
		TimePrecision: "minute", Confidence: 0.99, NeedsClarification: []string{},
		Status: "pending", CreatedAt: now, UpdatedAt: now,
	}
	if err = store.CreateCandidate(ctx, candidate); err != nil {
		t.Fatalf("CreateCandidate() error = %v", err)
	}
	entry := ledger.Entry{
		ID: entryID, UserID: userID, CandidateID: candidateID,
		Direction: "expense", Currency: "CNY", AmountMinor: 5000,
		Category: "餐饮", OccurredAt: occurredAt, Timezone: "Asia/Shanghai",
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	confirmedEntry, created, err := store.ConfirmCandidate(ctx, candidate, entry, "life-entry-confirm", now)
	if err != nil || !created || confirmedEntry.AmountMinor != 5000 {
		t.Fatalf("ConfirmCandidate() = %#v, %v, %v", confirmedEntry, created, err)
	}
	confirmedEntry, created, err = store.ConfirmCandidate(ctx, candidate, entry, "life-entry-confirm", now)
	if err != nil || created || confirmedEntry.ID != entryID {
		t.Fatalf("ConfirmCandidate(idempotent) = %#v, %v, %v", confirmedEntry, created, err)
	}
	var ledgerSideEffects int
	if err = store.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM eventing.audit_logs WHERE resource_id=$1)
			+
			(SELECT COUNT(*) FROM eventing.outbox_events
			 WHERE aggregate_id=$1 AND event_type='ledger.entry.created.v1')`,
		entryID,
	).Scan(&ledgerSideEffects); err != nil || ledgerSideEffects != 2 {
		t.Fatalf("ledger transactional side effects = %d, %v", ledgerSideEffects, err)
	}

	export := ledger.ExportJob{
		ID: exportID, UserID: userID, Month: "1999-01", Currency: "CNY",
		Timezone: "Asia/Shanghai", Status: "queued", CreatedAt: now, UpdatedAt: now,
	}
	export, created, err = store.CreateExport(ctx, export, "life-export")
	if err != nil || !created {
		t.Fatalf("CreateExport() = %#v, %v, %v", export, created, err)
	}
	export, err = store.ClaimExportByID(ctx, exportID, "life-worker", now, time.Minute)
	if err != nil || export.Status != "processing" {
		t.Fatalf("ClaimExportByID() = %#v, %v", export, err)
	}
	completedAt := now.Add(time.Second)
	export.StorageKey = "ledger/life-test.xlsx"
	export.FileName = "ledger-1999-01-CNY.xlsx"
	export.MediaType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	export.SHA256 = strings.Repeat("b", 64)
	export.SizeBytes = 128
	export.UpdatedAt = completedAt
	export.CompletedAt = &completedAt
	if err = store.CompleteExport(ctx, export); err != nil {
		t.Fatalf("CompleteExport() error = %v", err)
	}
	if err = store.ShareExportWithWorkspace(ctx, userID, workspaceID, exportID, completedAt); err != nil {
		t.Fatalf("ShareExportWithWorkspace() error = %v", err)
	}
	if shared, err := store.GetWorkspaceExport(ctx, workspaceID, exportID); err != nil || shared.FileName != export.FileName {
		t.Fatalf("GetWorkspaceExport() = %#v, %v", shared, err)
	}

	plan := planner.Plan{
		ID: planID, UserID: userID, Title: "今日计划", LocalDate: "1999-01-01",
		Timezone: "UTC", Status: "active", CreatedAt: now, UpdatedAt: now,
		Items: []planner.PlanItem{{
			ID: planItemID, PlanID: planID, Title: "整理计划", Priority: "medium",
			Status: "pending", Source: "migration-test", CreatedAt: now, UpdatedAt: now,
		}},
	}
	if err = store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	startsAt := now.Add(30 * time.Minute)
	scheduledItem := plan.Items[0]
	scheduledItem.StartsAt = &startsAt
	if err = store.UpdatePlanItemSchedule(ctx, userID, scheduledItem, &scheduledItem.UpdatedAt, now.Add(time.Second)); err != nil {
		t.Fatalf("UpdatePlanItemSchedule() error = %v", err)
	}
	if err = store.CompletePlanItem(ctx, userID, planItemID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("CompletePlanItem() error = %v", err)
	}

	dueAt := now.Add(time.Hour)
	reminder := planner.Reminder{
		ID: reminderID, UserID: userID, RawText: "九点提醒我提交资料",
		Title: "提交资料", DueAt: &dueAt, LocalDue: "1999-01-01 09:00",
		Timezone: "UTC", TimePrecision: "minute", Recurrence: "none",
		NeedsClarification: []string{}, Status: "pending_confirmation",
		SystemSyncStatus: "not_requested", CreatedAt: now, UpdatedAt: now,
	}
	if err = store.CreateReminder(ctx, reminder); err != nil {
		t.Fatalf("CreateReminder() error = %v", err)
	}
	confirmedReminder, created, err := store.ConfirmReminder(ctx, reminder, "life-reminder-confirm", now)
	if err != nil || !created || confirmedReminder.Status != "active" {
		t.Fatalf("ConfirmReminder() = %#v, %v, %v", confirmedReminder, created, err)
	}
	newDueAt := dueAt.Add(time.Hour)
	updatedReminder := confirmedReminder
	updatedReminder.DueAt = &newDueAt
	updatedReminder.LocalDue = "1999-01-01 10:00"
	expectedReminderVersion := confirmedReminder.UpdatedAt
	updatedReminder, err = store.RescheduleReminder(ctx, updatedReminder, &expectedReminderVersion, now.Add(time.Minute))
	if err != nil || updatedReminder.LocalDue != "1999-01-01 10:00" {
		t.Fatalf("RescheduleReminder() = %#v, %v", updatedReminder, err)
	}
	updatedReminder, err = store.UpdateReminderSync(ctx, userID, reminderID, planner.SyncResult{
		Status: "synced", Provider: "android_alarm", ExternalID: "life-test",
		Revision: "1",
	}, now.Add(2*time.Minute))
	if err != nil || updatedReminder.SystemSyncStatus != "synced" {
		t.Fatalf("UpdateReminderSync() = %#v, %v", updatedReminder, err)
	}
	enqueued, err := store.EnqueueDueNotifications(ctx, newDueAt.Add(time.Minute), 10)
	if err != nil || enqueued != 1 {
		t.Fatalf("EnqueueDueNotifications() = %d, %v", enqueued, err)
	}
	var deliveryID string
	if err = store.db.QueryRowContext(ctx, `
		SELECT id::text
		FROM app.notification_deliveries
		WHERE reminder_id=$1 AND scheduled_at=$2`,
		reminderID, newDueAt,
	).Scan(&deliveryID); err != nil {
		t.Fatalf("find notification delivery error = %v", err)
	}
	if err = store.DeliverNotification(ctx, deliveryID, newDueAt.Add(time.Minute)); err != nil {
		t.Fatalf("DeliverNotification() error = %v", err)
	}
	if err = store.CompleteReminder(ctx, userID, reminderID, newDueAt.Add(2*time.Minute)); err != nil {
		t.Fatalf("CompleteReminder() error = %v", err)
	}
}

func TestDocumentAndSkillStores(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	userID := mustID(t)
	fileID := mustID(t)
	documentID := mustID(t)
	ingestJobID := mustID(t)
	pageID := mustID(t)
	chunkID := mustID(t)
	pointID := mustID(t)
	documentWorkspaceID := mustID(t)
	runID := mustID(t)
	initialStepID := mustID(t)
	failedExecuteStepID := mustID(t)
	executeStepID := mustID(t)
	generatedFileID := mustID(t)
	skillWorkspaceID := mustID(t)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.workspace_generated_file_shares WHERE workspace_id=$1`, skillWorkspaceID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.generated_files WHERE run_id=$1`, runID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.tool_executions WHERE run_id=$1`, runID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.skill_run_steps WHERE run_id=$1`, runID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.skill_run_action_keys WHERE run_id=$1`, runID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.skill_runs WHERE id=$1`, runID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.user_skill_settings WHERE user_id=$1`, userID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.workspace_document_shares WHERE workspace_id=$1`, documentWorkspaceID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.workspace_members WHERE workspace_id IN ($1,$2)`, documentWorkspaceID, skillWorkspaceID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.workspaces WHERE id IN ($1,$2)`, documentWorkspaceID, skillWorkspaceID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.document_cleanup_jobs WHERE document_id=$1`, documentID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.document_chunks WHERE document_id=$1`, documentID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.document_pages WHERE document_id=$1`, documentID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.document_ingest_jobs WHERE document_id=$1`, documentID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.documents WHERE id=$1`, documentID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.files WHERE id=$1`, fileID)
		_, _ = store.db.ExecContext(cleanupCtx, `
			DELETE FROM eventing.outbox_events
			WHERE aggregate_id IN ($1,$2)`,
			documentID, runID,
		)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.audit_logs WHERE actor_id=$1`, userID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.users WHERE id=$1`, userID)
	})

	now := time.Date(1998, time.January, 1, 8, 0, 0, 0, time.UTC)
	user := identity.User{
		ID: userID, Email: "tool-migration@example.com", PasswordHash: "hash",
		DisplayName: "工具域迁移验收", Timezone: "Asia/Shanghai", Locale: "zh-CN",
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	if err = store.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	for workspaceID, name := range map[string]string{
		documentWorkspaceID: "文档迁移工作区",
		skillWorkspaceID:    "技能迁移工作区",
	} {
		if err = store.CreateWorkspace(ctx,
			team.Workspace{
				ID: workspaceID, Name: name, OwnerID: userID,
				Status: "active", CreatedAt: now, UpdatedAt: now,
			},
			team.Member{
				WorkspaceID: workspaceID, UserID: userID, Role: "owner",
				Status: "active", JoinedAt: now, UpdatedAt: now,
			},
		); err != nil {
			t.Fatalf("CreateWorkspace(%s) error = %v", name, err)
		}
	}

	item := document.Document{
		ID: documentID, UserID: userID, FileID: fileID, JobID: ingestJobID,
		Name: "migration.pdf", MediaType: "application/pdf", SizeBytes: 128,
		SHA256: strings.Repeat("c", 64), StorageKey: "documents/migration.pdf",
		Status: "queued", CreatedAt: now, UpdatedAt: now,
	}
	savedDocument, created, err := store.CreateDocument(ctx, item)
	if err != nil || !created || savedDocument.JobID != ingestJobID {
		t.Fatalf("CreateDocument() = %#v, %v, %v", savedDocument, created, err)
	}
	duplicate := item
	duplicate.ID, duplicate.FileID, duplicate.JobID = mustID(t), mustID(t), mustID(t)
	savedDocument, created, err = store.CreateDocument(ctx, duplicate)
	if err != nil || created || savedDocument.ID != documentID {
		t.Fatalf("CreateDocument(duplicate) = %#v, %v, %v", savedDocument, created, err)
	}
	ingestJob, err := store.ClaimIngestJobByID(ctx, ingestJobID, "document-worker", now, time.Minute)
	if err != nil || ingestJob.Attempts != 1 || ingestJob.Document.Status != "processing" {
		t.Fatalf("ClaimIngestJobByID() = %#v, %v", ingestJob, err)
	}
	parseResult := document.ParseResult{
		ParserVersion: "migration-test",
		Pages: []document.Page{{
			ID: pageID, PageNo: 1, Text: "迁移测试", Quality: 1,
			ContentHash: strings.Repeat("d", 64),
		}},
		Chunks: []document.Chunk{{
			ID: chunkID, PointID: pointID, Ordinal: 1, PageStart: 1, PageEnd: 1,
			Content: "迁移测试", TokenCount: 4, ContentHash: strings.Repeat("e", 64),
			EmbeddingVersion: document.EmbeddingVersion,
		}},
	}
	if err = store.SaveParsedDocument(ctx, ingestJob, parseResult, now.Add(time.Second)); err != nil {
		t.Fatalf("SaveParsedDocument() error = %v", err)
	}
	if err = store.CompleteIngestJob(ctx, ingestJobID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("CompleteIngestJob() error = %v", err)
	}
	readyDocument, err := store.GetDocument(ctx, userID, documentID)
	if err != nil || readyDocument.Status != "ready" || readyDocument.PageCount != 1 || readyDocument.ChunkCount != 1 {
		t.Fatalf("GetDocument(ready) = %#v, %v", readyDocument, err)
	}
	if err = store.ShareDocumentWithWorkspace(ctx, userID, documentWorkspaceID, documentID, now); err != nil {
		t.Fatalf("ShareDocumentWithWorkspace() error = %v", err)
	}
	if shared, err := store.ListWorkspaceDocuments(ctx, documentWorkspaceID, 10); err != nil || len(shared) != 1 {
		t.Fatalf("ListWorkspaceDocuments() = %#v, %v", shared, err)
	}
	if err = store.DeleteDocument(ctx, userID, documentID, now.Add(3*time.Second)); err != nil {
		t.Fatalf("DeleteDocument() error = %v", err)
	}
	var cleanupJobID string
	if err = store.db.QueryRowContext(ctx, `
		SELECT id::text FROM app.document_cleanup_jobs WHERE document_id=$1`,
		documentID,
	).Scan(&cleanupJobID); err != nil {
		t.Fatalf("find cleanup job error = %v", err)
	}
	cleanupJob, err := store.ClaimCleanupJobByID(ctx, cleanupJobID, "cleanup-worker", now.Add(3*time.Second), time.Minute)
	if err != nil || cleanupJob.Attempts != 1 {
		t.Fatalf("ClaimCleanupJobByID() = %#v, %v", cleanupJob, err)
	}
	if err = store.FailCleanupJob(ctx, cleanupJob, "cleanup-worker", errors.New("temporary cleanup failure"), now.Add(4*time.Second)); err != nil {
		t.Fatalf("FailCleanupJob() error = %v", err)
	}
	cleanupJob, err = store.ClaimCleanupJobByID(ctx, cleanupJobID, "cleanup-worker-2", now.Add(10*time.Second), time.Minute)
	if err != nil || cleanupJob.Attempts != 2 {
		t.Fatalf("ClaimCleanupJobByID(retry) = %#v, %v", cleanupJob, err)
	}
	if err = store.CompleteCleanupJob(ctx, cleanupJob, "cleanup-worker-2", now.Add(11*time.Second)); err != nil {
		t.Fatalf("CompleteCleanupJob() error = %v", err)
	}

	if err = store.SetSkillEnabled(ctx, userID, "office.translate", true, now); err != nil {
		t.Fatalf("SetSkillEnabled() error = %v", err)
	}
	input, _ := json.Marshal(map[string]string{"text": "你好", "target_language": "English"})
	initialCompleted := now
	run := skill.Run{
		ID: runID, UserID: userID, SkillName: "office.translate", SkillVersion: "1",
		ExecutionMode: "worker", Status: "queued", CurrentState: "queued",
		RiskLevel: "low", Input: input, CreateKey: "tool-migration-run",
		Attempt: 1, MaxSteps: 8, TimeoutMS: 30000, MaxInputBytes: 65536,
		Revision: 1, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
		Steps: []skill.Step{{
			ID: initialStepID, Sequence: 1, State: "receive", Status: "succeeded",
			StartedAt: now, CompletedAt: &initialCompleted,
		}},
	}
	if err = store.CreateSkillRun(ctx, run); err != nil {
		t.Fatalf("CreateSkillRun() error = %v", err)
	}
	claimedRun, err := store.ClaimSkillRunByID(ctx, runID, "skill-worker", now, time.Minute)
	if err != nil || claimedRun.Status != "running" || claimedRun.Revision != 2 {
		t.Fatalf("ClaimSkillRunByID() = %#v, %v", claimedRun, err)
	}
	if err = store.RenewSkillRunLease(ctx, runID, "skill-worker", claimedRun.Revision, now.Add(time.Second), time.Minute); err != nil {
		t.Fatalf("RenewSkillRunLease() error = %v", err)
	}
	output, _ := json.Marshal(map[string]any{
		"translated_text": "Hello",
		"model_usage": map[string]any{
			"provider": "openrouter", "config_version": "migration-skill-v1",
			"requested_model": "openai/gpt-5-mini",
			"returned_model":  "openai/gpt-5-mini-2026-01-01",
			"prompt_tokens":   40, "completion_tokens": 9,
			"cost_micros": 5, "latency_ms": 45,
		},
	})
	failedOutput, _ := json.Marshal(map[string]any{
		"operation_error": map[string]any{"code": "translation_model_truncated"},
		"model_usage": map[string]any{
			"provider": "openrouter", "config_version": "migration-skill-v1",
			"requested_model": "openai/gpt-5-mini",
			"returned_model":  "openai/gpt-5-mini-2026-01-01",
			"prompt_tokens":   15, "completion_tokens": 0,
			"cost_micros": 2, "latency_ms": 20,
		},
	})
	failedAt := now.Add(1500 * time.Millisecond)
	failedExecuteStep := skill.Step{
		ID: failedExecuteStepID, Sequence: 2, State: "execute", Status: "failed",
		ToolName: "office.translate", Input: input, Output: failedOutput,
		ErrorCode: "model_output_invalid", ErrorMessage: "translation_model_truncated",
		StartedAt: now.Add(time.Second), CompletedAt: &failedAt,
	}
	completedAt := now.Add(2 * time.Second)
	executeStep := skill.Step{
		ID: executeStepID, Sequence: 3, State: "execute", Status: "succeeded",
		ToolName: "office.translate", Input: input, Output: output,
		StartedAt: failedAt, CompletedAt: &completedAt,
	}
	claimedRun.Status = "succeeded"
	claimedRun.CurrentState = "deliver"
	claimedRun.Output = output
	claimedRun.Revision = 3
	claimedRun.UpdatedAt = completedAt
	claimedRun.CompletedAt = &completedAt
	generatedFile := skill.GeneratedFile{
		ID: generatedFileID, RunID: runID, Name: "translated.txt",
		MediaType: "text/plain", SizeBytes: 5, SHA256: strings.Repeat("f", 64),
		StorageKey: "skills/tool-migration/translated.txt", CreatedAt: completedAt,
	}
	if err = store.SaveSkillRun(ctx, claimedRun, 2, []skill.Step{failedExecuteStep, executeStep}, []skill.GeneratedFile{generatedFile}); err != nil {
		t.Fatalf("SaveSkillRun() error = %v", err)
	}
	completedRun, err := store.GetSkillRun(ctx, userID, runID)
	if err != nil || completedRun.Status != "succeeded" || len(completedRun.Files) != 1 || len(completedRun.Steps) != 3 {
		t.Fatalf("GetSkillRun(completed) = %#v, %v", completedRun, err)
	}
	var skillConfigVersion, skillRole string
	var skillCalls, skillCostMicros int64
	if err = store.db.QueryRowContext(ctx, `
		SELECT MIN(model_config_version),MIN(role),COUNT(*),SUM(cost_micros)
		FROM app.model_usage_observations
		WHERE source_type='skill' AND execution_id=$1`,
		runID,
	).Scan(&skillConfigVersion, &skillRole, &skillCalls, &skillCostMicros); err != nil ||
		skillConfigVersion != "migration-skill-v1" ||
		skillRole != "office.translate" || skillCalls != 2 || skillCostMicros != 7 {
		t.Fatalf("skill model usage observation = %q/%q/%d/%d, %v", skillConfigVersion, skillRole, skillCalls, skillCostMicros, err)
	}
	if err = store.ShareGeneratedFileWithWorkspace(ctx, userID, skillWorkspaceID, runID, generatedFileID, completedAt); err != nil {
		t.Fatalf("ShareGeneratedFileWithWorkspace() error = %v", err)
	}
	if sharedFile, err := store.GetWorkspaceGeneratedFile(ctx, skillWorkspaceID, generatedFileID); err != nil || sharedFile.Name != generatedFile.Name {
		t.Fatalf("GetWorkspaceGeneratedFile() = %#v, %v", sharedFile, err)
	}
	var toolSideEffects int
	if err = store.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM app.tool_executions WHERE run_id=$1)
			+
			(SELECT COUNT(*) FROM eventing.audit_logs
			 WHERE resource_id=$1 AND action='skill.execute')
			+
			(SELECT COUNT(*) FROM eventing.outbox_events
			 WHERE aggregate_id=$1 AND event_type='skill.run.succeeded.v1')`,
		runID,
	).Scan(&toolSideEffects); err != nil || toolSideEffects != 5 {
		t.Fatalf("skill transactional side effects = %d, %v", toolSideEffects, err)
	}
}

func TestGovernanceStores(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	ownerID := mustID(t)
	memberID := mustID(t)
	workspaceID := mustID(t)
	invitationID := mustID(t)
	deliveryID := mustID(t)
	subscriptionID := mustID(t)
	compensationID := mustID(t)
	deadLetterID := mustID(t)
	operatorOne := "migration-admin-" + ownerID[:8]
	operatorTwo := "migration-admin-" + memberID[:8]
	poisonConsumer := "migration-governance-" + ownerID[:8]

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.db.ExecContext(cleanupCtx, `
			DELETE FROM eventing.audit_logs
			WHERE actor_id IN ($1,$2)
				OR resource_id IN ($1,$2,$3,$4)
				OR metadata->>'operator_id' IN ($5,$6)`,
			ownerID, memberID, invitationID, deliveryID, operatorOne, operatorTwo,
		)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.kafka_poison_messages WHERE consumer_name=$1`, poisonConsumer)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.compensation_records WHERE id=$1`, compensationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.outbox_events WHERE id=$1 OR aggregate_id=$2`, deadLetterID, deliveryID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.email_deliveries WHERE id=$1`, deliveryID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.billing_subscriptions WHERE id=$1`, subscriptionID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.user_safety_policies WHERE user_id=$1`, ownerID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.workspace_invitations WHERE id=$1`, invitationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.workspace_members WHERE workspace_id=$1`, workspaceID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.workspaces WHERE id=$1`, workspaceID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.operator_accounts WHERE id IN ($1,$2)`, operatorOne, operatorTwo)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.refresh_sessions WHERE user_id IN ($1,$2)`, ownerID, memberID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.user_devices WHERE user_id IN ($1,$2)`, ownerID, memberID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM app.users WHERE id IN ($1,$2)`, ownerID, memberID)
	})

	now := time.Date(1997, time.January, 1, 8, 0, 0, 0, time.UTC)
	owner := identity.User{
		ID: ownerID, Email: "governance-owner@example.com", PasswordHash: "hash",
		DisplayName: "治理域所有者", Timezone: "Asia/Shanghai", Locale: "zh-CN",
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	member := identity.User{
		ID: memberID, Email: "governance-member@example.com", PasswordHash: "hash",
		DisplayName: "治理域成员", Timezone: "Asia/Shanghai", Locale: "zh-CN",
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	if err = store.CreateUser(ctx, owner); err != nil {
		t.Fatalf("CreateUser(owner) error = %v", err)
	}
	if err = store.CreateUser(ctx, member); err != nil {
		t.Fatalf("CreateUser(member) error = %v", err)
	}

	if err = store.CreateWorkspace(ctx,
		team.Workspace{
			ID: workspaceID, Name: "治理域迁移工作区", OwnerID: ownerID,
			Status: "active", CreatedAt: now, UpdatedAt: now,
		},
		team.Member{
			WorkspaceID: workspaceID, UserID: ownerID, Role: "owner",
			Status: "active", JoinedAt: now, UpdatedAt: now,
		},
	); err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	invitation := team.Invitation{
		ID: invitationID, WorkspaceID: workspaceID, Email: member.Email,
		Role: "member", Status: "pending", InvitedBy: ownerID,
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	if err = store.CreateInvitation(ctx, invitation); err != nil {
		t.Fatalf("CreateInvitation() error = %v", err)
	}
	acceptedMember, acceptedInvitation, err := store.AcceptInvitation(ctx, invitationID, member, now.Add(time.Minute))
	if err != nil || acceptedMember.Role != "member" || acceptedInvitation.Status != "accepted" {
		t.Fatalf("AcceptInvitation() = %#v, %#v, %v", acceptedMember, acceptedInvitation, err)
	}
	if workspaces, listErr := store.ListWorkspacesForUser(ctx, memberID); listErr != nil || len(workspaces) != 1 {
		t.Fatalf("ListWorkspacesForUser() = %#v, %v", workspaces, listErr)
	}

	delivery := email.Delivery{
		ID: deliveryID, ActorID: ownerID, ResourceType: "workspace_invitation",
		ResourceID: invitationID, Template: "workspace.invitation.v1",
		RecipientEmail: member.Email, Subject: "迁移测试邀请", BodyText: "请加入工作区",
		Status: "queued", AvailableAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err = store.CreateDelivery(ctx, delivery); err != nil {
		t.Fatalf("CreateDelivery() error = %v", err)
	}
	claimed, err := store.ClaimDeliveryByID(ctx, deliveryID, "governance-email-worker", now, time.Minute)
	if err != nil || claimed.Status != "processing" || claimed.Attempts != 1 {
		t.Fatalf("ClaimDeliveryByID() = %#v, %v", claimed, err)
	}
	if err = store.FailDelivery(ctx, claimed, "governance-email-worker", "temporary", now.Add(time.Second)); err != nil {
		t.Fatalf("FailDelivery() error = %v", err)
	}
	if _, err = store.ReplayDelivery(ctx, deliveryID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("ReplayDelivery() error = %v", err)
	}
	claimed, err = store.ClaimDeliveryByID(ctx, deliveryID, "governance-email-worker-2", now.Add(2*time.Second), time.Minute)
	if err != nil || claimed.Attempts != 2 {
		t.Fatalf("ClaimDeliveryByID(replay) = %#v, %v", claimed, err)
	}
	if err = store.CompleteDelivery(ctx, claimed, "governance-email-worker-2", "smtp-test", now.Add(3*time.Second)); err != nil {
		t.Fatalf("CompleteDelivery() error = %v", err)
	}
	if sent, getErr := store.GetDelivery(ctx, deliveryID); getErr != nil || sent.Status != "sent" {
		t.Fatalf("GetDelivery(sent) = %#v, %v", sent, getErr)
	}

	if _, err = store.db.ExecContext(ctx, `
		INSERT INTO app.billing_subscriptions (
			id,user_id,plan_code,status,current_period_start,current_period_end,
			provider,provider_ref,created_at,updated_at
		) VALUES ($1,$2,'free','active',$3,$4,'test','migration',$3,$3)`,
		subscriptionID, ownerID, now, now.Add(30*24*time.Hour),
	); err != nil {
		t.Fatalf("insert billing subscription error = %v", err)
	}
	if subscription, getErr := store.GetActiveSubscription(ctx, ownerID, now.Add(time.Hour)); getErr != nil || subscription.PlanCode != "free" {
		t.Fatalf("GetActiveSubscription() = %#v, %v", subscription, getErr)
	}
	if workspaces, countErr := store.CountOwnedWorkspaces(ctx, ownerID); countErr != nil || workspaces != 1 {
		t.Fatalf("CountOwnedWorkspaces() = %d, %v", workspaces, countErr)
	}
	var _ billing.Store = store

	policy := safety.UserPolicy{
		UserID: ownerID, MinorMode: true, GuardianEmail: "guardian@example.com",
		RiskySkillsAllowed: false, CreatedAt: now, UpdatedAt: now,
	}
	if saved, upsertErr := store.UpsertUserPolicy(ctx, policy); upsertErr != nil || !saved.MinorMode || saved.RiskySkillsAllowed {
		t.Fatalf("UpsertUserPolicy() = %#v, %v", saved, upsertErr)
	}

	audit := func(action string) opsauth.AuditInput {
		return opsauth.AuditInput{Actor: "migration-root", Action: action, Reason: "integration test", Now: now}
	}
	accountOne := opsauth.Account{
		ID: operatorOne, DisplayName: "迁移管理员一", Role: "admin", Status: "active",
		TokenHash: strings.Repeat("1", 64), TOTPSecret: "SECRET1", MFAEnabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	accountTwo := opsauth.Account{
		ID: operatorTwo, DisplayName: "迁移管理员二", Role: "admin", Status: "active",
		TokenHash: strings.Repeat("2", 64), TOTPSecret: "SECRET2", MFAEnabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err = store.CreateOperator(ctx, accountOne, audit("operator.create")); err != nil {
		t.Fatalf("CreateOperator(one) error = %v", err)
	}
	if _, err = store.CreateOperator(ctx, accountTwo, audit("operator.create")); err != nil {
		t.Fatalf("CreateOperator(two) error = %v", err)
	}
	if _, err = store.SetOperatorStatus(ctx, operatorOne, "disabled", audit("operator.status.update")); err != nil {
		t.Fatalf("SetOperatorStatus(one) error = %v", err)
	}
	if _, err = store.SetOperatorStatus(ctx, operatorTwo, "disabled", audit("operator.status.update")); !errors.Is(err, opsauth.ErrAdminLockout) {
		t.Fatalf("SetOperatorStatus(last admin) error = %v", err)
	}
	if _, err = store.ResetOperatorMFA(ctx, operatorTwo, "", false, audit("operator.mfa.reset")); !errors.Is(err, opsauth.ErrAdminLockout) {
		t.Fatalf("ResetOperatorMFA(last admin) error = %v", err)
	}
	newTokenHash := strings.Repeat("3", 64)
	if _, err = store.ResetOperatorToken(ctx, operatorTwo, newTokenHash, audit("operator.token.reset")); err != nil {
		t.Fatalf("ResetOperatorToken() error = %v", err)
	}
	if found, findErr := store.FindOperatorByTokenHash(ctx, newTokenHash); findErr != nil || found.ID != operatorTwo {
		t.Fatalf("FindOperatorByTokenHash() = %#v, %v", found, findErr)
	}

	if _, err = store.SetUserStatus(ctx, identity.UserModerationInput{
		UserID: memberID, Status: "disabled", Actor: operatorTwo,
		Reason: "integration test", Now: now.Add(4 * time.Second),
	}); err != nil {
		t.Fatalf("SetUserStatus(disabled) error = %v", err)
	}
	if logs, listErr := store.ListAuditLogs(ctx, identity.AuditLogFilter{
		ResourceType: "user", ResourceID: memberID, Limit: 10,
	}); listErr != nil || len(logs) != 1 || logs[0].ActorLabel != operatorTwo {
		t.Fatalf("ListAuditLogs(user) = %#v, %v", logs, listErr)
	}
	if _, err = store.SetUserStatus(ctx, identity.UserModerationInput{
		UserID: memberID, Status: "active", Actor: operatorTwo,
		Reason: "integration test", Now: now.Add(5 * time.Second),
	}); err != nil {
		t.Fatalf("SetUserStatus(active) error = %v", err)
	}
	if users, listErr := store.ListUserAccounts(ctx, identity.UserAccountFilter{
		Query: "governance-", Status: "active", Limit: 10,
	}); listErr != nil || len(users) != 2 {
		t.Fatalf("ListUserAccounts() = %#v, %v", users, listErr)
	}

	if err = store.RecordPoisonMessage(ctx, eventbus.PoisonMessageInput{
		ConsumerName: poisonConsumer, Topic: "migration.test.v1", Partition: 0,
		Offset: 1, EventID: mustID(t), EventType: "migration.test.v1",
		AggregateID: workspaceID, Reason: "integration test",
		Envelope: json.RawMessage(`{"test":true}`), ObservedAt: now,
	}); err != nil {
		t.Fatalf("RecordPoisonMessage() error = %v", err)
	}
	if poison, listErr := store.ListPoisonMessages(ctx, 500); listErr != nil || !hasPoisonConsumer(poison, poisonConsumer) {
		t.Fatalf("ListPoisonMessages() missing %q: %#v, %v", poisonConsumer, poison, listErr)
	}
	if _, err = store.CreateCompensationRecord(ctx, eventbus.CompensationInput{
		ID: compensationID, SourceType: "workspace", SourceID: workspaceID,
		Action: "migration.verify", Reason: "integration test", Actor: operatorTwo,
		Status: "recorded", Metadata: json.RawMessage(`{"verified":true}`), CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateCompensationRecord() error = %v", err)
	}
	if records, listErr := store.ListCompensationRecords(ctx, 500); listErr != nil || !hasCompensation(records, compensationID) {
		t.Fatalf("ListCompensationRecords() missing %q: %#v, %v", compensationID, records, listErr)
	}
	if _, err = store.db.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events (
			id,aggregate_type,aggregate_id,event_type,event_version,payload,
			occurred_at,status,available_at,attempts,last_error
		) VALUES ($1,'workspace',$2,'migration.dead.v1',1,'{}',$3,'dead_letter',$3,3,'test')`,
		deadLetterID, workspaceID, now,
	); err != nil {
		t.Fatalf("insert dead-letter event error = %v", err)
	}
	if dead, getErr := store.GetOutboxEvent(ctx, deadLetterID); getErr != nil || dead.Status != "dead_letter" {
		t.Fatalf("GetOutboxEvent() = %#v, %v", dead, getErr)
	}
	if err = store.ReplayOutboxEvent(ctx, deadLetterID, now.Add(time.Minute)); err != nil {
		t.Fatalf("ReplayOutboxEvent() error = %v", err)
	}
	if replayed, getErr := store.GetOutboxEvent(ctx, deadLetterID); getErr != nil || replayed.Status != "pending" || replayed.Attempts != 0 {
		t.Fatalf("GetOutboxEvent(replayed) = %#v, %v", replayed, getErr)
	}
}

func hasPoisonConsumer(records []eventbus.PoisonMessageRecord, consumer string) bool {
	for _, record := range records {
		if record.ConsumerName == consumer {
			return true
		}
	}
	return false
}

func hasCompensation(records []eventbus.CompensationRecord, id string) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func mustID(t *testing.T) string {
	t.Helper()
	value, err := id.New()
	if err != nil {
		t.Fatalf("id.New() error = %v", err)
	}
	return value
}
