package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

const maxMCPOutputBytes = 2 << 20

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeResult struct {
	ProtocolVersion string                     `json:"protocolVersion"`
	Capabilities    map[string]json.RawMessage `json:"capabilities"`
}

type listToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type stdioSession struct {
	encoder *json.Encoder
	decoder *json.Decoder
	nextID  int64
	mu      sync.Mutex
}

func callStdio(ctx context.Context, config ServerConfig, toolName string, arguments map[string]any) (ToolResult, error) {
	command := exec.CommandContext(ctx, config.Command, config.Args...)
	command.Env = []string{"LANG=C.UTF-8", "PATH=/usr/bin:/bin"}
	keys := make([]string, 0, len(config.Environment))
	for key := range config.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		command.Env = append(command.Env, key+"="+config.Environment[key])
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return ToolResult{}, fmt.Errorf("%w: open stdin: %v", ErrProtocol, err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return ToolResult{}, fmt.Errorf("%w: open stdout: %v", ErrProtocol, err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return ToolResult{}, fmt.Errorf("%w: open stderr: %v", ErrProtocol, err)
	}
	if err = command.Start(); err != nil {
		return ToolResult{}, fmt.Errorf("%w: start server: %v", ErrProtocol, err)
	}
	defer func() {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	stderrBuffer := &limitedBuffer{limit: 4096}
	go func() { _, _ = io.Copy(stderrBuffer, stderr) }()
	reader := &countingReader{reader: bufio.NewReader(stdout), limit: maxMCPOutputBytes}
	session := &stdioSession{encoder: json.NewEncoder(stdin), decoder: json.NewDecoder(reader)}
	if err = session.initialize(); err != nil {
		return ToolResult{}, withStderr(err, stderrBuffer.String())
	}
	tool, err := session.findTool(toolName)
	if err != nil {
		return ToolResult{}, withStderr(err, stderrBuffer.String())
	}
	if err = validateObject(arguments, tool.InputSchema); err != nil {
		return ToolResult{}, err
	}
	var result ToolResult
	if err = session.request("tools/call", map[string]any{"name": toolName, "arguments": arguments}, &result); err != nil {
		return ToolResult{}, withStderr(err, stderrBuffer.String())
	}
	if result.Content == nil {
		return ToolResult{}, fmt.Errorf("%w: tool result content is required", ErrProtocol)
	}
	if tool.OutputSchema != nil {
		if result.StructuredContent == nil {
			return ToolResult{}, fmt.Errorf("%w: structuredContent is required by outputSchema", ErrProtocol)
		}
		if err = validateObject(result.StructuredContent, tool.OutputSchema); err != nil {
			return ToolResult{}, fmt.Errorf("%w: invalid structuredContent: %v", ErrProtocol, err)
		}
	}
	if result.IsError {
		return result, ErrToolExecution
	}
	return result, nil
}

func (s *stdioSession) initialize() error {
	var result initializeResult
	if err := s.request("initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name": "ai-companion", "title": "伴AI", "version": "0.1.0", "description": "Bounded Skill Runtime MCP client",
		},
	}, &result); err != nil {
		return err
	}
	if result.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported negotiated version %q", ErrProtocol, result.ProtocolVersion)
	}
	if _, supported := result.Capabilities["tools"]; !supported {
		return fmt.Errorf("%w: server did not declare tools capability", ErrProtocol)
	}
	return s.notify("notifications/initialized", nil)
}

func (s *stdioSession) findTool(name string) (Tool, error) {
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 10; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result listToolsResult
		if err := s.request("tools/list", params, &result); err != nil {
			return Tool{}, err
		}
		for _, tool := range result.Tools {
			if !namePattern.MatchString(tool.Name) || tool.InputSchema == nil || seen[tool.Name] {
				return Tool{}, fmt.Errorf("%w: invalid or duplicate remote tool", ErrProtocol)
			}
			seen[tool.Name] = true
			if len(seen) > 256 {
				return Tool{}, fmt.Errorf("%w: remote tool limit exceeded", ErrProtocol)
			}
			if tool.Name == name {
				return tool, nil
			}
		}
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	return Tool{}, ErrToolNotAllowed
}

