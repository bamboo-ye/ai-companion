package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/windcry1/ai-companion/internal/semantic"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/windcry1/ai-companion/internal/contextengine"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrNotFound   = errors.New("memory not found")
	ErrValidation = errors.New("memory validation failed")
)

type Memory struct {
	ID                   string     `json:"id"`
	UserID               string     `json:"-"`
	Type                 string     `json:"type"`
	Content              string     `json:"content"`
	NormalizedHash       string     `json:"-"`
	SourceConversationID string     `json:"source_conversation_id,omitempty"`
	SourceMessageID      string     `json:"source_message_id,omitempty"`
	Confidence           float64    `json:"confidence"`
	Importance           float64    `json:"importance"`
	Sensitivity          string     `json:"sensitivity"`
	Pinned               bool       `json:"pinned"`
	Status               string     `json:"status"`
	ValidFrom            time.Time  `json:"valid_from"`
	ValidTo              *time.Time `json:"valid_to,omitempty"`
	SupersedesID         string     `json:"supersedes_id,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}
type UpdateInput struct {
	Content    *string  `json:"content"`
	Pinned     *bool    `json:"pinned"`
	Importance *float64 `json:"importance"`
}
type Store interface {
	UpsertMemory(context.Context, Memory) (Memory, bool, error)
	ListMemories(context.Context, string, int) ([]Memory, error)
	GetMemory(context.Context, string, string) (Memory, error)
	UpdateMemory(context.Context, Memory) error
	DeleteMemory(context.Context, string, string, time.Time) error
	ClearMemories(context.Context, string, time.Time) error
}
type Service struct {
	semantic   *semantic.Client
	cacheMu    sync.Mutex
	embeddings map[string]embeddingEntry
	store      Store
	now        func() time.Time
}

func NewService(store Store) *Service { return &Service{store: store, now: time.Now} }
func (s *Service) Observe(ctx context.Context, userID, conversationID, messageID, text string) error {
	content, ok := extractExplicit(text)
	if !ok {
		return nil
	}
	_, err := s.SaveFromModel(ctx, userID, conversationID, messageID, content)
	return err
}

func (s *Service) SaveFromModel(ctx context.Context, userID, conversationID, messageID, content string) (Memory, error) {
	content = strings.Trim(strings.TrimSpace(content), "。！？!? ")
	if len([]rune(content)) < 2 || len([]rune(content)) > 2000 {
		return Memory{}, fmt.Errorf("%w: content must contain 2-2000 characters", ErrValidation)
	}
	now := s.now().UTC()
	memoryID, err := id.New()
	if err != nil {
		return Memory{}, err
	}
	item := Memory{ID: memoryID, UserID: userID, Type: classify(content), Content: content, NormalizedHash: hash(normalize(content)), SourceConversationID: conversationID, SourceMessageID: messageID, Confidence: .99, Importance: .7, Sensitivity: sensitivity(content), Status: "active", ValidFrom: now, CreatedAt: now, UpdatedAt: now}
	saved, _, err := s.store.UpsertMemory(ctx, item)
	return saved, err
}
func (s *Service) Create(ctx context.Context, userID, content string) (Memory, error) {
	content = strings.TrimSpace(content)
	if len([]rune(content)) < 2 || len([]rune(content)) > 2000 {
		return Memory{}, fmt.Errorf("%w: content must contain 2-2000 characters", ErrValidation)
	}
	memoryID, err := id.New()
	if err != nil {
		return Memory{}, err
	}
	now := s.now().UTC()
	item := Memory{ID: memoryID, UserID: userID, Type: classify(content), Content: content, NormalizedHash: hash(normalize(content)), Confidence: 1, Importance: .7, Sensitivity: sensitivity(content), Status: "active", ValidFrom: now, CreatedAt: now, UpdatedAt: now}
	saved, _, err := s.store.UpsertMemory(ctx, item)
	return saved, err
}
func (s *Service) List(ctx context.Context, userID string) ([]Memory, error) {
	return s.store.ListMemories(ctx, userID, 500)
}
func (s *Service) Update(ctx context.Context, userID, memoryID string, input UpdateInput) (Memory, error) {
	item, err := s.store.GetMemory(ctx, userID, memoryID)
	if err != nil {
		return Memory{}, err
	}
	if input.Content != nil {
		content := strings.TrimSpace(*input.Content)
		if len([]rune(content)) < 2 || len([]rune(content)) > 2000 {
			return Memory{}, fmt.Errorf("%w: content must contain 2-2000 characters", ErrValidation)
		}
		item.Content = content
		item.NormalizedHash = hash(normalize(content))
		item.Type = classify(content)
		item.Sensitivity = sensitivity(content)
	}
	if input.Pinned != nil {
		item.Pinned = *input.Pinned
	}
	if input.Importance != nil {
		if *input.Importance < 0 || *input.Importance > 1 {
			return Memory{}, fmt.Errorf("%w: importance must be between 0 and 1", ErrValidation)
		}
		item.Importance = *input.Importance
	}
	item.UpdatedAt = s.now().UTC()
	if err = s.store.UpdateMemory(ctx, item); err != nil {
		return Memory{}, err
	}
	return item, nil
}
func (s *Service) Delete(ctx context.Context, userID, memoryID string) error {
	return s.store.DeleteMemory(ctx, userID, memoryID, s.now().UTC())
}
func (s *Service) Clear(ctx context.Context, userID string) error {
	return s.store.ClearMemories(ctx, userID, s.now().UTC())
}
func (s *Service) Recall(ctx context.Context, userID, query string, limit int) ([]string, error) {
	items, err := s.RecallContext(ctx, userID, query, limit)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.Content)
	}
	return result, nil
}

// RecallContext preserves provenance for both chat and Agent context builders.
func (s *Service) RecallContext(ctx context.Context, userID, query string, limit int) ([]contextengine.Item, error) {
	items, err := s.store.ListMemories(ctx, userID, 500)
	if err != nil {
		return nil, err
	}
	semanticScores := s.semanticScores(ctx, userID, query, items)
	queryTokens := tokens(query)
	type scored struct {
		item  Memory
		score float64
	}
	matches := []scored{}
	now := s.now().UTC()
	for _, item := range items {
		if item.Status != "active" || item.ValidFrom.After(now) || (item.ValidTo != nil && !item.ValidTo.After(now)) {
			continue
		}
		memoryTokens := tokens(item.Content)
		overlap := 0
		for token := range memoryTokens {
			if queryTokens[token] {
				overlap++
			}
		}
		if overlap == 0 && semanticScores[item.ID] < .5 && !item.Pinned {
			continue
		}
		semantic := 0.0
		if len(queryTokens) > 0 {
			semantic = math.Min(1, float64(overlap)/float64(len(queryTokens)))
		}
		semantic = math.Max(semantic, semanticScores[item.ID])
		ageDays := now.Sub(item.UpdatedAt).Hours() / 24
		if ageDays < 0 {
			ageDays = 0
		}
		recency := math.Exp(-ageDays / memoryHalfLifeDays(item.Type))
		score := .55*semantic + .25*recency + .20*item.Importance
		if item.Pinned {
			score += .35
		}
		if semantic > 0 || item.Pinned {
			matches = append(matches, scored{item, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	result := []contextengine.Item{}
	for _, match := range matches {
		item := match.item
		result = append(result, contextengine.Item{Content: item.Content, EstimatedTokens: contextengine.EstimateTokens(item.Content), Source: contextengine.Source{
			Kind: "memory", ID: item.ID, ConversationID: item.SourceConversationID,
			MessageID: item.SourceMessageID, UpdatedAt: &item.UpdatedAt,
		}})
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func memoryHalfLifeDays(memoryType string) float64 {
	switch memoryType {
	case "commitment":
		return 30
	case "experience":
		return 180
	case "preference", "relationship":
		return 365
	case "goal":
		return 90
	default:
		return 120
	}
}

var explicitPrefix = regexp.MustCompile(`^(?:请)?(?:帮我)?(?:记住|记一下|记得)[：,:，\s]*(.+)$`)
var sensitivePattern = regexp.MustCompile(`(?i)(?:\b\d{15,18}[0-9x]?\b|\b1\d{10}\b|[\w.+-]+@[\w.-]+\.[a-z]{2,})`)

func extractExplicit(text string) (string, bool) {
	match := explicitPrefix.FindStringSubmatch(strings.TrimSpace(text))
	if len(match) != 2 {
		return "", false
	}
	content := strings.Trim(strings.TrimSpace(match[1]), "。！？!? ")
	if len([]rune(content)) < 2 {
		return "", false
	}
	return content, true
}
func classify(content string) string {
	switch {
	case strings.Contains(content, "喜欢") || strings.Contains(content, "不吃") || strings.Contains(content, "不喜欢"):
		return "preference"
	case strings.Contains(content, "目标") || strings.Contains(content, "想要"):
		return "goal"
	case strings.Contains(content, "答应") || strings.Contains(content, "承诺"):
		return "commitment"
	case strings.Contains(content, "朋友") || strings.Contains(content, "家人") || strings.Contains(content, "同事"):
		return "relationship"
	case strings.Contains(content, "曾经") || strings.Contains(content, "经历"):
		return "experience"
	default:
		return "fact"
	}
}
func sensitivity(content string) string {
	if sensitivePattern.MatchString(content) {
		return "sensitive"
	}
	if strings.Contains(content, "住址") || strings.Contains(content, "病史") {
		return "personal"
	}
	return "normal"
}
func normalize(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}
func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func tokens(value string) map[string]bool {
	result := map[string]bool{}
	runes := []rune(normalize(value))
	for index := 0; index+1 < len(runes); index++ {
		result[string(runes[index:index+2])] = true
	}
	for _, word := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if len([]rune(word)) > 1 {
			result[word] = true
		}
	}
	return result
}
