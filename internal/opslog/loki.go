package opslog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

const maxLokiResponseBytes = 8 << 20

var ErrLokiUnavailable = errors.New("loki unavailable")

type QueryStore interface {
	QuerySystemLogs(context.Context, Filter) (Page, error)
}

type LokiOptions struct {
	BaseURL     string
	Environment string
	Job         string
	TenantID    string
	BearerToken string
	Timeout     time.Duration
	HTTPClient  *http.Client
}

type LokiStore struct {
	baseURL     *url.URL
	environment string
	job         string
	tenantID    string
	bearerToken string
	client      *http.Client
}

func NewLokiStore(options LokiOptions) (*LokiStore, error) {
	rawURL := strings.TrimSpace(options.BaseURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("%w: invalid base URL", ErrValidation)
	}
	job := strings.TrimSpace(options.Job)
	if job == "" {
		job = "ai-companion"
	}
	if len(job) > 128 || len(options.Environment) > 128 || len(options.TenantID) > 256 {
		return nil, ErrValidation
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &LokiStore{
		baseURL: parsed, environment: strings.TrimSpace(options.Environment), job: job,
		tenantID: strings.TrimSpace(options.TenantID), bearerToken: strings.TrimSpace(options.BearerToken),
		client: client,
	}, nil
}

func (s *LokiStore) QuerySystemLogs(ctx context.Context, filter Filter) (Page, error) {
	if s == nil || s.client == nil || s.baseURL == nil {
		return Page{}, ErrLokiUnavailable
	}
	filter, err := NormalizeFilter(filter, time.Now().UTC())
	if err != nil {
		return Page{}, err
	}
	queryURL := *s.baseURL
	queryURL.Path = strings.TrimRight(queryURL.Path, "/") + "/loki/api/v1/query_range"
	values := queryURL.Query()
	values.Set("query", s.logQL(filter))
	values.Set("start", strconv.FormatInt(filter.Since.UnixNano(), 10))
	end := filter.Until.UnixNano()
	if filter.BeforeID > 0 && filter.BeforeID <= math.MaxInt64/1000 {
		end = int64(filter.BeforeID*1000 - 1)
	}
	values.Set("end", strconv.FormatInt(end, 10))
	values.Set("direction", "backward")
	values.Set("limit", strconv.Itoa(filter.Limit+1))
	queryURL.RawQuery = values.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL.String(), nil)
	if err != nil {
		return Page{}, fmt.Errorf("%w: build query", ErrLokiUnavailable)
	}
	request.Header.Set("Accept", "application/json")
	if s.tenantID != "" {
		request.Header.Set("X-Scope-OrgID", s.tenantID)
	}
	if s.bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+s.bearerToken)
	}
	tracectx.InjectHTTP(ctx, request.Header)
	response, err := s.client.Do(request)
	if err != nil {
		return Page{}, fmt.Errorf("%w: query failed", ErrLokiUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return Page{}, fmt.Errorf("%w: status %d", ErrLokiUnavailable, response.StatusCode)
	}
	var payload lokiQueryResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxLokiResponseBytes+1))
	if err = decoder.Decode(&payload); err != nil || payload.Status != "success" || payload.Data.ResultType != "streams" {
		return Page{}, fmt.Errorf("%w: invalid response", ErrLokiUnavailable)
	}
	page := Page{Items: []Entry{}, Services: []string{}, Source: "loki"}
	serviceSet := map[string]struct{}{}
	for _, stream := range payload.Data.Result {
		for _, rawValue := range stream.Values {
			entry, ok := decodeLokiEntry(stream.Stream, rawValue)
			if !ok || !matches(entry, filter) {
				continue
			}
			page.Items = append(page.Items, entry)
			serviceSet[entry.Service] = struct{}{}
		}
	}
	sort.Slice(page.Items, func(i, j int) bool {
		if page.Items[i].ID == page.Items[j].ID {
			return page.Items[i].Service < page.Items[j].Service
		}
		return page.Items[i].ID > page.Items[j].ID
	})
	for _, entry := range page.Items {
		page.Summary.Total++
		if entry.Level == "ERROR" {
			page.Summary.Errors++
		}
		if entry.Level == "WARN" {
			page.Summary.Warnings++
		}
	}
	page.Summary.Services = int64(len(serviceSet))
	page.Services = sortedKeys(serviceSet)
	if len(page.Items) > filter.Limit {
		page.Items = page.Items[:filter.Limit]
		page.NextBeforeID = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

func (s *LokiStore) logQL(filter Filter) string {
	labels := []string{"job=" + strconv.Quote(s.job)}
	if s.environment != "" {
		labels = append(labels, "environment="+strconv.Quote(s.environment))
	}
	if filter.Service != "" {
		labels = append(labels, "service="+strconv.Quote(filter.Service))
	}
	if filter.Level != "" {
		labels = append(labels, "level="+strconv.Quote(filter.Level))
	}
	query := "{" + strings.Join(labels, ",") + "}"
	if filter.Query != "" {
		query += " |= " + strconv.Quote(filter.Query)
	}
	if filter.TraceID != "" || filter.RunID != "" {
		query += " | json"
	}
	if filter.TraceID != "" {
		quoted := strconv.Quote(filter.TraceID)
		query += " | trace_id = " + quoted + " or agent_trace_id = " + quoted
	}
	if filter.RunID != "" {
		query += " | run_id = " + strconv.Quote(filter.RunID)
	}
	return query
}

type lokiQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string   `json:"stream"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func decodeLokiEntry(stream map[string]string, raw []json.RawMessage) (Entry, bool) {
	if len(raw) < 2 {
		return Entry{}, false
	}
	var timestampText, line string
	if json.Unmarshal(raw[0], &timestampText) != nil || json.Unmarshal(raw[1], &line) != nil {
		return Entry{}, false
	}
	timestampNanos, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil || timestampNanos <= 0 {
		return Entry{}, false
	}
	id := uint64(timestampNanos / 1000)
	fields := map[string]any{}
	decoder := json.NewDecoder(bufio.NewReader(strings.NewReader(line)))
	decoder.UseNumber()
	structured := decoder.Decode(&fields) == nil
	service := cleanLokiField(stream["service"], 128)
	environment := cleanLokiField(stream["environment"], 128)
	level := strings.ToUpper(cleanLokiField(firstLokiString(fields, "level", stream["level"]), 16))
	if level != "DEBUG" && level != "INFO" && level != "WARN" && level != "ERROR" {
		level = "INFO"
	}
	message := line
	if structured {
		message = firstLokiString(fields, "msg", firstLokiString(fields, "message", ""))
	}
	message = redactText(truncate(message, 512))
	event := cleanLokiField(firstLokiString(fields, "event", ""), 128)
	if event == "" {
		event = eventName(message)
	}
	runID := cleanLokiField(firstLokiString(fields, "run_id", ""), 128)
	if !tracectx.ValidRunID(runID) {
		runID = ""
	}
	traceID := cleanLokiField(firstLokiString(fields, "trace_id", ""), 64)
	if !tracectx.Valid(traceID) {
		traceID = ""
	}
	spanID := cleanLokiField(firstLokiString(fields, "span_id", ""), 16)
	traceFlags := cleanLokiField(firstLokiString(fields, "trace_flags", ""), 2)
	agentID := cleanLokiField(firstLokiString(fields, "agent_trace_id", ""), 64)
	if !tracectx.Valid(agentID) {
		agentID = traceID
		if agentID == "" {
			agentID = agentTraceID(runID)
		}
	}
	attributes := sanitizeMap(fields, 0)
	encoded, err := json.Marshal(attributes)
	if err != nil || len(encoded) > 16*1024 {
		encoded = []byte(`{"attributes_truncated":true}`)
	}
	return Entry{
		ID: id, OccurredAt: time.Unix(0, timestampNanos).UTC(), Service: service,
		Environment: environment, Level: level, Event: event, Message: message,
		TraceID: traceID, SpanID: spanID, TraceFlags: traceFlags, RunID: runID, AgentTraceID: agentID,
		Node:      cleanLokiField(firstLokiString(fields, "node", firstLokiString(fields, "graph_node", "")), 128),
		ErrorCode: cleanLokiField(firstLokiString(fields, "error_code", ""), 128), Attributes: encoded,
	}, true
}

func firstLokiString(values map[string]any, key, fallback string) string {
	value, exists := values[key]
	if !exists || value == nil {
		return fallback
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func cleanLokiField(value string, maximum int) string {
	return redactText(truncate(strings.TrimSpace(value), maximum))
}

// QueryFallbackStore keeps PostgreSQL as the durable write/audit source while
// using Loki for interactive log reads. An unavailable or lagging collector is
// explicit in the response and never makes the operator log center unusable.
type QueryFallbackStore struct {
	durable Store
	loki    QueryStore
}

func NewQueryFallbackStore(durable Store, loki QueryStore) *QueryFallbackStore {
	return &QueryFallbackStore{durable: durable, loki: loki}
}

func (s *QueryFallbackStore) AppendSystemLog(ctx context.Context, entry Entry) error {
	if s == nil || s.durable == nil {
		return ErrLokiUnavailable
	}
	return s.durable.AppendSystemLog(ctx, entry)
}

func (s *QueryFallbackStore) QuerySystemLogs(ctx context.Context, filter Filter) (Page, error) {
	if s == nil || s.durable == nil {
		return Page{}, ErrLokiUnavailable
	}
	durablePage, durableErr := s.durable.QuerySystemLogs(ctx, filter)
	if s.loki == nil {
		return durablePage, durableErr
	}
	lokiPage, lokiErr := s.loki.QuerySystemLogs(ctx, filter)
	if lokiErr != nil || len(lokiPage.Items) == 0 && durableErr == nil && len(durablePage.Items) > 0 {
		if durableErr != nil {
			return Page{}, errors.Join(lokiErr, durableErr)
		}
		durablePage.Degraded = true
		return durablePage, nil
	}
	if durableErr == nil {
		lokiPage.Summary = durablePage.Summary
		lokiPage.Services = durablePage.Services
	}
	return lokiPage, nil
}
