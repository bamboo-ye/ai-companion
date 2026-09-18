package conversation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/windcry1/ai-companion/internal/contextengine"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"github.com/windcry1/ai-companion/internal/semantic"
)

const (
	DefaultRecentTokenBudget  = 6000
	DefaultSummaryTokenBudget = 1200
	contextPageSize           = 400
	maxSummaryRollsPerBuild   = 25
)

type ConversationSummary struct {
	ID                string    `json:"id"`
	ConversationID    string    `json:"conversation_id"`
	UserID            string    `json:"-"`
	Version           int       `json:"version"`
	StartSequence     uint64    `json:"start_sequence"`
	EndSequence       uint64    `json:"end_sequence"`
	RangeStartedAt    time.Time `json:"range_started_at"`
	RangeEndedAt      time.Time `json:"range_ended_at"`
	Content           string    `json:"content"`
	TokenCount        int       `json:"token_count"`
	SummarizerVersion string    `json:"summarizer_version"`
	CreatedAt         time.Time `json:"created_at"`
}

type ContextStore interface {
	GetLatestSummary(context.Context, string, string) (ConversationSummary, error)
	SaveSummary(context.Context, ConversationSummary) error
}

type ContextResult struct {
	Messages       []Message
	Summary        *ConversationSummary
	RecentTokens   int
	SummaryTokens  int
	SummariesAdded int
}

type ContextBuilder struct {
	store         Store
	recentBudget  int
	summaryBudget int
	now           func() time.Time
	semantic      *semantic.Client
}

func NewContextBuilder(store Store, recentBudget, summaryBudget int) *ContextBuilder {
	if recentBudget <= 0 {
		recentBudget = DefaultRecentTokenBudget
	}
	if summaryBudget <= 0 {
		summaryBudget = DefaultSummaryTokenBudget
	}
	return &ContextBuilder{store: store, recentBudget: recentBudget, summaryBudget: summaryBudget, now: time.Now}
}

func (b *ContextBuilder) Build(ctx context.Context, userID, conversationID string) (ContextResult, error) {
	contextStore, ok := b.store.(ContextStore)
	if !ok {
		messages, err := b.store.ListMessages(ctx, userID, conversationID, 0, 0, contextPageSize)
		if err != nil {
			return ContextResult{}, err
		}
		recent, tokens, _ := selectRecentMessages(messages, b.recentBudget, false)
		return ContextResult{Messages: recent, RecentTokens: tokens}, nil
	}

	var latest *ConversationSummary
	stored, err := contextStore.GetLatestSummary(ctx, userID, conversationID)
	if err == nil {
		latest = &stored
	} else if !errorsIsNotFound(err) {
		return ContextResult{}, err
	}

	result := ContextResult{}
	for roll := 0; roll < maxSummaryRollsPerBuild; roll++ {
		after := uint64(0)
		if latest != nil {
			after = latest.EndSequence
		}
		afterBubble := 0
		if after > 0 {
			afterBubble = int(^uint16(0))
		}
		messages, loadErr := b.store.ListMessages(ctx, userID, conversationID, after, afterBubble, contextPageSize)
		if loadErr != nil {
			return ContextResult{}, loadErr
		}
		recent, tokens, dropped := selectRecentMessages(messages, b.recentBudget, len(messages) == contextPageSize)
		if len(dropped) == 0 {
			result.Messages = recent
			result.RecentTokens = tokens
			result.Summary = latest
			if latest != nil {
				result.SummaryTokens = latest.TokenCount
				result.Messages = append([]Message{{Role: "system", Content: formatSummaryContext(*latest)}}, result.Messages...)
			}
			return result, nil
		}

		next, summaryErr := b.rollSummary(ctx, latest, userID, conversationID, dropped)
		if summaryErr != nil {
			return ContextResult{}, summaryErr
		}
		if saveErr := contextStore.SaveSummary(ctx, next); saveErr != nil {
			return ContextResult{}, saveErr
		}
		stored, loadErr = contextStore.GetLatestSummary(ctx, userID, conversationID)
		if loadErr != nil {
			return ContextResult{}, loadErr
		}
		latest = &stored
		result.SummariesAdded++
	}
	return ContextResult{}, fmt.Errorf("context summary roll limit exceeded")
}

func (b *ContextBuilder) rollSummary(ctx context.Context, previous *ConversationSummary, userID, conversationID string, messages []Message) (ConversationSummary, error) {
	if len(messages) == 0 {
		return ConversationSummary{}, fmt.Errorf("cannot summarize an empty message range")
	}
	summaryID, err := id.New()
	if err != nil {
		return ConversationSummary{}, err
	}
	version := 1
	startSequence := messages[0].Sequence
	startedAt := messages[0].CreatedAt

	if previous != nil {
		version = previous.Version + 1
		startSequence = previous.StartSequence
		startedAt = previous.RangeStartedAt

	}
	content, summaryVersion := b.summarize(ctx, previous, messages)
	now := b.now().UTC()
	return ConversationSummary{
		ID: summaryID, ConversationID: conversationID, UserID: userID, Version: version,
		StartSequence: startSequence, EndSequence: messages[len(messages)-1].Sequence,
		RangeStartedAt: startedAt, RangeEndedAt: messages[len(messages)-1].CreatedAt,
		Content: content, TokenCount: EstimateTokens(content), SummarizerVersion: summaryVersion, CreatedAt: now,
	}, nil
}

func selectRecentMessages(messages []Message, budget int, forceProgress bool) ([]Message, int, []Message) {
	if len(messages) == 0 {
		return nil, 0, nil
	}
	groups := groupMessages(messages)
	keptFrom := len(groups)
	tokens := 0
	for index := len(groups) - 1; index >= 0; index-- {
		groupTokens := messageGroupTokens(groups[index])
		if keptFrom < len(groups) && tokens+groupTokens > budget {
			break
		}
		tokens += groupTokens
		keptFrom = index
	}
	if forceProgress && keptFrom == 0 && len(groups) > 1 {
		keptFrom = len(groups) / 2
		tokens = 0
		for _, group := range groups[keptFrom:] {
			tokens += messageGroupTokens(group)
		}
	}
	recent := flattenMessageGroups(groups[keptFrom:])
	dropped := flattenMessageGroups(groups[:keptFrom])
	return recent, tokens, dropped
}

func groupMessages(messages []Message) [][]Message {
	groups := make([][]Message, 0)
	for _, message := range messages {
		if len(groups) == 0 || groups[len(groups)-1][0].Sequence != message.Sequence {
			groups = append(groups, []Message{message})
			continue
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], message)
	}
	return groups
}

func flattenMessageGroups(groups [][]Message) []Message {
	result := make([]Message, 0)
	for _, group := range groups {
		result = append(result, group...)
	}
	return result
}

func messageGroupTokens(group []Message) int {
	total := 0
	for _, message := range group {
		total += EstimateTokens(message.Content) + 4
	}
	return total
}

func formatSummaryContext(summary ConversationSummary) string {
	return fmt.Sprintf("此前对话滚动摘要（%s 至 %s，覆盖序号 %d–%d）：\n%s",
		summary.RangeStartedAt.Format(time.RFC3339), summary.RangeEndedAt.Format(time.RFC3339),
		summary.StartSequence, summary.EndSequence, summary.Content)
}

func EstimateTokens(value string) int {
	return contextengine.EstimateTokens(value)
}

func errorsIsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
