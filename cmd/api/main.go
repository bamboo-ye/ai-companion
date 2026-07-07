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

	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/httpserver"
	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/persistence/mysqlstore"
	"github.com/windcry1/ai-companion/internal/planner"
	"github.com/windcry1/ai-companion/internal/platform/config"
	"github.com/windcry1/ai-companion/internal/realtime"
	"github.com/windcry1/ai-companion/internal/reliability"
	"github.com/windcry1/ai-companion/internal/skill"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load("ai-companion-api")
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}

	identityStore := identity.Store(identity.NewMemoryStore())
	characterStore := character.Store(character.NewMemoryStore())
	conversationStore := conversation.Store(conversation.NewMemoryStore())
	memoryStore := memory.Store(memory.NewMemoryStore())
	documentStore := document.Store(document.NewMemoryStore())
	ledgerStore := ledger.Store(ledger.NewMemoryStore())
	plannerStore := planner.Store(planner.NewMemoryStore())
	skillStore := skill.Store(skill.NewMemoryStore())
	localBlobs, err := document.NewLocalBlobStore(cfg.DocumentStorageDir)
	if err != nil {
		logger.Error("initialize document storage", "error", err)
		os.Exit(1)
	}
	documentBlobs := document.BlobStore(localBlobs)
	var persistentStore *mysqlstore.Store
	if cfg.MySQLDSN != "" {
		connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		persistentStore, err = mysqlstore.Open(connectCtx, cfg.MySQLDSN)
		cancel()
		if err != nil {
			logger.Error("connect mysql", "error", err)
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
		logger.Info("using persistent mysql store")
	} else {
		logger.Warn("MYSQL_DSN is empty; using non-persistent in-memory store")
		if cfg.SkillWorkerEnabled {
			cfg.SkillWorkerEnabled = false
			logger.Warn("durable Skill queue disabled because API and Worker cannot share the in-memory store")
		}
	}

	provider := conversation.Provider(conversation.DevelopmentProvider{})
	if cfg.ModelProvider == "openai-compatible" {
		provider = conversation.NewOpenAICompatibleProvider(cfg.ModelBaseURL, cfg.ModelAPIKey, cfg.ModelName, cfg.ModelTimeout, cfg.ModelInputCostMicrosPerMillion, cfg.ModelOutputCostMicrosPerMillion)
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
	server.SetDocumentIndex(document.NewQdrantIndex(cfg.QdrantURL, cfg.QdrantCollection, cfg.QdrantAPIKey, 10*time.Second))
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
