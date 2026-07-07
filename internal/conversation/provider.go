package conversation

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/windcry1/ai-companion/internal/character"
)

type DevelopmentProvider struct{}

func (DevelopmentProvider) Generate(ctx context.Context, persona character.Character, history []Message) (string, Usage, error) {
	select {
	case <-ctx.Done():
		return "", Usage{}, ctx.Err()
	default:
	}
	started := time.Now()
	latest := ""
	memoryContext := ""
	if len(history) > 0 {
		for _, item := range history {
			if item.Role == "user" {
				latest = item.Content
			}
			if item.Role == "system" {
				memoryContext += item.Content
			}
		}
	}
	var text string
	if strings.Contains(latest, "香菜") && strings.Contains(memoryContext, "不吃香菜") {
		text = "我记得你不吃香菜。我们选餐或点菜时会避开它，你不用每次重新提醒我。"
	} else if strings.Contains(persona.Personality, "直接") || strings.Contains(persona.SpeechStyle, "结论") {
		text = "先说结论：我听见你的重点了，我们不绕弯。\n\n接下来可以先做一个最小动作，把最急的那件事写成一句话。然后我们一起把它拆成三步，我会陪你逐项推进。"
	} else {
		text = "我在呢，也认真听见你刚才说的事了。" + persona.Name + "不会急着评判你。\n\n如果你愿意，可以再告诉我一点：这件事里最让你难受或最在意的是什么？\n\n我们慢慢来，我会记住此刻真正重要的是「" + truncate(latest, 24) + "」。"
	}
	return text, Usage{Provider: "development", Model: "deterministic-persona-v1", InputTokens: len([]rune(latest))/2 + 1, OutputTokens: len([]rune(text))/2 + 1, Latency: time.Since(started)}, nil
}

func SplitBubbles(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	parts := semanticParts(text)
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			if utf8.RuneCountInString(part) <= 2 && len(clean) > 0 {
				clean[len(clean)-1] += part
				continue
			}
			clean = append(clean, part)
		}
	}
	if len(clean) < 2 || utf8.RuneCountInString(text) < 40 {
		return []string{text}
	}
	if len(clean) > 5 {
		tail := strings.Join(clean[4:], "。 ")
		clean = append(clean[:4], tail)
	}
	return clean
}

func semanticParts(text string) []string {
	parts := []string{}
	buffer := []rune{}
	newlines := 0
	flush := func() {
		value := strings.TrimSpace(string(buffer))
		if value != "" {
			parts = append(parts, value)
		}
		buffer = nil
		newlines = 0
	}
	for _, r := range []rune(text) {
		buffer = append(buffer, r)
		if r == '\n' {
			newlines++
			if newlines >= 2 {
				flush()
			}
			continue
		}
		newlines = 0
		if r == '。' || r == '！' || r == '？' || r == '!' || r == '?' {
			flush()
		}
	}
	flush()
	return parts
}
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
