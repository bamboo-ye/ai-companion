package config

import (
	"testing"
	"time"
)

func setProductionOpenRouter(t *testing.T) {
	t.Helper()
	t.Setenv("MODEL_PROVIDER", "openrouter")
	t.Setenv("MODEL_API_KEY", "production-openrouter-key")
	t.Setenv("MODEL_NAME", "deepseek/deepseek-v4-flash-0731")
	t.Setenv("MODEL_FALLBACK_NAMES", "openai/gpt-5-mini")
	t.Setenv("AGENT_CHAT_MODULES", "companion,life,work")
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	t.Setenv("KAFKA_BROKERS", "")

	cfg, err := Load("test-service")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ServiceName != "test-service" {
		t.Fatalf("ServiceName = %q", cfg.ServiceName)
	}
	if cfg.PostgresDSN != "" {
		t.Fatalf("PostgresDSN = %q", cfg.PostgresDSN)
	}
	if cfg.DatabaseDriver != "memory" {
		t.Fatalf("DatabaseDriver = %q", cfg.DatabaseDriver)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("ShutdownTimeout = %s", cfg.ShutdownTimeout)
	}
	if len(cfg.KafkaBrokers) != 1 || cfg.KafkaBrokers[0] != "127.0.0.1:9092" {
		t.Fatalf("KafkaBrokers = %#v", cfg.KafkaBrokers)
	}
	if !cfg.KafkaEnabled || cfg.OutboxRelayPollInterval != 500*time.Millisecond || cfg.OutboxRelayLeaseDuration != 30*time.Second || cfg.OutboxRelayBatchSize != 50 || cfg.OutboxRelayMaxAttempts != 8 {
		t.Fatalf("Kafka relay defaults = enabled:%v poll:%s lease:%s batch:%d attempts:%d", cfg.KafkaEnabled, cfg.OutboxRelayPollInterval, cfg.OutboxRelayLeaseDuration, cfg.OutboxRelayBatchSize, cfg.OutboxRelayMaxAttempts)
	}
	if cfg.KafkaConsumerGroup != "ai-companion-background-v1" || cfg.KafkaReconcileInterval != 30*time.Second {
		t.Fatalf("Kafka consumer defaults = group:%q reconcile:%s", cfg.KafkaConsumerGroup, cfg.KafkaReconcileInterval)
	}
	if cfg.DocumentMaxUploadBytes != 20<<20 {
		t.Fatalf("DocumentMaxUploadBytes = %d", cfg.DocumentMaxUploadBytes)
	}
	if cfg.DocumentWorkerPollInterval != time.Second {
		t.Fatalf("DocumentWorkerPollInterval = %s", cfg.DocumentWorkerPollInterval)
	}
	if cfg.ContextRecentTokenBudget != 6000 || cfg.ContextSummaryTokenBudget != 1200 {
		t.Fatalf("context budgets = %d/%d", cfg.ContextRecentTokenBudget, cfg.ContextSummaryTokenBudget)
	}
	if cfg.SkillStorageDir != ".data/skill-files" {
		t.Fatalf("SkillStorageDir = %q", cfg.SkillStorageDir)
	}
	if cfg.LedgerStorageDir != ".data/ledger-exports" {
		t.Fatalf("LedgerStorageDir = %q", cfg.LedgerStorageDir)
	}
	if !cfg.SkillWorkerEnabled || cfg.SkillWorkerPollInterval != time.Second || cfg.SkillWorkerLeaseDuration != 2*time.Minute || cfg.SkillWorkerRenewInterval != 30*time.Second {
		t.Fatalf("skill worker defaults = enabled:%v poll:%s lease:%s renew:%s", cfg.SkillWorkerEnabled, cfg.SkillWorkerPollInterval, cfg.SkillWorkerLeaseDuration, cfg.SkillWorkerRenewInterval)
	}
	if cfg.ReliabilityPollInterval != 10*time.Second {
		t.Fatalf("ReliabilityPollInterval = %s", cfg.ReliabilityPollInterval)
	}
	if cfg.NotificationSchedulerInterval != time.Second {
		t.Fatalf("NotificationSchedulerInterval = %s", cfg.NotificationSchedulerInterval)
	}
	if cfg.ModelCircuitFailureThreshold != 3 || cfg.ModelCircuitCooldown != 30*time.Second {
		t.Fatalf("model circuit defaults = %d/%s", cfg.ModelCircuitFailureThreshold, cfg.ModelCircuitCooldown)
	}
	if cfg.ModelConfigVersion != "2026-08-structured-composer-v3" {
		t.Fatalf("ModelConfigVersion = %q", cfg.ModelConfigVersion)
	}
	if cfg.MCPStdioServersJSON != "[]" {
		t.Fatalf("MCPStdioServersJSON = %q", cfg.MCPStdioServersJSON)
	}
	if cfg.OperatorToken != "development-operator-token" {
		t.Fatalf("OperatorToken = %q", cfg.OperatorToken)
	}
	if cfg.AgentGatewayToken != "development-agent-gateway-token" ||
		cfg.AgentConfirmationSecret != "development-agent-confirmation-secret" ||
		cfg.AgentConfirmationTTL != 15*time.Minute {
		t.Fatalf("agent gateway defaults = %q/%q/%s", cfg.AgentGatewayToken, cfg.AgentConfirmationSecret, cfg.AgentConfirmationTTL)
	}
	if cfg.AgentKafkaConsumerGroup != "ai-companion-agent-v1" ||
		cfg.AgentMetricsAddr != ":9467" ||
		cfg.AgentWorkerLeaseDuration != 10*time.Minute ||
		cfg.AgentWorkerPollInterval != 30*time.Second ||
		cfg.AgentWorkerRetryDelay != 30*time.Second ||
		cfg.AgentWorkerRetryMaxDelay != 2*time.Minute ||
		cfg.AgentWorkerRetryJitterPercent != 20 ||
		cfg.AgentWorkerMaxAttempts != 3 ||
		cfg.AgentWorkerConcurrency != 4 ||
		cfg.AgentDispatchQueueSize != 32 ||
		!cfg.AgentPythonPoolEnabled ||
		cfg.AgentPythonPoolWarmSize != 1 ||
		cfg.AgentWorkerTimeout != 7*time.Minute ||
		cfg.AgentRunTimeout != 15*time.Minute ||
		cfg.AgentControlPollInterval != 500*time.Millisecond {
		t.Fatalf(
			"agent worker defaults = group:%q metrics:%q lease:%s poll:%s retry:%s max_retry:%s jitter:%d attempts:%d concurrency:%d queue:%d python_pool:%v warm:%d timeout:%s run:%s control:%s",
			cfg.AgentKafkaConsumerGroup,
			cfg.AgentMetricsAddr,
			cfg.AgentWorkerLeaseDuration,
			cfg.AgentWorkerPollInterval,
			cfg.AgentWorkerRetryDelay,
			cfg.AgentWorkerRetryMaxDelay,
			cfg.AgentWorkerRetryJitterPercent,
			cfg.AgentWorkerMaxAttempts,
			cfg.AgentWorkerConcurrency,
			cfg.AgentDispatchQueueSize,
			cfg.AgentPythonPoolEnabled,
			cfg.AgentPythonPoolWarmSize,
			cfg.AgentWorkerTimeout,
			cfg.AgentRunTimeout,
			cfg.AgentControlPollInterval,
		)
	}
	if len(cfg.AgentChatModules) != 0 {
		t.Fatalf("AgentChatModules = %#v", cfg.AgentChatModules)
	}
}

