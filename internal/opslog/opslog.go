package opslog

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

var ErrValidation = errors.New("invalid system log filter")

// Entry is a redacted, queryable operational log record. Message and
// Attributes must never contain prompt bodies, credentials, or request bodies.
type Entry struct {
	ID           uint64          `json:"id"`
	OccurredAt   time.Time       `json:"occurred_at"`
	Service      string          `json:"service"`
	Environment  string          `json:"environment"`
	Level        string          `json:"level"`
	Event        string          `json:"event"`
	Message      string          `json:"message"`
	TraceID      string          `json:"trace_id,omitempty"`
	SpanID       string          `json:"span_id,omitempty"`
	TraceFlags   string          `json:"trace_flags,omitempty"`
	RunID        string          `json:"run_id,omitempty"`
	AgentTraceID string          `json:"agent_trace_id,omitempty"`
	Node         string          `json:"node,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	Attributes   json.RawMessage `json:"attributes"`
}

type Filter struct {
	Since    time.Time
	Until    time.Time
	Service  string
	Level    string
	Query    string
	TraceID  string
	RunID    string
	BeforeID uint64
	Limit    int
}

type Summary struct {
	Total    int64 `json:"total"`
	Errors   int64 `json:"errors"`
	Warnings int64 `json:"warnings"`
	Services int64 `json:"services"`
}

type Page struct {
	Items        []Entry  `json:"items"`
	NextBeforeID uint64   `json:"next_before_id,omitempty"`
	Summary      Summary  `json:"summary"`
	Services     []string `json:"services"`
	Source       string   `json:"source,omitempty"`
	Degraded     bool     `json:"degraded,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
}

type Store interface {
	AppendSystemLog(context.Context, Entry) error
	QuerySystemLogs(context.Context, Filter) (Page, error)
}

// MemoryStore keeps the API usable for development and makes the query
// contract testable without a database. PostgreSQL is used in deployed modes.
type MemoryStore struct {
	mu      sync.RWMutex
	nextID  uint64
	entries []Entry
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{nextID: 1} }

func (s *MemoryStore) AppendSystemLog(_ context.Context, entry Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.ID == 0 {
		entry.ID = s.nextID
		s.nextID++
	}
	if len(entry.Attributes) == 0 {
		entry.Attributes = json.RawMessage(`{}`)
	}
	if entry.AgentTraceID == "" && entry.RunID != "" {
		entry.AgentTraceID = entry.TraceID
		if entry.AgentTraceID == "" {
			entry.AgentTraceID = agentTraceID(entry.RunID)
		}
	}
	s.entries = append(s.entries, entry)
	return nil
}

func (s *MemoryStore) QuerySystemLogs(_ context.Context, filter Filter) (Page, error) {
	filter, err := NormalizeFilter(filter, time.Now().UTC())
	if err != nil {
		return Page{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	matched := make([]Entry, 0, len(s.entries))
	services := map[string]struct{}{}
	matchedServices := map[string]struct{}{}
	var summary Summary
	for _, entry := range s.entries {
		if entry.OccurredAt.Before(filter.Since) || entry.OccurredAt.After(filter.Until) {
			continue
		}
		services[entry.Service] = struct{}{}
		if !matches(entry, filter) {
			continue
		}
		matchedServices[entry.Service] = struct{}{}
		summary.Total++
		if entry.Level == "ERROR" {
			summary.Errors++
		}
		if entry.Level == "WARN" {
			summary.Warnings++
		}
		matched = append(matched, entry)
	}
	summary.Services = int64(len(matchedServices))
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID > matched[j].ID })
	page := Page{Summary: summary, Services: sortedKeys(services), Items: []Entry{}, Source: "memory"}
	for _, entry := range matched {
		if filter.BeforeID > 0 && entry.ID >= filter.BeforeID {
			continue
		}
		if len(page.Items) == filter.Limit {
			page.NextBeforeID = page.Items[len(page.Items)-1].ID
			break
		}
		page.Items = append(page.Items, entry)
	}
	return page, nil
}

func NormalizeFilter(filter Filter, now time.Time) (Filter, error) {
	filter.Service = strings.TrimSpace(filter.Service)
	filter.Level = strings.ToUpper(strings.TrimSpace(filter.Level))
	filter.Query = strings.TrimSpace(filter.Query)
	filter.TraceID = strings.TrimSpace(filter.TraceID)
	filter.RunID = strings.TrimSpace(filter.RunID)
	if filter.Until.IsZero() {
		filter.Until = now
	}
	if filter.Since.IsZero() {
		filter.Since = filter.Until.Add(-time.Hour)
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 500 {
		filter.Limit = 500
	}
	if filter.Level != "" && filter.Level != "DEBUG" && filter.Level != "INFO" && filter.Level != "WARN" && filter.Level != "ERROR" {
		return Filter{}, ErrValidation
	}
	if !filter.Since.Before(filter.Until) || filter.Until.Sub(filter.Since) > 31*24*time.Hour || len(filter.Service) > 128 || len(filter.Query) > 200 || len(filter.TraceID) > 128 || len(filter.RunID) > 128 {
		return Filter{}, ErrValidation
	}
	return filter, nil
}

func matches(entry Entry, filter Filter) bool {
	if filter.Service != "" && entry.Service != filter.Service {
		return false
	}
	if filter.Level != "" && entry.Level != filter.Level {
		return false
	}
	if filter.TraceID != "" && entry.TraceID != filter.TraceID && entry.AgentTraceID != filter.TraceID {
		return false
	}
	if filter.RunID != "" && entry.RunID != filter.RunID {
		return false
	}
	if filter.Query != "" {
		haystack := strings.ToLower(entry.Event + " " + entry.Message + " " + entry.ErrorCode + " " + string(entry.Attributes))
		if !strings.Contains(haystack, strings.ToLower(filter.Query)) {
			return false
		}
	}
	return true
}

func sortedKeys(values map[string]struct{}) []string {
	items := make([]string, 0, len(values))
	for value := range values {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

func agentTraceID(runID string) string { return tracectx.AgentRunTraceID(runID) }
