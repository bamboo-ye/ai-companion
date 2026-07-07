package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

const (
	DefaultRecentTokenBudget  = 6000
	DefaultSummaryTokenBudget = 1200
	contextPageSize           = 400
	maxSummaryRollsPerBuild   = 25
	summarizerVersion         = "extractive-rolling-v1"
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

		next, summaryErr := b.rollSummary(latest, userID, conversationID, dropped)
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

func (b *ContextBuilder) rollSummary(previous *ConversationSummary, userID, conversationID string, messages []Message) (ConversationSummary, error) {
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
	lines := make([]string, 0)
	if previous != nil {
		version = previous.Version + 1
		startSequence = previous.StartSequence
		startedAt = previous.RangeStartedAt
		lines = append(lines, summaryLines(previous.Content)...)
	}
	lines = append(lines, compactMessageGroups(messages)...)
	lines = fitSummaryLines(lines, b.summaryBudget)
	content := strings.Join(lines, "\n")
	now := b.now().UTC()
	return ConversationSummary{
		ID: summaryID, ConversationID: conversationID, UserID: userID, Version: version,
		StartSequence: startSequence, EndSequence: messages[len(messages)-1].Sequence,
		RangeStartedAt: startedAt, RangeEndedAt: messages[len(messages)-1].CreatedAt,
		Content: content, TokenCount: EstimateTokens(content), SummarizerVersion: summarizerVersion, CreatedAt: now,
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

func compactMessageGroups(messages []Message) []string {
	groups := groupMessages(messages)
	lines := make([]string, 0, len(groups))
	for _, group := range groups {
		parts := make([]string, 0, len(group))
		for _, message := range group {
			if value := strings.TrimSpace(message.Content); value != "" {
				parts = append(parts, value)
			}
		}
		if len(parts) == 0 {
			continue
		}
		role := "系统"
		switch group[0].Role {
		case "user":
			role = "用户"
		case "assistant":
			role = "伙伴"
		case "tool":
			role = "工具"
		}
		value := strings.Join(parts, " ")
		if utf8.RuneCountInString(value) > 240 {
			runes := []rune(value)
			value = string(runes[:240]) + "…"
		}
		lines = append(lines, fmt.Sprintf("- %s：%s", role, value))
	}
	return lines
}

func summaryLines(content string) []string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func fitSummaryLines(lines []string, budget int) []string {
	if len(lines) == 0 {
		return []string{"- 暂无可用摘要。"}
	}
	tokens := 0
	start := len(lines)
	for index := len(lines) - 1; index >= 0; index-- {
		lineTokens := EstimateTokens(lines[index]) + 2
		if start == len(lines) && lineTokens > budget {
			return []string{truncateToTokenBudget(lines[index], budget)}
		}
		if start < len(lines) && tokens+lineTokens > budget {
			break
		}
		tokens += lineTokens
		start = index
	}
	return lines[start:]
}

func truncateToTokenBudget(value string, budget int) string {
	if budget <= 0 || EstimateTokens(value) <= budget {
		return value
	}
	runes := []rune(value)
	low, high := 1, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		if EstimateTokens(string(runes[:mid])+"…") <= budget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return string(runes[:low]) + "…"
}

func formatSummaryContext(summary ConversationSummary) string {
	return fmt.Sprintf("此前对话滚动摘要（%s 至 %s，覆盖序号 %d–%d）：\n%s",
		summary.RangeStartedAt.Format(time.RFC3339), summary.RangeEndedAt.Format(time.RFC3339),
		summary.StartSequence, summary.EndSequence, summary.Content)
}

func EstimateTokens(value string) int {
	if value == "" {
		return 0
	}
	count := 0
	latinRunes := 0
	for _, r := range value {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			count++
			continue
		}
		latinRunes++
	}
	count += (latinRunes + 3) / 4
	if count == 0 {
		return 1
	}
	return count
}

func errorsIsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
