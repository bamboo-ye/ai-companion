package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const ProtocolVersion = "2025-11-25"

var (
	ErrServerNotAllowed = errors.New("mcp server is not allowed")
	ErrToolNotAllowed   = errors.New("mcp tool is not allowed")
	ErrProtocol         = errors.New("mcp protocol error")
	ErrToolExecution    = errors.New("mcp tool execution failed")
	namePattern         = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	envNamePattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type ServerConfig struct {
	Name         string            `json:"name"`
	Command      string            `json:"command"`
	Args         []string          `json:"args,omitempty"`
	Environment  map[string]string `json:"environment,omitempty"`
	AllowedTools []string          `json:"allowed_tools"`
	TimeoutMS    int               `json:"timeout_ms"`
	Enabled      bool              `json:"enabled"`
}

type ServerSummary struct {
	Name            string   `json:"name"`
	Enabled         bool     `json:"enabled"`
	AllowedTools    []string `json:"allowed_tools"`
	ProtocolVersion string   `json:"protocol_version"`
	Transport       string   `json:"transport"`
}

type Tool struct {
	Name         string         `json:"name"`
	Title        string         `json:"title,omitempty"`
	Description  string         `json:"description,omitempty"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
}

type ToolResult struct {
	Content           []map[string]any `json:"content"`
	StructuredContent map[string]any   `json:"structuredContent,omitempty"`
	IsError           bool             `json:"isError,omitempty"`
}

type Registry struct{ servers map[string]ServerConfig }

func ParseRegistry(raw string) (*Registry, error) {
	if strings.TrimSpace(raw) == "" {
		return NewRegistry(nil)
	}
	if len(raw) > 64<<10 {
		return nil, fmt.Errorf("decode MCP_STDIO_SERVERS_JSON: configuration exceeds 64KB")
	}
	var configs []ServerConfig
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configs); err != nil {
		return nil, fmt.Errorf("decode MCP_STDIO_SERVERS_JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode MCP_STDIO_SERVERS_JSON: expected one JSON array")
	}
	return NewRegistry(configs)
}

func NewRegistry(configs []ServerConfig) (*Registry, error) {
	if len(configs) > 16 {
		return nil, fmt.Errorf("%w: at most 16 servers are supported", ErrServerNotAllowed)
	}
	registry := &Registry{servers: make(map[string]ServerConfig, len(configs))}
	for _, config := range configs {
		if err := validateConfig(config); err != nil {
			return nil, err
		}
		if _, exists := registry.servers[config.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate server %s", ErrServerNotAllowed, config.Name)
		}
		config.Args = append([]string(nil), config.Args...)
		config.AllowedTools = append([]string(nil), config.AllowedTools...)
		sort.Strings(config.AllowedTools)
		config.Environment = cloneStrings(config.Environment)
		registry.servers[config.Name] = config
	}
	return registry, nil
}

func (r *Registry) Servers() []ServerSummary {
	items := make([]ServerSummary, 0, len(r.servers))
	for _, config := range r.servers {
		items = append(items, ServerSummary{Name: config.Name, Enabled: config.Enabled, AllowedTools: append([]string(nil), config.AllowedTools...), ProtocolVersion: ProtocolVersion, Transport: "stdio"})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

func (r *Registry) Call(ctx context.Context, serverName, toolName string, arguments map[string]any) (ToolResult, error) {
	config, exists := r.servers[serverName]
	if !exists || !config.Enabled {
		return ToolResult{}, ErrServerNotAllowed
	}
	if !contains(config.AllowedTools, toolName) {
		return ToolResult{}, ErrToolNotAllowed
	}
	timeout := time.Duration(config.TimeoutMS) * time.Millisecond
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return callStdio(callCtx, config, toolName, arguments)
}

func validateConfig(config ServerConfig) error {
	if !namePattern.MatchString(config.Name) {
		return fmt.Errorf("%w: invalid server name", ErrServerNotAllowed)
	}
	if !filepath.IsAbs(config.Command) || strings.ContainsRune(config.Command, '\x00') {
		return fmt.Errorf("%w: server command must be an absolute executable path", ErrServerNotAllowed)
	}
	if config.TimeoutMS < 100 || config.TimeoutMS > 30_000 {
		return fmt.Errorf("%w: timeout_ms must be between 100 and 30000", ErrServerNotAllowed)
	}
	if len(config.AllowedTools) == 0 || len(config.AllowedTools) > 128 {
		return fmt.Errorf("%w: allowed_tools must contain 1 to 128 names", ErrServerNotAllowed)
	}
	seen := map[string]bool{}
	for _, tool := range config.AllowedTools {
		if !namePattern.MatchString(tool) || seen[tool] {
			return fmt.Errorf("%w: invalid or duplicate tool name", ErrToolNotAllowed)
		}
		seen[tool] = true
	}
	for _, argument := range config.Args {
		if strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("%w: command argument contains NUL", ErrServerNotAllowed)
		}
	}
	for name, value := range config.Environment {
		if !envNamePattern.MatchString(name) || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("%w: invalid explicit environment entry", ErrServerNotAllowed)
		}
	}
	return nil
}

func cloneStrings(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
