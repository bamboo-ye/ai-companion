package opslog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

const (
	maxAttributeCount = 64
	maxStringLength   = 2048
)

var (
	nonEventCharacter = regexp.MustCompile(`[^a-z0-9]+`)
	bearerValue       = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]{8,}`)
	keyLikeValue      = regexp.MustCompile(`(?i)\b(?:sk|pk|key|token)[-_][a-z0-9_-]{8,}\b`)
	emailValue        = regexp.MustCompile(`(?i)\b[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9.-]+\.[a-z]{2,}\b`)
	urlCredentials    = regexp.MustCompile(`(?i)(https?://)[^\s/:@]+:[^\s/@]+@`)
)

type captureHandler struct {
	base        slog.Handler
	store       Store
	service     string
	environment string
	attrs       []slog.Attr
	groups      []string
}

// NewLogger writes one canonical JSON record to the base handler and optionally
// mirrors it to the durable operational-log store. Both paths receive exactly
// the same redacted attributes and correlation identifiers.
func NewLogger(base slog.Handler, store Store, service, environment string) *slog.Logger {
	return slog.New(&captureHandler{
		base: base, store: store,
		service: strings.TrimSpace(service), environment: strings.TrimSpace(environment),
	})
}

func (h *captureHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *captureHandler) Handle(ctx context.Context, record slog.Record) error {
	attributes := make(map[string]any, len(h.attrs)+record.NumAttrs())
	for _, attr := range h.attrs {
		collectAttribute(attributes, strings.Join(h.groups, "."), attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		collectAttribute(attributes, strings.Join(h.groups, "."), attr)
		return len(attributes) < maxAttributeCount
	})
	if _, exists := attributes["trace_id"]; !exists {
		if traceID := tracectx.ID(ctx); traceID != "" {
			attributes["trace_id"] = traceID
		}
	}
	if _, exists := attributes["span_id"]; !exists {
		if spanID := tracectx.SpanID(ctx); spanID != "" {
			attributes["span_id"] = spanID
		}
	}
	if _, exists := attributes["trace_flags"]; !exists {
		if flags := tracectx.TraceFlags(ctx); flags != "" {
			attributes["trace_flags"] = flags
		}
	}
	if _, exists := attributes["run_id"]; !exists {
		if runID := tracectx.RunID(ctx); runID != "" {
			attributes["run_id"] = runID
		}
	}
	if traceID := stringAttribute(attributes, "trace_id"); traceID != "" && !tracectx.Valid(traceID) {
		delete(attributes, "trace_id")
	}
	if runID := stringAttribute(attributes, "run_id"); runID != "" && !tracectx.ValidRunID(runID) {
		delete(attributes, "run_id")
	}
	if runID := stringAttribute(attributes, "run_id"); runID != "" {
		agentTraceID := stringAttribute(attributes, "trace_id")
		if agentTraceID == "" {
			agentTraceID = tracectx.AgentRunTraceID(runID)
		}
		if agentTraceID != "" {
			attributes["agent_trace_id"] = agentTraceID
		}
	}
	attributes["service"] = h.service
	attributes["environment"] = h.environment
	if stringAttribute(attributes, "event") == "" {
		attributes["event"] = eventName(record.Message)
	} else {
		attributes["event"] = truncate(redactText(stringAttribute(attributes, "event")), 128)
	}

	message := redactText(truncate(record.Message, 512))
	structured := slog.NewRecord(record.Time, record.Level, message, record.PC)
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		structured.AddAttrs(slog.Any(key, attributes[key]))
	}
	baseErr := h.base.Handle(ctx, structured)
	entry := Entry{
		OccurredAt:   record.Time.UTC(),
		Service:      h.service,
		Environment:  h.environment,
		Level:        levelName(record.Level),
		Message:      message,
		Event:        stringAttribute(attributes, "event"),
		TraceID:      stringAttribute(attributes, "trace_id"),
		SpanID:       stringAttribute(attributes, "span_id"),
		TraceFlags:   stringAttribute(attributes, "trace_flags"),
		RunID:        stringAttribute(attributes, "run_id"),
		AgentTraceID: stringAttribute(attributes, "agent_trace_id"),
		Node:         firstStringAttribute(attributes, "node", "graph_node"),
		ErrorCode:    stringAttribute(attributes, "error_code"),
	}
	if entry.OccurredAt.IsZero() {
		entry.OccurredAt = time.Now().UTC()
	}
	encoded, err := json.Marshal(attributes)
	if err != nil || len(encoded) > 16*1024 {
		encoded = json.RawMessage(`{"attributes_truncated":true}`)
	}
	entry.Attributes = encoded
	if h.store != nil {
		writeCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_ = h.store.AppendSystemLog(writeCtx, entry)
	}
	return baseErr
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cloned := *h
	cloned.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &cloned
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	cloned := *h
	cloned.groups = append(append([]string{}, h.groups...), name)
	return &cloned
}

