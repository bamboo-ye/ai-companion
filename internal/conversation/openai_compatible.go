package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

type OpenAICompatibleProvider struct {
	BaseURL, APIKey, Model                                string
	Models                                                []string
	ProviderName                                          string
	HTTPReferer, AppTitle                                 string
	DataCollection                                        string
	ZDRRequired                                           bool
	MaxTokens                                             int
	ReasoningEffort                                       string
	ReasoningExclude                                      bool
	ProviderSort                                          string
	AllowProviderFallbacks, RequireParameters             bool
	MaxPromptPrice, MaxCompletionPrice                    float64
	Client                                                *http.Client
	InputCostMicrosPerMillion, OutputCostMicrosPerMillion int64
}

func NewOpenAICompatibleProvider(baseURL, apiKey, model string, timeout time.Duration, inputCost, outputCost int64) *OpenAICompatibleProvider {
	return &OpenAICompatibleProvider{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model, Models: []string{model}, ProviderName: "openai-compatible", MaxTokens: 1024, Client: &http.Client{Timeout: timeout}, InputCostMicrosPerMillion: inputCost, OutputCostMicrosPerMillion: outputCost}
}

type OpenRouterOptions struct {
	BaseURL, APIKey                 string
	Models                          []string
	Timeout                         time.Duration
	MaxTokens                       int
	DataCollection, ReasoningEffort string
	ZDRRequired, ReasoningExclude   bool
	HTTPReferer, AppTitle           string
	InputCost, OutputCost           int64
	ProviderSort                    string
	AllowProviderFallbacks          bool
	RequireParameters               bool
	MaxPromptPrice                  float64
	MaxCompletionPrice              float64
}

func NewOpenRouterProvider(options OpenRouterOptions) *OpenAICompatibleProvider {
	models := append([]string(nil), options.Models...)
	if len(models) == 0 {
		models = []string{"deepseek/deepseek-v4-flash-0731"}
	}
	if options.ProviderSort == "" {
		options.ProviderSort = "price"
	}
	if options.MaxPromptPrice <= 0 {
		options.MaxPromptPrice = 0.3
	}
	if options.MaxCompletionPrice <= 0 {
		options.MaxCompletionPrice = 2.5
	}
	provider := NewOpenAICompatibleProvider(options.BaseURL, options.APIKey, models[0], options.Timeout, options.InputCost, options.OutputCost)
	provider.Models = models
	provider.ProviderName = "openrouter"
	provider.MaxTokens = options.MaxTokens
	provider.DataCollection = options.DataCollection
	provider.ZDRRequired = options.ZDRRequired
	provider.ReasoningEffort = options.ReasoningEffort
	provider.ReasoningExclude = options.ReasoningExclude
	provider.ProviderSort = options.ProviderSort
	provider.AllowProviderFallbacks = options.AllowProviderFallbacks
	provider.RequireParameters = options.RequireParameters
	provider.MaxPromptPrice = options.MaxPromptPrice
	provider.MaxCompletionPrice = options.MaxCompletionPrice
	provider.HTTPReferer = options.HTTPReferer
	provider.AppTitle = options.AppTitle
	return provider
}

func (p *OpenAICompatibleProvider) Generate(ctx context.Context, persona character.Character, history []Message) (string, Usage, error) {
	turn, usage, err := p.generate(ctx, persona, history, nil)
	if err != nil {
		return "", Usage{}, err
	}
	if strings.TrimSpace(turn.Text) == "" {
		return "", Usage{}, fmt.Errorf("model provider returned empty response")
	}
	return turn.Text, usage, nil
}

func (p *OpenAICompatibleProvider) GenerateWithTools(ctx context.Context, persona character.Character, history []Message, tools []ModelToolDefinition) (ModelToolTurn, Usage, error) {
	if len(tools) == 0 {
		return ModelToolTurn{}, Usage{}, fmt.Errorf("tool definitions are required")
	}
	return p.generate(ctx, persona, history, tools)
}

