package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

type ResourceLimit struct {
	Resource string `json:"resource"`
	Limit    int64  `json:"limit"`
}

type BillingPlanPayload struct {
	DisplayName         string          `json:"display_name"`
	Status              string          `json:"status"`
	EffectiveMode       string          `json:"effective_mode"`
	Limits              []ResourceLimit `json:"limits"`
	AllowedModelClasses []string        `json:"allowed_model_classes"`
	AllowedSkills       []string        `json:"allowed_skills"`
}

type ModelRoleConfig struct {
	Models           []string `json:"models"`
	MaxTokens        int      `json:"max_tokens"`
	ReasoningEffort  string   `json:"reasoning_effort"`
	TimeoutMS        int      `json:"timeout_ms"`
	AttemptTimeoutMS int      `json:"attempt_timeout_ms"`
}

type ProviderPolicy struct {
	Sort               string  `json:"sort"`
	AllowFallbacks     bool    `json:"allow_fallbacks"`
	RequireParameters  bool    `json:"require_parameters"`
	DataCollection     string  `json:"data_collection"`
	ZDRRequired        bool    `json:"zdr_required"`
	MaxPromptPrice     float64 `json:"max_prompt_price"`
	MaxCompletionPrice float64 `json:"max_completion_price"`
}

type ModelProfilePayload struct {
	ProviderConnection string                     `json:"provider_connection"`
	ConfigVersion      string                     `json:"config_version"`
	Roles              map[string]ModelRoleConfig `json:"roles"`
	ProviderPolicy     ProviderPolicy             `json:"provider_policy"`
}

// PromptPayload is an independently versioned prompt asset. Variables use the
// explicit {{variable_name}} form so Studio can validate bindings before an
// Agent version is persisted.
type PromptPayload struct {
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Template    string   `json:"template"`
	Variables   []string `json:"variables"`
}

