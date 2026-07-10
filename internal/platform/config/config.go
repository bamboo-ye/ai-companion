package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment                     string
	ServiceName                     string
	LogLevel                        string
	HTTPAddr                        string
	ShutdownTimeout                 time.Duration
	MySQLDSN                        string
	RedisAddr                       string
	RedisPassword                   string
	KafkaBrokers                    []string
	KafkaEnabled                    bool
	KafkaConsumerGroup              string
	KafkaReconcileInterval          time.Duration
	OutboxRelayPollInterval         time.Duration
	OutboxRelayLeaseDuration        time.Duration
	OutboxRelayBatchSize            int
	OutboxRelayMaxAttempts          int
	QdrantURL                       string
	QdrantAPIKey                    string
	QdrantCollection                string
	MinIOEndpoint                   string
	DocumentStorageDir              string
	DocumentMaxUploadBytes          int64
	DocumentWorkerPollInterval      time.Duration
	PythonExecutable                string
	PythonWorkerPath                string
	SpreadsheetExecutable           string
	SpreadsheetWorkerPath           string
	SkillStorageDir                 string
	LedgerStorageDir                string
	SkillWorkerEnabled              bool
	SkillWorkerPollInterval         time.Duration
	SkillWorkerLeaseDuration        time.Duration
	SkillWorkerRenewInterval        time.Duration
	ReliabilityPollInterval         time.Duration
	NotificationSchedulerInterval   time.Duration
	EmailWorkerPollInterval         time.Duration
	EmailWorkerLeaseDuration        time.Duration
	SMTPAddr                        string
	SMTPHost                        string
	SMTPUsername                    string
	SMTPPassword                    string
	SMTPFrom                        string
	SMTPUseTLS                      bool
	MCPStdioServersJSON             string
	WebOrigin                       string
	AuthTokenSecret                 string
	OperatorToken                   string
	OperatorMFARequired             bool
	AccessTokenTTL                  time.Duration
	RefreshTokenTTL                 time.Duration
	PresenceTTL                     time.Duration
	ChatRateLimit                   int
	ChatRateWindow                  time.Duration
	ContextRecentTokenBudget        int
	ContextSummaryTokenBudget       int
	ModelProvider                   string
	ModelBaseURL                    string
	ModelAPIKey                     string
	ModelName                       string
	ModelTimeout                    time.Duration
	ModelCircuitFailureThreshold    int
	ModelCircuitCooldown            time.Duration
	ModelInputCostMicrosPerMillion  int64
	ModelOutputCostMicrosPerMillion int64
}

func Load(serviceName string) (Config, error) {
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
	presenceTTL, err := time.ParseDuration(value("PRESENCE_TTL", "90s"))
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
	if modelProvider != "development" && modelProvider != "openai-compatible" {
		return Config{}, fmt.Errorf("MODEL_PROVIDER must be development or openai-compatible")
	}
	if modelProvider == "openai-compatible" && (value("MODEL_BASE_URL", "") == "" || value("MODEL_API_KEY", "") == "" || value("MODEL_NAME", "") == "") {
		return Config{}, fmt.Errorf("MODEL_BASE_URL, MODEL_API_KEY and MODEL_NAME are required for openai-compatible provider")
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
	if value("APP_ENV", "development") == "production" && authTokenSecret == "development-only-change-me" {
		return Config{}, fmt.Errorf("AUTH_TOKEN_SECRET must be set in production")
	}
	operatorToken := value("OPERATOR_TOKEN", "development-operator-token")
	if value("APP_ENV", "development") == "production" && operatorToken == "development-operator-token" {
		return Config{}, fmt.Errorf("OPERATOR_TOKEN must be set in production")
	}
	operatorMFARequired, err := strconv.ParseBool(value("OPERATOR_MFA_REQUIRED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("OPERATOR_MFA_REQUIRED must be true or false")
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
		Environment:                   value("APP_ENV", "development"),
		ServiceName:                   serviceName,
		LogLevel:                      value("LOG_LEVEL", "info"),
		HTTPAddr:                      value("API_HTTP_ADDR", ":8080"),
		ShutdownTimeout:               shutdownTimeout,
		MySQLDSN:                      os.Getenv("MYSQL_DSN"),
		RedisAddr:                     value("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:                 os.Getenv("REDIS_PASSWORD"),
		KafkaBrokers:                  brokers,
		KafkaEnabled:                  kafkaEnabled,
		KafkaConsumerGroup:            value("KAFKA_CONSUMER_GROUP", "ai-companion-background-v1"),
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
		SpreadsheetExecutable:         value("SPREADSHEET_EXECUTABLE", "node"),
		SpreadsheetWorkerPath:         value("SPREADSHEET_WORKER_PATH", "workers/spreadsheet/ledger_export.mjs"),
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
		WebOrigin:                     value("WEB_ORIGIN", "http://localhost:3000"),
		AuthTokenSecret:               authTokenSecret,
		OperatorToken:                 operatorToken,
		OperatorMFARequired:           operatorMFARequired,
		AccessTokenTTL:                accessTokenTTL,
		RefreshTokenTTL:               refreshTokenTTL,
		PresenceTTL:                   presenceTTL, ChatRateLimit: chatRateLimit, ChatRateWindow: chatRateWindow,
		ContextRecentTokenBudget: contextRecentTokenBudget, ContextSummaryTokenBudget: contextSummaryTokenBudget,
		ModelProvider: modelProvider, ModelBaseURL: value("MODEL_BASE_URL", ""), ModelAPIKey: value("MODEL_API_KEY", ""), ModelName: value("MODEL_NAME", ""), ModelTimeout: modelTimeout, ModelCircuitFailureThreshold: modelCircuitFailureThreshold, ModelCircuitCooldown: modelCircuitCooldown,
		ModelInputCostMicrosPerMillion: inputCost, ModelOutputCostMicrosPerMillion: outputCost,
	}, nil
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