func (p *OpenAICompatibleProvider) generate(ctx context.Context, persona character.Character, history []Message, tools []ModelToolDefinition) (ModelToolTurn, Usage, error) {
	started := time.Now()
	messages := []map[string]string{{"role": "system", "content": systemPrompt(persona)}}
	for _, item := range history {
		if item.Role == "user" || item.Role == "assistant" || item.Role == "system" {
			messages = append(messages, map[string]string{"role": item.Role, "content": item.Content})
		}
	}
	maxTokens := p.MaxTokens
	requestPayload := map[string]any{"messages": messages, "stream": false, "max_tokens": maxTokens}
	if len(tools) > 0 {
		functionTools := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			functionTools = append(functionTools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters,
				},
			})
		}
		requestPayload["tools"] = functionTools
		requestPayload["tool_choice"] = "required"
		if maxTokens > 256 {
			requestPayload["max_tokens"] = 256
		}
	}
	if len(p.Models) > 1 {
		requestPayload["models"] = p.Models
	} else {
		requestPayload["model"] = p.Model
	}
	if p.ProviderName == "openrouter" {
		requestPayload["provider"] = map[string]any{
			"data_collection":    p.DataCollection,
			"zdr":                p.ZDRRequired,
			"sort":               p.ProviderSort,
			"allow_fallbacks":    p.AllowProviderFallbacks,
			"require_parameters": p.RequireParameters,
			"max_price": map[string]float64{
				"prompt": p.MaxPromptPrice, "completion": p.MaxCompletionPrice,
			},
		}
		if p.ReasoningEffort != "" {
			requestPayload["reasoning"] = map[string]any{"effort": p.ReasoningEffort, "exclude": p.ReasoningExclude}
		}
	}
	body, _ := json.Marshal(requestPayload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ModelToolTurn{}, Usage{}, err
	}
	request.Header.Set("Authorization", "Bearer "+p.APIKey)
	request.Header.Set("Content-Type", "application/json")
	tracectx.InjectHTTP(ctx, request.Header)
	if p.HTTPReferer != "" {
		request.Header.Set("HTTP-Referer", p.HTTPReferer)
	}
	if p.AppTitle != "" {
		request.Header.Set("X-OpenRouter-Title", p.AppTitle)
	}
	response, err := p.Client.Do(request)
	if err != nil {
		return ModelToolTurn{}, Usage{}, err
	}
	defer response.Body.Close()
	responsePayload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return ModelToolTurn{}, Usage{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ModelToolTurn{}, Usage{}, fmt.Errorf("model provider status %d", response.StatusCode)
	}
	var result struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int      `json:"prompt_tokens"`
			Completion int      `json:"completion_tokens"`
			Cost       *float64 `json:"cost"`
		} `json:"usage"`
	}
	if err = json.Unmarshal(responsePayload, &result); err != nil {
		return ModelToolTurn{}, Usage{}, fmt.Errorf("decode model response: %w", err)
	}
	if len(result.Choices) == 0 {
		return ModelToolTurn{}, Usage{}, fmt.Errorf("model provider returned no choices")
	}
	costNumerator := int64(result.Usage.Prompt)*p.InputCostMicrosPerMillion + int64(result.Usage.Completion)*p.OutputCostMicrosPerMillion
	cost := int64(0)
	if costNumerator > 0 {
		cost = (costNumerator + 999_999) / 1_000_000
	}
	if result.Usage.Cost != nil && *result.Usage.Cost > 0 {
		cost = int64(math.Ceil(*result.Usage.Cost * 1_000_000))
	}
	model := result.Model
	if model == "" {
		model = p.Model
	}
	providerName := p.ProviderName
	if providerName == "" {
		providerName = "openai-compatible"
	}
	turn := ModelToolTurn{Text: strings.TrimSpace(result.Choices[0].Message.Content)}
	if len(result.Choices[0].Message.ToolCalls) > 0 {
		selected := result.Choices[0].Message.ToolCalls[0]
		arguments := map[string]any{}
		if value := strings.TrimSpace(selected.Function.Arguments); value != "" {
			if err = json.Unmarshal([]byte(value), &arguments); err != nil {
				return ModelToolTurn{}, Usage{}, fmt.Errorf("decode model tool arguments: %w", err)
			}
		}
		turn.Call = &ModelToolCall{ID: selected.ID, Name: selected.Function.Name, Arguments: arguments}
	}
	if len(tools) > 0 && turn.Call == nil {
		return ModelToolTurn{}, Usage{}, fmt.Errorf("model provider did not return a required tool call")
	}
	if turn.Call == nil && turn.Text == "" {
		return ModelToolTurn{}, Usage{}, fmt.Errorf("model provider returned neither text nor a tool call")
	}
	return turn, Usage{Provider: providerName, Model: model, InputTokens: result.Usage.Prompt, OutputTokens: result.Usage.Completion, EstimatedCostMicros: cost, Latency: time.Since(started)}, nil
}
func systemPrompt(item character.Character) string {
	return fmt.Sprintf("你是角色%s，与用户的关系是%s。性格：%s。说话风格：%s。回复长度偏好：%s。始终遵守产品安全策略，不声称自己是人类，不执行未经确认的外部操作。", item.Name, item.Relationship, item.Personality, item.SpeechStyle, item.ReplyLength)
}

func PersonaSystemPrompt(item character.Character) string {
	return systemPrompt(item)
}
