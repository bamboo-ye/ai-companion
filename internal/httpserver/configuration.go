package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/controlplane"
)

type createBillingPlanVersionRequest struct {
	BaseVersion         int                          `json:"base_version"`
	DisplayName         string                       `json:"display_name"`
	Status              string                       `json:"status"`
	EffectiveMode       string                       `json:"effective_mode"`
	Limits              []controlplane.ResourceLimit `json:"limits"`
	AllowedModelClasses []string                     `json:"allowed_model_classes"`
	AllowedSkills       []string                     `json:"allowed_skills"`
	Reason              string                       `json:"reason"`
}

type createModelProfileVersionRequest struct {
	BaseVersion        int                                     `json:"base_version"`
	ProviderConnection string                                  `json:"provider_connection"`
	ConfigVersion      string                                  `json:"config_version"`
	Roles              map[string]controlplane.ModelRoleConfig `json:"roles"`
	ProviderPolicy     controlplane.ProviderPolicy             `json:"provider_policy"`
	Reason             string                                  `json:"reason"`
}

type createAgentDefinitionVersionRequest struct {
	BaseVersion  int                            `json:"base_version"`
	DisplayName  string                         `json:"display_name"`
	Description  string                         `json:"description"`
	Modules      []string                       `json:"modules"`
	ModelProfile string                         `json:"model_profile"`
	EntryNode    string                         `json:"entry_node"`
	Nodes        []controlplane.AgentNodeConfig `json:"nodes"`
	Edges        []controlplane.AgentEdgeConfig `json:"edges"`
	Budget       controlplane.AgentBudgetConfig `json:"budget"`
	Reason       string                         `json:"reason"`
}

