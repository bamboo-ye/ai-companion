package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestRegistryRejectsUnsafeConfigurationAndToolsBeforeSpawn(t *testing.T) {
	if _, err := NewRegistry([]ServerConfig{{Name: "unsafe", Command: "sh", AllowedTools: []string{"safe.echo"}, TimeoutMS: 1000, Enabled: true}}); !errors.Is(err, ErrServerNotAllowed) {
		t.Fatalf("relative command error = %v", err)
	}
	registry, err := NewRegistry([]ServerConfig{{Name: "safe", Command: "/does/not/exist", AllowedTools: []string{"safe.echo"}, TimeoutMS: 1000, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Call(context.Background(), "safe", "danger.delete", map[string]any{}); !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("disallowed tool error = %v", err)
	}
	if _, err = registry.Call(context.Background(), "missing", "safe.echo", map[string]any{}); !errors.Is(err, ErrServerNotAllowed) {
		t.Fatalf("disallowed server error = %v", err)
	}
}

func TestParseRegistryRejectsTrailingJSON(t *testing.T) {
	if _, err := ParseRegistry(`[] []`); err == nil {
		t.Fatal("trailing JSON must fail")
	}
}

func TestStdioLifecycleDiscoverySchemaAndCall(t *testing.T) {
	registry := fixtureRegistry(t, ProtocolVersion, []string{"safe.echo"})
	result, err := registry.Call(context.Background(), "fixture", "safe.echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.StructuredContent["echo"] != "hello" || len(result.Content) != 1 {
		t.Fatalf("result = %#v", result)
	}
	servers := registry.Servers()
	if len(servers) != 1 || servers[0].Name != "fixture" || servers[0].ProtocolVersion != ProtocolVersion || strings.Join(servers[0].AllowedTools, ",") != "safe.echo" {
		t.Fatalf("servers = %#v", servers)
	}
}

func TestStdioRejectsUnadvertisedToolAndVersionMismatch(t *testing.T) {
	registry := fixtureRegistry(t, ProtocolVersion, []string{"missing.tool"})
	if _, err := registry.Call(context.Background(), "fixture", "missing.tool", map[string]any{}); !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("unadvertised tool error = %v", err)
	}
	registry = fixtureRegistry(t, "2025-06-18", []string{"safe.echo"})
	if _, err := registry.Call(context.Background(), "fixture", "safe.echo", map[string]any{"text": "hello"}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("version mismatch error = %v", err)
	}
}

func TestStdioValidatesArgumentsAgainstRemoteSchema(t *testing.T) {
	registry := fixtureRegistry(t, ProtocolVersion, []string{"safe.echo"})
	if _, err := registry.Call(context.Background(), "fixture", "safe.echo", map[string]any{"unexpected": true}); !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("schema validation error = %v", err)
	}
}

func fixtureRegistry(t *testing.T, version string, tools []string) *Registry {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry([]ServerConfig{{
		Name: "fixture", Command: executable, Args: []string{"-test.run=TestMCPFixtureProcess"},
		Environment:  map[string]string{"MCP_FIXTURE_PROCESS": "1", "MCP_FIXTURE_VERSION": version},
		AllowedTools: tools, TimeoutMS: 3000, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestMCPFixtureProcess(t *testing.T) {
	if os.Getenv("MCP_FIXTURE_PROCESS") != "1" {
		return
	}
	decoder := json.NewDecoder(bufio.NewReader(os.Stdin))
	encoder := json.NewEncoder(os.Stdout)
	var request map[string]any
	if decoder.Decode(&request) != nil || request["method"] != "initialize" {
		os.Exit(2)
	}
	version := os.Getenv("MCP_FIXTURE_VERSION")
	_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": map[string]any{
		"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fixture", "version": "1.0.0"},
	}})
	if version != ProtocolVersion {
		os.Exit(0)
	}
	if decoder.Decode(&request) != nil || request["method"] != "notifications/initialized" {
		os.Exit(3)
	}
	if decoder.Decode(&request) != nil || request["method"] != "tools/list" {
		os.Exit(4)
	}
	_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": map[string]any{"tools": []any{
		map[string]any{"name": "safe.echo", "description": "untrusted remote description", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}}, "outputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"echo"}, "properties": map[string]any{"echo": map[string]any{"type": "string"}}}},
		map[string]any{"name": "danger.delete", "inputSchema": map[string]any{"type": "object", "additionalProperties": false}},
	}}})
	if decoder.Decode(&request) != nil || request["method"] != "tools/call" {
		os.Exit(5)
	}
	params, _ := request["params"].(map[string]any)
	arguments, _ := params["arguments"].(map[string]any)
	text, _ := arguments["text"].(string)
	_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}}, "structuredContent": map[string]any{"echo": text}, "isError": false,
	}})
	os.Exit(0)
}
