package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/eventbus"
	"github.com/windcry1/ai-companion/internal/persistence"
	"github.com/windcry1/ai-companion/internal/platform/config"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load("ai-companion-agent-worker")
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	if cfg.DatabaseDriver != "postgres" {
		logger.Error("Agent worker requires PostgreSQL", "database_driver", cfg.DatabaseDriver)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	connectCtx, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	store, err := persistence.Open(connectCtx, cfg.DatabaseDriver, cfg.MySQLDSN, cfg.PostgresDSN)
	cancelConnect()
	if err != nil {
		logger.Error("connect persistent database", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	agentStore, ok := any(store).(agent.Store)
	if !ok {
		logger.Error("database does not implement the Agent Run store")
		os.Exit(1)
	}
	workerID, err := id.New()
	if err != nil {
		logger.Error("create worker id", "error", err)
		os.Exit(1)
	}
	service := agent.NewService(agentStore)
	metrics := newAgentWorkerMetrics()
	executorConfig := agent.PythonRuntimeExecutor{
		Executable: cfg.PythonExecutable,
		ModulePath: cfg.PythonWorkerPath,
		Timeout:    cfg.AgentWorkerTimeout,
		OnObserve:  metrics.observePythonRuntime,
	}
	var executor agent.RuntimeExecutor = executorConfig
	if cfg.AgentPythonPoolEnabled {
		pool := agent.NewPythonRuntimePool(executorConfig, cfg.AgentWorkerConcurrency)
		pool.SetObservationHandler(func(observation agent.PythonRuntimeObservation) {
			metrics.observePythonRuntime(observation)
			logger.Info(
				"Agent Python execution",
				"run_id", observation.RunID,
				"execution_mode", observation.ExecutionMode,
				"result_status", observation.ResultStatus,
				"pool_wait_ms", observation.PoolWait.Milliseconds(),
				"duration_ms", observation.Duration.Milliseconds(),
				"model_calls", observation.ModelCalls,
				"model_duration_ms", observation.ModelDuration.Milliseconds(),
				"max_model_attempt_timeout_ms", observation.MaxModelTimeout.Milliseconds(),
				"runtime_overhead_ms", observation.RuntimeOverhead.Milliseconds(),
				"cold_start", observation.ColdStart,
				"recycled", observation.Recycled,
				"success", observation.Success,
				"error_code", observation.ErrorCode,
			)
		})
		warmupStarted := time.Now()
		warmupCtx, cancelWarmup := context.WithTimeout(ctx, 30*time.Second)
		warmed, warmupErr := pool.Warm(warmupCtx, cfg.AgentPythonPoolWarmSize)
		cancelWarmup()
		warmupValues := []any{
			"requested", cfg.AgentPythonPoolWarmSize,
			"ready", warmed,
			"duration_ms", time.Since(warmupStarted).Milliseconds(),
			"success", warmupErr == nil,
		}
		if warmupErr != nil {
			if warmupBlocksReadiness(warmupErr) {
				logger.Error(
					"Agent Python pool identity validation",
					append(warmupValues, "error", warmupErr)...,
				)
				pool.Close()
				os.Exit(1)
			}
			logger.Warn("Agent Python pool warmup", append(warmupValues, "error", warmupErr)...)
		} else {
			logger.Info("Agent Python pool warmup", warmupValues...)
		}
		defer func() {
			pool.Close()
			stats := pool.Stats()
			logger.Info(
				"Agent Python pool stopped",
				"requests", stats.Requests,
				"request_errors", stats.RequestErrors,
				"process_starts", stats.ProcessStarts,
				"process_discards", stats.ProcessDiscards,
			)
		}()
		executor = pool
	}
	runtimeWorker := agent.NewRuntimeWorker(
		service,
		executor,
		workerID,
		cfg.AgentWorkerLeaseDuration,
		cfg.AgentWorkerRetryDelay,
	)
	runtimeWorker.SetControlPollInterval(cfg.AgentControlPollInterval)
	runtimeWorker.SetMaxExecutionAttempts(cfg.AgentWorkerMaxAttempts)
	runtimeWorker.SetRetryPolicy(
		cfg.AgentWorkerRetryMaxDelay,
		cfg.AgentWorkerRetryJitterPercent,
	)
	runtimeWorker.SetRetryObservationHandler(func(observation agent.RuntimeRetryObservation) {
		logger.Info(
			"Agent execution retry scheduled",
			"run_id", observation.RunID,
			"attempt", observation.Attempt,
			"maximum_attempts", observation.MaximumAttempts,
			"delay_ms", observation.Delay.Milliseconds(),
			"retry_after_ms", observation.AdvisedDelay.Milliseconds(),
			"base_delay_ms", observation.BaseDelay.Milliseconds(),
			"max_delay_ms", observation.MaximumDelay.Milliseconds(),
			"jitter_percent", observation.JitterPercent,
			"policy", observation.Policy,
			"provider_status", observation.ProviderStatus,
			"error_code", observation.ErrorCode,
			"deadline_remaining_ms", observation.DeadlineRemaining.Milliseconds(),
		)
	})
	runtimeWorker.SetMaxConcurrency(cfg.AgentWorkerConcurrency)
	runtimeWorker.SetToolReadinessProbe(agent.SkillTaskReadinessProbe{Store: store})

	runCtx, cancelRun := context.WithCancel(ctx)
	errCh := make(chan error, 4)
	runners := 1
	go runNamed(errCh, "Agent Run reconciler", func() error {
		return runtimeWorker.RunReconciler(runCtx, cfg.AgentWorkerPollInterval)
	})

	var consumer *eventbus.KafkaConsumer
	var dispatcher *agent.RunDispatcher
	if cfg.KafkaEnabled {
		consumer, err = eventbus.NewKafkaConsumer(
			cfg.KafkaBrokers,
			cfg.ServiceName+"-"+workerID,
			cfg.AgentKafkaConsumerGroup,
			[]string{
				"agent.run.requested.v1", "agent.run.resume.requested.v1",
				"skill.run.succeeded.v1", "skill.run.failed.v1", "skill.run.cancelled.v1",
			},
		)
		if err != nil {
			logger.Error("initialize Agent Kafka consumer", "error", err)
			os.Exit(1)
		}
		defer consumer.Close()
		dispatcher = agent.NewRunDispatcher(
			runtimeWorker,
			cfg.AgentWorkerConcurrency,
			cfg.AgentDispatchQueueSize,
		)
		dispatcher.SetErrorHandler(func(runID string, dispatchErr error) {
			logger.Warn("Agent run dispatch", "run_id", runID, "error", dispatchErr)
		})
		dispatcher.SetStartHandler(func(runID string, queueWait time.Duration) {
			logger.Debug("Agent run dispatch started", "run_id", runID, "queue_wait_ms", queueWait.Milliseconds())
		})
		dispatcher.SetHintHandler(metrics.observeDispatchHint)
		dispatcher.SetCompleteHandler(func(observation agent.RunDispatchObservation) {
			metrics.observeDispatchCompletion(observation)
			outcome := "processed"
			if observation.Err != nil {
				outcome = "error"
			} else if !observation.Processed {
				outcome = "not_claimed"
			}
			logger.Info(
				"Agent run dispatch completed",
				"run_id", observation.RunID,
				"queue_wait_ms", observation.QueueWait.Milliseconds(),
				"execution_duration_ms", observation.ExecutionDuration.Milliseconds(),
				"replay", observation.Replay,
				"processed", observation.Processed,
				"outcome", outcome,
			)
		})
		runners++
		go runNamed(errCh, "Agent Run dispatcher", func() error {
			return dispatcher.Run(runCtx)
		})
		processor := newAgentEventProcessor(service, dispatcher, func(observation toolWakeObservation) {
			metrics.observeToolWake(observation)
			values := []any{
				"task_id", observation.TaskID,
				"event_type", observation.EventType,
				"event_age_ms", observation.EventAge.Milliseconds(),
				"wake_duration_ms", observation.WakeDuration.Milliseconds(),
				"wake_latency_ms", observation.WakeLatency.Milliseconds(),
				"awakened_runs", observation.AwakenedRuns,
				"outcome", observation.Outcome,
			}
			if observation.Outcome == "no_match" {
				logger.Debug("Agent tool task wake", values...)
				return
			}
			logger.Info("Agent tool task wake", values...)
		}, metrics.trackCanaryRun)
		runner := eventbus.NewConsumerRunner(
			store,
			consumer,
			processor,
			cfg.AgentKafkaConsumerGroup,
		)
		runner.SetErrorHandler(func(consumerErr error) {
			logger.Warn("Agent Kafka consumer", "error", consumerErr)
		})
		runners++
		go runNamed(errCh, "Agent Kafka consumer", func() error {
			return runner.Run(runCtx)
		})
	}
	metricsServer := newAgentMetricsServer(cfg.AgentMetricsAddr, metrics, func() agent.RunDispatcherStats {
		if dispatcher == nil {
			return agent.RunDispatcherStats{}
		}
		return dispatcher.Stats()
	})
	metricsServer.markReady()
	runners++
	go runNamed(errCh, "Agent metrics server", func() error {
		return metricsServer.run(runCtx, cfg.ShutdownTimeout)
	})

	logger.Info(
		"Agent worker ready",
		"worker_id", workerID,
		"graph_version", agent.GraphVersion,
		"kafka_enabled", cfg.KafkaEnabled,
		"kafka_group", cfg.AgentKafkaConsumerGroup,
		"lease", cfg.AgentWorkerLeaseDuration,
		"runtime_timeout", cfg.AgentWorkerTimeout,
		"run_timeout", cfg.AgentRunTimeout,
		"max_execution_attempts", cfg.AgentWorkerMaxAttempts,
		"retry_base_delay", cfg.AgentWorkerRetryDelay,
		"retry_max_delay", cfg.AgentWorkerRetryMaxDelay,
		"retry_base_delay_ms", cfg.AgentWorkerRetryDelay.Milliseconds(),
		"retry_max_delay_ms", cfg.AgentWorkerRetryMaxDelay.Milliseconds(),
		"retry_jitter_percent", cfg.AgentWorkerRetryJitterPercent,
		"execution_concurrency", cfg.AgentWorkerConcurrency,
		"dispatch_queue_size", cfg.AgentDispatchQueueSize,
		"python_pool_enabled", cfg.AgentPythonPoolEnabled,
		"python_pool_warm_size", cfg.AgentPythonPoolWarmSize,
		"control_poll_interval", cfg.AgentControlPollInterval,
		"metrics_address", cfg.AgentMetricsAddr,
	)
	runErr := <-errCh
	cancelRun()
	for index := 1; index < runners; index++ {
		<-errCh
	}
	if runErr != nil {
		logger.Error("Agent worker failed", "error", runErr)
		os.Exit(1)
	}
	logger.Info("Agent worker stopped")
}

func warmupBlocksReadiness(err error) bool {
	var classified *agent.ExecutorError
	return errors.As(err, &classified) && !classified.Retryable()
}

func runNamed(target chan<- error, name string, run func() error) {
	if err := run(); err != nil {
		target <- fmt.Errorf("%s: %w", name, err)
		return
	}
	target <- nil
}

type toolRunWaker interface {
	WakeForTool(context.Context, string, string, int) ([]agent.Run, error)
}

type runDispatchQueue interface {
	Dispatch(context.Context, string) error
}

type toolWakeObservation struct {
	TaskID       string
	EventType    string
	Outcome      string
	Canary       bool
	EventAge     time.Duration
	WakeDuration time.Duration
	WakeLatency  time.Duration
	AwakenedRuns int
}

func newAgentEventProcessor(
	waker toolRunWaker,
	dispatcher runDispatchQueue,
	onToolWake func(toolWakeObservation),
	onCanaryRun func(string, bool),
) eventbus.Processor {
	return eventbus.ProcessorFunc(func(ctx context.Context, event eventbus.Event) error {
		var payload struct {
			RunID  string `json:"run_id"`
			UserID string `json:"user_id"`
			Canary bool   `json:"canary"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode %s event: %w", event.Type, err)
		}
		payload.RunID = strings.TrimSpace(payload.RunID)
		payload.UserID = strings.TrimSpace(payload.UserID)
		if payload.RunID == "" {
			return fmt.Errorf("%s event requires run_id", event.Type)
		}
		switch event.Type {
		case "agent.run.requested.v1", "agent.run.resume.requested.v1":
			if payload.Canary && onCanaryRun != nil {
				onCanaryRun(payload.RunID, true)
			}
			err := dispatcher.Dispatch(ctx, payload.RunID)
			if err != nil && payload.Canary && onCanaryRun != nil {
				onCanaryRun(payload.RunID, false)
			}
			return err
		case "skill.run.succeeded.v1", "skill.run.failed.v1", "skill.run.cancelled.v1":
			if payload.UserID == "" {
				return fmt.Errorf("%s event requires user_id", event.Type)
			}
			startedAt := time.Now()
			eventAge := startedAt.Sub(event.OccurredAt)
			if event.OccurredAt.IsZero() || eventAge < 0 {
				eventAge = 0
			}
			observeWake := func(outcome string, awakenedRuns int) {
				if onToolWake == nil {
					return
				}
				finishedAt := time.Now()
				onToolWake(toolWakeObservation{
					TaskID: payload.RunID, EventType: event.Type, Outcome: outcome, Canary: payload.Canary,
					EventAge: eventAge, WakeDuration: finishedAt.Sub(startedAt),
					WakeLatency:  eventAge + finishedAt.Sub(startedAt),
					AwakenedRuns: awakenedRuns,
				})
			}
			runs, err := waker.WakeForTool(ctx, payload.UserID, payload.RunID, 100)
			if err != nil {
				observeWake("error", 0)
				return fmt.Errorf("wake Agent runs for tool %s: %w", payload.RunID, err)
			}
			for _, run := range runs {
				if err = dispatcher.Dispatch(ctx, run.ID); err != nil {
					observeWake("error", len(runs))
					return err
				}
			}
			outcome := "awakened"
			if len(runs) == 0 {
				outcome = "no_match"
			}
			observeWake(outcome, len(runs))
			return nil
		default:
			return fmt.Errorf("unsupported Agent event %s", event.Type)
		}
	})
}
