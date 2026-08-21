package character

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrNotFound   = errors.New("character not found")
	ErrValidation = errors.New("character validation failed")
)

type Character struct {
	ID             string    `json:"id"`
	UserID         string    `json:"-"`
	Module         string    `json:"module"`
	Name           string    `json:"name"`
	AvatarURL      string    `json:"avatar_url,omitempty"`
	Relationship   string    `json:"relationship"`
	Personality    string    `json:"personality"`
	SpeechStyle    string    `json:"speech_style"`
	Hobbies        []string  `json:"hobbies"`
	Boundaries     []string  `json:"boundaries"`
	Initiative     string    `json:"initiative"`
	ReplyLength    string    `json:"reply_length"`
	StickerStyle   string    `json:"sticker_style"`
	RawPrompt      string    `json:"raw_prompt"`
	PersonaVersion int       `json:"persona_version"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type PersonaVersion struct {
	CharacterID     string         `json:"character_id"`
	Version         int            `json:"version"`
	CompilerVersion string         `json:"compiler_version"`
	Compiled        map[string]any `json:"compiled"`
	CreatedAt       time.Time      `json:"created_at"`
}

type Input struct {
	Module       string   `json:"module"`
	Name         string   `json:"name"`
	AvatarURL    string   `json:"avatar_url"`
	Relationship string   `json:"relationship"`
	Personality  string   `json:"personality"`
	SpeechStyle  string   `json:"speech_style"`
	Hobbies      []string `json:"hobbies"`
	Boundaries   []string `json:"boundaries"`
	Initiative   string   `json:"initiative"`
	ReplyLength  string   `json:"reply_length"`
	StickerStyle string   `json:"sticker_style"`
	RawPrompt    string   `json:"raw_prompt"`
}

type Store interface {
	Create(context.Context, Character, PersonaVersion) error
	List(context.Context, string) ([]Character, error)
	Get(context.Context, string, string) (Character, error)
	Update(context.Context, Character, PersonaVersion) error
	Delete(context.Context, string, string, time.Time) error
	ListPersonaVersions(context.Context, string, string) ([]PersonaVersion, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service { return &Service{store: store, now: time.Now} }

func (s *Service) Create(ctx context.Context, userID string, input Input) (Character, PersonaVersion, error) {
	input = applyCreateDefaults(input)
	if err := validate(input); err != nil {
		return Character{}, PersonaVersion{}, err
	}
	characterID, err := id.New()
	if err != nil {
		return Character{}, PersonaVersion{}, err
	}
	now := s.now().UTC()
	character := fromInput(characterID, userID, 1, now, now, input)
	persona := compile(character, now)
	if err := s.store.Create(ctx, character, persona); err != nil {
		return Character{}, PersonaVersion{}, err
	}
	return character, persona, nil
}

type createDefaults struct {
	Name         string
	Relationship string
	Personality  string
	SpeechStyle  string
}

func applyCreateDefaults(input Input) Input {
	input.Module = defaultString(input.Module, "companion")
	defaults := defaultsForModule(input.Module)
	input.Name = defaultString(input.Name, defaults.Name)
	input.Relationship = defaultString(input.Relationship, defaults.Relationship)
	input.Personality = defaultString(input.Personality, defaults.Personality)
	input.SpeechStyle = defaultString(input.SpeechStyle, defaults.SpeechStyle)
	input.Initiative = defaultString(input.Initiative, "balanced")
	input.ReplyLength = defaultString(input.ReplyLength, "short")
	return input
}

func defaultsForModule(module string) createDefaults {
	switch module {
	case "life":
		return createDefaults{Name: "小满", Relationship: "生活管家", Personality: "细心、可靠，善于把日常安排得井井有条", SpeechStyle: "自然简洁，给出清晰且容易执行的建议"}
	case "work":
		return createDefaults{Name: "阿策", Relationship: "工作搭档", Personality: "清晰、高效，重视目标与行动", SpeechStyle: "先给结论，再给下一步建议"}
	default:
		return createDefaults{Name: "小棉", Relationship: "温柔朋友", Personality: "温柔、耐心，善于倾听且不说教", SpeechStyle: "使用自然短句，先理解感受再回应"}
	}
}

func (s *Service) List(ctx context.Context, userID string) ([]Character, error) {
	return s.store.List(ctx, userID)
}

func (s *Service) Get(ctx context.Context, userID, characterID string) (Character, error) {
	return s.store.Get(ctx, userID, characterID)
}

func (s *Service) Update(ctx context.Context, userID, characterID string, input Input) (Character, PersonaVersion, error) {
	current, err := s.store.Get(ctx, userID, characterID)
	if err != nil {
		return Character{}, PersonaVersion{}, err
	}
	if strings.TrimSpace(input.Module) == "" {
		input.Module = current.Module
	}
	if err := validate(input); err != nil {
		return Character{}, PersonaVersion{}, err
	}
	updated := fromInput(current.ID, current.UserID, current.PersonaVersion+1, current.CreatedAt, s.now().UTC(), input)
	persona := compile(updated, updated.UpdatedAt)
	if err := s.store.Update(ctx, updated, persona); err != nil {
		return Character{}, PersonaVersion{}, err
	}
	return updated, persona, nil
}

func (s *Service) Delete(ctx context.Context, userID, characterID string) error {
	return s.store.Delete(ctx, userID, characterID, s.now().UTC())
}

func (s *Service) PersonaVersions(ctx context.Context, userID, characterID string) ([]PersonaVersion, error) {
	return s.store.ListPersonaVersions(ctx, userID, characterID)
}

func validate(input Input) error {
	if length := len([]rune(strings.TrimSpace(input.Name))); length < 1 || length > 40 {
		return fmt.Errorf("%w: name must contain 1-40 characters", ErrValidation)
	}
	if len([]rune(input.Personality)) > 1000 || len([]rune(input.SpeechStyle)) > 500 || len([]rune(input.RawPrompt)) > 4000 {
		return fmt.Errorf("%w: persona text is too long", ErrValidation)
	}
	if !oneOf(defaultString(input.Module, "companion"), "companion", "life", "work") {
		return fmt.Errorf("%w: invalid module", ErrValidation)
	}
	if !oneOf(defaultString(input.Initiative, "balanced"), "low", "balanced", "high") {
		return fmt.Errorf("%w: invalid initiative", ErrValidation)
	}
	if !oneOf(defaultString(input.ReplyLength, "short"), "short", "medium", "long") {
		return fmt.Errorf("%w: invalid reply_length", ErrValidation)
	}
	if len(input.Hobbies) > 20 || len(input.Boundaries) > 20 {
		return fmt.Errorf("%w: too many list items", ErrValidation)
	}
	return nil
}

func fromInput(characterID, userID string, version int, createdAt, updatedAt time.Time, input Input) Character {
	return Character{ID: characterID, UserID: userID, Module: defaultString(input.Module, "companion"), Name: strings.TrimSpace(input.Name), AvatarURL: strings.TrimSpace(input.AvatarURL), Relationship: defaultString(input.Relationship, "AI 伙伴"), Personality: strings.TrimSpace(input.Personality), SpeechStyle: strings.TrimSpace(input.SpeechStyle), Hobbies: cleanList(input.Hobbies), Boundaries: cleanList(input.Boundaries), Initiative: defaultString(input.Initiative, "balanced"), ReplyLength: defaultString(input.ReplyLength, "short"), StickerStyle: strings.TrimSpace(input.StickerStyle), RawPrompt: strings.TrimSpace(input.RawPrompt), PersonaVersion: version, Status: "active", CreatedAt: createdAt, UpdatedAt: updatedAt}
}

func compile(character Character, now time.Time) PersonaVersion {
	compiled := map[string]any{
		"identity":           map[string]any{"name": character.Name, "relationship": character.Relationship, "module": character.Module},
		"voice":              map[string]any{"personality": character.Personality, "speech_style": character.SpeechStyle, "reply_length": character.ReplyLength, "initiative": character.Initiative},
		"interests":          character.Hobbies,
		"user_boundaries":    character.Boundaries,
		"creative_direction": character.RawPrompt,
		"system_rules":       []string{"不得声称自己是人类", "不得覆盖产品安全策略", "执行外部操作前必须获得明确确认"},
	}
	return PersonaVersion{CharacterID: character.ID, Version: character.PersonaVersion, CompilerVersion: "persona-compiler/v1", Compiled: compiled, CreatedAt: now}
}

func cleanList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
