package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/windcry1/ai-companion/internal/buildinfo"
	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/eventbus"
	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/mcpclient"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/planner"
	"github.com/windcry1/ai-companion/internal/platform/config"
	"github.com/windcry1/ai-companion/internal/realtime"
	"github.com/windcry1/ai-companion/internal/reliability"
	"github.com/windcry1/ai-companion/internal/router"
	"github.com/windcry1/ai-companion/internal/skill"
)

type Server struct {
	httpServer     *http.Server
	ready          atomic.Bool
	identity       *identity.Service
	characters     *character.Service
	conversations  *conversation.Service
	memories       *memory.Service
	documents      *document.Service
	ledger         *ledger.Service
	planner        *planner.Service
	skills         *skill.Service
	intentRouter   *router.Router
	mcp            *mcpclient.Registry
	reliability    *reliability.Controller
	metrics        *reliability.Metrics
	operations     eventbus.OperationsStore
	operatorToken  string
	realtime       realtime.Gateway
	presenceTTL    time.Duration
	chatRateLimit  int
	chatRateWindow time.Duration
}

func New(cfg config.Config, logger *slog.Logger) *Server {
	return NewWithStores(cfg, logger, identity.NewMemoryStore(), character.NewMemoryStore())
}

func NewWithStores(cfg config.Config, logger *slog.Logger, identityStore identity.Store, characterStore character.Store) *Server {
	return NewWithAllStores(cfg, logger, identityStore, characterStore, conversation.NewMemoryStore())
}

func NewWithAllStores(cfg config.Config, logger *slog.Logger, identityStore identity.Store, characterStore character.Store, conversationStore conversation.Store) *Server {
	return NewWithDependencies(cfg, logger, identityStore, characterStore, conversationStore, conversation.DevelopmentProvider{}, realtime.NewMemoryGateway())
}

func NewWithDependencies(cfg config.Config, logger *slog.Logger, identityStore identity.Store, characterStore character.Store, conversationStore conversation.Store, provider conversation.Provider, gateway realtime.Gateway) *Server {
	return NewWithM2Dependencies(cfg, logger, identityStore, characterStore, conversationStore, memory.NewMemoryStore(), provider, gateway)
}

func NewWithM2Dependencies(cfg config.Config, logger *slog.Logger, identityStore identity.Store, characterStore character.Store, conversationStore conversation.Store, memoryStore memory.Store, provider conversation.Provider, gateway realtime.Gateway) *Server {
	return NewWithDocumentDependencies(cfg, logger, identityStore, characterStore, conversationStore, memoryStore, document.NewMemoryStore(), document.NewMemoryBlobStore(), provider, gateway)
}

func NewWithDocumentDependencies(cfg config.Config, logger *slog.Logger, identityStore identity.Store, characterStore character.Store, conversationStore conversation.Store, memoryStore memory.Store, documentStore document.Store, blobStore document.BlobStore, provider conversation.Provider, gateway realtime.Gateway) *Server {
	return NewWithLifeDependencies(cfg, logger, identityStore, characterStore, conversationStore, memoryStore, documentStore, blobStore, ledger.NewMemoryStore(), provider, gateway)
}

func NewWithLifeDependencies(cfg config.Config, logger *slog.Logger, identityStore identity.Store, characterStore character.Store, conversationStore conversation.Store, memoryStore memory.Store, documentStore document.Store, blobStore document.BlobStore, ledgerStore ledger.Store, provider conversation.Provider, gateway realtime.Gateway) *Server {
	return NewWithM3Dependencies(cfg, logger, identityStore, characterStore, conversationStore, memoryStore, documentStore, blobStore, ledgerStore, planner.NewMemoryStore(), provider, gateway)
}

func NewWithM3Dependencies(cfg config.Config, logger *slog.Logger, identityStore identity.Store, characterStore character.Store, conversationStore conversation.Store, memoryStore memory.Store, documentStore document.Store, blobStore document.BlobStore, ledgerStore ledger.Store, plannerStore planner.Store, provider conversation.Provider, gateway realtime.Gateway) *Server {
	return NewWithM4Dependencies(cfg, logger, identityStore, characterStore, conversationStore, memoryStore, documentStore, blobStore, ledgerStore, plannerStore, skill.NewMemoryStore(), skill.NewMemoryFileStore(), provider, gateway)
}