// AgentPromptReference pins an Agent node to one immutable prompt version.
// A client may submit only Key; Service resolves it to the active version and
// fills the remaining fields before validation and persistence.
type AgentPromptReference struct {
	Key         string `json:"key"`
	VersionID   string `json:"version_id,omitempty"`
	Version     int    `json:"version,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type AgentNodeConfig struct {
	Key            string                `json:"key"`
	Type           string                `json:"type"`
	ModelRole      string                `json:"model_role,omitempty"`
	Prompt         *AgentPromptReference `json:"prompt,omitempty"`
	PromptTemplate string                `json:"prompt_template,omitempty"`
	Tools          []string              `json:"tools,omitempty"`
}

type AgentEdgeConfig struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Condition string `json:"condition,omitempty"`
}

type AgentBudgetConfig struct {
	MaxSteps       int `json:"max_steps"`
	MaxModelCalls  int `json:"max_model_calls"`
	MaxToolCalls   int `json:"max_tool_calls"`
	MaxTotalTokens int `json:"max_total_tokens"`
	TimeoutMS      int `json:"timeout_ms"`
}

// AgentDefinitionPayload is the first Agent Studio DSL. It intentionally uses
// a small, declarative graph instead of executable code.
type AgentDefinitionPayload struct {
	DisplayName  string            `json:"display_name"`
	Description  string            `json:"description"`
	Modules      []string          `json:"modules"`
	ModelProfile string            `json:"model_profile"`
	EntryNode    string            `json:"entry_node"`
	Nodes        []AgentNodeConfig `json:"nodes"`
	Edges        []AgentEdgeConfig `json:"edges"`
	Budget       AgentBudgetConfig `json:"budget"`
}

type AgentCompiledEdge struct {
	To        string `json:"to"`
	Condition string `json:"condition,omitempty"`
}

type AgentCompiledNode struct {
	Key       string                `json:"key"`
	Type      string                `json:"type"`
	ModelRole string                `json:"model_role,omitempty"`
	Prompt    *AgentPromptReference `json:"prompt,omitempty"`
	Tools     []string              `json:"tools,omitempty"`
	Outgoing  []AgentCompiledEdge   `json:"outgoing"`
}

type AgentDefinitionCompilation struct {
	CompilerVersion string                 `json:"compiler_version"`
	Executable      bool                   `json:"executable"`
	Fingerprint     string                 `json:"fingerprint"`
	EntryNode       string                 `json:"entry_node"`
	Nodes           []AgentCompiledNode    `json:"nodes"`
	Budget          AgentBudgetConfig      `json:"budget"`
	Capabilities    map[string]bool        `json:"capabilities"`
	Definition      AgentDefinitionPayload `json:"definition"`
}

// CompileAgentDefinition performs the exact normalization and graph checks
// used by persisted versions without creating or publishing configuration.
func CompileAgentDefinition(key string, raw json.RawMessage) (AgentDefinitionCompilation, error) {
	normalizedKey := normalizeKey(key)
	if !validKey(normalizedKey) {
		return AgentDefinitionCompilation{}, validationError("invalid Agent definition key")
	}
	validated, err := validateAgentDefinition(raw)
	if err != nil {
		return AgentDefinitionCompilation{}, err
	}
	var definition AgentDefinitionPayload
	if err = json.Unmarshal(validated, &definition); err != nil {
		return AgentDefinitionCompilation{}, validationError("invalid normalized Agent definition: %v", err)
	}
	outgoing := make(map[string][]AgentCompiledEdge, len(definition.Nodes))
	for _, edge := range definition.Edges {
		outgoing[edge.From] = append(outgoing[edge.From], AgentCompiledEdge{To: edge.To, Condition: edge.Condition})
	}
	nodes := make([]AgentCompiledNode, 0, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodeOutgoing := outgoing[node.Key]
		if nodeOutgoing == nil {
			nodeOutgoing = []AgentCompiledEdge{}
		}
		nodes = append(nodes, AgentCompiledNode{
			Key: node.Key, Type: node.Type, ModelRole: node.ModelRole,
			Prompt: cloneAgentPromptReference(node.Prompt),
			Tools:  append([]string(nil), node.Tools...), Outgoing: nodeOutgoing,
		})
	}
	return AgentDefinitionCompilation{
		CompilerVersion: "agent-studio-langgraph/v1",
		Executable:      true,
		Fingerprint:     fingerprint(KindAgentDefinition, normalizedKey, validated),
		EntryNode:       definition.EntryNode,
		Nodes:           nodes,
		Budget:          definition.Budget,
		Capabilities: map[string]bool{
			"durable_checkpoints": true,
			"tool_approvals":      true,
			"async_tool_wait":     true,
			"model_observability": true,
			"tool_allowlists":     true,
		},
		Definition: definition,
	}, nil
}

var promptVariablePattern = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)

func validatePrompt(raw json.RawMessage) (json.RawMessage, error) {
	var payload PromptPayload
	if err := decodeStrict(raw, &payload); err != nil {
		return nil, validationError("invalid prompt payload: %v", err)
	}
	payload.DisplayName = strings.TrimSpace(payload.DisplayName)
	payload.Description = strings.TrimSpace(payload.Description)
	payload.Template = strings.TrimSpace(payload.Template)
	if payload.DisplayName == "" || len([]rune(payload.DisplayName)) > 120 {
		return nil, validationError("display_name is required and must not exceed 120 characters")
	}
	if len([]rune(payload.Description)) > 1000 {
		return nil, validationError("description must not exceed 1000 characters")
	}
	if payload.Template == "" || len([]rune(payload.Template)) > 12000 {
		return nil, validationError("template is required and must not exceed 12000 characters")
	}
	if strings.Count(payload.Template, "{{") != strings.Count(payload.Template, "}}") {
		return nil, validationError("template contains unbalanced variable delimiters")
	}
	declared := make(map[string]bool, len(payload.Variables))
	for index, variable := range payload.Variables {
		variable = strings.ToLower(strings.TrimSpace(variable))
		if !validPromptVariable(variable) || declared[variable] || len(payload.Variables) > 32 {
			return nil, validationError("variables contains an invalid or duplicate name")
		}
		declared[variable], payload.Variables[index] = true, variable
	}
	for _, match := range promptVariablePattern.FindAllStringSubmatch(payload.Template, -1) {
		if !declared[match[1]] {
			return nil, validationError("template variable %s is not declared", match[1])
		}
	}
	return json.Marshal(payload)
}

func validPromptVariable(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, char := range value[1:] {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

func cloneAgentPromptReference(value *AgentPromptReference) *AgentPromptReference {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

var supportedLimits = map[string]int64{
	"documents_active":                1_000_000,
	"document_storage_bytes":          1_000_000_000_000,
	"skill_runs_monthly":              10_000_000,
	"agent_runs_monthly":              10_000_000,
	"model_prompt_tokens_monthly":     1_000_000_000_000,
	"model_completion_tokens_monthly": 1_000_000_000_000,
	"model_cost_micros_monthly":       1_000_000_000_000,
	"concurrent_agent_runs":           10_000,
	"workspaces":                      10_000,
	"team_seats":                      100_000,
}

var requiredModelRoles = []string{"planner", "router", "composer", "assessor", "responder", "companion_responder", "repairer", "translation"}

func validateBillingPlan(key string, raw json.RawMessage) (json.RawMessage, error) {
	var payload BillingPlanPayload
	if err := decodeStrict(raw, &payload); err != nil {
		return nil, validationError("invalid billing plan payload: %v", err)
	}
	payload.DisplayName = strings.TrimSpace(payload.DisplayName)
	payload.Status = strings.ToLower(strings.TrimSpace(payload.Status))
	payload.EffectiveMode = strings.ToLower(strings.TrimSpace(payload.EffectiveMode))
	if payload.DisplayName == "" || len([]rune(payload.DisplayName)) > 120 {
		return nil, validationError("display_name is required and must not exceed 120 characters")
	}
	if payload.Status != "active" && payload.Status != "archived" {
		return nil, validationError("status must be active or archived")
	}
	if payload.EffectiveMode != "immediate" && payload.EffectiveMode != "next_period" {
		return nil, validationError("effective_mode must be immediate or next_period")
	}
	seen := map[string]bool{}
	for index := range payload.Limits {
		item := &payload.Limits[index]
		item.Resource = strings.ToLower(strings.TrimSpace(item.Resource))
		maximum, ok := supportedLimits[item.Resource]
		if !ok || seen[item.Resource] {
			return nil, validationError("unsupported or duplicate limit resource %q", item.Resource)
		}
		seen[item.Resource] = true
		if item.Limit < -1 || item.Limit > maximum {
			return nil, validationError("limit for %s must be between 0 and %d", item.Resource, maximum)
		}
		if item.Limit == -1 && key != "team" {
			return nil, validationError("unlimited resources are restricted to the team plan")
		}
	}
	for _, required := range []string{"documents_active", "skill_runs_monthly", "workspaces"} {
		if !seen[required] {
			return nil, validationError("required limit %s is missing", required)
		}
	}
	classes := map[string]bool{}
	for index, value := range payload.AllowedModelClasses {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "free" && value != "standard" && value != "quality" {
			return nil, validationError("unsupported model class %q", value)
		}
		if classes[value] {
			return nil, validationError("duplicate model class %q", value)
		}
		classes[value], payload.AllowedModelClasses[index] = true, value
	}
	if len(payload.AllowedModelClasses) == 0 {
		return nil, validationError("at least one allowed_model_classes entry is required")
	}
	for index, skill := range payload.AllowedSkills {
		skill = strings.TrimSpace(skill)
		if skill == "" || len(skill) > 128 {
			return nil, validationError("allowed_skills contains an invalid key")
		}
		payload.AllowedSkills[index] = skill
	}
	return json.Marshal(payload)
}

func validateModelProfile(raw json.RawMessage, providers []ProviderConnection, models []ModelCatalogEntry, production bool) (json.RawMessage, error) {
	var payload ModelProfilePayload
	if err := decodeStrict(raw, &payload); err != nil {
		return nil, validationError("invalid model profile payload: %v", err)
	}
	payload.ProviderConnection = strings.ToLower(strings.TrimSpace(payload.ProviderConnection))
	payload.ConfigVersion = strings.TrimSpace(payload.ConfigVersion)
	providerFound := false
	for _, provider := range providers {
		if provider.Key == payload.ProviderConnection && provider.Status == "active" {
			providerFound = true
			if production && provider.BaseURL != "https://openrouter.ai/api/v1" {
				return nil, validationError("production provider must use the canonical OpenRouter endpoint")
			}
		}
	}
	if !providerFound {
		return nil, validationError("provider_connection is not active or registered")
	}
	if payload.ConfigVersion == "" || len(payload.ConfigVersion) > 128 {
		return nil, validationError("config_version is required and must not exceed 128 characters")
	}
	catalog := map[string]ModelCatalogEntry{}
	for _, model := range models {
		if model.ProviderKey == payload.ProviderConnection && model.Status == "approved" {
			catalog[model.ModelID] = model
		}
	}
	allowedEffort := map[string]bool{"none": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
	for _, role := range requiredModelRoles {
		config, ok := payload.Roles[role]
		if !ok {
			return nil, validationError("required model role %s is missing", role)
		}
		if len(config.Models) == 0 || len(config.Models) > 3 {
			return nil, validationError("role %s must contain between one and three models", role)
		}
		for index, modelID := range config.Models {
			modelID = strings.TrimSpace(modelID)
			entry, exists := catalog[modelID]
			if !exists || dynamicModelAlias(modelID) {
				return nil, validationError("role %s uses an unapproved or unpinned model %s", role, modelID)
			}
			if role == "companion_responder" && !entry.ZeroPrice {
				return nil, validationError("companion_responder models must be explicitly zero price")
			}
			config.Models[index] = modelID
		}
		minimumTokens := 64
		if role == "translation" {
			minimumTokens = 256
		}
		if config.MaxTokens < minimumTokens || config.MaxTokens > 32768 {
			return nil, validationError("role %s max_tokens is outside the governed range", role)
		}
		config.ReasoningEffort = strings.ToLower(strings.TrimSpace(config.ReasoningEffort))
		if !allowedEffort[config.ReasoningEffort] {
			return nil, validationError("role %s has an unsupported reasoning_effort", role)
		}
		if config.TimeoutMS < 1000 || config.TimeoutMS > 120000 || config.AttemptTimeoutMS < 1000 || config.AttemptTimeoutMS > config.TimeoutMS {
			return nil, validationError("role %s timeout settings are invalid", role)
		}
		payload.Roles[role] = config
	}
	if len(payload.Roles) != len(requiredModelRoles) {
		return nil, validationError("model profile contains unsupported roles")
	}
	policy := &payload.ProviderPolicy
	policy.Sort = strings.ToLower(strings.TrimSpace(policy.Sort))
	policy.DataCollection = strings.ToLower(strings.TrimSpace(policy.DataCollection))
	if policy.Sort != "price" && policy.Sort != "latency" && policy.Sort != "throughput" {
		return nil, validationError("provider_policy.sort is invalid")
	}
	if policy.DataCollection != "allow" && policy.DataCollection != "deny" {
		return nil, validationError("provider_policy.data_collection is invalid")
	}
	if production && (policy.Sort != "price" || policy.DataCollection != "deny" || !policy.RequireParameters) {
		return nil, validationError("production provider policy must sort by price, deny data collection and require parameters")
	}
	if policy.MaxPromptPrice <= 0 || policy.MaxPromptPrice > .3 || policy.MaxCompletionPrice <= 0 || policy.MaxCompletionPrice > 2.5 {
		return nil, validationError("provider price caps exceed the production governance ceiling")
	}
	return json.Marshal(payload)
}

func validateAgentDefinition(raw json.RawMessage) (json.RawMessage, error) {
	var payload AgentDefinitionPayload
	if err := decodeStrict(raw, &payload); err != nil {
		return nil, validationError("invalid Agent definition payload: %v", err)
	}
	payload.DisplayName = strings.TrimSpace(payload.DisplayName)
	payload.Description = strings.TrimSpace(payload.Description)
	payload.ModelProfile = normalizeKey(payload.ModelProfile)
	payload.EntryNode = normalizeKey(payload.EntryNode)
	if payload.DisplayName == "" || len([]rune(payload.DisplayName)) > 120 {
		return nil, validationError("display_name is required and must not exceed 120 characters")
	}
	if len([]rune(payload.Description)) > 1000 {
		return nil, validationError("description must not exceed 1000 characters")
	}
	if !validKey(payload.ModelProfile) || !validKey(payload.EntryNode) {
		return nil, validationError("model_profile and entry_node must be valid keys")
	}
	modules := map[string]bool{}
	for index, module := range payload.Modules {
		module = strings.ToLower(strings.TrimSpace(module))
		if module != "companion" && module != "life" && module != "work" {
			return nil, validationError("unsupported Agent module %q", module)
		}
		if modules[module] {
			return nil, validationError("duplicate Agent module %q", module)
		}
		modules[module], payload.Modules[index] = true, module
	}
	if len(payload.Modules) == 0 {
		return nil, validationError("at least one Agent module is required")
	}
	if len(payload.Nodes) == 0 || len(payload.Nodes) > 64 {
		return nil, validationError("Agent graph must contain between one and 64 nodes")
	}
	allowedRoles := map[string]bool{}
	for _, role := range requiredModelRoles {
		if role != "translation" {
			allowedRoles[role] = true
		}
	}
	nodes := map[string]AgentNodeConfig{}
	endNodes := map[string]bool{}
	for index := range payload.Nodes {
		node := &payload.Nodes[index]
		node.Key = normalizeKey(node.Key)
		node.Type = strings.ToLower(strings.TrimSpace(node.Type))
		node.ModelRole = strings.ToLower(strings.TrimSpace(node.ModelRole))
		node.PromptTemplate = strings.TrimSpace(node.PromptTemplate)
		if node.Prompt != nil {
			node.Prompt.Key = normalizeKey(node.Prompt.Key)
			node.Prompt.VersionID = strings.TrimSpace(node.Prompt.VersionID)
			node.Prompt.Fingerprint = strings.ToLower(strings.TrimSpace(node.Prompt.Fingerprint))
		}
		if !validKey(node.Key) || nodes[node.Key].Key != "" {
			return nil, validationError("Agent node keys must be valid and unique")
		}
		if node.Type != "model" && node.Type != "router" && node.Type != "tool" && node.Type != "end" {
			return nil, validationError("Agent node %s has unsupported type %q", node.Key, node.Type)
		}
		if node.Type == "model" || node.Type == "router" {
			if !allowedRoles[node.ModelRole] || node.PromptTemplate == "" || len([]rune(node.PromptTemplate)) > 12000 || len(node.Tools) != 0 {
				return nil, validationError("Agent node %s has an invalid model contract", node.Key)
			}
			if node.Prompt != nil && (!validKey(node.Prompt.Key) || node.Prompt.VersionID == "" || node.Prompt.Version <= 0 || !validFingerprint(node.Prompt.Fingerprint)) {
				return nil, validationError("Agent node %s has an unresolved prompt reference", node.Key)
			}
		} else if node.ModelRole != "" || node.PromptTemplate != "" || node.Prompt != nil {
			return nil, validationError("Agent node %s cannot define model fields", node.Key)
		}
		seenTools := map[string]bool{}
		for toolIndex, tool := range node.Tools {
			tool = normalizeKey(tool)
			if node.Type != "tool" || !validKey(tool) || seenTools[tool] || len(node.Tools) > 8 {
				return nil, validationError("Agent node %s has an invalid tool allowlist", node.Key)
			}
			seenTools[tool], node.Tools[toolIndex] = true, tool
		}
		if node.Type == "tool" && len(node.Tools) == 0 {
			return nil, validationError("Agent tool node %s requires at least one allowed tool", node.Key)
		}
		if node.Type == "end" {
			endNodes[node.Key] = true
		}
		nodes[node.Key] = *node
	}
	if nodes[payload.EntryNode].Key == "" || len(endNodes) == 0 {
		return nil, validationError("Agent graph entry_node and at least one end node are required")
	}
	if len(payload.Edges) > 256 {
		return nil, validationError("Agent graph must not contain more than 256 edges")
	}
	outgoing := map[string][]string{}
	incoming := map[string][]string{}
	edges := map[string]bool{}
	routerConditions := map[string]map[string]bool{}
	for index := range payload.Edges {
		edge := &payload.Edges[index]
		edge.From, edge.To = normalizeKey(edge.From), normalizeKey(edge.To)
		edge.Condition = normalizeKey(edge.Condition)
		source := nodes[edge.From]
		if source.Type == "router" {
			if !validKey(edge.Condition) {
				return nil, validationError("Agent router edge from %s requires a valid condition", edge.From)
			}
			if routerConditions[edge.From] == nil {
				routerConditions[edge.From] = map[string]bool{}
			}
			if routerConditions[edge.From][edge.Condition] {
				return nil, validationError("Agent router %s contains duplicate condition %q", edge.From, edge.Condition)
			}
			routerConditions[edge.From][edge.Condition] = true
		} else if edge.Condition != "" {
			return nil, validationError("Agent non-router edge from %s cannot define a condition", edge.From)
		}
		identity := edge.From + "\x00" + edge.To + "\x00" + edge.Condition
		if nodes[edge.From].Key == "" || nodes[edge.To].Key == "" || edge.From == edge.To ||
			edges[identity] || len([]rune(edge.Condition)) > 128 {
			return nil, validationError("Agent graph contains an invalid or duplicate edge")
		}
		edges[identity] = true
		outgoing[edge.From] = append(outgoing[edge.From], edge.To)
		incoming[edge.To] = append(incoming[edge.To], edge.From)
	}
	for key, node := range nodes {
		count := len(outgoing[key])
		if (node.Type == "end" && count != 0) || (node.Type != "end" && count == 0) ||
			(node.Type == "router" && count < 2) ||
			(node.Type != "router" && node.Type != "end" && count != 1) {
			return nil, validationError("Agent node %s has an invalid outgoing edge contract", key)
		}
	}
	if !allAgentNodesReachable(payload.EntryNode, outgoing, len(nodes)) {
		return nil, validationError("all Agent nodes must be reachable from entry_node")
	}
	terminating := make(map[string]bool, len(nodes))
	queue := make([]string, 0, len(endNodes))
	for key := range endNodes {
		terminating[key], queue = true, append(queue, key)
	}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		for _, previous := range incoming[key] {
			if !terminating[previous] {
				terminating[previous], queue = true, append(queue, previous)
			}
		}
	}
	if len(terminating) != len(nodes) {
		return nil, validationError("every Agent node must have a path to an end node")
	}
	budget := payload.Budget
	if budget.MaxSteps < 1 || budget.MaxSteps > 128 || budget.MaxModelCalls < 1 || budget.MaxModelCalls > 64 ||
		budget.MaxToolCalls < 0 || budget.MaxToolCalls > 64 || budget.MaxTotalTokens < 64 ||
		budget.MaxTotalTokens > 2_000_000 || budget.TimeoutMS < 1000 || budget.TimeoutMS > 900000 {
		return nil, validationError("Agent budget is outside the governed range")
	}
	if budget.MaxToolCalls == 0 {
		for _, node := range nodes {
			if node.Type == "tool" {
				return nil, validationError("Agent tool nodes require a positive max_tool_calls budget")
			}
		}
	}
	return json.Marshal(payload)
}

func validFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func allAgentNodesReachable(entry string, outgoing map[string][]string, expected int) bool {
	visited := map[string]bool{entry: true}
	queue := []string{entry}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		for _, next := range outgoing[key] {
			if !visited[next] {
				visited[next], queue = true, append(queue, next)
			}
		}
	}
	return len(visited) == expected
}

func decodeStrict(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return fmt.Errorf("payload is empty or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("payload contains trailing values")
	}
	return nil
}

func dynamicModelAlias(modelID string) bool {
	value := strings.ToLower(strings.TrimSpace(modelID))
	return value == "openrouter/free" || value == "auto" || strings.HasSuffix(value, ":latest") || strings.HasSuffix(value, "-latest")
}
