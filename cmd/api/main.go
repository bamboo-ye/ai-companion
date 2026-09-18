package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/billing"
	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/controlplane"
	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/email"
	"github.com/windcry1/ai-companion/internal/httpserver"
	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/incident"
	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/opslog"
	"github.com/windcry1/ai-companion/internal/performance"
	"github.com/windcry1/ai-companion/internal/persistence"
	"github.com/windcry1/ai-companion/internal/planner"
	"github.com/windcry1/ai-companion/internal/platform/config"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"github.com/windcry1/ai-companion/internal/realtime"
	"github.com/windcry1/ai-companion/internal/reliability"
	"github.com/windcry1/ai-companion/internal/safety"
	"github.com/windcry1/ai-companion/internal/skill"
	"github.com/windcry1/ai-companion/internal/team"
)

func main() {
	baseLogHandler := slog.NewJSONHandler(os.Stdout, nil)
	logger := slog.New(baseLogHandler)
	cfg, err := config.Load("ai-companion-api")
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	logger = opslog.NewLogger(baseLogHandler, nil, cfg.ServiceName, cfg.Environment)

	identityStore := identity.Store(identity.NewMemoryStore())
	characterStore := character.Store(character.NewMemoryStore())
	conversationStore := conversation.Store(conversation.NewMemoryStore())
	memoryStore := memory.Store(memory.NewMemoryStore())
	documentStore := document.Store(document.NewMemoryStore())
	ledgerStore := ledger.Store(ledger.NewMemoryStore())
	plannerStore := planner.Store(planner.NewMemoryStore())
	skillStore := skill.Store(skill.NewMemoryStore())
	teamStore := team.Store(team.NewMemoryStore())
	emailStore := email.Store(email.NewMemoryStore())
	billingStore := billing.Store(billing.NewMemoryStore())
	safetyStore := safety.Store(safety.NewMemoryStore())
	localBlobs, err := document.NewLocalBlobStore(cfg.DocumentStorageDir)
	if err != nil {
		logger.Error("initialize document storage", "error", err)
		os.Exit(1)
	}
	documentBlobs := document.BlobStore(localBlobs)
	var persistentStore persistence.ApplicationStore
	var systemLogStore opslog.Store
	var incidentStore incident.Store
	var performanceStore performance.Store
	if cfg.DatabaseDriver != "memory" {
		connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		persistentStore, err = persistence.Open(connectCtx, cfg.DatabaseDriver, cfg.MySQLDSN, cfg.PostgresDSN)
		cancel()
		if err != nil {
			logger.Error("connect persistent database", "driver", cfg.DatabaseDriver, "error", err)
			os.Exit(1)
		}
		defer persistentStore.Close()
		identityStore = persistentStore
		characterStore = persistentStore
		conversationStore = persistentStore
		memoryStore = persistentStore
		documentStore = persistentStore
		ledgerStore = persistentStore
		plannerStore = persistentStore
		skillStore = persistentStore
		teamStore = persistentStore
		emailStore = persistentStore
		billingStore = persistentStore
		safetyStore = persistentStore
		logger.Info("using persistent store", "driver", cfg.DatabaseDriver)
	} else {
		logger.Warn("DATABASE_DRIVER=memory; using non-persistent in-memory store")
		if cfg.SkillWorkerEnabled {
			cfg.SkillWorkerEnabled = false
			logger.Warn("durable Skill queue disabled because API and Worker cannot share the in-memory store")
		}
	}
	if persistentStore != nil {
		if durableLogs, ok := any(persistentStore).(opslog.Store); ok {
			systemLogStore = durableLogs
			logger = opslog.NewLogger(baseLogHandler, durableLogs, cfg.ServiceName, cfg.Environment)
			logger.Info("system log capture enabled", "event", "opslog.capture.enabled")
		}
		if durableIncidents, ok := any(persistentStore).(incident.Store); ok {
			incidentStore = durableIncidents
		}
	}
	if systemLogStore == nil {
		systemLogStore = opslog.NewMemoryStore()
		logger = opslog.NewLogger(baseLogHandler, systemLogStore, cfg.ServiceName, cfg.Environment)
		logger.Info("system log capture enabled", "event", "opslog.capture.enabled", "storage", "memory")
	}
	systemLogQueryStore := systemLogStore
	if cfg.LokiEnabled {
		lokiStore, lokiErr := opslog.NewLokiStore(opslog.LokiOptions{
			BaseURL: cfg.LokiBaseURL, Environment: cfg.Environment, Job: "ai-companion",
			TenantID: cfg.LokiTenantID, BearerToken: cfg.LokiBearerToken, Timeout: cfg.LokiQueryTimeout,
		})
		if lokiErr != nil {
			logger.Error("initialize Loki query client", "event", "loki.client.failed", "error", lokiErr)
			os.Exit(1)
		}
		systemLogQueryStore = opslog.NewQueryFallbackStore(systemLogStore, lokiStore)
		logger.Info("Loki log query enabled", "event", "loki.query.enabled", "base_url", cfg.LokiBaseURL)
	}

	provider := conversation.Provider(conversation.DevelopmentProvider{})
	if cfg.ModelProvider == "openrouter" {
		models := append([]string{cfg.ModelName}, cfg.ModelFallbackNames...)
		provider = conversation.NewOpenRouterProvider(conversation.OpenRouterOptions{
			ContextWindow: cfg.ModelContextWindow,
			BaseURL:       cfg.ModelBaseURL, APIKey: cfg.ModelAPIKey, Models: models, Timeout: cfg.ModelTimeout, MaxTokens: cfg.ModelMaxTokens,
			DataCollection: cfg.ModelDataCollection, ZDRRequired: cfg.ModelZDRRequired, ReasoningEffort: cfg.ModelReasoningEffort, ReasoningExclude: cfg.ModelReasoningExclude,
			ProviderSort: cfg.ModelProviderSort, AllowProviderFallbacks: cfg.ModelAllowProviderFallbacks, RequireParameters: cfg.ModelRequireParameters,
			MaxPromptPrice: cfg.ModelMaxPromptPrice, MaxCompletionPrice: cfg.ModelMaxCompletionPrice,
			HTTPReferer: cfg.ModelHTTPReferer, AppTitle: cfg.ModelAppTitle, InputCost: cfg.ModelInputCostMicrosPerMillion, OutputCost: cfg.ModelOutputCostMicrosPerMillion,
		})
	}
	provider = conversation.NewCircuitBreakerProvider(provider, reliability.NewCircuitBreaker(cfg.ModelCircuitFailureThreshold, cfg.ModelCircuitCooldown))
	realtimeGateway := realtime.Gateway(realtime.NewMemoryGateway())
	if cfg.RedisAddr != "" {
		connectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		redisGateway, redisErr := realtime.OpenRedis(connectCtx, cfg.RedisAddr, cfg.RedisPassword)
		cancel()
		if redisErr != nil {
			logger.Error("connect redis", "error", redisErr)
			os.Exit(1)
		}
		defer redisGateway.Close()
		realtimeGateway = redisGateway
		logger.Info("using redis realtime gateway")
	}
	skillFiles, err := skill.NewLocalFileStore(cfg.SkillStorageDir)
	if err != nil {
		logger.Error("initialize skill file storage", "error", err)
		os.Exit(1)
	}
	server := httpserver.NewWithM4Dependencies(cfg, logger, identityStore, characterStore, conversationStore, memoryStore, documentStore, documentBlobs, ledgerStore, plannerStore, skillStore, skillFiles, provider, realtimeGateway)
	server.SetTeamStore(teamStore)
	server.SetEmailStore(emailStore)
	server.SetBillingStore(billingStore)
	controlPlaneStore := controlplane.Store(controlplane.NewMemoryStore())
	if persistentStore != nil {
		if configured, ok := any(persistentStore).(controlplane.Store); ok {
			controlPlaneStore = configured
		}
	}
	if err = server.SetControlPlaneStore(controlPlaneStore); err != nil {
		logger.Error("initialize configuration control plane", "error", err)
		os.Exit(1)
	}
	server.SetSafetyStore(safetyStore)
	server.SetSystemLogStore(systemLogQueryStore)
	if persistentStore != nil {
		server.SetOperationsStore(persistentStore)
		server.SetIncidentStore(incidentStore)
		if durablePerformance, ok := any(persistentStore).(performance.Store); ok {
			performanceStore = durablePerformance
			server.SetPerformanceStore(durablePerformance)
		}
		server.SetOperatorAuthStore(persistentStore)
		server.SetIdentityAdminStore(persistentStore)
		if agentStore, ok := any(persistentStore).(agent.Store); ok {
			server.SetAgentStore(agentStore)
		}
	}
	server.SetDocumentIndex(document.NewKnowledgeIndex(cfg.QdrantURL, cfg.QdrantCollection, cfg.QdrantAPIKey, 10*time.Second, cfg.Knowledge))
	server.SetLedgerExporter(ledger.ArtifactToolExporter{Executable: cfg.SpreadsheetExecutable, ScriptPath: cfg.SpreadsheetWorkerPath, Timeout: 30 * time.Second})
	ledgerFiles, ledgerFileErr := ledger.NewLocalExportFileStore(cfg.LedgerStorageDir)
	if ledgerFileErr != nil {
		logger.Error("initialize ledger export storage", "error", ledgerFileErr)
		os.Exit(1)
	}
	server.SetLedgerExportFileStore(ledgerFiles)
	recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err = server.Recover(recoveryCtx); err != nil {
		recoveryCancel()
		logger.Error("recover interrupted jobs", "error", err)
		os.Exit(1)
	}
	recoveryCancel()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	runtimeInstanceID, runtimeIDErr := id.New()
	if runtimeIDErr != nil {
		logger.Error("create API runtime instance id", "error", runtimeIDErr)
		os.Exit(1)
	}
	runtimeStartedAt := time.Now().UTC()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			syncCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			syncErr := server.SyncRuntimeConfiguration(syncCtx, runtimeInstanceID, cfg.ServiceName, runtimeStartedAt)
			cancel()
			if syncErr != nil {
				logger.Warn("synchronize runtime configuration", "instance_id", runtimeInstanceID, "error", syncErr)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	if persistentStore != nil {
		go func() {
			ticker := time.NewTicker(cfg.ReliabilityPollInterval)
			defer ticker.Stop()
			lastLevel := ""
			for {
				sampleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				sample, sampleErr := persistentStore.ReliabilitySample(sampleCtx, time.Now().UTC())
				cancel()
				if sampleErr != nil {
					logger.Warn("sample reliability signals", "error", sampleErr)
				} else {
					snapshot := server.ObserveReliability(sample, time.Now().UTC())
					if snapshot.Level != lastLevel {
						logger.Info("degradation level", "level", snapshot.Level, "reason", snapshot.Reason, "queue_lag", snapshot.QueueLag)
						lastLevel = snapshot.Level
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	if incidentStore != nil {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				evaluationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				report, evaluationErr := server.EvaluateAlerts(evaluationCtx, time.Now().UTC())
				cancel()
				if evaluationErr != nil {
					logger.Warn("evaluate alert rules", "event", "alerts.evaluation.failed", "error", evaluationErr)
				} else if report.Opened > 0 || report.Resolved > 0 {
					logger.Info("alert incident transitions", "event", "alerts.evaluation.transition", "opened", report.Opened, "resolved", report.Resolved, "triggered", report.Triggered)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	if performanceStore != nil {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				evaluationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				report, evaluationErr := server.EvaluatePerformanceBudgets(evaluationCtx, time.Now().UTC())
				cancel()
				if evaluationErr != nil && !errors.Is(evaluationErr, performance.ErrConflict) {
					logger.Warn("evaluate performance budgets", "event", "performance.budgets.evaluation.failed", "error", evaluationErr)
				} else if report.Opened > 0 || report.Escalated > 0 || report.Resolved > 0 {
					logger.Info("performance budget transitions", "event", "performance.budgets.evaluation.transition", "projected_exceeded", report.ProjectedExceeded, "opened", report.Opened, "escalated", report.Escalated, "resolved", report.Resolved)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api listening", "address", cfg.HTTPAddr, "environment", cfg.Environment)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown api", "error", err)
			os.Exit(1)
		}
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("api failed", "error", err)
			os.Exit(1)
		}
	}
}