func NewWithM4Dependencies(cfg config.Config, logger *slog.Logger, identityStore identity.Store, characterStore character.Store, conversationStore conversation.Store, memoryStore memory.Store, documentStore document.Store, blobStore document.BlobStore, ledgerStore ledger.Store, plannerStore planner.Store, skillStore skill.Store, skillFiles skill.FileStore, provider conversation.Provider, gateway realtime.Gateway) *Server {
	accessTTL := cfg.AccessTokenTTL
	if accessTTL == 0 {
		accessTTL = 15 * time.Minute
	}
	refreshTTL := cfg.RefreshTokenTTL
	if refreshTTL == 0 {
		refreshTTL = 30 * 24 * time.Hour
	}
	secret := cfg.AuthTokenSecret
	if secret == "" {
		secret = "test-only-ai-companion-secret"
	}
	characterService := character.NewService(characterStore)
	memoryService := memory.NewService(memoryStore)
	documentService := document.NewService(documentStore, blobStore, cfg.DocumentMaxUploadBytes)
	ledgerService := ledger.NewService(ledgerStore)
	plannerService := planner.NewService(plannerStore)
	skillRegistry := skill.NewRegistry()
	if err := skill.RegisterBuiltins(skillRegistry); err != nil {
		panic(err)
	}
	if err := skill.RegisterOfficeSkills(skillRegistry, skill.PythonOfficeWorker{Executable: cfg.PythonExecutable, ModulePath: cfg.PythonWorkerPath, Timeout: 30 * time.Second}); err != nil {
		panic(err)
	}
	skillService := skill.NewService(skillStore, skillFiles, skillRegistry)
	skillService.SetWorkerQueue(cfg.SkillWorkerEnabled)
	mcpRegistry, err := mcpclient.ParseRegistry(cfg.MCPStdioServersJSON)
	if err != nil {
		panic(err)
	}
	reliabilityController := reliability.NewController(reliability.Config{})
	conversationService := conversation.NewService(conversationStore, characterService, provider)
	conversationService.SetPolicySource(reliabilityController)
	conversationService.SetMemoryContext(memoryService)
	conversationService.SetContextBudgets(cfg.ContextRecentTokenBudget, cfg.ContextSummaryTokenBudget)
	if durable, ok := conversationStore.(interface{ DurableChatDispatch() bool }); ok && cfg.KafkaEnabled && durable.DurableChatDispatch() {
		conversationService.SetAsyncDispatch(true)
	}
	presenceTTL := cfg.PresenceTTL
	if presenceTTL == 0 {
		presenceTTL = 90 * time.Second
	}
	rateLimit := cfg.ChatRateLimit
	if rateLimit == 0 {
		rateLimit = 30
	}
	rateWindow := cfg.ChatRateWindow
	if rateWindow == 0 {
		rateWindow = time.Minute
	}
	server := &Server{
		identity:      identity.NewService(identityStore, secret, accessTTL, refreshTTL),
		characters:    characterService,
		conversations: conversationService, memories: memoryService, documents: documentService, ledger: ledgerService, planner: plannerService, skills: skillService,
		intentRouter: router.New(), mcp: mcpRegistry,
		reliability: reliabilityController, metrics: reliability.NewMetrics(),
		operatorToken: cfg.OperatorToken,
		realtime:      gateway, presenceTTL: presenceTTL, chatRateLimit: rateLimit, chatRateWindow: rateWindow,
	}
	if operations, ok := skillStore.(eventbus.OperationsStore); ok {
		server.operations = operations
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	mux.HandleFunc("GET /readyz", server.readiness)
	mux.HandleFunc("GET /v1/meta", server.meta(cfg))
	mux.HandleFunc("GET /metrics", server.prometheusMetrics)
	mux.HandleFunc("POST /v1/auth/register", server.register)
	mux.HandleFunc("POST /v1/auth/login", server.login)
	mux.HandleFunc("POST /v1/auth/refresh", server.refresh)
	mux.Handle("POST /v1/auth/logout", server.requireAuth(http.HandlerFunc(server.logout)))
	mux.Handle("POST /v1/auth/logout-all", server.requireAuth(http.HandlerFunc(server.logoutAll)))
	mux.Handle("GET /v1/characters", server.requireAuth(http.HandlerFunc(server.listCharacters)))
	mux.Handle("POST /v1/characters", server.requireAuth(http.HandlerFunc(server.createCharacter)))
	mux.Handle("GET /v1/characters/{character_id}", server.requireAuth(http.HandlerFunc(server.getCharacter)))
	mux.Handle("PUT /v1/characters/{character_id}", server.requireAuth(http.HandlerFunc(server.updateCharacter)))
	mux.Handle("DELETE /v1/characters/{character_id}", server.requireAuth(http.HandlerFunc(server.deleteCharacter)))
	mux.Handle("GET /v1/characters/{character_id}/persona-versions", server.requireAuth(http.HandlerFunc(server.listPersonaVersions)))
	mux.Handle("GET /v1/conversations", server.requireAuth(http.HandlerFunc(server.listConversations)))
	mux.Handle("POST /v1/conversations", server.requireAuth(http.HandlerFunc(server.createConversation)))
	mux.Handle("GET /v1/conversations/{conversation_id}/messages", server.requireAuth(http.HandlerFunc(server.listMessages)))
	mux.Handle("POST /v1/conversations/{conversation_id}/messages", server.requireAuth(http.HandlerFunc(server.sendMessage)))
	mux.Handle("GET /v1/generation-jobs/{job_id}", server.requireAuth(http.HandlerFunc(server.getGenerationJob)))
	mux.Handle("GET /v1/generation-jobs/{job_id}/events", server.requireAuth(http.HandlerFunc(server.streamGenerationEvents)))
	mux.Handle("POST /v1/generation-jobs/{job_id}/cancel", server.requireAuth(http.HandlerFunc(server.cancelGeneration)))
	mux.Handle("POST /v1/generation-jobs/{job_id}/retry", server.requireAuth(http.HandlerFunc(server.retryGeneration)))
	mux.Handle("POST /v1/presence/heartbeat", server.requireAuth(http.HandlerFunc(server.presenceHeartbeat)))
	mux.Handle("GET /v1/presence/me", server.requireAuth(http.HandlerFunc(server.getMyPresence)))
	mux.Handle("GET /v1/memories", server.requireAuth(http.HandlerFunc(server.listMemories)))
	mux.Handle("POST /v1/memories", server.requireAuth(http.HandlerFunc(server.createMemory)))
	mux.Handle("PATCH /v1/memories/{memory_id}", server.requireAuth(http.HandlerFunc(server.updateMemory)))
	mux.Handle("DELETE /v1/memories/{memory_id}", server.requireAuth(http.HandlerFunc(server.deleteMemory)))
	mux.Handle("DELETE /v1/memories", server.requireAuth(http.HandlerFunc(server.clearMemories)))
	mux.Handle("GET /v1/documents", server.requireAuth(http.HandlerFunc(server.listDocuments)))
	mux.Handle("POST /v1/documents", server.requireAuth(http.HandlerFunc(server.uploadDocument)))
	mux.Handle("POST /v1/documents/query", server.requireAuth(http.HandlerFunc(server.queryDocuments)))
	mux.Handle("GET /v1/documents/{document_id}", server.requireAuth(http.HandlerFunc(server.getDocument)))
	mux.Handle("DELETE /v1/documents/{document_id}", server.requireAuth(http.HandlerFunc(server.deleteDocument)))
	mux.Handle("POST /v1/ledger/candidates", server.requireAuth(http.HandlerFunc(server.createLedgerCandidate)))
	mux.Handle("POST /v1/ledger/candidates/{candidate_id}/confirm", server.requireAuth(http.HandlerFunc(server.confirmLedgerCandidate)))
	mux.Handle("GET /v1/ledger/entries", server.requireAuth(http.HandlerFunc(server.listLedgerEntries)))
	mux.Handle("GET /v1/ledger/entries/{entry_id}", server.requireAuth(http.HandlerFunc(server.getLedgerEntry)))
	mux.Handle("PATCH /v1/ledger/entries/{entry_id}", server.requireAuth(http.HandlerFunc(server.updateLedgerEntry)))
	mux.Handle("DELETE /v1/ledger/entries/{entry_id}", server.requireAuth(http.HandlerFunc(server.deleteLedgerEntry)))
	mux.Handle("GET /v1/ledger/summary", server.requireAuth(http.HandlerFunc(server.ledgerMonthlySummary)))
	mux.Handle("POST /v1/ledger/exports", server.requireAuth(http.HandlerFunc(server.exportLedger)))
	mux.Handle("GET /v1/ledger/exports/{export_id}", server.requireAuth(http.HandlerFunc(server.getLedgerExport)))
	mux.Handle("GET /v1/ledger/exports/{export_id}/file", server.requireAuth(http.HandlerFunc(server.downloadLedgerExport)))
	mux.Handle("GET /v1/plans", server.requireAuth(http.HandlerFunc(server.listPlans)))
	mux.Handle("POST /v1/plans", server.requireAuth(http.HandlerFunc(server.createPlan)))
	mux.Handle("POST /v1/reminders/candidates", server.requireAuth(http.HandlerFunc(server.createReminderCandidate)))
	mux.Handle("POST /v1/reminders/{reminder_id}/confirm", server.requireAuth(http.HandlerFunc(server.confirmReminder)))
	mux.Handle("GET /v1/reminders", server.requireAuth(http.HandlerFunc(server.listReminders)))
	mux.Handle("POST /v1/reminders/{reminder_id}/complete", server.requireAuth(http.HandlerFunc(server.completeReminder)))
	mux.Handle("POST /v1/reminders/{reminder_id}/system-sync", server.requireAuth(http.HandlerFunc(server.reportReminderSync)))
	mux.Handle("GET /v1/skills", server.requireAuth(http.HandlerFunc(server.listSkills)))
	mux.Handle("PUT /v1/skills/{skill_name}/settings", server.requireAuth(http.HandlerFunc(server.updateSkillSettings)))
	mux.Handle("POST /v1/skills/{skill_name}/runs", server.requireAuth(http.HandlerFunc(server.startSkillRun)))
	mux.Handle("GET /v1/skill-runs", server.requireAuth(http.HandlerFunc(server.listSkillRuns)))
	mux.Handle("GET /v1/skill-runs/{run_id}", server.requireAuth(http.HandlerFunc(server.getSkillRun)))
	mux.Handle("POST /v1/skill-runs/{run_id}/confirm", server.requireAuth(http.HandlerFunc(server.confirmSkillRun)))
	mux.Handle("POST /v1/skill-runs/{run_id}/cancel", server.requireAuth(http.HandlerFunc(server.cancelSkillRun)))
	mux.Handle("POST /v1/skill-runs/{run_id}/retry", server.requireAuth(http.HandlerFunc(server.retrySkillRun)))
	mux.Handle("GET /v1/skill-runs/{run_id}/files/{file_id}", server.requireAuth(http.HandlerFunc(server.downloadSkillFile)))
	mux.Handle("POST /v1/intent/route", server.requireAuth(http.HandlerFunc(server.routeIntent)))
	mux.Handle("GET /v1/mcp/servers", server.requireAuth(http.HandlerFunc(server.listMCPServers)))
	mux.Handle("GET /v1/reliability", server.requireAuth(http.HandlerFunc(server.getReliability)))
	mux.Handle("GET /v1/ops/outbox/dead-letter", server.requireOperator(http.HandlerFunc(server.listDeadLetterOutboxEvents)))
	mux.Handle("GET /v1/ops/outbox/{event_id}", server.requireOperator(http.HandlerFunc(server.getOutboxEvent)))
	mux.Handle("POST /v1/ops/outbox/{event_id}/replay", server.requireOperator(http.HandlerFunc(server.replayOutboxEvent)))
	mux.Handle("GET /v1/ops/compensations", server.requireOperator(http.HandlerFunc(server.listCompensationRecords)))
	mux.Handle("POST /v1/ops/compensations", server.requireOperator(http.HandlerFunc(server.createCompensationRecord)))

	server.httpServer = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           traceRequests(requestLog(logger, server.metrics, cors(cfg.WebOrigin, mux))),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	server.ready.Store(true)
	return server
}

func (s *Server) Recover(ctx context.Context) error {
	if err := s.conversations.Recover(ctx); err != nil {
		return err
	}
	_, err := s.skills.Recover(ctx)
	return err
}

func (s *Server) SetDocumentIndex(index document.VectorIndex) { s.documents.SetVectorIndex(index) }

func (s *Server) SetLedgerExporter(exporter ledger.Exporter) { s.ledger.SetExporter(exporter) }
func (s *Server) SetLedgerExportFileStore(files ledger.ExportFileStore) {
	s.ledger.SetExportFileStore(files)
}
func (s *Server) SetOperationsStore(store eventbus.OperationsStore) { s.operations = store }

func (s *Server) ObserveReliability(sample reliability.Sample, now time.Time) reliability.Snapshot {
	return s.reliability.Observe(sample, now)
}

func cors(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin != "" && r.Header.Get("Origin") == origin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Operator-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.ready.Store(false)
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readiness(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) meta(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"service":     cfg.ServiceName,
			"environment": cfg.Environment,
			"build":       buildinfo.Current(),
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func requestLog(logger *slog.Logger, metrics *reliability.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		response := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(response, r)
		duration := time.Since(started)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		metrics.ObserveRequest(r.Method, route, response.status, duration)
		logger.Info("http request", "trace_id", requestTraceID(r.Context()), "method", r.Method, "route", route, "status", response.status, "duration", duration)
	})
}