type createPromptVersionRequest struct {
	BaseVersion int      `json:"base_version"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Template    string   `json:"template"`
	Variables   []string `json:"variables"`
	Reason      string   `json:"reason"`
}

type dryRunAgentDefinitionRequest struct {
	Definition controlplane.AgentDefinitionPayload `json:"definition"`
	Scenario   map[string]any                      `json:"scenario"`
	DebugNode  string                              `json:"debug_node,omitempty"`
}

type dryRunAgentDefinitionVersionRequest struct {
	Scenario  map[string]any `json:"scenario"`
	DebugNode string         `json:"debug_node,omitempty"`
}

type startAgentRolloutRequest struct {
	TrafficPercent int                             `json:"traffic_percent"`
	Policy         controlplane.AgentRolloutPolicy `json:"policy"`
}

func (s *Server) listOperatorBillingPlans(w http.ResponseWriter, r *http.Request) {
	s.listOperatorConfiguration(w, r, controlplane.KindBillingPlan)
}

func (s *Server) getOperatorRuntimeConvergence(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	report, err := service.RuntimeConvergence(r.Context(), 2*time.Minute)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) listOperatorModelProfiles(w http.ResponseWriter, r *http.Request) {
	s.listOperatorConfiguration(w, r, controlplane.KindModelProfile)
}

func (s *Server) listOperatorAgentDefinitions(w http.ResponseWriter, r *http.Request) {
	s.listOperatorConfiguration(w, r, controlplane.KindAgentDefinition)
}

func (s *Server) listOperatorPrompts(w http.ResponseWriter, r *http.Request) {
	s.listOperatorConfiguration(w, r, controlplane.KindPrompt)
}

func (s *Server) compileOperatorAgentDefinition(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	var input controlplane.AgentDefinitionPayload
	if !decodeJSON(w, r, &input) {
		return
	}
	payload, err := json.Marshal(input)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	compilation, err := service.CompileAgentDefinition(r.Context(), r.PathValue("config_key"), payload)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"compilation": compilation})
}

func (s *Server) dryRunOperatorAgentDefinition(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	var input dryRunAgentDefinitionRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	payload, err := json.Marshal(input.Definition)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	compilation, err := service.CompileAgentDefinition(r.Context(), r.PathValue("config_key"), payload)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	s.executeAgentDryRun(w, r, compilation, r.PathValue("config_key"), "draft", input.Scenario, input.DebugNode)
}

func (s *Server) dryRunOperatorAgentDefinitionVersion(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	var input dryRunAgentDefinitionVersionRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	version, err := service.Get(r.Context(), r.PathValue("version_id"))
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	if version.Kind != controlplane.KindAgentDefinition {
		writeConfigurationError(w, controlplane.ErrNotFound)
		return
	}
	compilation, err := controlplane.CompileAgentDefinition(version.Key, version.Payload)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	s.executeAgentDryRun(w, r, compilation, version.Key, version.ID, input.Scenario, input.DebugNode)
}

func (s *Server) evaluateOperatorAgentDefinitionVersion(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	if s.agentSandbox == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_sandbox_unavailable", Message: "Agent 合成沙箱暂未启用"})
		return
	}
	var suite controlplane.AgentEvaluationSuite
	if !decodeJSON(w, r, &suite) {
		return
	}
	run, err := service.EvaluateAgentVersion(
		r.Context(), r.PathValue("version_id"), suite, currentOperator(r).Actor, s.agentSandbox,
	)
	if err != nil {
		writeAgentSandboxError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"evaluation_run": run})
}

func (s *Server) listOperatorAgentEvaluationRuns(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	runs, err := service.EvaluationRuns(r.Context(), r.PathValue("version_id"), queryLimitMax(r, 20, 100))
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"evaluation_runs": runs})
}

func (s *Server) getOperatorAgentEvaluationRun(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	run, err := service.EvaluationRun(r.Context(), r.PathValue("run_id"))
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"evaluation_run": run})
}

func (s *Server) startOperatorAgentRollout(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationAdmin(w, r)
	if !ok || !s.requireConfigurationStepUpMFA(w, r) {
		return
	}
	var input startAgentRolloutRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	rollout, err := service.StartAgentRollout(r.Context(), r.PathValue("version_id"), controlplane.StartAgentRolloutInput{
		TrafficPercent: input.TrafficPercent, Policy: input.Policy, Actor: currentOperator(r).Actor,
	})
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"rollout": rollout})
}

func (s *Server) listOperatorAgentRollouts(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	rollouts, err := service.AgentRollouts(r.Context(), r.PathValue("config_key"), queryLimitMax(r, 20, 100))
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rollouts": rollouts})
}

func (s *Server) getOperatorAgentRollout(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	rollout, err := service.AgentRollout(r.Context(), r.PathValue("rollout_id"))
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rollout": rollout})
}

func (s *Server) refreshOperatorAgentRollout(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	rollout, err := service.RefreshAgentRollout(r.Context(), r.PathValue("rollout_id"), currentOperator(r).Actor)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rollout": rollout})
}

func (s *Server) abortOperatorAgentRollout(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationAdmin(w, r)
	if !ok || !s.requireConfigurationStepUpMFA(w, r) {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	rollout, err := service.AbortAgentRollout(r.Context(), r.PathValue("rollout_id"), currentOperator(r).Actor, input.Reason)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rollout": rollout})
}

func (s *Server) executeAgentDryRun(
	w http.ResponseWriter,
	r *http.Request,
	compilation controlplane.AgentDefinitionCompilation,
	definitionKey string,
	versionID string,
	scenario map[string]any,
	debugNode string,
) {
	if s.agentSandbox == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_sandbox_unavailable", Message: "Agent 合成沙箱暂未启用"})
		return
	}
	if scenario == nil {
		scenario = map[string]any{}
	}
	report, err := s.agentSandbox.Run(r.Context(), controlplane.AgentSandboxRequest{
		Definition: compilation.Definition,
		Identity: controlplane.AgentSandboxIdentity{
			Key:         strings.ToLower(strings.TrimSpace(definitionKey)),
			VersionID:   versionID,
			Fingerprint: compilation.Fingerprint,
		},
		Scenario: scenario,
	})
	if err != nil {
		writeAgentSandboxError(w, err)
		return
	}
	response := map[string]any{"report": report}
	if strings.TrimSpace(debugNode) != "" {
		debug, ok := buildAgentNodeDebug(compilation, report, scenario, debugNode)
		if !ok {
			writeConfigurationError(w, controlplane.ErrValidation)
			return
		}
		response["node_debug"] = debug
	}
	writeJSON(w, http.StatusOK, response)
}

func buildAgentNodeDebug(compilation controlplane.AgentDefinitionCompilation, report controlplane.AgentSandboxReport, scenario map[string]any, requested string) (map[string]any, bool) {
	key := strings.ToLower(strings.TrimSpace(requested))
	var node controlplane.AgentNodeConfig
	found := false
	for _, candidate := range compilation.Definition.Nodes {
		if candidate.Key == key {
			node, found = candidate, true
			break
		}
	}
	if !found {
		return nil, false
	}
	trace := make([]map[string]any, 0)
	modelCalls := make([]map[string]any, 0)
	toolCalls := make([]map[string]any, 0)
	status := "not_visited"
	for _, item := range report.NodeTrace {
		if agentRuntimeNodeMatches(item["node"], key) {
			trace = append(trace, item)
			if value, ok := item["status"].(string); ok && value != "" {
				status = value
			}
		}
	}
	for _, item := range report.ModelCalls {
		if agentRuntimeNodeMatches(item["graph_node"], key) {
			modelCalls = append(modelCalls, item)
		}
	}
	for _, item := range report.ToolCalls {
		if agentRuntimeNodeMatches(item["node"], key) {
			toolCalls = append(toolCalls, item)
		}
	}
	upstream := map[string]any{}
	for _, edge := range compilation.Definition.Edges {
		if edge.To == key {
			if output, ok := report.NodeOutputs[edge.From]; ok {
				upstream[edge.From] = output
			}
		}
	}
	input := map[string]any{"upstream_outputs": upstream}
	for _, field := range []string{"module", "user_message"} {
		if value, ok := scenario[field]; ok {
			input[field] = value
		}
	}
	return map[string]any{
		"node": node, "status": status, "input": input, "output": report.NodeOutputs[key],
		"trace": trace, "model_calls": modelCalls, "tool_calls": toolCalls,
	}, true
}

func agentRuntimeNodeMatches(value any, key string) bool {
	runtimeNode, ok := value.(string)
	if !ok {
		return false
	}
	return runtimeNode == key || runtimeNode == "studio__"+key || strings.HasSuffix(runtimeNode, "__"+key)
}

func (s *Server) listOperatorConfiguration(w http.ResponseWriter, r *http.Request, kind string) {
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	versions, err := service.List(r.Context(), kind, key, queryLimitMax(r, 100, 200))
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	deployments, err := service.Active(r.Context(), kind)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions, "deployments": deployments})
}

func (s *Server) createOperatorBillingPlanVersion(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationAdmin(w, r)
	if !ok {
		return
	}
	var input createBillingPlanVersionRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	payload, err := json.Marshal(controlplane.BillingPlanPayload{
		DisplayName: input.DisplayName, Status: input.Status, EffectiveMode: input.EffectiveMode,
		Limits: input.Limits, AllowedModelClasses: input.AllowedModelClasses, AllowedSkills: input.AllowedSkills,
	})
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	item, err := service.Create(r.Context(), controlplane.CreateInput{
		Kind: controlplane.KindBillingPlan, Key: r.PathValue("config_key"), BaseVersion: input.BaseVersion,
		Payload: payload, Reason: input.Reason, Actor: currentOperator(r).Actor,
	})
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"version": item})
}

func (s *Server) createOperatorModelProfileVersion(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationAdmin(w, r)
	if !ok {
		return
	}
	var input createModelProfileVersionRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	payload, err := json.Marshal(controlplane.ModelProfilePayload{
		ProviderConnection: input.ProviderConnection, ConfigVersion: input.ConfigVersion,
		Roles: input.Roles, ProviderPolicy: input.ProviderPolicy,
	})
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	item, err := service.Create(r.Context(), controlplane.CreateInput{
		Kind: controlplane.KindModelProfile, Key: r.PathValue("config_key"), BaseVersion: input.BaseVersion,
		Payload: payload, Reason: input.Reason, Actor: currentOperator(r).Actor,
	})
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"version": item})
}

func (s *Server) createOperatorAgentDefinitionVersion(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationAdmin(w, r)
	if !ok {
		return
	}
	var input createAgentDefinitionVersionRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	payload, err := json.Marshal(controlplane.AgentDefinitionPayload{
		DisplayName: input.DisplayName, Description: input.Description, Modules: input.Modules,
		ModelProfile: input.ModelProfile, EntryNode: input.EntryNode, Nodes: input.Nodes,
		Edges: input.Edges, Budget: input.Budget,
	})
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	item, err := service.Create(r.Context(), controlplane.CreateInput{
		Kind: controlplane.KindAgentDefinition, Key: r.PathValue("config_key"),
		BaseVersion: input.BaseVersion, Payload: payload, Reason: input.Reason,
		Actor: currentOperator(r).Actor,
	})
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"version": item})
}

func (s *Server) createOperatorPromptVersion(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationAdmin(w, r)
	if !ok {
		return
	}
	var input createPromptVersionRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	payload, err := json.Marshal(controlplane.PromptPayload{
		DisplayName: input.DisplayName, Description: input.Description, Template: input.Template, Variables: input.Variables,
	})
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	item, err := service.Create(r.Context(), controlplane.CreateInput{
		Kind: controlplane.KindPrompt, Key: r.PathValue("config_key"), BaseVersion: input.BaseVersion,
		Payload: payload, Reason: input.Reason, Actor: currentOperator(r).Actor,
	})
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"version": item})
}

func (s *Server) validateOperatorConfigVersion(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationAdmin(w, r)
	if !ok {
		return
	}
	item, err := service.Validate(r.Context(), r.PathValue("version_id"), currentOperator(r).Actor)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": item, "validation": map[string]any{"valid": true, "fingerprint": item.Fingerprint}})
}

func (s *Server) submitOperatorConfigVersion(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationAdmin(w, r)
	if !ok {
		return
	}
	item, err := service.Submit(r.Context(), r.PathValue("version_id"), currentOperator(r).Actor)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": item})
}

func (s *Server) publishOperatorConfigVersion(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationAdmin(w, r)
	if !ok || !s.requireConfigurationStepUpMFA(w, r) {
		return
	}
	item, deployment, err := service.Publish(r.Context(), r.PathValue("version_id"), currentOperator(r).Actor)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	runtimeApplied, runtimeMessage := false, "配置已发布；Agent Worker 将在配置轮询周期内热加载，实际版本请通过 Worker 指标确认"
	if item.Kind == controlplane.KindBillingPlan {
		if reloadErr := s.reloadBillingCatalog(r.Context()); reloadErr == nil {
			runtimeApplied, runtimeMessage = true, "套餐目录已原子热更新"
		} else {
			runtimeMessage = "配置已发布；当前实例将在下一次启动时重新加载"
		}
	}
	if item.Kind == controlplane.KindAgentDefinition {
		runtimeApplied = true
		runtimeMessage = "Agent Studio 定义已发布；后续新 Run 将固化该版本，并按声明节点、边、预算、超时与工具白名单执行"
	}
	if item.Kind == controlplane.KindPrompt {
		runtimeApplied = true
		runtimeMessage = "Prompt 已独立发布；新建 Agent 版本可绑定该版本，已有 Agent 与 Run 保持原快照"
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": item, "deployment": deployment, "runtime_applied": runtimeApplied, "runtime_message": runtimeMessage})
}

func (s *Server) rollbackOperatorBillingPlan(w http.ResponseWriter, r *http.Request) {
	s.rollbackOperatorConfiguration(w, r, controlplane.KindBillingPlan)
}

func (s *Server) rollbackOperatorModelProfile(w http.ResponseWriter, r *http.Request) {
	s.rollbackOperatorConfiguration(w, r, controlplane.KindModelProfile)
}

func (s *Server) rollbackOperatorAgentDefinition(w http.ResponseWriter, r *http.Request) {
	s.rollbackOperatorConfiguration(w, r, controlplane.KindAgentDefinition)
}

func (s *Server) rollbackOperatorPrompt(w http.ResponseWriter, r *http.Request) {
	s.rollbackOperatorConfiguration(w, r, controlplane.KindPrompt)
}

func (s *Server) rollbackOperatorConfiguration(w http.ResponseWriter, r *http.Request, kind string) {
	service, ok := s.requireConfigurationAdmin(w, r)
	if !ok || !s.requireConfigurationStepUpMFA(w, r) {
		return
	}
	var input struct {
		TargetVersion int    `json:"target_version"`
		Reason        string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	deployment, err := service.Rollback(r.Context(), kind, r.PathValue("config_key"), input.TargetVersion, currentOperator(r).Actor, input.Reason)
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	runtimeApplied := false
	if kind == controlplane.KindBillingPlan {
		runtimeApplied = s.reloadBillingCatalog(r.Context()) == nil
	}
	runtimeMessage := "Agent Worker 将在配置轮询周期内热加载回滚版本"
	if kind == controlplane.KindBillingPlan {
		runtimeMessage = "套餐目录已回滚并在当前 API 实例重新加载"
	}
	if kind == controlplane.KindAgentDefinition {
		runtimeApplied = true
		runtimeMessage = "Agent 定义部署已回滚；后续新 Run 使用目标版本，已有 Run 保持原快照"
	}
	if kind == controlplane.KindPrompt {
		runtimeApplied = true
		runtimeMessage = "Prompt 部署已回滚；新建 Agent 版本绑定目标版本，已有 Agent 快照不变"
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployment": deployment, "runtime_applied": runtimeApplied, "runtime_message": runtimeMessage})
}

func (s *Server) listOperatorModelProviders(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	items, err := service.Providers(r.Context())
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": items})
}

func (s *Server) listOperatorModelCatalog(w http.ResponseWriter, r *http.Request) {
	service, ok := s.requireConfigurationService(w)
	if !ok {
		return
	}
	items, err := service.Models(r.Context())
	if err != nil {
		writeConfigurationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": items})
}

func (s *Server) requireConfigurationService(w http.ResponseWriter) (*controlplane.Service, bool) {
	if s.configuration == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "configuration_unavailable", Message: "配置中心暂未启用"})
		return nil, false
	}
	return s.configuration, true
}

func (s *Server) requireConfigurationAdmin(w http.ResponseWriter, r *http.Request) (*controlplane.Service, bool) {
	if !s.requireOperatorRole(w, r, "admin") {
		return nil, false
	}
	return s.requireConfigurationService(w)
}

func (s *Server) requireConfigurationStepUpMFA(w http.ResponseWriter, r *http.Request) bool {
	operator := currentOperator(r)
	if !operator.MFA && !(operator.Legacy && !s.operatorMFARequired) {
		writeJSON(w, http.StatusForbidden, apiError{Code: "operator_step_up_mfa_required", Message: "发布和回滚配置必须使用已验证 MFA 的运维账号"})
		return false
	}
	return true
}

func writeConfigurationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, controlplane.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "configuration_not_found", Message: "配置版本不存在"})
	case errors.Is(err, controlplane.ErrConflict):
		writeJSON(w, http.StatusConflict, apiError{Code: "configuration_conflict", Message: "配置版本或工作流状态已变化，请刷新后重试"})
	case errors.Is(err, controlplane.ErrApproval):
		writeJSON(w, http.StatusForbidden, apiError{Code: "configuration_approval_separation_required", Message: "发布或回滚必须由另一名管理员审批"})
	case errors.Is(err, controlplane.ErrEvaluation):
		writeJSON(w, http.StatusConflict, apiError{Code: "agent_evaluation_required", Message: "Agent 版本必须先通过与当前指纹匹配的评测套件"})
	case errors.Is(err, controlplane.ErrRollout):
		writeJSON(w, http.StatusConflict, apiError{Code: "agent_rollout_required", Message: "Agent 更新必须先完成基于当前稳定版本的线上灰度门禁"})
	case errors.Is(err, controlplane.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "configuration_validation_failed", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "configuration_unavailable", Message: "配置中心暂时不可用"})
	}
}

func writeAgentSandboxError(w http.ResponseWriter, err error) {
	if errors.Is(err, controlplane.ErrSandboxUnavailable) {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_sandbox_unavailable", Message: "Agent 合成沙箱暂时不可用"})
		return
	}
	writeConfigurationError(w, err)
}