func TestLoadPostgresDSN(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://ai_companion:secret@127.0.0.1:5432/ai_companion?sslmode=disable")

	cfg, err := Load("test-service")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PostgresDSN != "postgres://ai_companion:secret@127.0.0.1:5432/ai_companion?sslmode=disable" {
		t.Fatalf("PostgresDSN = %q", cfg.PostgresDSN)
	}
	if cfg.DatabaseDriver != "postgres" {
		t.Fatalf("DatabaseDriver = %q", cfg.DatabaseDriver)
	}
}

func TestLoadDatabaseDriverValidation(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "mysql")
	t.Setenv("MYSQL_DSN", "")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected MYSQL_DSN error")
	}
	t.Setenv("DATABASE_DRIVER", "postgres")
	t.Setenv("POSTGRES_DSN", "")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected POSTGRES_DSN error")
	}
	t.Setenv("DATABASE_DRIVER", "sqlite")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected unsupported driver error")
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	t.Setenv("SHUTDOWN_TIMEOUT", "later")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an error")
	}
}

func TestLoadRejectsInvalidDocumentLimit(t *testing.T) {
	t.Setenv("DOCUMENT_MAX_UPLOAD_BYTES", "0")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an error")
	}
}

func TestLoadRejectsInvalidDocumentWorkerInterval(t *testing.T) {
	t.Setenv("DOCUMENT_WORKER_POLL_INTERVAL", "0s")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an error")
	}
}

