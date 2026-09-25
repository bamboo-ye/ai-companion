package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/chattool"
	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/email"
	"github.com/windcry1/ai-companion/internal/eventbus"
	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/opslog"
	"github.com/windcry1/ai-companion/internal/persistence"
	"github.com/windcry1/ai-companion/internal/planner"
	"github.com/windcry1/ai-companion/internal/platform/config"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"github.com/windcry1/ai-companion/internal/reliability"
	"github.com/windcry1/ai-companion/internal/semantic"
	"github.com/windcry1/ai-companion/internal/skill"
	"github.com/windcry1/ai-companion/internal/worker"
)

func main() {
	baseLogHandler := slog.NewJSONHandler(os.Stdout, nil)
	logger := slog.New(baseLogHandler)
	cfg, err := config.Load("ai-companion-worker")
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	logger = opslog.NewLogger(baseLogHandler, nil, cfg.ServiceName, cfg.Environment)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	workerLogger := logger.With("environment", cfg.Environment, "service", cfg.ServiceName)
	if cfg.DatabaseDriver != "memory" {
		connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		store, openErr := persistence.Open(connectCtx, cfg.DatabaseDriver, cfg.MySQLDSN, cfg.PostgresDSN)
		cancel()
		if openErr != nil {
			logger.Error("connect persistent database", "driver", cfg.DatabaseDriver, "error", openErr)
			os.Exit(1)
		}
		defer store.Close()
		if durableLogs, ok := any(store).(opslog.Store); ok {
			logger = opslog.NewLogger(baseLogHandler, durableLogs, cfg.ServiceName, cfg.Environment)
			workerLogger = logger.With("environment", cfg.Environment, "service", cfg.ServiceName)
			logger.Info("system log capture enabled", "event", "opslog.capture.enabled")
		}
		blobs, blobErr := document.NewLocalBlobStore(cfg.DocumentStorageDir)
		if blobErr != nil {
			logger.Error("initialize document storage", "error", blobErr)
			os.Exit(1)
		}
		index := document.NewKnowledgeIndex(cfg.QdrantURL, cfg.QdrantCollection, cfg.QdrantAPIKey, 15*time.Second, cfg.Knowledge)
		if ensureErr := index.Ensure(ctx); ensureErr != nil {
			logger.Warn("qdrant unavailable; document jobs will retry independently while skill jobs continue", "error", ensureErr)
		}
		workerID, idErr := id.New()
		if idErr != nil {
			logger.Error("create worker id", "error", idErr)
			os.Exit(1)
		}
		parser := document.PythonParser{Executable: cfg.PythonExecutable, ModulePath: cfg.PythonWorkerPath}
		skillFiles, skillFileErr := skill.NewLocalFileStore(cfg.SkillStorageDir)
		if skillFileErr != nil {
			logger.Error("initialize skill file storage", "error", skillFileErr)
			os.Exit(1)
		}
		skillRegistry := skill.NewRegistry()
		if registerErr := skill.RegisterBuiltins(skillRegistry); registerErr != nil {
			logger.Error("register built-in skills", "error", registerErr)
			os.Exit(1)
		}
		officeWorker := skill.PythonOfficeWorker{Executable: cfg.PythonExecutable, ModulePath: cfg.PythonWorkerPath, Timeout: 5 * time.Minute}
		if registerErr := skill.RegisterOfficeSkills(skillRegistry, officeWorker); registerErr != nil {
			logger.Error("register office skills", "error", registerErr)
			os.Exit(1)
		}
		skillService := skill.NewService(store, skillFiles, skillRegistry)
		skillRunner := skill.NewRunner(skillService, workerID, cfg.SkillWorkerLeaseDuration, cfg.SkillWorkerRenewInterval)
		skillRunner.SetMaxConcurrency(cfg.SkillWorkerConcurrency)
		skillRunner.SetResourceConcurrency(cfg.SkillParseConcurrency, cfg.SkillRenderConcurrency)
		documentIngestor := document.NewIngestor(store, blobs, parser, index, workerID)
		documentCleaner := document.NewCleaner(store, blobs, index, workerID)
		knowledgeClient := semantic.New(cfg.Knowledge)
		memoryService := memory.NewService(store)
		memoryService.SetSemanticClient(knowledgeClient)
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
		reliabilityController := reliability.NewController(reliability.Config{})
		conversationService := conversation.NewService(store, character.NewService(store), provider)
		conversationService.SetTimeout(6 * time.Minute)
		conversationService.SetPolicySource(reliabilityController)
		conversationService.SetMemoryContext(memoryService)
		conversationService.SetContextBudgets(cfg.ContextRecentTokenBudget, cfg.ContextSummaryTokenBudget)
		conversationService.SetSemanticClient(knowledgeClient)
		ledgerFiles, ledgerFileErr := ledger.NewLocalExportFileStore(cfg.LedgerStorageDir)
		if ledgerFileErr != nil {
			logger.Error("initialize ledger export storage", "error", ledgerFileErr)
			os.Exit(1)
		}
		ledgerService := ledger.NewService(store)
		ledgerService.SetExporter(ledger.ArtifactToolExporter{Executable: cfg.SpreadsheetExecutable, ScriptPath: cfg.SpreadsheetWorkerPath, Timeout: 30 * time.Second})
		ledgerService.SetExportFileStore(ledgerFiles)
		documentService := document.NewService(store, blobs, cfg.DocumentMaxUploadBytes)
		documentService.SetVectorIndex(index)
		documentService.SetKnowledge(knowledgeClient, cfg.Knowledge.WikiEnabled)
		plannerService := planner.NewService(store)
		conversationService.SetToolExecutor(chattool.New(ledgerService, plannerService, documentService, skillService, memoryService))
		emailSender := email.Sender(email.NoopSender{})
		if cfg.SMTPAddr != "" && cfg.SMTPFrom != "" {
			emailSender = email.SMTPSender{Addr: cfg.SMTPAddr, Host: cfg.SMTPHost, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, From: cfg.SMTPFrom, UseTLS: cfg.SMTPUseTLS}
		}
		emailService := email.NewService(store, emailSender)
		runCtx, cancelRun := context.WithCancel(ctx)
		errCh := make(chan error, 16)
		runNamed := func(name string, run func() error) {
			if runErr := run(); runErr != nil {
				errCh <- fmt.Errorf("%s: %w", name, runErr)
				return
			}
			errCh <- nil
		}
		reconcileInterval := cfg.DocumentWorkerPollInterval
		skillPollInterval := cfg.SkillWorkerPollInterval
		if cfg.KafkaEnabled {
			reconcileInterval, skillPollInterval = cfg.KafkaReconcileInterval, cfg.KafkaReconcileInterval
		}
		go runNamed("document reconciler", func() error { return documentIngestor.Run(runCtx, reconcileInterval) })
		go runNamed("document cleanup reconciler", func() error { return documentCleaner.Run(runCtx, reconcileInterval) })
		go runNamed("chat reconciler", func() error {
			return conversationService.RunReconciler(runCtx, workerID, 7*time.Minute, reconcileInterval)
		})
		go runNamed("ledger export reconciler", func() error {
			return ledgerService.RunExportReconciler(runCtx, workerID, 2*time.Minute, reconcileInterval)
		})
		go runNamed("email reconciler", func() error {
			return emailService.RunReconciler(runCtx, workerID, cfg.EmailWorkerLeaseDuration, cfg.EmailWorkerPollInterval)
		})
		go runNamed("reliability sampler", func() error {
			ticker := time.NewTicker(cfg.ReliabilityPollInterval)
			defer ticker.Stop()
			for {
				sampleCtx, cancel := context.WithTimeout(runCtx, 5*time.Second)
				sample, sampleErr := store.ReliabilitySample(sampleCtx, time.Now().UTC())
				cancel()
				if sampleErr == nil {
					reliabilityController.Observe(sample, time.Now().UTC())
				} else if runCtx.Err() == nil {
					logger.Warn("sample reliability signals", "error", sampleErr)
				}
				select {
				case <-runCtx.Done():
					return nil
				case <-ticker.C:
				}
			}
		})
		runnerCount := 7
		go runNamed("wiki compiler", func() error { return documentService.RunWikiCompiler(runCtx, reconcileInterval) })
		if cfg.SkillWorkerEnabled {
			runnerCount++
			go runNamed("skill reconciler", func() error { return skillRunner.Run(runCtx, skillPollInterval) })
		}
		var kafkaPublisher *eventbus.KafkaPublisher
		kafkaConsumers := make([]*eventbus.KafkaConsumer, 0, 6)
		defer func() {
			for _, consumer := range kafkaConsumers {
				consumer.Close()
			}
		}()
		if cfg.KafkaEnabled {
			kafkaPublisher, err = eventbus.NewKafkaPublisher(cfg.KafkaBrokers, cfg.ServiceName+"-outbox")
			if err != nil {
				logger.Error("initialize Kafka publisher", "error", err)
				os.Exit(1)
			}
			defer kafkaPublisher.Close()
			runnerCount++
			relay := eventbus.NewRelay(store, kafkaPublisher, workerID, cfg.OutboxRelayLeaseDuration, cfg.OutboxRelayBatchSize, cfg.OutboxRelayMaxAttempts)
			relay.SetErrorHandler(func(relayErr error) { logger.Warn("Kafka outbox relay", "error", relayErr) })
			go runNamed("Kafka outbox relay", func() error { return relay.Run(runCtx, cfg.OutboxRelayPollInterval) })
			startConsumer := func(name string, topics []string, processor eventbus.Processor) error {
				group := cfg.KafkaConsumerGroup + "-" + name
				consumer, consumerErr := eventbus.NewKafkaConsumer(cfg.KafkaBrokers, cfg.ServiceName+"-"+name, group, topics)
				if consumerErr != nil {
					return consumerErr
				}
				kafkaConsumers = append(kafkaConsumers, consumer)
				runner := eventbus.NewConsumerRunner(store, consumer, processor, group)
				runner.SetErrorHandler(func(consumerErr error) {
					logger.Warn("Kafka consumer", "name", name, "group", group, "error", consumerErr)
				})
				runnerCount++
				go runNamed("Kafka "+name+" consumer", func() error { return runner.Run(runCtx) })
				return nil
			}
			documentProcessor := eventbus.ProcessorFunc(func(processCtx context.Context, event eventbus.Event) error {
				var payload struct {
					JobID string `json:"job_id"`
				}
				if decodeErr := json.Unmarshal(event.Payload, &payload); decodeErr != nil {
					return fmt.Errorf("decode %s payload: %w", event.Type, decodeErr)
				}
				switch event.Type {
				case "document.ingest.v1":
					processed, processErr := documentIngestor.RunJob(processCtx, payload.JobID)
					if processed && processErr != nil {
						logger.Warn("Kafka document job reached terminal failure", "job_id", payload.JobID, "error", processErr)
						return nil
					}
					return processErr
				case "document.cleanup.v1":
					processed, processErr := documentCleaner.RunJob(processCtx, payload.JobID)
					if processed && processErr != nil {
						logger.Warn("Kafka document cleanup scheduled for retry", "job_id", payload.JobID, "error", processErr)
						return nil
					}
					return processErr
				default:
					return fmt.Errorf("unsupported Kafka command %s", event.Type)
				}
			})
			skillProcessor := eventbus.ProcessorFunc(func(processCtx context.Context, event eventbus.Event) error {
				var payload struct {
					RunID string `json:"run_id"`
				}
				if decodeErr := json.Unmarshal(event.Payload, &payload); decodeErr != nil {
					return decodeErr
				}
				_, processErr := skillRunner.RunID(processCtx, payload.RunID)
				return processErr
			})
			chatProcessor := eventbus.ProcessorFunc(func(processCtx context.Context, event eventbus.Event) error {
				var payload struct {
					JobID string `json:"job_id"`
				}
				if decodeErr := json.Unmarshal(event.Payload, &payload); decodeErr != nil {
					return decodeErr
				}
				_, processErr := conversationService.RunJob(processCtx, payload.JobID, workerID, 7*time.Minute)
				return processErr
			})
			memoryProcessor := eventbus.ProcessorFunc(func(processCtx context.Context, event eventbus.Event) error {
				// Drain legacy v1 events without extracting memory. New memories are
				// written only after the chat model selects memory_save_explicit.
				return nil
			})
			ledgerProcessor := eventbus.ProcessorFunc(func(processCtx context.Context, event eventbus.Event) error {
				var payload struct {
					ExportID string `json:"export_id"`
				}
				if decodeErr := json.Unmarshal(event.Payload, &payload); decodeErr != nil {
					return decodeErr
				}
				_, processErr := ledgerService.RunExport(processCtx, payload.ExportID, workerID, 2*time.Minute)
				return processErr
			})
			notificationProcessor := eventbus.ProcessorFunc(func(processCtx context.Context, event eventbus.Event) error {
				var payload struct {
					DeliveryID string `json:"delivery_id"`
				}
				if decodeErr := json.Unmarshal(event.Payload, &payload); decodeErr != nil {
					return decodeErr
				}
				return store.DeliverNotification(processCtx, payload.DeliveryID, time.Now().UTC())
			})
			emailProcessor := eventbus.ProcessorFunc(func(processCtx context.Context, event eventbus.Event) error {
				var payload struct {
					DeliveryID string `json:"delivery_id"`
				}
				if decodeErr := json.Unmarshal(event.Payload, &payload); decodeErr != nil {
					return decodeErr
				}
				_, processErr := emailService.RunDelivery(processCtx, payload.DeliveryID, workerID, cfg.EmailWorkerLeaseDuration)
				return processErr
			})
			for _, consumer := range []struct {
				name   string
				topics []string
				work   eventbus.Processor
			}{
				{"document", []string{"document.ingest.v1", "document.cleanup.v1"}, documentProcessor},
				{"skill", []string{"skill.execute.v1"}, skillProcessor},
				{"chat", []string{"chat.command.v1"}, chatProcessor},
				{"memory", []string{"memory.extract.v1"}, memoryProcessor},
				{"ledger", []string{"ledger.export.v1"}, ledgerProcessor},
				{"notification", []string{"notification.deliver.v1"}, notificationProcessor},
				{"email", []string{"email.deliver.v1"}, emailProcessor},
			} {
				if consumerErr := startConsumer(consumer.name, consumer.topics, consumer.work); consumerErr != nil {
					logger.Error("initialize Kafka consumer", "name", consumer.name, "error", consumerErr)
					os.Exit(1)
				}
			}
		} else {
			runnerCount++
			databaseRelay := eventbus.NewRelay(
				store,
				eventbus.DatabaseReconciledPublisher{},
				workerID+"-database-outbox",
				cfg.OutboxRelayLeaseDuration,
				cfg.OutboxRelayBatchSize,
				cfg.OutboxRelayMaxAttempts,
			)
			databaseRelay.SetErrorHandler(func(relayErr error) {
				logger.Warn("database outbox settlement", "error", relayErr)
			})
			go runNamed("database outbox settlement", func() error {
				return databaseRelay.Run(runCtx, cfg.OutboxRelayPollInterval)
			})
			runnerCount++
			go runNamed("database notification reconciler", func() error {
				return runNotificationDeliveryReconciler(
					runCtx,
					store,
					cfg.NotificationSchedulerInterval,
				)
			})
		}
		runnerCount++
		go runNamed("notification scheduler", func() error {
			return runNotificationScheduler(runCtx, store, cfg.NotificationSchedulerInterval)
		})
		dispatchMode := "database"
		if cfg.KafkaEnabled {
			dispatchMode = "kafka"
		}
		workerLogger.Info(
			"durable workers ready",
			"worker_id", workerID,
			"database_driver", cfg.DatabaseDriver,
			"dispatch_mode", dispatchMode,
			"skill_lease", cfg.SkillWorkerLeaseDuration,
		)
		runErr := <-errCh
		cancelRun()
		for runnerIndex := 1; runnerIndex < runnerCount; runnerIndex++ {
			<-errCh
		}
		if runErr != nil {
			logger.Error("durable worker failed", "error", runErr)
			os.Exit(1)
		}
		logger.Info("worker stopped")
		return
	}
	if err := worker.New(workerLogger, 30*time.Second).Run(ctx); err != nil {
		logger.Error("worker failed", "error", err)
		os.Exit(1)
	}
	logger.Info("worker stopped")
}

type notificationStore interface {
	EnqueueDueNotifications(context.Context, time.Time, int) (int, error)
	DeliverNextNotification(context.Context, time.Time) (bool, error)
}

func runNotificationScheduler(ctx context.Context, store notificationStore, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for {
			count, err := store.EnqueueDueNotifications(ctx, time.Now().UTC(), 100)
			if err != nil && ctx.Err() == nil {
				return err
			}
			if count == 0 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func runNotificationDeliveryReconciler(
	ctx context.Context,
	store notificationStore,
	interval time.Duration,
) error {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := deliverAvailableNotifications(ctx, store, time.Now().UTC()); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func deliverAvailableNotifications(
	ctx context.Context,
	store notificationStore,
	now time.Time,
) (int, error) {
	delivered := 0
	for {
		processed, err := store.DeliverNextNotification(ctx, now)
		if err != nil {
			if ctx.Err() != nil {
				return delivered, nil
			}
			return delivered, err
		}
		if !processed {
			return delivered, nil
		}
		delivered++
	}
}
