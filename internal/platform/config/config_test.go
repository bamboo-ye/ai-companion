package config

import (
	"testing"
	"time"
)

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
	if cfg.MCPStdioServersJSON != "[]" {
		t.Fatalf("MCPStdioServersJSON = %q", cfg.MCPStdioServersJSON)
	}
	if cfg.OperatorToken != "development-operator-token" {
		t.Fatalf("OperatorToken = %q", cfg.OperatorToken)
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

func TestLoadRequiresOperatorTokenInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("AUTH_TOKEN_SECRET", "production-auth-token-secret")
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
	t.Setenv("AUTH_TOKEN_SECRET", "production-auth-token-secret")
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