func TestLoadRejectsInvalidSkillWorkerLease(t *testing.T) {
	t.Setenv("SKILL_WORKER_LEASE_DURATION", "10s")
	t.Setenv("SKILL_WORKER_RENEW_INTERVAL", "10s")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an error")
	}
}

func TestLoadRejectsInvalidReliabilityInterval(t *testing.T) {
	t.Setenv("RELIABILITY_POLL_INTERVAL", "0s")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an error")
	}
}

func TestLoadRejectsInvalidNotificationSchedulerInterval(t *testing.T) {
	t.Setenv("NOTIFICATION_SCHEDULER_INTERVAL", "0s")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an error")
	}
}

func TestLoadRejectsInvalidAgentRetryPolicy(t *testing.T) {
	t.Setenv("AGENT_WORKER_RETRY_DELAY", "30s")
	t.Setenv("AGENT_WORKER_RETRY_MAX_DELAY", "20s")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() accepted retry maximum below the base delay")
	}
	t.Setenv("AGENT_WORKER_RETRY_MAX_DELAY", "2m")
	t.Setenv("AGENT_WORKER_RETRY_JITTER_PERCENT", "101")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() accepted retry jitter above 100 percent")
	}
}

func TestLoadRejectsInvalidContextBudgets(t *testing.T) {
	t.Setenv("CONTEXT_RECENT_TOKEN_BUDGET", "127")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an error")
	}
	t.Setenv("CONTEXT_RECENT_TOKEN_BUDGET", "6000")
	t.Setenv("CONTEXT_SUMMARY_TOKEN_BUDGET", "63")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an error")
	}
}

func TestLoadRejectsInvalidModelCircuit(t *testing.T) {
	t.Setenv("MODEL_CIRCUIT_FAILURE_THRESHOLD", "0")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an error")
	}
	t.Setenv("MODEL_CIRCUIT_FAILURE_THRESHOLD", "3")
	t.Setenv("MODEL_CIRCUIT_COOLDOWN", "0s")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an error")
	}
}

func TestLoadOpenRouterRouting(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "openrouter")
	t.Setenv("MODEL_API_KEY", "secret")
	t.Setenv("MODEL_NAME", "deepseek/deepseek-v4-flash-0731")
	t.Setenv("MODEL_FALLBACK_NAMES", "openai/gpt-5-mini")
	t.Setenv("MODEL_MAX_TOKENS", "768")
	t.Setenv("MODEL_DATA_COLLECTION", "deny")
	t.Setenv("MODEL_ZDR_REQUIRED", "true")
	t.Setenv("MODEL_REASONING_EFFORT", "low")
	t.Setenv("MODEL_REASONING_EXCLUDE", "true")
	t.Setenv("MODEL_CONFIG_VERSION", "deepseek-v4-flash-0731-v1")
	t.Setenv("MODEL_REQUIRE_PINNED", "true")
	t.Setenv("MODEL_PROVIDER_SORT", "price")
	t.Setenv("MODEL_ALLOW_PROVIDER_FALLBACKS", "true")
	t.Setenv("MODEL_REQUIRE_PARAMETERS", "true")
	t.Setenv("MODEL_MAX_PROMPT_PRICE", "0.3")
	t.Setenv("MODEL_MAX_COMPLETION_PRICE", "2.5")

	cfg, err := Load("test-service")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ModelBaseURL != "https://openrouter.ai/api/v1" || cfg.ModelMaxTokens != 768 || !cfg.ModelZDRRequired || cfg.ModelReasoningEffort != "low" || !cfg.ModelReasoningExclude {
		t.Fatalf("unexpected OpenRouter config: %#v", cfg)
	}
	if len(cfg.ModelFallbackNames) != 1 || cfg.ModelFallbackNames[0] != "openai/gpt-5-mini" {
		t.Fatalf("fallbacks = %#v", cfg.ModelFallbackNames)
	}
	if cfg.ModelConfigVersion != "deepseek-v4-flash-0731-v1" || !cfg.ModelRequirePinned ||
		cfg.ModelProviderSort != "price" || !cfg.ModelAllowProviderFallbacks ||
		!cfg.ModelRequireParameters || cfg.ModelMaxPromptPrice != 0.3 ||
		cfg.ModelMaxCompletionPrice != 2.5 {
		t.Fatalf("unexpected OpenRouter governance: %#v", cfg)
	}
	if got := cfg.ModelRoleNames["router"]; len(got) != 2 || got[0] != "deepseek/deepseek-v4-flash-0731" || got[1] != "openai/gpt-5-nano" {
		t.Fatalf("router models = %#v", got)
	}
	if got := cfg.ModelRoleNames["composer"]; len(got) != 2 || got[0] != "deepseek/deepseek-v4-flash-0731" || got[1] != "openai/gpt-5-mini" {
		t.Fatalf("composer models = %#v", got)
	}
	if got := cfg.ModelRoleNames["repairer"]; len(got) != 1 || got[0] != "deepseek/deepseek-v4-flash-0731" {
		t.Fatalf("repairer models = %#v", got)
	}
	if got := cfg.ModelRoleNames["companion_responder"]; len(got) != 3 ||
		got[0] != "openai/gpt-oss-20b:free" ||
		got[1] != "google/gemma-4-31b-it:free" ||
		got[2] != "inclusionai/ling-3.0-flash:free" {
		t.Fatalf("companion responder models = %#v", got)
	}
	if cfg.ModelComposerMaxTokens != 12288 || cfg.ModelTranslationName != "openai/gpt-5-mini" ||
		cfg.ModelTranslationMaxTokens != 8000 || cfg.ModelTranslationMaxCostMicros != 60000 {
		t.Fatalf("unexpected role/translation budgets: %#v", cfg)
	}
}

