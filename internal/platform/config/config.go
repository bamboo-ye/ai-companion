package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTextModel          = "deepseek/deepseek-v4-flash-0731"
	defaultQualityFallback    = "openai/gpt-5-mini"
	defaultLightFallback      = "openai/gpt-5-nano"
	defaultCompanionFreeModel = "openai/gpt-oss-20b:free"
	defaultModelConfigVersion = "2026-08-structured-composer-v2"
)

type Config struct {
	Environment                        string
	ServiceName                        string
	LogLevel                           string
	HTTPAddr                           string
	ShutdownTimeout                    time.Duration
	DatabaseDriver                     string
	MySQLDSN                           string
	PostgresDSN                        string
	RedisAddr                          string
	RedisPassword                      string
	KafkaBrokers                       []string
	KafkaEnabled                       bool
	KafkaConsumerGroup                 string
	AgentKafkaConsumerGroup            string
	AgentMetricsAddr                   string
	KafkaReconcileInterval             time.Duration
	OutboxRelayPollInterval            time.Duration
	OutboxRelayLeaseDuration           time.Duration
	OutboxRelayBatchSize               int
	OutboxRelayMaxAttempts             int
	QdrantURL                          string
	QdrantAPIKey                       string
	QdrantCollection                   string
	MinIOEndpoint                      string
	DocumentStorageDir                 string
	DocumentMaxUploadBytes             int64
	DocumentWorkerPollInterval         time.Duration
	PythonExecutable                   string
	PythonWorkerPath                   string
	SpreadsheetExecutable              string
	SpreadsheetWorkerPath              string
	SkillStorageDir                    string
	LedgerStorageDir                   string
	SkillWorkerEnabled                 bool
	SkillWorkerPollInterval            time.Duration
	SkillWorkerLeaseDuration           time.Duration
	SkillWorkerRenewInterval           time.Duration
	ReliabilityPollInterval            time.Duration
	NotificationSchedulerInterval      time.Duration
	EmailWorkerPollInterval            time.Duration
	EmailWorkerLeaseDuration           time.Duration
	SMTPAddr                           string
	SMTPHost                           string
	SMTPUsername                       string
	SMTPPassword                       string
	SMTPFrom                           string
	SMTPUseTLS                         bool
	MCPStdioServersJSON                string
	WebOrigin                          string
	AuthTokenSecret                    string
	AgentGatewayToken                  string
	AgentConfirmationSecret            string
	AgentConfirmationTTL               time.Duration
	AgentWorkerLeaseDuration           time.Duration
	AgentWorkerPollInterval            time.Duration
	AgentWorkerRetryDelay              time.Duration
	AgentWorkerRetryMaxDelay           time.Duration
	AgentWorkerRetryJitterPercent      int
	AgentWorkerMaxAttempts             int
	AgentWorkerConcurrency             int
	AgentDispatchQueueSize             int
	AgentPythonPoolEnabled             bool
	AgentPythonPoolWarmSize            int
	AgentWorkerTimeout                 time.Duration
	AgentRunTimeout                    time.Duration
	AgentControlPollInterval           time.Duration
	AgentChatModules                   []string
	OperatorToken                      string
	OperatorMFARequired                bool
	AccessTokenTTL                     time.Duration
	RefreshTokenTTL                    time.Duration
	PresenceTTL                        time.Duration
	ChatRateLimit                      int
	ChatRateWindow                     time.Duration
	ContextRecentTokenBudget           int
	ContextSummaryTokenBudget          int
	ModelProvider                      string
	ModelBaseURL                       string
	ModelAPIKey                        string
	ModelName                          string
	ModelFallbackNames                 []string
	ModelRoleNames                     map[string][]string
	ModelMaxTokens                     int
	ModelComposerMaxTokens             int
	ModelDataCollection                string
	ModelZDRRequired                   bool
	ModelReasoningEffort               string
	ModelReasoningExclude              bool
	ModelConfigVersion                 string
	ModelRequirePinned                 bool
	ModelProviderSort                  string
	ModelAllowProviderFallbacks        bool
	ModelRequireParameters             bool
	ModelMaxPromptPrice                float64
	ModelMaxCompletionPrice            float64
	ModelHTTPReferer                   string
	ModelAppTitle                      string
	ModelTimeout                       time.Duration
	ModelCircuitFailureThreshold       int
	ModelCircuitCooldown               time.Duration
	ModelInputCostMicrosPerMillion     int64
	ModelOutputCostMicrosPerMillion    int64
	ModelTranslationName               string
	ModelTranslationMaxTokens          int
	ModelTranslationMaxCostMicros      int64
	ModelTranslationMaxPromptPrice     float64
	ModelTranslationMaxCompletionPrice float64
}

