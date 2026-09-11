package tracectx

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	TraceParentHeader = "traceparent"
	TraceStateHeader  = "tracestate"
	LegacyTraceHeader = "X-Trace-ID"
)

type contextKey uint8

const runIDKey contextKey = iota

var (
	w3cTraceContext propagation.TraceContext
	fallbackNonce   atomic.Uint64
)

// ExtractHTTP extracts an upstream W3C Trace Context and creates the local
// server context. A legacy X-Trace-ID is accepted only when traceparent is not
// present and is normalized into a standards-compliant 128-bit trace ID.
func ExtractHTTP(ctx context.Context, header http.Header) context.Context {
	extracted := w3cTraceContext.Extract(ctx, propagation.HeaderCarrier(header))
	if trace.SpanContextFromContext(extracted).IsValid() {
		return Child(extracted)
	}
	if legacy := strings.TrimSpace(header.Get(LegacyTraceHeader)); ValidInput(legacy) {
		return WithID(ctx, legacy)
	}
	return New(ctx)
}

// InjectHTTP writes the canonical W3C propagation headers. X-Trace-ID remains
// available as a temporary compatibility and log-search header.
func InjectHTTP(ctx context.Context, header http.Header) {
	w3cTraceContext.Inject(ctx, propagation.HeaderCarrier(header))
	if traceID := ID(ctx); traceID != "" {
		header.Set(LegacyTraceHeader, traceID)
	}
}

// ExtractMap extracts a remote W3C context from a transport-neutral carrier.
func ExtractMap(ctx context.Context, carrier map[string]string) context.Context {
	return w3cTraceContext.Extract(ctx, propagation.MapCarrier(carrier))
}

func InjectMap(ctx context.Context, carrier map[string]string) {
	w3cTraceContext.Inject(ctx, propagation.MapCarrier(carrier))
}

// FromPropagation reconstructs a remote context from durable event metadata.
// traceparent is authoritative. traceID is a compatibility fallback for events
// created before W3C context persistence was introduced.
func FromPropagation(ctx context.Context, traceParent, traceState, traceID string) (context.Context, bool) {
	traceParent = strings.TrimSpace(traceParent)
	traceState = strings.TrimSpace(traceState)
	traceID = strings.TrimSpace(traceID)
	if traceParent != "" {
		carrier := propagation.MapCarrier{TraceParentHeader: traceParent}
		if traceState != "" {
			carrier[TraceStateHeader] = traceState
		}
		extracted := ExtractMap(ctx, carrier)
		spanContext := trace.SpanContextFromContext(extracted)
		if !spanContext.IsValid() {
			return ctx, false
		}
		if traceID != "" {
			normalized, ok := NormalizeID(traceID)
			if !ok || normalized != spanContext.TraceID().String() {
				return ctx, false
			}
		}
		return extracted, true
	}
	if traceID == "" || !ValidInput(traceID) {
		return ctx, false
	}
	return WithID(ctx, traceID), true
}

// New creates a sampled local root SpanContext. This package is responsible
// for propagation and correlation; an SDK/exporter can be registered without
// changing this transport contract.
func New(ctx context.Context) context.Context {
	return contextWithSpanContext(ctx, newTraceID(), newSpanID(), trace.FlagsSampled, trace.TraceState{}, false)
}

// Child keeps the trace identity and trace state while rotating the span ID for
// a new local HTTP, producer, consumer, or worker hop.
func Child(ctx context.Context) context.Context {
	parent := trace.SpanContextFromContext(ctx)
	if !parent.IsValid() {
		return New(ctx)
	}
	return contextWithSpanContext(ctx, parent.TraceID(), newSpanID(), parent.TraceFlags(), parent.TraceState(), false)
}

// WithID attaches a canonical trace identifier. Valid legacy identifiers are
// deterministically normalized so old clients and pre-migration outbox rows can
// join the new W3C propagation chain.
func WithID(ctx context.Context, traceID string) context.Context {
	normalized, ok := NormalizeID(traceID)
	if !ok {
		return ctx
	}
	parsed, err := trace.TraceIDFromHex(normalized)
	if err != nil || !parsed.IsValid() {
		return ctx
	}
	return contextWithSpanContext(ctx, parsed, newSpanID(), trace.FlagsSampled, trace.TraceState{}, false)
}

func ID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

func SpanID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.SpanID().String()
}

func TraceParent(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return fmt.Sprintf("00-%s-%s-%02x", spanContext.TraceID(), spanContext.SpanID(), byte(spanContext.TraceFlags()))
}

func TraceState(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceState().String()
}

func TraceFlags(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return fmt.Sprintf("%02x", byte(spanContext.TraceFlags()))
}

func WithRunID(ctx context.Context, runID string) context.Context {
	runID = strings.TrimSpace(runID)
	if !ValidRunID(runID) {
		return ctx
	}
	return context.WithValue(ctx, runIDKey, runID)
}

func RunID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(runIDKey).(string)
	if !ValidRunID(value) {
		return ""
	}
	return value
}

func ValidRunID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

// AgentRunTraceID is the deterministic fallback used only when a reconciled
// Agent Run has no propagated request context.
func AgentRunTraceID(runID string) string {
	runID = strings.TrimSpace(runID)
	if !ValidRunID(runID) {
		return ""
	}
	digest := sha256.Sum256([]byte("agent-run:" + runID))
	return hex.EncodeToString(digest[:16])
}

// Valid accepts only a W3C/OTel 128-bit trace ID.
func Valid(value string) bool {
	parsed, err := trace.TraceIDFromHex(strings.ToLower(strings.TrimSpace(value)))
	return err == nil && parsed.IsValid()
}

// ValidInput includes the bounded legacy form accepted at compatibility
// boundaries. New durable data and responses always use Valid's canonical form.
func ValidInput(value string) bool {
	value = strings.TrimSpace(value)
	if Valid(value) {
		return true
	}
	if len(value) < 16 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func NormalizeID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if Valid(value) {
		return strings.ToLower(value), true
	}
	if !ValidInput(value) {
		return "", false
	}
	digest := sha256.Sum256([]byte("legacy-trace:" + value))
	return hex.EncodeToString(digest[:16]), true
}

func contextWithSpanContext(ctx context.Context, traceID trace.TraceID, spanID trace.SpanID, flags trace.TraceFlags, state trace.TraceState, remote bool) context.Context {
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: flags, TraceState: state, Remote: remote,
	})
	if remote {
		return trace.ContextWithRemoteSpanContext(ctx, spanContext)
	}
	return trace.ContextWithSpanContext(ctx, spanContext)
}

func newTraceID() trace.TraceID {
	var value trace.TraceID
	if _, err := rand.Read(value[:]); err == nil && value.IsValid() {
		return value
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("trace:%d:%d", time.Now().UnixNano(), fallbackNonce.Add(1))))
	copy(value[:], digest[:16])
	return value
}

func newSpanID() trace.SpanID {
	var value trace.SpanID
	if _, err := rand.Read(value[:]); err == nil && value.IsValid() {
		return value
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("span:%d:%d", time.Now().UnixNano(), fallbackNonce.Add(1))))
	copy(value[:], digest[:8])
	return value
}