func TestLoadRejectsNonOpenRouterExternalProvider(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "openai-compatible")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected non-OpenRouter provider error")
	}
}

func TestLoadRejectsNonCanonicalOpenRouterEndpoint(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "openrouter")
	t.Setenv("MODEL_BASE_URL", "https://models.example.com/v1")
	t.Setenv("MODEL_API_KEY", "secret")
	t.Setenv("MODEL_NAME", "openai/gpt-5-mini")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected non-canonical OpenRouter endpoint error")
	}
}

func TestLoadProductionRequiresEveryChatModuleOnGraph(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	setProductionOpenRouter(t)
	t.Setenv("AGENT_CHAT_MODULES", "life,work")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected full Graph cut-over error")
	}
}

func TestLoadProductionRejectsRelaxedModelGovernance(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	setProductionOpenRouter(t)

	t.Setenv("MODEL_DATA_COLLECTION", "allow")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected production data-policy error")
	}
	t.Setenv("MODEL_DATA_COLLECTION", "deny")
	t.Setenv("MODEL_REQUIRE_PINNED", "false")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected production model-pin error")
	}
	t.Setenv("MODEL_REQUIRE_PINNED", "true")
	t.Setenv("MODEL_REQUIRE_PARAMETERS", "false")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected production parameter-policy error")
	}
	t.Setenv("MODEL_REQUIRE_PARAMETERS", "true")
	t.Setenv("MODEL_PROVIDER_SORT", "latency")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected production provider-sort error")
	}
	t.Setenv("MODEL_PROVIDER_SORT", "price")
	t.Setenv("MODEL_MAX_COMPLETION_PRICE", "2.51")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected production price-ceiling error")
	}
}

func TestLoadRejectsDynamicOpenRouterModelWhenPinned(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "openrouter")
	t.Setenv("MODEL_API_KEY", "secret")
	t.Setenv("MODEL_NAME", "openrouter/free")
	t.Setenv("MODEL_REQUIRE_PINNED", "true")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected dynamic model error")
	}
}

func TestLoadRejectsDynamicRoleAndTranslationModels(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "openrouter")
	t.Setenv("MODEL_API_KEY", "secret")
	t.Setenv("MODEL_NAME", "openai/gpt-5-mini")
	t.Setenv("MODEL_COMPOSER_NAME", "openrouter/free")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected dynamic composer model error")
	}
	t.Setenv("MODEL_COMPOSER_NAME", "openai/gpt-5-mini")
	t.Setenv("MODEL_TRANSLATION_NAME", "openai/gpt-5-mini-latest")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected dynamic translation model error")
	}
}