func Load(serviceName string) (Config, error) {
	environment := value("APP_ENV", "development")
	production := environment == "production"
	mysqlDSN := strings.TrimSpace(os.Getenv("MYSQL_DSN"))
	postgresDSN := strings.TrimSpace(os.Getenv("POSTGRES_DSN"))
	databaseDriver := strings.ToLower(strings.TrimSpace(os.Getenv("APP_DATABASE_DRIVER")))
	if databaseDriver == "" {
		databaseDriver = strings.ToLower(strings.TrimSpace(os.Getenv("DATABASE_DRIVER")))
	}
	if databaseDriver == "" {
		switch {
		case postgresDSN != "":
			databaseDriver = "postgres"
		case mysqlDSN != "":
			databaseDriver = "mysql"
		default:
			databaseDriver = "memory"
		}
	}
	switch databaseDriver {
	case "memory":
	case "mysql":
		if mysqlDSN == "" {
			return Config{}, fmt.Errorf("MYSQL_DSN is required when DATABASE_DRIVER=mysql")
		}
	case "postgres", "postgresql", "pgx":
		databaseDriver = "postgres"
		if postgresDSN == "" {
			return Config{}, fmt.Errorf("POSTGRES_DSN is required when DATABASE_DRIVER=postgres")
		}
	default:
		return Config{}, fmt.Errorf("DATABASE_DRIVER must be memory, mysql or postgres")
	}
	shutdownTimeout, err := time.ParseDuration(value("SHUTDOWN_TIMEOUT", "10s"))
	if err != nil {
		return Config{}, fmt.Errorf("parse SHUTDOWN_TIMEOUT: %w", err)
	}

	if serviceName == "" {
		return Config{}, fmt.Errorf("service name is required")
	}
	accessTokenTTL, err := time.ParseDuration(value("ACCESS_TOKEN_TTL", "15m"))
	if err != nil {
		return Config{}, fmt.Errorf("parse ACCESS_TOKEN_TTL: %w", err)
	}
	refreshTokenTTL, err := time.ParseDuration(value("REFRESH_TOKEN_TTL", "720h"))
	if err != nil {
		return Config{}, fmt.Errorf("parse REFRESH_TOKEN_TTL: %w", err)
	}
	presenceTTL, err := time.ParseDuration(value("PRESENCE_TTL", "24h"))
	if err != nil {
		return Config{}, fmt.Errorf("parse PRESENCE_TTL: %w", err)
	}
	chatRateWindow, err := time.ParseDuration(value("CHAT_RATE_WINDOW", "1m"))
	if err != nil {
		return Config{}, fmt.Errorf("parse CHAT_RATE_WINDOW: %w", err)
	}
	modelTimeout, err := time.ParseDuration(value("MODEL_TIMEOUT", "30s"))
	if err != nil {
		return Config{}, fmt.Errorf("parse MODEL_TIMEOUT: %w", err)
	}
	modelCircuitCooldown, err := time.ParseDuration(value("MODEL_CIRCUIT_COOLDOWN", "30s"))
	if err != nil || modelCircuitCooldown <= 0 {
		return Config{}, fmt.Errorf("MODEL_CIRCUIT_COOLDOWN must be a positive duration")
	}
	documentWorkerPollInterval, err := time.ParseDuration(value("DOCUMENT_WORKER_POLL_INTERVAL", "1s"))
	if err != nil || documentWorkerPollInterval <= 0 {
		return Config{}, fmt.Errorf("DOCUMENT_WORKER_POLL_INTERVAL must be a positive duration")
	}
	skillWorkerEnabled, err := strconv.ParseBool(value("SKILL_WORKER_ENABLED", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("SKILL_WORKER_ENABLED must be true or false")
	}
	skillWorkerPollInterval, err := time.ParseDuration(value("SKILL_WORKER_POLL_INTERVAL", "1s"))
	if err != nil || skillWorkerPollInterval <= 0 {
		return Config{}, fmt.Errorf("SKILL_WORKER_POLL_INTERVAL must be a positive duration")
	}
	skillWorkerLeaseDuration, err := time.ParseDuration(value("SKILL_WORKER_LEASE_DURATION", "2m"))
	if err != nil || skillWorkerLeaseDuration <= 0 {
		return Config{}, fmt.Errorf("SKILL_WORKER_LEASE_DURATION must be a positive duration")
	}
	skillWorkerRenewInterval, err := time.ParseDuration(value("SKILL_WORKER_RENEW_INTERVAL", "30s"))
	if err != nil || skillWorkerRenewInterval <= 0 || skillWorkerRenewInterval >= skillWorkerLeaseDuration {
		return Config{}, fmt.Errorf("SKILL_WORKER_RENEW_INTERVAL must be positive and shorter than SKILL_WORKER_LEASE_DURATION")
	}
	reliabilityPollInterval, err := time.ParseDuration(value("RELIABILITY_POLL_INTERVAL", "10s"))
	if err != nil || reliabilityPollInterval <= 0 {
		return Config{}, fmt.Errorf("RELIABILITY_POLL_INTERVAL must be a positive duration")
	}
	notificationSchedulerInterval, err := time.ParseDuration(value("NOTIFICATION_SCHEDULER_INTERVAL", "1s"))
	if err != nil || notificationSchedulerInterval <= 0 {
		return Config{}, fmt.Errorf("NOTIFICATION_SCHEDULER_INTERVAL must be a positive duration")
	}
	emailWorkerPollInterval, err := time.ParseDuration(value("EMAIL_WORKER_POLL_INTERVAL", "1s"))
	if err != nil || emailWorkerPollInterval <= 0 {
		return Config{}, fmt.Errorf("EMAIL_WORKER_POLL_INTERVAL must be a positive duration")
	}
	emailWorkerLeaseDuration, err := time.ParseDuration(value("EMAIL_WORKER_LEASE_DURATION", "2m"))
	if err != nil || emailWorkerLeaseDuration <= 0 {
		return Config{}, fmt.Errorf("EMAIL_WORKER_LEASE_DURATION must be a positive duration")
	}
	smtpUseTLS, err := strconv.ParseBool(value("SMTP_USE_TLS", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("SMTP_USE_TLS must be true or false")
	}
	chatRateLimit := 30
	if _, err := fmt.Sscanf(value("CHAT_RATE_LIMIT", "30"), "%d", &chatRateLimit); err != nil || chatRateLimit < 1 {
		return Config{}, fmt.Errorf("CHAT_RATE_LIMIT must be a positive integer")
	}
	contextRecentTokenBudget := 6000
	if _, err := fmt.Sscanf(value("CONTEXT_RECENT_TOKEN_BUDGET", "6000"), "%d", &contextRecentTokenBudget); err != nil || contextRecentTokenBudget < 128 {
		return Config{}, fmt.Errorf("CONTEXT_RECENT_TOKEN_BUDGET must be an integer of at least 128")
	}
	contextSummaryTokenBudget := 1200
	if _, err := fmt.Sscanf(value("CONTEXT_SUMMARY_TOKEN_BUDGET", "1200"), "%d", &contextSummaryTokenBudget); err != nil || contextSummaryTokenBudget < 64 {
		return Config{}, fmt.Errorf("CONTEXT_SUMMARY_TOKEN_BUDGET must be an integer of at least 64")
	}
	modelProvider := value("MODEL_PROVIDER", "development")
	if modelProvider != "development" && modelProvider != "openrouter" {
		return Config{}, fmt.Errorf("MODEL_PROVIDER must be development or openrouter")
	}
	if environment == "production" && modelProvider != "openrouter" {
		return Config{}, fmt.Errorf("production requires MODEL_PROVIDER=openrouter")
	}
	modelBaseURL := value("MODEL_BASE_URL", "")
	if modelProvider == "openrouter" && modelBaseURL == "" {
		modelBaseURL = "https://openrouter.ai/api/v1"
	}
	if modelProvider == "openrouter" && strings.TrimRight(modelBaseURL, "/") != "https://openrouter.ai/api/v1" {
		return Config{}, fmt.Errorf("MODEL_BASE_URL must be the canonical OpenRouter API endpoint")
	}
	modelName := value("MODEL_NAME", "")
	if modelProvider == "openrouter" && (value("MODEL_API_KEY", "") == "" || modelName == "") {
		return Config{}, fmt.Errorf("MODEL_API_KEY and MODEL_NAME are required for OpenRouter")
	}
	modelFallbackNames := splitNonEmpty(value("MODEL_FALLBACK_NAMES", ""))
	if len(modelFallbackNames) > 2 {
		return Config{}, fmt.Errorf("MODEL_FALLBACK_NAMES supports at most two entries")
	}
	modelMaxTokens := 1024
	if _, err := fmt.Sscanf(value("MODEL_MAX_TOKENS", "1024"), "%d", &modelMaxTokens); err != nil || modelMaxTokens < 64 || modelMaxTokens > 32768 {
		return Config{}, fmt.Errorf("MODEL_MAX_TOKENS must be between 64 and 32768")
	}
	modelComposerMaxTokens := 12288
	if _, err := fmt.Sscanf(value("MODEL_COMPOSER_MAX_TOKENS", "12288"), "%d", &modelComposerMaxTokens); err != nil || modelComposerMaxTokens < 64 || modelComposerMaxTokens > 32768 {
		return Config{}, fmt.Errorf("MODEL_COMPOSER_MAX_TOKENS must be between 64 and 32768")
	}
	modelDataCollection := value("MODEL_DATA_COLLECTION", "deny")
	if modelDataCollection != "allow" && modelDataCollection != "deny" {
		return Config{}, fmt.Errorf("MODEL_DATA_COLLECTION must be allow or deny")
	}
	if production && modelDataCollection != "deny" {
		return Config{}, fmt.Errorf("production requires MODEL_DATA_COLLECTION=deny")
	}
	modelZDRRequired, err := strconv.ParseBool(value("MODEL_ZDR_REQUIRED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("MODEL_ZDR_REQUIRED must be true or false")
	}
	modelReasoningEffort := value("MODEL_REASONING_EFFORT", "minimal")
	switch modelReasoningEffort {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return Config{}, fmt.Errorf("MODEL_REASONING_EFFORT must be none, minimal, low, medium, high, xhigh or max")
	}
	modelReasoningExclude, err := strconv.ParseBool(value("MODEL_REASONING_EXCLUDE", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("MODEL_REASONING_EXCLUDE must be true or false")
	}
	modelConfigVersion := value("MODEL_CONFIG_VERSION", defaultModelConfigVersion)
	modelRequirePinned, err := strconv.ParseBool(value("MODEL_REQUIRE_PINNED", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("MODEL_REQUIRE_PINNED must be true or false")
	}
	if production && !modelRequirePinned {
		return Config{}, fmt.Errorf("production requires MODEL_REQUIRE_PINNED=true")
	}
	if modelProvider == "openrouter" && modelRequirePinned {
		for _, candidate := range append([]string{modelName}, modelFallbackNames...) {
			if dynamicOpenRouterModel(candidate) {
				return Config{}, fmt.Errorf("OpenRouter production models must use concrete slugs: %s", candidate)
			}
		}
	}
	modelProviderSort := value("MODEL_PROVIDER_SORT", "price")
	if modelProviderSort != "price" && modelProviderSort != "latency" && modelProviderSort != "throughput" {
		return Config{}, fmt.Errorf("MODEL_PROVIDER_SORT must be price, latency or throughput")
	}
	if production && modelProviderSort != "price" {
		return Config{}, fmt.Errorf("production requires MODEL_PROVIDER_SORT=price")
	}
	modelAllowProviderFallbacks, err := strconv.ParseBool(value("MODEL_ALLOW_PROVIDER_FALLBACKS", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("MODEL_ALLOW_PROVIDER_FALLBACKS must be true or false")
	}
	modelRequireParameters, err := strconv.ParseBool(value("MODEL_REQUIRE_PARAMETERS", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("MODEL_REQUIRE_PARAMETERS must be true or false")
	}
	if production && !modelRequireParameters {
		return Config{}, fmt.Errorf("production requires MODEL_REQUIRE_PARAMETERS=true")
	}
	modelMaxPromptPrice, err := strconv.ParseFloat(value("MODEL_MAX_PROMPT_PRICE", "0.3"), 64)
	if err != nil || math.IsNaN(modelMaxPromptPrice) || math.IsInf(modelMaxPromptPrice, 0) || modelMaxPromptPrice <= 0 || (production && modelMaxPromptPrice > 0.3) {
		return Config{}, fmt.Errorf("MODEL_MAX_PROMPT_PRICE must be positive and at most 0.3 in production")
	}
	modelMaxCompletionPrice, err := strconv.ParseFloat(value("MODEL_MAX_COMPLETION_PRICE", "2.5"), 64)
	if err != nil || math.IsNaN(modelMaxCompletionPrice) || math.IsInf(modelMaxCompletionPrice, 0) || modelMaxCompletionPrice <= 0 || (production && modelMaxCompletionPrice > 2.5) {
		return Config{}, fmt.Errorf("MODEL_MAX_COMPLETION_PRICE must be positive and at most 2.5 in production")
	}
	modelRoleNames := map[string][]string{
		"planner":   roleModels("PLANNER", []string{defaultTextModel, defaultQualityFallback}),
		"router":    roleModels("ROUTER", []string{defaultTextModel, defaultLightFallback}),
		"composer":  roleModels("COMPOSER", []string{defaultTextModel, defaultQualityFallback}),
		"assessor":  roleModels("ASSESSOR", []string{defaultTextModel, defaultLightFallback}),
		"responder": roleModels("RESPONDER", append([]string{modelName}, modelFallbackNames...)),
		"companion_responder": roleModels("COMPANION_RESPONDER", []string{
			defaultCompanionFreeModel,
			"google/gemma-4-31b-it:free",
			"inclusionai/ling-3.0-flash:free",
		}),
		"repairer": roleModels("REPAIRER", []string{defaultTextModel}),
	}
	translationNameRaw, translationNameConfigured := os.LookupEnv("MODEL_TRANSLATION_NAME")
	translationName := strings.TrimSpace(translationNameRaw)
	if !translationNameConfigured {
		translationName = defaultQualityFallback
	}
	translationMaxTokens := 8000
	if _, err := fmt.Sscanf(value("MODEL_TRANSLATION_MAX_TOKENS", "8000"), "%d", &translationMaxTokens); err != nil || translationMaxTokens < 256 || translationMaxTokens > 32768 {
		return Config{}, fmt.Errorf("MODEL_TRANSLATION_MAX_TOKENS must be between 256 and 32768")
	}
	var translationMaxCostMicros int64
	if _, err := fmt.Sscanf(value("MODEL_TRANSLATION_MAX_COST_MICROS", "60000"), "%d", &translationMaxCostMicros); err != nil || translationMaxCostMicros < 1 || translationMaxCostMicros > 60000 {
		return Config{}, fmt.Errorf("MODEL_TRANSLATION_MAX_COST_MICROS must be between 1 and 60000")
	}
	translationMaxPromptPrice, err := strconv.ParseFloat(value("MODEL_TRANSLATION_MAX_PROMPT_PRICE", "0.3"), 64)
	if err != nil || math.IsNaN(translationMaxPromptPrice) || math.IsInf(translationMaxPromptPrice, 0) || translationMaxPromptPrice <= 0 || translationMaxPromptPrice > 0.3 {
		return Config{}, fmt.Errorf("MODEL_TRANSLATION_MAX_PROMPT_PRICE must be positive and at most 0.3")
	}
	translationMaxOutputPrice, err := strconv.ParseFloat(value("MODEL_TRANSLATION_MAX_COMPLETION_PRICE", "2.5"), 64)
	if err != nil || math.IsNaN(translationMaxOutputPrice) || math.IsInf(translationMaxOutputPrice, 0) || translationMaxOutputPrice <= 0 || translationMaxOutputPrice > 2.5 {
		return Config{}, fmt.Errorf("MODEL_TRANSLATION_MAX_COMPLETION_PRICE must be positive and at most 2.5")
	}
	if modelProvider == "openrouter" {
		for role, candidates := range modelRoleNames {
			if len(candidates) == 0 || len(candidates) > 3 {
				return Config{}, fmt.Errorf("MODEL_%s model list must contain between one and three entries", strings.ToUpper(role))
			}
			if modelRequirePinned {
				for _, candidate := range candidates {
					if role == "companion_responder" {
						if !concreteFreeOpenRouterModel(candidate) {
							return Config{}, fmt.Errorf("OpenRouter companion responder must use concrete zero-price model variants: %s", candidate)
						}
						continue
					}
					if dynamicOpenRouterModel(candidate) {
						return Config{}, fmt.Errorf("OpenRouter %s role must use concrete model slugs: %s", role, candidate)
					}
				}
			}
		}
		if translationName == "" {
			return Config{}, fmt.Errorf("MODEL_TRANSLATION_NAME is required for OpenRouter PDF translation")
		}
		if dynamicOpenRouterModel(translationName) {
			return Config{}, fmt.Errorf("OpenRouter PDF translation must use a concrete model slug: %s", translationName)
		}
	}
	var inputCost, outputCost int64
	if _, err := fmt.Sscanf(value("MODEL_INPUT_COST_MICROS_PER_MILLION", "0"), "%d", &inputCost); err != nil || inputCost < 0 {
		return Config{}, fmt.Errorf("MODEL_INPUT_COST_MICROS_PER_MILLION must be a non-negative integer")
	}
	if _, err := fmt.Sscanf(value("MODEL_OUTPUT_COST_MICROS_PER_MILLION", "0"), "%d", &outputCost); err != nil || outputCost < 0 {
		return Config{}, fmt.Errorf("MODEL_OUTPUT_COST_MICROS_PER_MILLION must be a non-negative integer")
	}
	modelCircuitFailureThreshold := 3
	if _, err := fmt.Sscanf(value("MODEL_CIRCUIT_FAILURE_THRESHOLD", "3"), "%d", &modelCircuitFailureThreshold); err != nil || modelCircuitFailureThreshold < 1 || modelCircuitFailureThreshold > 100 {
		return Config{}, fmt.Errorf("MODEL_CIRCUIT_FAILURE_THRESHOLD must be between 1 and 100")
	}
	var documentMaxUploadBytes int64
	if _, err := fmt.Sscanf(value("DOCUMENT_MAX_UPLOAD_BYTES", "20971520"), "%d", &documentMaxUploadBytes); err != nil || documentMaxUploadBytes < 1 {
		return Config{}, fmt.Errorf("DOCUMENT_MAX_UPLOAD_BYTES must be a positive integer")
	}
	authTokenSecret := value("AUTH_TOKEN_SECRET", "development-only-change-me")
	if production && authTokenSecret == "development-only-change-me" {
		return Config{}, fmt.Errorf("AUTH_TOKEN_SECRET must be set in production")
	}
	agentGatewayToken := value("AGENT_GATEWAY_TOKEN", "development-agent-gateway-token")
	if production && agentGatewayToken == "development-agent-gateway-token" {
		return Config{}, fmt.Errorf("AGENT_GATEWAY_TOKEN must be set in production")
	}
	agentConfirmationSecret := value("AGENT_CONFIRMATION_SECRET", "development-agent-confirmation-secret")
	if len(agentConfirmationSecret) < 16 {
		return Config{}, fmt.Errorf("AGENT_CONFIRMATION_SECRET must contain at least 16 characters")
	}
	if production && agentConfirmationSecret == "development-agent-confirmation-secret" {
		return Config{}, fmt.Errorf("AGENT_CONFIRMATION_SECRET must be set in production")
	}
	agentConfirmationTTL, err := time.ParseDuration(value("AGENT_CONFIRMATION_TTL", "15m"))
	if err != nil || agentConfirmationTTL <= 0 {
		return Config{}, fmt.Errorf("AGENT_CONFIRMATION_TTL must be a positive duration")
	}
	agentWorkerLeaseDuration, err := time.ParseDuration(value("AGENT_WORKER_LEASE_DURATION", "10m"))
	if err != nil || agentWorkerLeaseDuration <= 0 {
		return Config{}, fmt.Errorf("AGENT_WORKER_LEASE_DURATION must be a positive duration")
	}
	agentWorkerPollInterval, err := time.ParseDuration(value("AGENT_WORKER_POLL_INTERVAL", "30s"))
	if err != nil || agentWorkerPollInterval <= 0 {
		return Config{}, fmt.Errorf("AGENT_WORKER_POLL_INTERVAL must be a positive duration")
	}
	agentWorkerRetryDelay, err := time.ParseDuration(value("AGENT_WORKER_RETRY_DELAY", "30s"))
	if err != nil || agentWorkerRetryDelay <= 0 {
		return Config{}, fmt.Errorf("AGENT_WORKER_RETRY_DELAY must be a positive duration")
	}
	agentWorkerRetryMaxDelay, err := time.ParseDuration(value("AGENT_WORKER_RETRY_MAX_DELAY", "2m"))
	if err != nil || agentWorkerRetryMaxDelay < agentWorkerRetryDelay || agentWorkerRetryMaxDelay > 24*time.Hour {
		return Config{}, fmt.Errorf("AGENT_WORKER_RETRY_MAX_DELAY must be between AGENT_WORKER_RETRY_DELAY and 24h")
	}
	agentWorkerRetryJitterPercent := 20
	if _, err := fmt.Sscanf(value("AGENT_WORKER_RETRY_JITTER_PERCENT", "20"), "%d", &agentWorkerRetryJitterPercent); err != nil || agentWorkerRetryJitterPercent < 0 || agentWorkerRetryJitterPercent > 100 {
		return Config{}, fmt.Errorf("AGENT_WORKER_RETRY_JITTER_PERCENT must be between 0 and 100")
	}
	agentWorkerMaxAttempts := 3
	if _, err := fmt.Sscanf(value("AGENT_WORKER_MAX_ATTEMPTS", "3"), "%d", &agentWorkerMaxAttempts); err != nil || agentWorkerMaxAttempts < 1 || agentWorkerMaxAttempts > 20 {
		return Config{}, fmt.Errorf("AGENT_WORKER_MAX_ATTEMPTS must be between 1 and 20")
	}
	agentWorkerConcurrency := 4
	if _, err := fmt.Sscanf(value("AGENT_WORKER_CONCURRENCY", "4"), "%d", &agentWorkerConcurrency); err != nil || agentWorkerConcurrency < 1 || agentWorkerConcurrency > 64 {
		return Config{}, fmt.Errorf("AGENT_WORKER_CONCURRENCY must be between 1 and 64")
	}
	agentDispatchQueueSize := 32
	if _, err := fmt.Sscanf(value("AGENT_DISPATCH_QUEUE_SIZE", "32"), "%d", &agentDispatchQueueSize); err != nil || agentDispatchQueueSize < agentWorkerConcurrency || agentDispatchQueueSize > 4096 {
		return Config{}, fmt.Errorf("AGENT_DISPATCH_QUEUE_SIZE must be between AGENT_WORKER_CONCURRENCY and 4096")
	}
	agentPythonPoolEnabled, err := strconv.ParseBool(value("AGENT_PYTHON_POOL_ENABLED", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("AGENT_PYTHON_POOL_ENABLED must be true or false")
	}
	agentPythonPoolWarmSize := 1
	if _, err := fmt.Sscanf(value("AGENT_PYTHON_POOL_WARM_SIZE", "1"), "%d", &agentPythonPoolWarmSize); err != nil || agentPythonPoolWarmSize < 0 || agentPythonPoolWarmSize > agentWorkerConcurrency {
		return Config{}, fmt.Errorf("AGENT_PYTHON_POOL_WARM_SIZE must be between 0 and AGENT_WORKER_CONCURRENCY")
	}
	agentWorkerTimeout, err := time.ParseDuration(value("AGENT_WORKER_TIMEOUT", "7m"))
	if err != nil || agentWorkerTimeout <= 0 || agentWorkerTimeout >= agentWorkerLeaseDuration {
		return Config{}, fmt.Errorf("AGENT_WORKER_TIMEOUT must be positive and shorter than AGENT_WORKER_LEASE_DURATION")
	}
	agentRunTimeout, err := time.ParseDuration(value("AGENT_RUN_TIMEOUT", "15m"))
	if err != nil || agentRunTimeout <= 0 {
		return Config{}, fmt.Errorf("AGENT_RUN_TIMEOUT must be a positive duration")
	}
	agentControlPollInterval, err := time.ParseDuration(value("AGENT_CONTROL_POLL_INTERVAL", "500ms"))
	if err != nil || agentControlPollInterval <= 0 {
		return Config{}, fmt.Errorf("AGENT_CONTROL_POLL_INTERVAL must be a positive duration")
	}
	agentChatModules := splitNonEmpty(value("AGENT_CHAT_MODULES", ""))
	seenAgentModules := map[string]bool{}
	for _, module := range agentChatModules {
		if module != "companion" && module != "life" && module != "work" {
			return Config{}, fmt.Errorf("AGENT_CHAT_MODULES supports companion, life and work")
		}
		if seenAgentModules[module] {
			return Config{}, fmt.Errorf("AGENT_CHAT_MODULES must not contain duplicates")
		}
		seenAgentModules[module] = true
	}
	if production {
		for _, module := range []string{"companion", "life", "work"} {
			if !seenAgentModules[module] {
				return Config{}, fmt.Errorf("production AGENT_CHAT_MODULES must include companion, life and work")
			}
		}
	}
	operatorToken := value("OPERATOR_TOKEN", "development-operator-token")
	if production && operatorToken == "development-operator-token" {
		return Config{}, fmt.Errorf("OPERATOR_TOKEN must be set in production")
	}
	operatorMFARequired, err := strconv.ParseBool(value("OPERATOR_MFA_REQUIRED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("OPERATOR_MFA_REQUIRED must be true or false")
	}
	if production && !operatorMFARequired {
		return Config{}, fmt.Errorf("OPERATOR_MFA_REQUIRED must be true in production")
	}
	webOrigin := value("WEB_ORIGIN", "http://localhost:3000")
	if production && !strings.HasPrefix(webOrigin, "https://") {
		return Config{}, fmt.Errorf("WEB_ORIGIN must use https in production")
	}

	brokers := splitNonEmpty(value("KAFKA_BROKERS", "127.0.0.1:9092"))
	if len(brokers) == 0 {
		return Config{}, fmt.Errorf("at least one Kafka broker is required")
	}
	kafkaEnabled, err := strconv.ParseBool(value("KAFKA_ENABLED", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("KAFKA_ENABLED must be true or false")
	}
	kafkaReconcileInterval, err := time.ParseDuration(value("KAFKA_RECONCILE_INTERVAL", "30s"))
	if err != nil || kafkaReconcileInterval <= 0 {
		return Config{}, fmt.Errorf("KAFKA_RECONCILE_INTERVAL must be a positive duration")
	}
	outboxRelayPollInterval, err := time.ParseDuration(value("OUTBOX_RELAY_POLL_INTERVAL", "500ms"))
	if err != nil || outboxRelayPollInterval <= 0 {
		return Config{}, fmt.Errorf("OUTBOX_RELAY_POLL_INTERVAL must be a positive duration")
	}
	outboxRelayLeaseDuration, err := time.ParseDuration(value("OUTBOX_RELAY_LEASE_DURATION", "30s"))
	if err != nil || outboxRelayLeaseDuration <= 0 {
		return Config{}, fmt.Errorf("OUTBOX_RELAY_LEASE_DURATION must be a positive duration")
	}
	outboxRelayBatchSize := 50
	if _, err := fmt.Sscanf(value("OUTBOX_RELAY_BATCH_SIZE", "50"), "%d", &outboxRelayBatchSize); err != nil || outboxRelayBatchSize < 1 || outboxRelayBatchSize > 500 {
		return Config{}, fmt.Errorf("OUTBOX_RELAY_BATCH_SIZE must be between 1 and 500")
	}
	outboxRelayMaxAttempts := 8
	if _, err := fmt.Sscanf(value("OUTBOX_RELAY_MAX_ATTEMPTS", "8"), "%d", &outboxRelayMaxAttempts); err != nil || outboxRelayMaxAttempts < 1 || outboxRelayMaxAttempts > 100 {
		return Config{}, fmt.Errorf("OUTBOX_RELAY_MAX_ATTEMPTS must be between 1 and 100")
	}

	return Config{
		Environment:                   environment,
		ServiceName:                   serviceName,
		LogLevel:                      value("LOG_LEVEL", "info"),
		HTTPAddr:                      value("API_HTTP_ADDR", ":8080"),
		ShutdownTimeout:               shutdownTimeout,
		DatabaseDriver:                databaseDriver,
		MySQLDSN:                      mysqlDSN,
		PostgresDSN:                   postgresDSN,
		RedisAddr:                     value("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:                 os.Getenv("REDIS_PASSWORD"),
		KafkaBrokers:                  brokers,
		KafkaEnabled:                  kafkaEnabled,
		KafkaConsumerGroup:            value("KAFKA_CONSUMER_GROUP", "ai-companion-background-v1"),
		AgentKafkaConsumerGroup:       value("AGENT_KAFKA_CONSUMER_GROUP", "ai-companion-agent-v1"),
		AgentMetricsAddr:              value("AGENT_METRICS_ADDR", ":9467"),
		KafkaReconcileInterval:        kafkaReconcileInterval,
		OutboxRelayPollInterval:       outboxRelayPollInterval,
		OutboxRelayLeaseDuration:      outboxRelayLeaseDuration,
		OutboxRelayBatchSize:          outboxRelayBatchSize,
		OutboxRelayMaxAttempts:        outboxRelayMaxAttempts,
		QdrantURL:                     value("QDRANT_URL", "http://127.0.0.1:6333"),
		QdrantAPIKey:                  os.Getenv("QDRANT_API_KEY"),
		QdrantCollection:              value("QDRANT_COLLECTION", "ai_companion_document_chunks_v1"),
		MinIOEndpoint:                 value("MINIO_ENDPOINT", "127.0.0.1:9000"),
		DocumentStorageDir:            value("DOCUMENT_STORAGE_DIR", ".data/files"),
		DocumentMaxUploadBytes:        documentMaxUploadBytes,
		DocumentWorkerPollInterval:    documentWorkerPollInterval,
		PythonExecutable:              value("PYTHON_EXECUTABLE", "python3"),
		PythonWorkerPath:              value("PYTHON_WORKER_PATH", "workers/python/src"),
		SpreadsheetExecutable:         value("SPREADSHEET_EXECUTABLE", "python3"),
		SpreadsheetWorkerPath:         value("SPREADSHEET_WORKER_PATH", "workers/python/src/ai_companion_worker/ledger_export.py"),
		SkillStorageDir:               value("SKILL_STORAGE_DIR", ".data/skill-files"),
		LedgerStorageDir:              value("LEDGER_STORAGE_DIR", ".data/ledger-exports"),
		SkillWorkerEnabled:            skillWorkerEnabled,
		SkillWorkerPollInterval:       skillWorkerPollInterval,
		SkillWorkerLeaseDuration:      skillWorkerLeaseDuration,
		SkillWorkerRenewInterval:      skillWorkerRenewInterval,
		ReliabilityPollInterval:       reliabilityPollInterval,
		NotificationSchedulerInterval: notificationSchedulerInterval,
		EmailWorkerPollInterval:       emailWorkerPollInterval,
		EmailWorkerLeaseDuration:      emailWorkerLeaseDuration,
		SMTPAddr:                      value("SMTP_ADDR", ""),
		SMTPHost:                      value("SMTP_HOST", ""),
		SMTPUsername:                  value("SMTP_USERNAME", ""),
		SMTPPassword:                  os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:                      value("SMTP_FROM", ""),
		SMTPUseTLS:                    smtpUseTLS,
		MCPStdioServersJSON:           value("MCP_STDIO_SERVERS_JSON", "[]"),
		WebOrigin:                     webOrigin,
		AuthTokenSecret:               authTokenSecret,
		AgentGatewayToken:             agentGatewayToken,
		AgentConfirmationSecret:       agentConfirmationSecret,
		AgentConfirmationTTL:          agentConfirmationTTL,
		AgentWorkerLeaseDuration:      agentWorkerLeaseDuration,
		AgentWorkerPollInterval:       agentWorkerPollInterval,
		AgentWorkerRetryDelay:         agentWorkerRetryDelay,
		AgentWorkerRetryMaxDelay:      agentWorkerRetryMaxDelay,
		AgentWorkerRetryJitterPercent: agentWorkerRetryJitterPercent,
		AgentWorkerMaxAttempts:        agentWorkerMaxAttempts,
		AgentWorkerConcurrency:        agentWorkerConcurrency,
		AgentDispatchQueueSize:        agentDispatchQueueSize,
		AgentPythonPoolEnabled:        agentPythonPoolEnabled,
		AgentPythonPoolWarmSize:       agentPythonPoolWarmSize,
		AgentWorkerTimeout:            agentWorkerTimeout,
		AgentRunTimeout:               agentRunTimeout,
		AgentControlPollInterval:      agentControlPollInterval,
		AgentChatModules:              agentChatModules,
		OperatorToken:                 operatorToken,
		OperatorMFARequired:           operatorMFARequired,
		AccessTokenTTL:                accessTokenTTL,
		RefreshTokenTTL:               refreshTokenTTL,
		PresenceTTL:                   presenceTTL, ChatRateLimit: chatRateLimit, ChatRateWindow: chatRateWindow,
		ContextRecentTokenBudget: contextRecentTokenBudget, ContextSummaryTokenBudget: contextSummaryTokenBudget,
		ModelProvider: modelProvider, ModelBaseURL: modelBaseURL, ModelAPIKey: value("MODEL_API_KEY", ""), ModelName: modelName, ModelFallbackNames: modelFallbackNames, ModelRoleNames: modelRoleNames,
		ModelMaxTokens: modelMaxTokens, ModelComposerMaxTokens: modelComposerMaxTokens, ModelDataCollection: modelDataCollection, ModelZDRRequired: modelZDRRequired, ModelReasoningEffort: modelReasoningEffort, ModelReasoningExclude: modelReasoningExclude,
		ModelConfigVersion: modelConfigVersion, ModelRequirePinned: modelRequirePinned, ModelProviderSort: modelProviderSort,
		ModelAllowProviderFallbacks: modelAllowProviderFallbacks, ModelRequireParameters: modelRequireParameters,
		ModelMaxPromptPrice: modelMaxPromptPrice, ModelMaxCompletionPrice: modelMaxCompletionPrice,
		ModelHTTPReferer: value("MODEL_HTTP_REFERER", ""), ModelAppTitle: value("MODEL_APP_TITLE", "AI Companion"),
		ModelTimeout: modelTimeout, ModelCircuitFailureThreshold: modelCircuitFailureThreshold, ModelCircuitCooldown: modelCircuitCooldown,
		ModelInputCostMicrosPerMillion: inputCost, ModelOutputCostMicrosPerMillion: outputCost,
		ModelTranslationName: translationName, ModelTranslationMaxTokens: translationMaxTokens, ModelTranslationMaxCostMicros: translationMaxCostMicros,
		ModelTranslationMaxPromptPrice: translationMaxPromptPrice, ModelTranslationMaxCompletionPrice: translationMaxOutputPrice,
	}, nil
}

func roleModels(role string, fallback []string) []string {
	primary := strings.TrimSpace(os.Getenv("MODEL_" + role + "_NAME"))
	if primary == "" {
		return append([]string(nil), fallback...)
	}
	return append([]string{primary}, splitNonEmpty(os.Getenv("MODEL_"+role+"_FALLBACK_NAMES"))...)
}

func dynamicOpenRouterModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return normalized == "openrouter/free" || normalized == "openrouter/auto" ||
		strings.HasPrefix(normalized, "~") || strings.HasSuffix(normalized, "-latest") ||
		strings.HasSuffix(normalized, ":free") || strings.HasSuffix(normalized, ":floor") ||
		strings.HasSuffix(normalized, ":nitro")
}

func concreteFreeOpenRouterModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	base := strings.TrimSuffix(normalized, ":free")
	return strings.HasSuffix(normalized, ":free") &&
		normalized != "openrouter/free" && normalized != "openrouter/auto" &&
		!strings.HasPrefix(normalized, "~") && !strings.HasSuffix(base, "-latest") &&
		strings.Contains(base, "/")
}

func value(key, fallback string) string {
	if current := strings.TrimSpace(os.Getenv(key)); current != "" {
		return current
	}
	return fallback
}

func splitNonEmpty(value string) []string {
	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
