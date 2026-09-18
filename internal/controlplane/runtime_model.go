package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/windcry1/ai-companion/internal/contextengine"
)

const DefaultModelProfileKey = "production-default"

// ModelRuntimeSnapshot is the validated, secret-free runtime projection of a
// published model profile. Environment contains only model routing settings;
// provider credentials continue to come from the worker's secret store.
type ModelRuntimeSnapshot struct {
	Environment         string            `json:"environment"`
	Key                 string            `json:"key"`
	Revision            int               `json:"revision"`
	VersionID           string            `json:"version_id"`
	Version             int               `json:"version"`
	Fingerprint         string            `json:"fingerprint"`
	ConfigVersion       string            `json:"config_version"`
	CredentialReference string            `json:"credential_reference"`
	Variables           map[string]string `json:"-"`
}

// ActiveModelRuntime projects the active deployment into the environment
// contract consumed by the Python Agent runtime. The published payload is
// revalidated against the current provider/model catalog before it can be used.
func (s *Service) ActiveModelRuntime(ctx context.Context, key string) (ModelRuntimeSnapshot, error) {
	key = normalizeKey(key)
	if !validKey(key) {
		return ModelRuntimeSnapshot{}, ErrValidation
	}
	deployments, err := s.Active(ctx, KindModelProfile)
	if err != nil {
		return ModelRuntimeSnapshot{}, err
	}
	var active Deployment
	for _, item := range deployments {
		if item.Key == key {
			active = item
			break
		}
	}
	if active.Version.ID == "" {
		return ModelRuntimeSnapshot{}, ErrNotFound
	}
	validated, err := s.validatePayload(ctx, KindModelProfile, key, active.Version.Payload)
	if err != nil {
		return ModelRuntimeSnapshot{}, err
	}
	var payload ModelProfilePayload
	if err = json.Unmarshal(validated, &payload); err != nil {
		return ModelRuntimeSnapshot{}, validationError("decode active model profile: %v", err)
	}
	providers, err := s.Providers(ctx)
	if err != nil {
		return ModelRuntimeSnapshot{}, err
	}
	var provider ProviderConnection
	for _, item := range providers {
		if item.Key == payload.ProviderConnection && item.Status == "active" {
			provider = item
			break
		}
	}
	if provider.Key == "" {
		return ModelRuntimeSnapshot{}, validationError("active model provider is unavailable")
	}
	credentialReference := strings.TrimSpace(provider.CredentialRef)
	if !strings.HasPrefix(credentialReference, "env:") || strings.TrimSpace(strings.TrimPrefix(credentialReference, "env:")) == "" {
		return ModelRuntimeSnapshot{}, validationError("provider credential reference must use an env secret reference")
	}
	variables := map[string]string{
		"MODEL_PROVIDER":                 provider.ProviderType,
		"MODEL_BASE_URL":                 provider.BaseURL,
		"MODEL_CONFIG_VERSION":           payload.ConfigVersion,
		"MODEL_PROVIDER_SORT":            payload.ProviderPolicy.Sort,
		"MODEL_ALLOW_PROVIDER_FALLBACKS": strconv.FormatBool(payload.ProviderPolicy.AllowFallbacks),
		"MODEL_REQUIRE_PARAMETERS":       strconv.FormatBool(payload.ProviderPolicy.RequireParameters),
		"MODEL_DATA_COLLECTION":          payload.ProviderPolicy.DataCollection,
		"MODEL_ZDR_REQUIRED":             strconv.FormatBool(payload.ProviderPolicy.ZDRRequired),
		"MODEL_MAX_PROMPT_PRICE":         strconv.FormatFloat(payload.ProviderPolicy.MaxPromptPrice, 'f', -1, 64),
		"MODEL_MAX_COMPLETION_PRICE":     strconv.FormatFloat(payload.ProviderPolicy.MaxCompletionPrice, 'f', -1, 64),
		"MODEL_REQUIRE_PINNED":           "true",
	}
	models, err := s.Models(ctx)
	if err != nil {
		return ModelRuntimeSnapshot{}, err
	}
	modelWindows := map[string]int{}
	for _, model := range models {
		modelWindows[model.ModelID] = model.ContextWindow
	}
	for role, config := range payload.Roles {
		prefix := "MODEL_" + strings.ToUpper(role)
		// Every configured fallback must fit the same request. Snapshot the
		// smallest window so a later catalog update cannot change a resumed run.
		window := 0
		for _, model := range config.Models {
			candidate := modelWindows[model]
			if candidate <= 0 {
				candidate = contextengine.DefaultContextWindow
			}
			if window == 0 || candidate < window {
				window = candidate
			}
		}
		variables[prefix+"_CONTEXT_WINDOW"] = strconv.Itoa(window)
		variables[prefix+"_NAME"] = config.Models[0]
		variables[prefix+"_FALLBACK_NAMES"] = strings.Join(config.Models[1:], ",")
		variables[prefix+"_MAX_TOKENS"] = strconv.Itoa(config.MaxTokens)
		variables[prefix+"_REASONING_EFFORT"] = config.ReasoningEffort
		variables[prefix+"_TIMEOUT_SECONDS"] = millisecondsAsSeconds(config.TimeoutMS)
		variables[prefix+"_ATTEMPT_TIMEOUT_SECONDS"] = millisecondsAsSeconds(config.AttemptTimeoutMS)
	}
	responder := payload.Roles["responder"]
	variables["MODEL_NAME"] = responder.Models[0]
	variables["MODEL_FALLBACK_NAMES"] = strings.Join(responder.Models[1:], ",")
	variables["MODEL_MAX_TOKENS"] = strconv.Itoa(responder.MaxTokens)
	variables["MODEL_REASONING_EFFORT"] = responder.ReasoningEffort
	variables["MODEL_TIMEOUT_SECONDS"] = millisecondsAsSeconds(responder.TimeoutMS)
	variables["MODEL_ATTEMPT_TIMEOUT_SECONDS"] = millisecondsAsSeconds(responder.AttemptTimeoutMS)
	composer := payload.Roles["composer"]
	variables["MODEL_COMPOSER_MAX_TOKENS"] = strconv.Itoa(composer.MaxTokens)
	composerBatchMaxTokens := composer.MaxTokens
	if composerBatchMaxTokens > 6144 {
		composerBatchMaxTokens = 6144
	}
	variables["MODEL_COMPOSER_BATCH_MAX_TOKENS"] = strconv.Itoa(composerBatchMaxTokens)
	variables["MODEL_COMPOSER_TIMEOUT_SECONDS"] = millisecondsAsSeconds(composer.TimeoutMS)
	variables["MODEL_COMPOSER_ATTEMPT_TIMEOUT_SECONDS"] = millisecondsAsSeconds(composer.AttemptTimeoutMS)
	variables["MODEL_REPAIRER_MAX_TOKENS"] = strconv.Itoa(payload.Roles["repairer"].MaxTokens)
	minimumFallbackTimeoutMS := responder.TimeoutMS
	if minimumFallbackTimeoutMS > 5000 {
		minimumFallbackTimeoutMS = 5000
	}
	variables["MODEL_MIN_FALLBACK_TIMEOUT_SECONDS"] = millisecondsAsSeconds(minimumFallbackTimeoutMS)
	translation := payload.Roles["translation"]
	variables["MODEL_TRANSLATION_NAME"] = translation.Models[0]
	variables["MODEL_TRANSLATION_MAX_TOKENS"] = strconv.Itoa(translation.MaxTokens)
	return ModelRuntimeSnapshot{
		Environment: s.environment, Key: key, Revision: active.Revision,
		VersionID: active.Version.ID, Version: active.Version.Version,
		Fingerprint: active.Version.Fingerprint, ConfigVersion: payload.ConfigVersion,
		CredentialReference: credentialReference, Variables: variables,
	}, nil
}

func millisecondsAsSeconds(value int) string {
	seconds := float64(value) / 1000
	return strconv.FormatFloat(seconds, 'f', -1, 64)
}

func (snapshot ModelRuntimeSnapshot) ValidateCredentialEnvironment(lookup func(string) (string, bool)) error {
	if lookup == nil {
		return ErrValidation
	}
	name := strings.TrimSpace(strings.TrimPrefix(snapshot.CredentialReference, "env:"))
	value, exists := lookup(name)
	if name == "" || !exists || strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: provider credential environment %s is unavailable", ErrValidation, name)
	}
	return nil
}