func (s *stdioSession) request(method string, params any, target any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := s.nextID
	if err := s.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return fmt.Errorf("%w: write %s request: %v", ErrProtocol, method, err)
	}
	for messages := 0; messages < 64; messages++ {
		var envelope rpcEnvelope
		if err := s.decoder.Decode(&envelope); err != nil {
			return fmt.Errorf("%w: read %s response: %v", ErrProtocol, method, err)
		}
		if envelope.JSONRPC != "2.0" {
			return fmt.Errorf("%w: invalid jsonrpc version", ErrProtocol)
		}
		if envelope.Method != "" {
			if len(envelope.ID) > 0 && string(envelope.ID) != "null" {
				return fmt.Errorf("%w: server requests are not enabled", ErrProtocol)
			}
			continue
		}
		var responseID int64
		if err := json.Unmarshal(envelope.ID, &responseID); err != nil || responseID != id {
			return fmt.Errorf("%w: response id mismatch", ErrProtocol)
		}
		if envelope.Error != nil {
			return fmt.Errorf("%w: rpc %d: %s", ErrProtocol, envelope.Error.Code, truncate(envelope.Error.Message, 300))
		}
		if len(envelope.Result) == 0 {
			return fmt.Errorf("%w: missing result", ErrProtocol)
		}
		if err := json.Unmarshal(envelope.Result, target); err != nil {
			return fmt.Errorf("%w: decode %s result: %v", ErrProtocol, method, err)
		}
		return nil
	}
	return fmt.Errorf("%w: too many unsolicited messages", ErrProtocol)
}

func (s *stdioSession) notify(method string, params any) error {
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		message["params"] = params
	}
	if err := s.encoder.Encode(message); err != nil {
		return fmt.Errorf("%w: write notification: %v", ErrProtocol, err)
	}
	return nil
}

func validateObject(input map[string]any, schema map[string]any) error {
	if schema == nil || schema["type"] != "object" {
		return fmt.Errorf("%w: only object schemas are supported", ErrProtocol)
	}
	required, _ := schema["required"].([]any)
	properties, _ := schema["properties"].(map[string]any)
	for _, raw := range required {
		field, ok := raw.(string)
		if !ok {
			return fmt.Errorf("%w: invalid required field", ErrProtocol)
		}
		if _, exists := input[field]; !exists {
			return fmt.Errorf("%w: missing argument %s", ErrToolNotAllowed, field)
		}
	}
	for field, value := range input {
		raw, exists := properties[field]
		if !exists {
			if additional, explicit := schema["additionalProperties"].(bool); explicit && !additional {
				return fmt.Errorf("%w: unknown argument %s", ErrToolNotAllowed, field)
			}
			continue
		}
		definition, _ := raw.(map[string]any)
		if !matchesType(value, definition["type"]) {
			return fmt.Errorf("%w: invalid type for %s", ErrToolNotAllowed, field)
		}
	}
	return nil
}

func matchesType(value any, expected any) bool {
	switch expected {
	case nil:
		return true
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		switch value.(type) {
		case float64, int, int64:
			return true
		default:
			return false
		}
	case "integer":
		switch number := value.(type) {
		case float64:
			return number == float64(int64(number))
		case int, int64:
			return true
		default:
			return false
		}
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

type countingReader struct {
	reader io.Reader
	read   int
	limit  int
}

func (r *countingReader) Read(data []byte) (int, error) {
	if r.read >= r.limit {
		return 0, errors.New("MCP stdout limit exceeded")
	}
	if len(data) > r.limit-r.read {
		data = data[:r.limit-r.read]
	}
	count, err := r.reader.Read(data)
	r.read += count
	return count, err
}

type limitedBuffer struct {
	mu    sync.Mutex
	data  strings.Builder
	limit int
}

func (w *limitedBuffer) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(data)
	remaining := w.limit - w.data.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = w.data.Write(data)
	}
	return original, nil
}

func (w *limitedBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}

func withStderr(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%w; server stderr: %s", err, truncate(stderr, 300))
}

func truncate(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}