func collectAttribute(target map[string]any, prefix string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	key := attr.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	if attr.Value.Kind() == slog.KindGroup {
		for _, child := range attr.Value.Group() {
			collectAttribute(target, key, child)
		}
		return
	}
	if sensitiveKey(key) {
		target[key] = "[REDACTED]"
		return
	}
	target[key] = attributeValue(attr.Value)
}

func attributeValue(value slog.Value) any {
	switch value.Kind() {
	case slog.KindString:
		return redactText(truncate(value.String(), maxStringLength))
	case slog.KindDuration:
		return value.Duration().Milliseconds()
	case slog.KindTime:
		return value.Time().UTC().Format(time.RFC3339Nano)
	case slog.KindAny:
		if err, ok := value.Any().(error); ok {
			return redactText(truncate(err.Error(), maxStringLength))
		}
		return sanitizeAny(value.Any(), 0)
	default:
		return value.Any()
	}
}

func sanitizeAny(value any, depth int) any {
	if depth > 6 {
		return "[TRUNCATED]"
	}
	switch typed := value.(type) {
	case nil, bool, json.Number:
		return typed
	case string:
		return redactText(truncate(typed, maxStringLength))
	case float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return typed
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	case time.Duration:
		return typed.Milliseconds()
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(typed, &decoded) == nil {
			return sanitizeAny(decoded, depth+1)
		}
		return "[INVALID_JSON]"
	case []byte:
		return fmt.Sprintf("[BINARY:%d]", len(typed))
	case []any:
		limit := len(typed)
		if limit > maxAttributeCount {
			limit = maxAttributeCount
		}
		items := make([]any, 0, limit)
		for _, item := range typed[:limit] {
			items = append(items, sanitizeAny(item, depth+1))
		}
		return items
	case map[string]any:
		return sanitizeMap(typed, depth+1)
	}
	encoded, err := json.Marshal(value)
	if err == nil && len(encoded) <= 16*1024 {
		var decoded any
		if json.Unmarshal(encoded, &decoded) == nil {
			return sanitizeAny(decoded, depth+1)
		}
	}
	return fmt.Sprintf("[%T]", value)
}

func sanitizeMap(values map[string]any, depth int) map[string]any {
	clean := make(map[string]any, min(len(values), maxAttributeCount))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > maxAttributeCount {
		keys = keys[:maxAttributeCount]
	}
	for _, key := range keys {
		if sensitiveKey(key) {
			clean[key] = "[REDACTED]"
			continue
		}
		clean[key] = sanitizeAny(values[key], depth+1)
	}
	return clean
}

func redactText(value string) string {
	value = bearerValue.ReplaceAllString(value, "[REDACTED]")
	value = keyLikeValue.ReplaceAllString(value, "[REDACTED]")
	value = emailValue.ReplaceAllString(value, "[REDACTED_EMAIL]")
	return urlCredentials.ReplaceAllString(value, `${1}[REDACTED]@`)
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), ".", "_"))
	for _, exact := range []string{"authorization", "cookie", "set_cookie", "password", "passwd", "secret", "api_key", "access_token", "refresh_token", "operator_token", "gateway_token", "confirmation_token", "prompt", "content", "response", "input", "output", "body", "payload", "envelope"} {
		if key == exact || strings.HasSuffix(key, "_"+exact) {
			return true
		}
	}
	return strings.HasSuffix(key, "_password") || strings.HasSuffix(key, "_secret") || strings.HasSuffix(key, "_api_key") || strings.HasSuffix(key, "_token")
}

func stringAttribute(attributes map[string]any, key string) string {
	value, ok := attributes[key]
	if !ok {
		return ""
	}
	return truncate(fmt.Sprint(value), 256)
}

func firstStringAttribute(attributes map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringAttribute(attributes, key); value != "" {
			return value
		}
	}
	return ""
}

func eventName(message string) string {
	value := nonEventCharacter.ReplaceAllString(strings.ToLower(strings.TrimSpace(message)), ".")
	value = strings.Trim(value, ".")
	if value == "" {
		return "log"
	}
	return truncate(value, 128)
}

func levelName(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARN"
	case level >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

func truncate(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum] + "…"
}
