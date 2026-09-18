package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/windcry1/ai-companion/internal/adminpasskey"
	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/billing"
	"github.com/windcry1/ai-companion/internal/buildinfo"
	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/chattool"
	"github.com/windcry1/ai-companion/internal/controlplane"
	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/email"
	"github.com/windcry1/ai-companion/internal/eventbus"
	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/incident"
	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/mcpclient"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/opsauth"
	"github.com/windcry1/ai-companion/internal/opslog"
	"github.com/windcry1/ai-companion/internal/performance"
	"github.com/windcry1/ai-companion/internal/planner"
	"github.com/windcry1/ai-companion/internal/platform/config"
	"github.com/windcry1/ai-companion/internal/platform/tracectx"
	"github.com/windcry1/ai-companion/internal/realtime"
	"github.com/windcry1/ai-companion/internal/reliability"
	"github.com/windcry1/ai-companion/internal/router"
	"github.com/windcry1/ai-companion/internal/safety"
	"github.com/windcry1/ai-companion/internal/semantic"
	"github.com/windcry1/ai-companion/internal/skill"
	"github.com/windcry1/ai-companion/internal/team"
)

type Server struct {
	httpServer           *http.Server
	ready                atomic.Bool
	identity             *identity.Service
	identityAdmin        *identity.AdminService
	characters           *character.Service
	conversations        *conversation.Service
	memories             *memory.Service
	documents            *document.Service
	ledger               *ledger.Service
	planner              *planner.Service
	skills               *skill.Service
	teams                *team.Service
	emails               *email.Service
	billing              *billing.Service
	configuration        *controlplane.Service
	agentSandbox         controlplane.AgentSandbox
	userSafety           *safety.Service
	intentRouter         *router.Router
	mcp                  *mcpclient.Registry
	reliability          *reliability.Controller
	metrics              *reliability.Metrics
	operations           eventbus.OperationsStore
	systemLogs           opslog.Store
	incidents            *incident.Service
	performance          *performance.Service
	environment          string
	webOrigin            string
	kafkaEnabled         bool
	modelProvider        string
	operatorToken        string
	operatorMFARequired  bool
	operatorAuth         *opsauth.Service
	adminPasskeys        *adminpasskey.Service
	realtime             realtime.Gateway
	presenceTTL          time.Duration
	chatRateLimit        int
	chatRateWindow       time.Duration
	billingQuotaDisabled bool
	langfuseEnabled      bool
	langfuseConfigured   bool
	langfuseBaseURL      string
	langfuseCapture      bool
	langfuseSampleRate   float64
	lokiEnabled          bool
	lokiConfigured       bool
	lokiBaseURL          string
	agentRuns            *agent.Service
	agentOperations      agent.OperationsStore
	agentGateway         *agent.ToolGateway
	agentToolExecutor    conversation.ModelToolExecutor
	agentGatewayToken    string
	agentConfirmSecret   string
	agentConfirmTTL      time.Duration
	agentRunTimeout      time.Duration
	agentChatModules     map[string]bool
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
	if err := skill.RegisterOfficeSkills(skillRegistry, skill.PythonOfficeWorker{Executable: cfg.PythonExecutable, ModulePath: cfg.PythonWorkerPath, Timeout: 5 * time.Minute}); err != nil {
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
	conversationService.SetTimeout(6 * time.Minute)
	conversationService.SetPolicySource(reliabilityController)
	conversationService.SetMemoryContext(memoryService)
	conversationService.SetContextBudgets(cfg.ContextRecentTokenBudget, cfg.ContextSummaryTokenBudget)
	knowledgeClient := semantic.New(cfg.Knowledge)
	memoryService.SetSemanticClient(knowledgeClient)
	conversationService.SetSemanticClient(knowledgeClient)
	documentService.SetKnowledge(knowledgeClient, cfg.Knowledge.WikiEnabled)
	chatTools := chattool.New(ledgerService, plannerService, documentService, skillService, memoryService)
	conversationService.SetToolExecutor(chatTools)
	if durable, ok := conversationStore.(interface{ DurableChatDispatch() bool }); ok && durable.DurableChatDispatch() {
		conversationService.SetAsyncDispatch(true)
	}
	presenceTTL := cfg.PresenceTTL
	if presenceTTL == 0 {
		presenceTTL = 24 * time.Hour
	}
	rateLimit := cfg.ChatRateLimit
	if rateLimit == 0 {
		rateLimit = 30
	}
	rateWindow := cfg.ChatRateWindow
	if rateWindow == 0 {
		rateWindow = time.Minute
	}
	var identityAdmin *identity.AdminService
	if adminStore, ok := identityStore.(identity.AdminStore); ok {
		identityAdmin = identity.NewAdminService(adminStore)
	}
	var operatorAuth *opsauth.Service
	if operatorStore, ok := identityStore.(opsauth.Store); ok {
		operatorAuth = opsauth.NewService(operatorStore)
	}
	agentGatewayToken := cfg.AgentGatewayToken
	if agentGatewayToken == "" {
		agentGatewayToken = "development-agent-gateway-token"
	}
	agentConfirmSecret := cfg.AgentConfirmationSecret
	if agentConfirmSecret == "" {
		agentConfirmSecret = "development-agent-confirmation-secret"
	}
	billingService := billing.NewService(billing.NewMemoryStore(), billing.DefaultPlans())
	billingService.SetQuotaDisabled(cfg.BillingQuotaDisabled)
	server := &Server{
		identity:      identity.NewService(identityStore, secret, accessTTL, refreshTTL),
		identityAdmin: identityAdmin,
		characters:    characterService,
		conversations: conversationService, memories: memoryService, documents: documentService, ledger: ledgerService, planner: plannerService, skills: skillService, teams: team.NewService(team.NewMemoryStore()), emails: email.NewService(email.NewMemoryStore(), email.NoopSender{}), billing: billingService, userSafety: safety.NewService(safety.NewMemoryStore()),
		intentRouter: router.New(), mcp: mcpRegistry,
		reliability: reliabilityController, metrics: reliability.NewMetrics(),
		systemLogs:  opslog.NewMemoryStore(),
		incidents:   incident.NewService(incident.NewMemoryStore()),
		performance: performance.NewService(performance.NewMemoryStore()),
		environment: cfg.Environment, webOrigin: cfg.WebOrigin, kafkaEnabled: cfg.KafkaEnabled, modelProvider: cfg.ModelProvider,
		operatorToken: cfg.OperatorToken, operatorMFARequired: cfg.OperatorMFARequired, operatorAuth: operatorAuth,
		realtime: gateway, presenceTTL: presenceTTL, chatRateLimit: rateLimit, chatRateWindow: rateWindow,
		billingQuotaDisabled: cfg.BillingQuotaDisabled,
		langfuseEnabled:      cfg.LangfuseEnabled,
		langfuseConfigured:   cfg.LangfuseConfigured,
		langfuseBaseURL:      cfg.LangfuseBaseURL,
		langfuseCapture:      cfg.LangfuseCaptureContent,
		langfuseSampleRate:   cfg.LangfuseSampleRate,
		lokiEnabled:          cfg.LokiEnabled,
		lokiConfigured:       cfg.LokiConfigured,
		lokiBaseURL:          cfg.LokiBaseURL,
		agentSandbox: controlplane.PythonAgentSandbox{
			Executable: cfg.PythonExecutable,
			ModulePath: cfg.PythonWorkerPath,
			Timeout:    10 * time.Second,
		},
		agentToolExecutor: chatTools, agentGatewayToken: agentGatewayToken,
		agentConfirmSecret: agentConfirmSecret, agentConfirmTTL: cfg.AgentConfirmationTTL,
		agentRunTimeout:  cfg.AgentRunTimeout,
		agentChatModules: map[string]bool{},
	}
	for _, module := range cfg.AgentChatModules {
		server.agentChatModules[module] = true
	}
	if operations, ok := skillStore.(eventbus.OperationsStore); ok {
		server.operations = operations
	}
	if store, ok := identityStore.(opsauth.Store); ok {
		server.SetOperatorAuthStore(store)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ops/auth/passkey/login/options", server.beginAdminLogin)
	mux.HandleFunc("POST /v1/ops/auth/passkey/login/verify", server.finishAdminLogin)
	mux.HandleFunc("POST /v1/ops/auth/passkey/register/options", server.beginAdminRegistration)
	mux.HandleFunc("POST /v1/ops/auth/passkey/register/verify", server.finishAdminRegistration)
	mux.HandleFunc("POST /v1/ops/auth/logout", server.logoutAdmin)
	mux.Handle("POST /v1/ops/auth/invitations", server.requireOperator(http.HandlerFunc(server.inviteAdmin)))
	mux.Handle("POST /v1/ops/auth/sessions/revoke", server.requireOperator(http.HandlerFunc(server.revokeAdminSessions)))
	mux.HandleFunc("GET /healthz", server.health)
	mux.HandleFunc("GET /readyz", server.readiness)
	mux.HandleFunc("GET /v1/meta", server.meta(cfg))
	mux.HandleFunc("GET /metrics", server.prometheusMetrics)
	mux.Handle("GET /internal/v1/agent/runs/{run_id}/tools", server.requireAgentService(http.HandlerFunc(server.listAgentTools)))
	mux.Handle("POST /internal/v1/agent/runs/{run_id}/tools/prepare", server.requireAgentService(http.HandlerFunc(server.prepareAgentTool)))
	mux.Handle("POST /internal/v1/agent/runs/{run_id}/tools/commit", server.requireAgentService(http.HandlerFunc(server.commitAgentTool)))
	mux.Handle("GET /internal/v1/agent/runs/{run_id}/tasks/{task_id}", server.requireAgentService(http.HandlerFunc(server.observeAgentTask)))
	mux.Handle("POST /internal/v1/agent/runs/{run_id}/tasks/{task_id}/retry", server.requireAgentService(http.HandlerFunc(server.retryAgentTask)))
	mux.HandleFunc("POST /v1/auth/register", server.register)
	mux.HandleFunc("POST /v1/auth/login", server.login)
	mux.HandleFunc("POST /v1/auth/refresh", server.refresh)
	mux.Handle("POST /v1/auth/logout", server.requireAuth(http.HandlerFunc(server.logout)))
	mux.Handle("POST /v1/auth/logout-all", server.requireAuth(http.HandlerFunc(server.logoutAll)))
	mux.Handle("GET /v1/users/me", server.requireAuth(http.HandlerFunc(server.getCurrentUser)))
	mux.Handle("POST /v1/users/me/password", server.requireAuth(http.HandlerFunc(server.changePassword)))
	mux.Handle("GET /v1/billing/me", server.requireAuth(http.HandlerFunc(server.getBillingSummary)))
	mux.Handle("GET /v1/safety/me", server.requireAuth(http.HandlerFunc(server.getSafetyPolicy)))
	mux.Handle("PATCH /v1/safety/me", server.requireAuth(http.HandlerFunc(server.updateSafetyPolicy)))
	mux.Handle("GET /v1/characters", server.requireAuth(http.HandlerFunc(server.listCharacters)))
	mux.Handle("POST /v1/characters", server.requireAuth(http.HandlerFunc(server.createCharacter)))
	mux.Handle("GET /v1/characters/{character_id}", server.requireAuth(http.HandlerFunc(server.getCharacter)))
	mux.Handle("PUT /v1/characters/{character_id}", server.requireAuth(http.HandlerFunc(server.updateCharacter)))
	mux.Handle("DELETE /v1/characters/{character_id}", server.requireAuth(http.HandlerFunc(server.deleteCharacter)))
	mux.Handle("GET /v1/characters/{character_id}/persona-versions", server.requireAuth(http.HandlerFunc(server.listPersonaVersions)))
	mux.Handle("GET /v1/conversations", server.requireAuth(http.HandlerFunc(server.listConversations)))
	mux.Handle("POST /v1/conversations", server.requireAuth(http.HandlerFunc(server.createConversation)))
	mux.Handle("DELETE /v1/conversations/{conversation_id}", server.requireAuth(http.HandlerFunc(server.deleteConversation)))
	mux.Handle("GET /v1/conversations/{conversation_id}/messages", server.requireAuth(http.HandlerFunc(server.listMessages)))
	mux.Handle("GET /v1/conversations/{conversation_id}/active-agent-run", server.requireAuth(http.HandlerFunc(server.getActiveAgentRun)))
	mux.Handle("POST /v1/conversations/{conversation_id}/messages", server.requireAuth(http.HandlerFunc(server.sendMessage)))
	mux.Handle("GET /v1/generation-jobs/{job_id}", server.requireAuth(http.HandlerFunc(server.getGenerationJob)))
	mux.Handle("GET /v1/agent-runs/{run_id}", server.requireAuth(http.HandlerFunc(server.getAgentRun)))
	mux.Handle("POST /v1/agent-runs/{run_id}/resolve", server.requireAuth(http.HandlerFunc(server.resolveAgentRun)))
	mux.Handle("POST /v1/agent-runs/{run_id}/retry", server.requireAuth(http.HandlerFunc(server.retryAgentRun)))
	mux.Handle("POST /v1/agent-runs/{run_id}/cancel", server.requireAuth(http.HandlerFunc(server.cancelAgentRun)))
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
	mux.Handle("GET /v1/knowledge/builtin/pages", server.requireAuth(http.HandlerFunc(server.listBuiltinKnowledge)))
	mux.Handle("GET /v1/knowledge/builtin/pages/{page_id}", server.requireAuth(http.HandlerFunc(server.readBuiltinKnowledge)))
	mux.Handle("POST /v1/knowledge/builtin/search", server.requireAuth(http.HandlerFunc(server.searchBuiltinKnowledge)))
	mux.Handle("GET /v1/wiki/pages", server.requireAuth(http.HandlerFunc(server.listWiki)))
	mux.Handle("POST /v1/wiki/pages/{page_id}/rebuild", server.requireAuth(http.HandlerFunc(server.resetWiki)))
	mux.Handle("POST /v1/wiki/search", server.requireAuth(http.HandlerFunc(server.searchWiki)))
	mux.Handle("GET /v1/wiki/pages/{page_id}", server.requireAuth(http.HandlerFunc(server.readWiki)))
	mux.Handle("PATCH /v1/wiki/pages/{page_id}", server.requireAuth(http.HandlerFunc(server.updateWiki)))
	mux.Handle("GET /v1/wiki/pages/{page_id}/links", server.requireAuth(http.HandlerFunc(server.followWiki)))
	mux.Handle("GET /v1/wiki/pages/{page_id}/versions", server.requireAuth(http.HandlerFunc(server.wikiHistory)))
	mux.Handle("GET /v1/wiki/pages/{page_id}/export", server.requireAuth(http.HandlerFunc(server.exportWiki)))
	mux.Handle("POST /v1/wiki/pages/{page_id}/feedback", server.requireAuth(http.HandlerFunc(server.wikiFeedback)))
	mux.Handle("POST /v1/documents/{document_id}/wiki/rebuild", server.requireAuth(http.HandlerFunc(server.rebuildWiki)))
	mux.Handle("POST /v1/documents/{document_id}/reindex", server.requireAuth(http.HandlerFunc(server.reindexDocument)))
	mux.Handle("POST /v1/memories/{memory_id}/corrections", server.requireAuth(http.HandlerFunc(server.correctMemory)))
	mux.Handle("GET /v1/workspaces/{workspace_id}/wiki/pages", server.requireAuth(http.HandlerFunc(server.listWiki)))
	mux.Handle("POST /v1/workspaces/{workspace_id}/wiki/search", server.requireAuth(http.HandlerFunc(server.searchWiki)))
	mux.Handle("GET /v1/workspaces/{workspace_id}/wiki/pages/{page_id}", server.requireAuth(http.HandlerFunc(server.readWiki)))
	mux.Handle("GET /v1/workspaces/{workspace_id}/wiki/pages/{page_id}/links", server.requireAuth(http.HandlerFunc(server.followWiki)))
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
	mux.Handle("GET /v1/plans/today", server.requireAuth(http.HandlerFunc(server.getTodayPlan)))
	mux.Handle("POST /v1/plans/today/items", server.requireAuth(http.HandlerFunc(server.addTodayPlanItem)))
	mux.Handle("POST /v1/plans/today/items/{item_id}/complete", server.requireAuth(http.HandlerFunc(server.completeTodayPlanItem)))
	mux.Handle("POST /v1/plans/today/items/{item_id}/schedule", server.requireAuth(http.HandlerFunc(server.scheduleTodayPlanItem)))
	mux.Handle("POST /v1/reminders/candidates", server.requireAuth(http.HandlerFunc(server.createReminderCandidate)))
	mux.Handle("POST /v1/reminders/{reminder_id}/confirm", server.requireAuth(http.HandlerFunc(server.confirmReminder)))
	mux.Handle("GET /v1/reminders", server.requireAuth(http.HandlerFunc(server.listReminders)))
	mux.Handle("POST /v1/reminders/{reminder_id}/complete", server.requireAuth(http.HandlerFunc(server.completeReminder)))
	mux.Handle("POST /v1/reminders/{reminder_id}/reschedule", server.requireAuth(http.HandlerFunc(server.rescheduleReminder)))
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
	mux.Handle("GET /v1/workspaces", server.requireAuth(http.HandlerFunc(server.listWorkspaces)))
	mux.Handle("POST /v1/workspaces", server.requireAuth(http.HandlerFunc(server.createWorkspace)))
	mux.Handle("GET /v1/workspaces/{workspace_id}/documents", server.requireAuth(http.HandlerFunc(server.listWorkspaceDocuments)))
	mux.Handle("POST /v1/workspaces/{workspace_id}/documents", server.requireAuth(http.HandlerFunc(server.shareWorkspaceDocument)))
	mux.Handle("POST /v1/workspaces/{workspace_id}/documents/query", server.requireAuth(http.HandlerFunc(server.queryWorkspaceDocuments)))
	mux.Handle("GET /v1/workspaces/{workspace_id}/skill-files", server.requireAuth(http.HandlerFunc(server.listWorkspaceSkillFiles)))
	mux.Handle("POST /v1/workspaces/{workspace_id}/skill-files", server.requireAuth(http.HandlerFunc(server.shareWorkspaceSkillFile)))
	mux.Handle("GET /v1/workspaces/{workspace_id}/skill-files/{file_id}", server.requireAuth(http.HandlerFunc(server.downloadWorkspaceSkillFile)))
	mux.Handle("GET /v1/workspaces/{workspace_id}/ledger-exports", server.requireAuth(http.HandlerFunc(server.listWorkspaceLedgerExports)))
	mux.Handle("POST /v1/workspaces/{workspace_id}/ledger-exports", server.requireAuth(http.HandlerFunc(server.shareWorkspaceLedgerExport)))
	mux.Handle("GET /v1/workspaces/{workspace_id}/ledger-exports/{export_id}", server.requireAuth(http.HandlerFunc(server.downloadWorkspaceLedgerExport)))
	mux.Handle("GET /v1/workspaces/{workspace_id}/members", server.requireAuth(http.HandlerFunc(server.listWorkspaceMembers)))
	mux.Handle("GET /v1/workspaces/{workspace_id}/invitations", server.requireAuth(http.HandlerFunc(server.listWorkspaceInvitations)))
	mux.Handle("POST /v1/workspaces/{workspace_id}/invitations", server.requireAuth(http.HandlerFunc(server.createWorkspaceInvitation)))
	mux.Handle("POST /v1/workspace-invitations/{invitation_id}/accept", server.requireAuth(http.HandlerFunc(server.acceptWorkspaceInvitation)))
	mux.Handle("POST /v1/ops/context/backfill", server.requireOperator(http.HandlerFunc(server.backfillContext)))
	mux.Handle("GET /v1/ops/context/metrics", server.requireOperator(http.HandlerFunc(server.contextMetrics)))
	mux.Handle("GET /v1/ops/outbox/dead-letter", server.requireOperator(http.HandlerFunc(server.listDeadLetterOutboxEvents)))
	mux.Handle("GET /v1/ops/outbox/{event_id}", server.requireOperator(http.HandlerFunc(server.getOutboxEvent)))
	mux.Handle("POST /v1/ops/outbox/{event_id}/replay", server.requireOperator(http.HandlerFunc(server.replayOutboxEvent)))
	mux.Handle("GET /v1/ops/kafka/poison-messages", server.requireOperator(http.HandlerFunc(server.listPoisonMessages)))
	mux.Handle("GET /v1/ops/email/deliveries", server.requireOperator(http.HandlerFunc(server.listEmailDeliveries)))
	mux.Handle("GET /v1/ops/email/deliveries/{delivery_id}", server.requireOperator(http.HandlerFunc(server.getEmailDelivery)))
	mux.Handle("POST /v1/ops/email/deliveries/{delivery_id}/replay", server.requireOperator(http.HandlerFunc(server.replayEmailDelivery)))
	mux.Handle("GET /v1/ops/users", server.requireOperator(http.HandlerFunc(server.listOperatorUsers)))
	mux.Handle("GET /v1/ops/users/{user_id}", server.requireOperator(http.HandlerFunc(server.getOperatorUser)))
	mux.Handle("POST /v1/ops/users/{user_id}/disable", server.requireOperator(http.HandlerFunc(server.disableOperatorUser)))
	mux.Handle("POST /v1/ops/users/{user_id}/enable", server.requireOperator(http.HandlerFunc(server.enableOperatorUser)))
	mux.Handle("GET /v1/ops/operators", server.requireOperator(http.HandlerFunc(server.listOperatorAccounts)))
	mux.Handle("POST /v1/ops/operators", server.requireOperator(http.HandlerFunc(server.createOperatorAccount)))
	mux.Handle("GET /v1/ops/operators/{operator_id}", server.requireOperator(http.HandlerFunc(server.getOperatorAccount)))
	mux.Handle("POST /v1/ops/operators/{operator_id}/disable", server.requireOperator(http.HandlerFunc(server.disableOperatorAccount)))
	mux.Handle("POST /v1/ops/operators/{operator_id}/enable", server.requireOperator(http.HandlerFunc(server.enableOperatorAccount)))
	mux.Handle("POST /v1/ops/operators/{operator_id}/reset-token", server.requireOperator(http.HandlerFunc(server.resetOperatorAccountToken)))
	mux.Handle("POST /v1/ops/operators/{operator_id}/reset-mfa", server.requireOperator(http.HandlerFunc(server.resetOperatorAccountMFA)))
	mux.Handle("GET /v1/ops/release-readiness", server.requireOperator(http.HandlerFunc(server.getReleaseReadiness)))
	mux.Handle("GET /v1/ops/console/bootstrap", server.requireOperator(http.HandlerFunc(server.getOperatorConsoleBootstrap)))
	mux.Handle("GET /v1/ops/reliability", server.requireOperator(http.HandlerFunc(server.getReliability)))
	mux.Handle("GET /v1/ops/agent-runs", server.requireOperator(http.HandlerFunc(server.listOperatorAgentRuns)))
	mux.Handle("GET /v1/ops/agent-runs/{run_id}", server.requireOperator(http.HandlerFunc(server.getOperatorAgentRun)))
	mux.Handle("GET /v1/ops/performance/versions", server.requireOperator(http.HandlerFunc(server.listOperatorVersionPerformance)))
	mux.Handle("GET /v1/ops/performance/models", server.requireOperator(http.HandlerFunc(server.listOperatorModelPerformance)))
	mux.Handle("GET /v1/ops/performance/anomalies", server.requireOperator(http.HandlerFunc(server.getOperatorPerformanceAnomalies)))
	mux.Handle("GET /v1/ops/performance/trend", server.requireOperator(http.HandlerFunc(server.listOperatorPerformanceTrend)))
	mux.Handle("GET /v1/ops/performance/forecast", server.requireOperator(http.HandlerFunc(server.getOperatorPerformanceForecast)))
	mux.Handle("GET /v1/ops/performance/budgets", server.requireOperator(http.HandlerFunc(server.listOperatorPerformanceBudgets)))
	mux.Handle("GET /v1/ops/performance/budgets/forecast-history", server.requireOperator(http.HandlerFunc(server.getOperatorPerformanceBudgetForecastHistory)))
	mux.Handle("GET /v1/ops/performance/budgets/report", server.requireOperator(http.HandlerFunc(server.exportOperatorPerformanceBudgetReview)))
	mux.Handle("POST /v1/ops/performance/budgets", server.requireOperator(http.HandlerFunc(server.createOperatorPerformanceBudget)))
	mux.Handle("PATCH /v1/ops/performance/budgets/{budget_id}", server.requireOperator(http.HandlerFunc(server.updateOperatorPerformanceBudget)))
	mux.Handle("POST /v1/ops/performance/budgets/{budget_id}/preview", server.requireOperator(http.HandlerFunc(server.previewOperatorPerformanceBudget)))
	mux.Handle("POST /v1/ops/performance/budgets/{budget_id}/recommendations/{recommendation_key}/decision", server.requireOperator(http.HandlerFunc(server.decideOperatorPerformanceBudgetRecommendation)))
	mux.Handle("POST /v1/ops/performance/budgets/{budget_id}/effects/{decision_id}/acknowledge", server.requireOperator(http.HandlerFunc(server.acknowledgeOperatorPerformanceBudgetEffect)))
	mux.Handle("POST /v1/ops/performance/budgets/{budget_id}/effects/{decision_id}/close", server.requireOperator(http.HandlerFunc(server.closeOperatorPerformanceBudgetEffect)))
	mux.Handle("POST /v1/ops/performance/budgets/evaluate", server.requireOperator(http.HandlerFunc(server.evaluateOperatorPerformanceBudgets)))
	mux.Handle("GET /v1/ops/logs", server.requireOperator(http.HandlerFunc(server.listOperatorSystemLogs)))
	mux.Handle("GET /v1/ops/alert-rules", server.requireOperator(http.HandlerFunc(server.listOperatorAlertRules)))
	mux.Handle("POST /v1/ops/alert-rules", server.requireOperator(http.HandlerFunc(server.createOperatorAlertRule)))
	mux.Handle("PATCH /v1/ops/alert-rules/{rule_id}", server.requireOperator(http.HandlerFunc(server.updateOperatorAlertRule)))
	mux.Handle("GET /v1/ops/alert-subscriptions", server.requireOperator(http.HandlerFunc(server.listOperatorAlertSubscriptions)))
	mux.Handle("POST /v1/ops/alert-subscriptions", server.requireOperator(http.HandlerFunc(server.createOperatorAlertSubscription)))
	mux.Handle("PATCH /v1/ops/alert-subscriptions/{subscription_id}", server.requireOperator(http.HandlerFunc(server.updateOperatorAlertSubscription)))
	mux.Handle("POST /v1/ops/alerts/evaluate", server.requireOperator(http.HandlerFunc(server.evaluateOperatorAlerts)))
	mux.Handle("GET /v1/ops/incidents", server.requireOperator(http.HandlerFunc(server.listOperatorIncidents)))
	mux.Handle("GET /v1/ops/incidents/{incident_id}", server.requireOperator(http.HandlerFunc(server.getOperatorIncident)))
	mux.Handle("GET /v1/ops/incidents/{incident_id}/notifications", server.requireOperator(http.HandlerFunc(server.listOperatorIncidentNotifications)))
	mux.Handle("GET /v1/ops/incidents/{incident_id}/evidence", server.requireOperator(http.HandlerFunc(server.exportOperatorIncidentEvidence)))
	mux.Handle("POST /v1/ops/incidents/{incident_id}/acknowledge", server.requireOperator(http.HandlerFunc(server.acknowledgeOperatorIncident)))
	mux.Handle("POST /v1/ops/incidents/{incident_id}/resolve", server.requireOperator(http.HandlerFunc(server.resolveOperatorIncident)))
	mux.Handle("GET /v1/ops/billing/plans", server.requireOperator(http.HandlerFunc(server.listOperatorBillingPlans)))
	mux.Handle("GET /v1/ops/configuration/convergence", server.requireOperator(http.HandlerFunc(server.getOperatorRuntimeConvergence)))
	mux.Handle("GET /v1/ops/billing/users/{user_id}/usage", server.requireOperator(http.HandlerFunc(server.getOperatorBillingUsage)))
	mux.Handle("GET /v1/ops/billing/users/{user_id}/adjustments", server.requireOperator(http.HandlerFunc(server.listOperatorBillingAdjustments)))
	mux.Handle("POST /v1/ops/billing/users/{user_id}/adjustments", server.requireOperator(http.HandlerFunc(server.createOperatorBillingAdjustment)))
	mux.Handle("POST /v1/ops/billing/plans/{config_key}/versions", server.requireOperator(http.HandlerFunc(server.createOperatorBillingPlanVersion)))
	mux.Handle("POST /v1/ops/billing/plans/{config_key}/rollback", server.requireOperator(http.HandlerFunc(server.rollbackOperatorBillingPlan)))
	mux.Handle("GET /v1/ops/model/providers", server.requireOperator(http.HandlerFunc(server.listOperatorModelProviders)))
	mux.Handle("GET /v1/ops/model/catalog", server.requireOperator(http.HandlerFunc(server.listOperatorModelCatalog)))
	mux.Handle("GET /v1/ops/model-profiles", server.requireOperator(http.HandlerFunc(server.listOperatorModelProfiles)))
	mux.Handle("POST /v1/ops/model-profiles/{config_key}/versions", server.requireOperator(http.HandlerFunc(server.createOperatorModelProfileVersion)))
	mux.Handle("POST /v1/ops/model-profiles/{config_key}/rollback", server.requireOperator(http.HandlerFunc(server.rollbackOperatorModelProfile)))
	mux.Handle("GET /v1/ops/prompts", server.requireOperator(http.HandlerFunc(server.listOperatorPrompts)))
	mux.Handle("POST /v1/ops/prompts/{config_key}/versions", server.requireOperator(http.HandlerFunc(server.createOperatorPromptVersion)))
	mux.Handle("POST /v1/ops/prompts/{config_key}/rollback", server.requireOperator(http.HandlerFunc(server.rollbackOperatorPrompt)))
	mux.Handle("GET /v1/ops/agents", server.requireOperator(http.HandlerFunc(server.listOperatorAgentDefinitions)))
	mux.Handle("POST /v1/ops/agents/{config_key}/compile", server.requireOperator(http.HandlerFunc(server.compileOperatorAgentDefinition)))
	mux.Handle("POST /v1/ops/agents/{config_key}/dry-run", server.requireOperator(http.HandlerFunc(server.dryRunOperatorAgentDefinition)))
	mux.Handle("POST /v1/ops/agents/{config_key}/versions", server.requireOperator(http.HandlerFunc(server.createOperatorAgentDefinitionVersion)))
	mux.Handle("POST /v1/ops/agents/{config_key}/rollback", server.requireOperator(http.HandlerFunc(server.rollbackOperatorAgentDefinition)))
	mux.Handle("POST /v1/ops/agent-versions/{version_id}/dry-runs", server.requireOperator(http.HandlerFunc(server.dryRunOperatorAgentDefinitionVersion)))
	mux.Handle("POST /v1/ops/agent-versions/{version_id}/evaluations", server.requireOperator(http.HandlerFunc(server.evaluateOperatorAgentDefinitionVersion)))
	mux.Handle("GET /v1/ops/agent-versions/{version_id}/evaluations", server.requireOperator(http.HandlerFunc(server.listOperatorAgentEvaluationRuns)))
	mux.Handle("GET /v1/ops/evaluation-runs/{run_id}", server.requireOperator(http.HandlerFunc(server.getOperatorAgentEvaluationRun)))
	mux.Handle("POST /v1/ops/agent-versions/{version_id}/rollouts", server.requireOperator(http.HandlerFunc(server.startOperatorAgentRollout)))
	mux.Handle("GET /v1/ops/agents/{config_key}/rollouts", server.requireOperator(http.HandlerFunc(server.listOperatorAgentRollouts)))
	mux.Handle("GET /v1/ops/agent-rollouts/{rollout_id}", server.requireOperator(http.HandlerFunc(server.getOperatorAgentRollout)))
	mux.Handle("POST /v1/ops/agent-rollouts/{rollout_id}/refresh", server.requireOperator(http.HandlerFunc(server.refreshOperatorAgentRollout)))
	mux.Handle("POST /v1/ops/agent-rollouts/{rollout_id}/abort", server.requireOperator(http.HandlerFunc(server.abortOperatorAgentRollout)))
	mux.Handle("POST /v1/ops/config-versions/{version_id}/validate", server.requireOperator(http.HandlerFunc(server.validateOperatorConfigVersion)))
	mux.Handle("POST /v1/ops/config-versions/{version_id}/submit", server.requireOperator(http.HandlerFunc(server.submitOperatorConfigVersion)))
	mux.Handle("POST /v1/ops/config-versions/{version_id}/publish", server.requireOperator(http.HandlerFunc(server.publishOperatorConfigVersion)))
	mux.Handle("GET /v1/ops/audit-logs", server.requireOperator(http.HandlerFunc(server.listAuditLogs)))
	mux.Handle("GET /v1/ops/audit-logs/export", server.requireOperator(http.HandlerFunc(server.exportAuditLogs)))
	mux.Handle("GET /v1/ops/compensations", server.requireOperator(http.HandlerFunc(server.listCompensationRecords)))
	mux.Handle("POST /v1/ops/compensations", server.requireOperator(http.HandlerFunc(server.createCompensationRecord)))

	server.httpServer = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           traceRequests(securityHeaders(requestLog(logger, server.metrics, cors(cfg.WebOrigin, mux)))),
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

func (s *Server) SetSystemLogStore(store opslog.Store) {
	if store != nil {
		s.systemLogs = store
	}
}

func (s *Server) SetIncidentStore(store incident.Store) {
	if store != nil {
		s.incidents = incident.NewService(store)
	}
}

func (s *Server) SetPerformanceStore(store performance.Store) {
	if store != nil {
		s.performance = performance.NewService(store)
	}
}

func (s *Server) EvaluateAlerts(ctx context.Context, now time.Time) (incident.Evaluation, error) {
	return s.incidents.Evaluate(ctx, now)
}

func (s *Server) EvaluatePerformanceBudgets(ctx context.Context, now time.Time) (performance.BudgetEvaluation, error) {
	return s.performance.EvaluateBudgets(ctx, now)
}

func (s *Server) SetOperatorAuthStore(store opsauth.Store) {
	s.operatorAuth = opsauth.NewService(store)
	state := adminpasskey.Store(adminpasskey.NewMemoryStore())
	if provider, ok := store.(adminpasskey.StoreProvider); ok {
		state = provider.AdminPasskeyStore()
	}
	origin := s.webOrigin
	if origin == "" && s.environment != "production" {
		origin = "http://localhost:3000"
	}
	// Invalid origins fail closed: all browser authentication endpoints return 503.
	s.adminPasskeys, _ = adminpasskey.New(state, store, origin)
}

func (s *Server) SetTeamStore(store team.Store) { s.teams = team.NewService(store) }

func (s *Server) SetEmailStore(store email.Store) {
	s.emails = email.NewService(store, email.NoopSender{})
}

func (s *Server) SetBillingStore(store billing.Store) {
	s.billing = billing.NewService(store, billing.DefaultPlans())
	s.billing.SetQuotaDisabled(s.billingQuotaDisabled)
}

func (s *Server) SetControlPlaneStore(store controlplane.Store) error {
	s.configuration = controlplane.NewService(store, s.environment)
	s.bindAgentModelProfileResolver()
	return s.reloadBillingCatalog(context.Background())
}

func (s *Server) SetAgentSandbox(sandbox controlplane.AgentSandbox) {
	s.agentSandbox = sandbox
}

func (s *Server) reloadBillingCatalog(ctx context.Context) error {
	if s.configuration == nil || s.billing == nil {
		return nil
	}
	deployments, err := s.configuration.Active(ctx, controlplane.KindBillingPlan)
	if err != nil {
		return err
	}
	plans, err := controlplane.BillingPlans(deployments)
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		return nil
	}
	return s.billing.ReplaceCatalog(plans)
}

// SyncRuntimeConfiguration refreshes every cached API-side configuration and
// persists proof of the exact immutable revisions loaded by this instance.
func (s *Server) SyncRuntimeConfiguration(ctx context.Context, instanceID, serviceName string, startedAt time.Time) error {
	if err := s.reloadBillingCatalog(ctx); err != nil {
		return err
	}
	deployments, err := s.configuration.Active(ctx, controlplane.KindBillingPlan)
	if err != nil {
		return err
	}
	for _, deployment := range deployments {
		if _, err = s.configuration.ReportRuntimeConfig(ctx, controlplane.RuntimeConfigReport{
			InstanceID: instanceID, Service: serviceName, Kind: deployment.Kind, Key: deployment.Key,
			VersionID: deployment.Version.ID, Revision: deployment.Revision,
			Fingerprint: deployment.Version.Fingerprint, Status: controlplane.RuntimeStatusApplied,
			StartedAt: startedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) SetSafetyStore(store safety.Store) {
	s.userSafety = safety.NewService(store)
}

func (s *Server) SetIdentityAdminStore(store identity.AdminStore) {
	s.identityAdmin = identity.NewAdminService(store)
}

func (s *Server) SetAgentStore(store agent.Store) {
	s.agentRuns = agent.NewService(store)
	s.agentRuns.SetRunTimeout(s.agentRunTimeout)
	s.bindAgentModelProfileResolver()
	if operations, ok := any(store).(agent.OperationsStore); ok {
		s.agentOperations = operations
	}
	s.agentGateway = agent.NewToolGateway(
		s.agentRuns, s.agentToolExecutor, s.ledger, s.planner, s.skills,
		s.agentConfirmSecret, s.agentConfirmTTL,
	)
}

func (s *Server) bindAgentModelProfileResolver() {
	if s == nil || s.agentRuns == nil || s.configuration == nil {
		return
	}
	s.agentRuns.SetAgentDefinitionResolver(func(ctx context.Context, module, routingKey string) (agent.RunAgentDefinitionSnapshot, bool, error) {
		snapshot, err := s.configuration.RoutedAgentRuntime(ctx, module, routingKey)
		if errors.Is(err, controlplane.ErrNotFound) {
			return agent.RunAgentDefinitionSnapshot{}, false, nil
		}
		if err != nil {
			return agent.RunAgentDefinitionSnapshot{}, false, err
		}
		definition, err := json.Marshal(snapshot.Definition)
		if err != nil {
			return agent.RunAgentDefinitionSnapshot{}, false, err
		}
		return agent.RunAgentDefinitionSnapshot{
			Key: snapshot.Key, VersionID: snapshot.VersionID, Version: snapshot.Version,
			Revision: snapshot.Revision, Fingerprint: snapshot.Fingerprint,
			ModelProfile: snapshot.ModelProfile, Definition: definition,
		}, true, nil
	})
	if strings.ToLower(strings.TrimSpace(s.modelProvider)) != "openrouter" {
		s.agentRuns.SetModelProfileKeyResolver(nil)
		return
	}
	s.agentRuns.SetModelProfileKeyResolver(func(ctx context.Context, profileKey string) (agent.RunModelProfileSnapshot, error) {
		if strings.TrimSpace(profileKey) == "" {
			profileKey = controlplane.DefaultModelProfileKey
		}
		snapshot, err := s.configuration.ActiveModelRuntime(ctx, profileKey)
		if err != nil {
			return agent.RunModelProfileSnapshot{}, err
		}
		return agent.RunModelProfileSnapshot{
			ProfileKey: snapshot.Key, VersionID: snapshot.VersionID, Version: snapshot.Version,
			Revision: snapshot.Revision, ConfigVersion: snapshot.ConfigVersion,
			Fingerprint: snapshot.Fingerprint, Variables: snapshot.Variables,
		}, nil
	})
}

// SetAgentOperationsStore allows a read-only operator projection to be wired
// independently from the runtime store, including in tests and split services.
func (s *Server) SetAgentOperationsStore(store agent.OperationsStore) {
	s.agentOperations = store
}

func (s *Server) ObserveReliability(sample reliability.Sample, now time.Time) reliability.Snapshot {
	return s.reliability.Observe(sample, now)
}

func cors(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestOrigin := r.Header.Get("Origin")
		if corsOriginAllowed(origin, requestOrigin) {
			w.Header().Set("Access-Control-Allow-Origin", requestOrigin)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Operator-ID, X-Operator-TOTP, X-Trace-ID, traceparent, tracestate")
			w.Header().Set("Access-Control-Expose-Headers", "X-Trace-ID, traceparent, tracestate")
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

func corsOriginAllowed(configuredOrigin, requestOrigin string) bool {
	if configuredOrigin == "" || requestOrigin == "" {
		return false
	}
	if requestOrigin == configuredOrigin {
		return true
	}
	configured, configuredErr := url.Parse(configuredOrigin)
	requested, requestedErr := url.Parse(requestOrigin)
	if configuredErr != nil || requestedErr != nil || configured.Scheme != "http" || requested.Scheme != "http" || configured.Port() != requested.Port() {
		return false
	}
	localHost := func(host string) bool { return host == "localhost" || host == "127.0.0.1" || host == "::1" }
	return localHost(configured.Hostname()) && localHost(requested.Hostname())
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
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
		logCtx := tracectx.WithRunID(r.Context(), r.PathValue("run_id"))
		logger.InfoContext(logCtx, "http request", "event", "http.request", "trace_id", requestTraceID(r.Context()), "span_id", tracectx.SpanID(r.Context()), "trace_flags", tracectx.TraceFlags(r.Context()), "run_id", tracectx.RunID(logCtx), "method", r.Method, "route", route, "status", response.status, "duration_ms", duration.Milliseconds())
	})
}
