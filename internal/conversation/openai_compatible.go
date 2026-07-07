package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/character"
)

type OpenAICompatibleProvider struct {
	BaseURL, APIKey, Model                                string
	Client                                                *http.Client
	InputCostMicrosPerMillion, OutputCostMicrosPerMillion int64
}

func NewOpenAICompatibleProvider(baseURL, apiKey, model string, timeout time.Duration, inputCost, outputCost int64) *OpenAICompatibleProvider {
	return &OpenAICompatibleProvider{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model, Client: &http.Client{Timeout: timeout}, InputCostMicrosPerMillion: inputCost, OutputCostMicrosPerMillion: outputCost}
}
func (p *OpenAICompatibleProvider) Generate(ctx context.Context, persona character.Character, history []Message) (string, Usage, error) {
	started := time.Now()
	messages := []map[string]string{{"role": "system", "content": systemPrompt(persona)}}
	for _, item := range history {
		if item.Role == "user" || item.Role == "assistant" || item.Role == "system" {
			messages = append(messages, map[string]string{"role": item.Role, "content": item.Content})
		}
	}
	body, _ := json.Marshal(map[string]any{"model": p.Model, "messages": messages, "temperature": 0.8, "stream": false})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	request.Header.Set("Authorization", "Bearer "+p.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := p.Client.Do(request)
	if err != nil {
		return "", Usage{}, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", Usage{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", Usage{}, fmt.Errorf("model provider status %d", response.StatusCode)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err = json.Unmarshal(payload, &result); err != nil {
		return "", Usage{}, fmt.Errorf("decode model response: %w", err)
	}
	if len(result.Choices) == 0 || strings.TrimSpace(result.Choices[0].Message.Content) == "" {
		return "", Usage{}, fmt.Errorf("model provider returned empty response")
	}
	cost := (int64(result.Usage.Prompt)*p.InputCostMicrosPerMillion + int64(result.Usage.Completion)*p.OutputCostMicrosPerMillion) / 1_000_000
	return result.Choices[0].Message.Content, Usage{Provider: "openai-compatible", Model: p.Model, InputTokens: result.Usage.Prompt, OutputTokens: result.Usage.Completion, EstimatedCostMicros: cost, Latency: time.Since(started)}, nil
}
func systemPrompt(item character.Character) string {
	return fmt.Sprintf("你是角色%s，与用户的关系是%s。性格：%s。说话风格：%s。回复长度偏好：%s。始终遵守产品安全策略，不声称自己是人类，不执行未经确认的外部操作。", item.Name, item.Relationship, item.Personality, item.SpeechStyle, item.ReplyLength)
}