func TestLoadAcceptsConcreteFreeCompanionResponderModels(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "openrouter")
	t.Setenv("MODEL_API_KEY", "secret")
	t.Setenv("MODEL_NAME", "deepseek/deepseek-v4-flash-0731")
	t.Setenv("MODEL_COMPANION_RESPONDER_NAME", "openai/gpt-oss-20b:free")
	t.Setenv("MODEL_COMPANION_RESPONDER_FALLBACK_NAMES", "google/gemma-4-31b-it:free")
	t.Setenv("MODEL_REQUIRE_PINNED", "true")

	cfg, err := Load("test-service")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.ModelRoleNames["companion_responder"]; len(got) != 2 ||
		got[0] != "openai/gpt-oss-20b:free" || got[1] != "google/gemma-4-31b-it:free" {
		t.Fatalf("companion responder models = %#v", got)
	}
}

func TestLoadRejectsPaidOrDynamicCompanionResponder(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "openrouter")
	t.Setenv("MODEL_API_KEY", "secret")
	t.Setenv("MODEL_NAME", "deepseek/deepseek-v4-flash-0731")
	t.Setenv("MODEL_REQUIRE_PINNED", "true")
	t.Setenv("MODEL_COMPANION_RESPONDER_NAME", "openai/gpt-5-mini")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected paid companion responder error")
	}
	t.Setenv("MODEL_COMPANION_RESPONDER_NAME", "openrouter/free")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected dynamic companion responder error")
	}
}

func TestLoadRejectsExplicitlyEmptyTranslationModel(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "openrouter")
	t.Setenv("MODEL_API_KEY", "secret")
	t.Setenv("MODEL_NAME", "openai/gpt-5-mini")
	t.Setenv("MODEL_TRANSLATION_NAME", "")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected empty translation model error")
	}
}

func TestLoadRejectsInvalidTranslationBudget(t *testing.T) {
	t.Setenv("MODEL_TRANSLATION_MAX_TOKENS", "255")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected translation max-token error")
	}
	t.Setenv("MODEL_TRANSLATION_MAX_TOKENS", "8000")
	t.Setenv("MODEL_TRANSLATION_MAX_PROMPT_PRICE", "0.31")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected translation prompt-price error")
	}
	t.Setenv("MODEL_TRANSLATION_MAX_PROMPT_PRICE", "0.3")
	t.Setenv("MODEL_TRANSLATION_MAX_COST_MICROS", "60001")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected translation cost-budget error")
	}
}

func TestLoadRejectsNonFiniteModelPrices(t *testing.T) {
	t.Setenv("MODEL_MAX_PROMPT_PRICE", "NaN")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected non-finite model prompt-price error")
	}
	t.Setenv("MODEL_MAX_PROMPT_PRICE", "0.3")
	t.Setenv("MODEL_TRANSLATION_MAX_COMPLETION_PRICE", "+Inf")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected non-finite translation completion-price error")
	}
}

func TestLoadRejectsInvalidModelRoutingPolicy(t *testing.T) {
	t.Setenv("MODEL_MAX_TOKENS", "63")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected max-token error")
	}
	t.Setenv("MODEL_MAX_TOKENS", "1024")
	t.Setenv("MODEL_DATA_COLLECTION", "sometimes")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected data-collection error")
	}
	t.Setenv("MODEL_DATA_COLLECTION", "deny")
	t.Setenv("MODEL_FALLBACK_NAMES", "one,two,three")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected fallback-count error")
	}
	t.Setenv("MODEL_FALLBACK_NAMES", "")
	t.Setenv("MODEL_REASONING_EFFORT", "lots")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected reasoning-effort error")
	}
}

func TestLoadRequiresOperatorTokenInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	setProductionOpenRouter(t)
	t.Setenv("AUTH_TOKEN_SECRET", "production-auth-token-secret")
	t.Setenv("AGENT_GATEWAY_TOKEN", "production-agent-gateway-token")
	t.Setenv("AGENT_CONFIRMATION_SECRET", "production-agent-confirmation-secret")
	t.Setenv("WEB_ORIGIN", "https://app.example.com")
	t.Setenv("OPERATOR_MFA_REQUIRED", "true")
	t.Setenv("OPERATOR_TOKEN", "")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an error")
	}
	t.Setenv("OPERATOR_TOKEN", "production-operator-token")
	if _, err := Load("test-service"); err != nil {
		t.Fatalf("Load() unexpected error = %v", err)
	}
}

func TestLoadRequiresOperatorMFAAndHTTPSOriginInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	setProductionOpenRouter(t)
	t.Setenv("AUTH_TOKEN_SECRET", "production-auth-token-secret")
	t.Setenv("AGENT_GATEWAY_TOKEN", "production-agent-gateway-token")
	t.Setenv("AGENT_CONFIRMATION_SECRET", "production-agent-confirmation-secret")
	t.Setenv("OPERATOR_TOKEN", "production-operator-token")
	t.Setenv("WEB_ORIGIN", "https://app.example.com")
	t.Setenv("OPERATOR_MFA_REQUIRED", "false")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected MFA-required production error")
	}
	t.Setenv("OPERATOR_MFA_REQUIRED", "true")
	t.Setenv("WEB_ORIGIN", "http://app.example.com")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected HTTPS-origin production error")
	}
	t.Setenv("WEB_ORIGIN", "https://app.example.com")
	if _, err := Load("test-service"); err != nil {
		t.Fatalf("Load() unexpected error = %v", err)
	}
}

func TestLoadRequiresAgentGatewaySecretsInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	setProductionOpenRouter(t)
	t.Setenv("AUTH_TOKEN_SECRET", "production-auth-token-secret")
	t.Setenv("OPERATOR_TOKEN", "production-operator-token")
	t.Setenv("OPERATOR_MFA_REQUIRED", "true")
	t.Setenv("WEB_ORIGIN", "https://app.example.com")
	t.Setenv("AGENT_GATEWAY_TOKEN", "")
	t.Setenv("AGENT_CONFIRMATION_SECRET", "")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an Agent gateway token error")
	}
	t.Setenv("AGENT_GATEWAY_TOKEN", "production-agent-gateway-token")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an Agent confirmation secret error")
	}
	t.Setenv("AGENT_CONFIRMATION_SECRET", "production-agent-confirmation-secret")
	if _, err := Load("test-service"); err != nil {
		t.Fatalf("Load() unexpected error = %v", err)
	}
}

func TestLoadRejectsInvalidAgentConfirmationTTL(t *testing.T) {
	t.Setenv("AGENT_CONFIRMATION_TTL", "0s")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an Agent confirmation TTL error")
	}
}

func TestLoadRejectsAgentWorkerTimeoutAtOrBeyondLease(t *testing.T) {
	t.Setenv("AGENT_WORKER_LEASE_DURATION", "1m")
	t.Setenv("AGENT_WORKER_TIMEOUT", "1m")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an Agent worker timeout error")
	}
}

func TestLoadRejectsInvalidAgentWorkerAttemptBudget(t *testing.T) {
	t.Setenv("AGENT_WORKER_MAX_ATTEMPTS", "0")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an Agent worker attempt budget error")
	}
}

func TestLoadRejectsAgentDispatchQueueBelowConcurrency(t *testing.T) {
	t.Setenv("AGENT_WORKER_CONCURRENCY", "8")
	t.Setenv("AGENT_DISPATCH_QUEUE_SIZE", "4")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an Agent dispatch queue error")
	}
}

func TestLoadRejectsInvalidAgentPythonPoolFlag(t *testing.T) {
	t.Setenv("AGENT_PYTHON_POOL_ENABLED", "sometimes")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an Agent Python pool flag error")
	}
}

func TestLoadRejectsAgentPythonPoolWarmSizeAboveConcurrency(t *testing.T) {
	t.Setenv("AGENT_WORKER_CONCURRENCY", "4")
	t.Setenv("AGENT_PYTHON_POOL_WARM_SIZE", "5")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an Agent Python pool warm size error")
	}
}

func TestLoadRejectsInvalidAgentRunControlDurations(t *testing.T) {
	t.Setenv("AGENT_RUN_TIMEOUT", "0s")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an Agent run timeout error")
	}
	t.Setenv("AGENT_RUN_TIMEOUT", "15m")
	t.Setenv("AGENT_CONTROL_POLL_INTERVAL", "0s")
	if _, err := Load("test-service"); err == nil {
		t.Fatal("Load() expected an Agent control poll interval error")
	}
}

func TestLoadAgentChatModules(t *testing.T) {
	t.Setenv("AGENT_CHAT_MODULES", "life, work")
	cfg, err := Load("test-service")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.AgentChatModules) != 2 || cfg.AgentChatModules[0] != "life" ||
		cfg.AgentChatModules[1] != "work" {
		t.Fatalf("AgentChatModules = %#v", cfg.AgentChatModules)
	}
	t.Setenv("AGENT_CHAT_MODULES", "life,finance")
	if _, err = Load("test-service"); err == nil {
		t.Fatal("Load() expected invalid Agent module error")
	}
}
