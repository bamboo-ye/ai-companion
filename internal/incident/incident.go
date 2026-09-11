package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrValidation = errors.New("invalid incident input")
	ErrNotFound   = errors.New("incident resource not found")
	ErrConflict   = errors.New("incident state conflict")
)

type AlertRule struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	Service       string    `json:"service,omitempty"`
	Level         string    `json:"level"`
	EventPrefix   string    `json:"event_prefix,omitempty"`
	WindowMinutes int       `json:"window_minutes"`
	Threshold     int       `json:"threshold"`
	Severity      string    `json:"severity"`
	Enabled       bool      `json:"enabled"`
	CreatedBy     string    `json:"created_by"`
	UpdatedBy     string    `json:"updated_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Incident struct {
	ID              string          `json:"id"`
	RuleID          string          `json:"rule_id,omitempty"`
	RuleName        string          `json:"rule_name"`
	SourceType      string          `json:"source_type"`
	SourceID        string          `json:"source_id"`
	Status          string          `json:"status"`
	Severity        string          `json:"severity"`
	Title           string          `json:"title"`
	Summary         string          `json:"summary"`
	Service         string          `json:"service,omitempty"`
	Level           string          `json:"level"`
	EventPrefix     string          `json:"event_prefix,omitempty"`
	ObservedValue   int             `json:"observed_value"`
	Threshold       int             `json:"threshold"`
	WindowMinutes   int             `json:"window_minutes"`
	WindowStartedAt time.Time       `json:"window_started_at"`
	WindowEndedAt   time.Time       `json:"window_ended_at"`
	Evidence        json.RawMessage `json:"evidence"`
	OpenedAt        time.Time       `json:"opened_at"`
	AcknowledgedAt  *time.Time      `json:"acknowledged_at,omitempty"`
	AcknowledgedBy  string          `json:"acknowledged_by,omitempty"`
	ResolvedAt      *time.Time      `json:"resolved_at,omitempty"`
	ResolvedBy      string          `json:"resolved_by,omitempty"`
	Resolution      string          `json:"resolution,omitempty"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type IncidentFilter struct {
	Status   string
	Severity string
	Source   string
	Limit    int
}

type Evaluation struct {
	RulesEvaluated int `json:"rules_evaluated"`
	Triggered      int `json:"triggered"`
	Opened         int `json:"opened"`
	Updated        int `json:"updated"`
	Resolved       int `json:"resolved"`
}

type AlertSubscription struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	RuleID           string    `json:"rule_id,omitempty"`
	RuleName         string    `json:"rule_name,omitempty"`
	Channel          string    `json:"channel"`
	Target           string    `json:"target"`
	MinimumSeverity  string    `json:"minimum_severity"`
	NotifyOnOpen     bool      `json:"notify_on_open"`
	NotifyOnResolved bool      `json:"notify_on_resolved"`
	Enabled          bool      `json:"enabled"`
	CreatedBy        string    `json:"created_by"`
	UpdatedBy        string    `json:"updated_by"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type IncidentNotification struct {
	ID          string     `json:"id"`
	IncidentID  string     `json:"-"`
	Transition  string     `json:"transition"`
	Channel     string     `json:"channel"`
	Target      string     `json:"target"`
	DeliveryID  string     `json:"delivery_id,omitempty"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	FailureCode string     `json:"failure_code,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
}

type IncidentActivity struct {
	Action     string    `json:"action"`
	ActorType  string    `json:"actor_type"`
	Actor      string    `json:"actor"`
	Reason     string    `json:"reason,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type EvidenceLog struct {
	ID         int64     `json:"id"`
	OccurredAt time.Time `json:"occurred_at"`
	Service    string    `json:"service"`
	Level      string    `json:"level"`
	Event      string    `json:"event"`
	Message    string    `json:"message"`
	TraceID    string    `json:"trace_id,omitempty"`
	RunID      string    `json:"run_id,omitempty"`
	ErrorCode  string    `json:"error_code,omitempty"`
}

type EvidenceBundle struct {
	Incident      Incident               `json:"incident"`
	Activity      []IncidentActivity     `json:"activity"`
	Notifications []IncidentNotification `json:"notifications"`
	Logs          []EvidenceLog          `json:"logs"`
	GeneratedAt   time.Time              `json:"generated_at"`
}

type Store interface {
	ListAlertRules(context.Context) ([]AlertRule, error)
	CreateAlertRule(context.Context, AlertRule, string) (AlertRule, error)
	SetAlertRuleEnabled(context.Context, string, bool, string, string, time.Time) (AlertRule, error)
	CountAlertMatches(context.Context, AlertRule, time.Time, time.Time) (int, error)
	ReconcileAlertIncident(context.Context, AlertRule, int, time.Time) (*Incident, string, error)
	ListIncidents(context.Context, IncidentFilter) ([]Incident, error)
	GetIncident(context.Context, string) (Incident, error)
	SetIncidentStatus(context.Context, string, string, string, string, time.Time) (Incident, error)
	ListAlertSubscriptions(context.Context) ([]AlertSubscription, error)
	CreateAlertSubscription(context.Context, AlertSubscription, string) (AlertSubscription, error)
	SetAlertSubscriptionEnabled(context.Context, string, bool, string, string, time.Time) (AlertSubscription, error)
	ListIncidentNotifications(context.Context, string) ([]IncidentNotification, error)
	ListIncidentActivities(context.Context, string) ([]IncidentActivity, error)
	ListIncidentEvidenceLogs(context.Context, Incident, int) ([]EvidenceLog, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service { return &Service{store: store, now: time.Now} }

func (s *Service) Rules(ctx context.Context) ([]AlertRule, error) { return s.store.ListAlertRules(ctx) }

func (s *Service) CreateRule(ctx context.Context, rule AlertRule, actor, reason string) (AlertRule, error) {
	actor, reason = strings.TrimSpace(actor), strings.TrimSpace(reason)
	if err := normalizeRule(&rule); err != nil || actor == "" || reason == "" || len(reason) > 512 {
		return AlertRule{}, ErrValidation
	}
	generated, err := id.New()
	if err != nil {
		return AlertRule{}, err
	}
	now := s.now().UTC()
	rule.ID, rule.CreatedBy, rule.UpdatedBy, rule.CreatedAt, rule.UpdatedAt = generated, actor, actor, now, now
	return s.store.CreateAlertRule(ctx, rule, reason)
}

func (s *Service) SetRuleEnabled(ctx context.Context, ruleID string, enabled bool, actor, reason string) (AlertRule, error) {
	ruleID, actor, reason = strings.TrimSpace(ruleID), strings.TrimSpace(actor), strings.TrimSpace(reason)
	if ruleID == "" || actor == "" || reason == "" || len(reason) > 512 {
		return AlertRule{}, ErrValidation
	}
	return s.store.SetAlertRuleEnabled(ctx, ruleID, enabled, actor, reason, s.now().UTC())
}

func (s *Service) Incidents(ctx context.Context, filter IncidentFilter) ([]Incident, error) {
	filter.Status = strings.ToLower(strings.TrimSpace(filter.Status))
	filter.Severity = strings.ToLower(strings.TrimSpace(filter.Severity))
	filter.Source = strings.ToLower(strings.TrimSpace(filter.Source))
	if filter.Status != "" && filter.Status != "open" && filter.Status != "acknowledged" && filter.Status != "resolved" {
		return nil, ErrValidation
	}
	if filter.Severity != "" && filter.Severity != "warning" && filter.Severity != "critical" {
		return nil, ErrValidation
	}
	if filter.Source != "" && filter.Source != "log_rule" && filter.Source != "performance_budget" {
		return nil, ErrValidation
	}
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	return s.store.ListIncidents(ctx, filter)
}

func (s *Service) GetIncident(ctx context.Context, incidentID string) (Incident, error) {
	if strings.TrimSpace(incidentID) == "" {
		return Incident{}, ErrValidation
	}
	return s.store.GetIncident(ctx, incidentID)
}

func (s *Service) Acknowledge(ctx context.Context, incidentID, actor, reason string) (Incident, error) {
	return s.setIncidentStatus(ctx, incidentID, "acknowledged", actor, reason)
}

func (s *Service) Resolve(ctx context.Context, incidentID, actor, reason string) (Incident, error) {
	return s.setIncidentStatus(ctx, incidentID, "resolved", actor, reason)
}

func (s *Service) Subscriptions(ctx context.Context) ([]AlertSubscription, error) {
	return s.store.ListAlertSubscriptions(ctx)
}

func (s *Service) CreateSubscription(ctx context.Context, item AlertSubscription, actor, reason string) (AlertSubscription, error) {
	actor, reason = strings.TrimSpace(actor), strings.TrimSpace(reason)
	item.Name, item.RuleID = strings.TrimSpace(item.Name), strings.TrimSpace(item.RuleID)
	item.Channel, item.MinimumSeverity = strings.ToLower(strings.TrimSpace(item.Channel)), strings.ToLower(strings.TrimSpace(item.MinimumSeverity))
	item.Target = strings.ToLower(strings.TrimSpace(item.Target))
	targetValid := false
	if item.Channel == "email" {
		address, addressErr := mail.ParseAddress(item.Target)
		targetValid = addressErr == nil && address.Address == item.Target
	} else if item.Channel == "console" {
		targetValid = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{1,127}$`).MatchString(item.Target)
	}
	if item.Name == "" || len(item.Name) > 128 || !targetValid ||
		(item.MinimumSeverity != "warning" && item.MinimumSeverity != "critical") || (!item.NotifyOnOpen && !item.NotifyOnResolved) || actor == "" || reason == "" || len(reason) > 512 {
		return AlertSubscription{}, ErrValidation
	}
	generated, err := id.New()
	if err != nil {
		return AlertSubscription{}, err
	}
	now := s.now().UTC()
	item.ID, item.CreatedBy, item.UpdatedBy, item.CreatedAt, item.UpdatedAt = generated, actor, actor, now, now
	return s.store.CreateAlertSubscription(ctx, item, reason)
}

func (s *Service) SetSubscriptionEnabled(ctx context.Context, subscriptionID string, enabled bool, actor, reason string) (AlertSubscription, error) {
	subscriptionID, actor, reason = strings.TrimSpace(subscriptionID), strings.TrimSpace(actor), strings.TrimSpace(reason)
	if subscriptionID == "" || actor == "" || reason == "" || len(reason) > 512 {
		return AlertSubscription{}, ErrValidation
	}
	return s.store.SetAlertSubscriptionEnabled(ctx, subscriptionID, enabled, actor, reason, s.now().UTC())
}

func (s *Service) Notifications(ctx context.Context, incidentID string) ([]IncidentNotification, error) {
	if strings.TrimSpace(incidentID) == "" {
		return nil, ErrValidation
	}
	if _, err := s.store.GetIncident(ctx, incidentID); err != nil {
		return nil, err
	}
	return s.store.ListIncidentNotifications(ctx, incidentID)
}

func (s *Service) Evidence(ctx context.Context, incidentID string) (EvidenceBundle, error) {
	item, err := s.GetIncident(ctx, incidentID)
	if err != nil {
		return EvidenceBundle{}, err
	}
	activity, err := s.store.ListIncidentActivities(ctx, incidentID)
	if err != nil {
		return EvidenceBundle{}, err
	}
	notifications, err := s.store.ListIncidentNotifications(ctx, incidentID)
	if err != nil {
		return EvidenceBundle{}, err
	}
	logs, err := s.store.ListIncidentEvidenceLogs(ctx, item, 200)
	if err != nil {
		return EvidenceBundle{}, err
	}
	return EvidenceBundle{Incident: item, Activity: activity, Notifications: notifications, Logs: logs, GeneratedAt: s.now().UTC()}, nil
}

func (s *Service) setIncidentStatus(ctx context.Context, incidentID, status, actor, reason string) (Incident, error) {
	incidentID, actor, reason = strings.TrimSpace(incidentID), strings.TrimSpace(actor), strings.TrimSpace(reason)
	if incidentID == "" || actor == "" || reason == "" || len(reason) > 512 {
		return Incident{}, ErrValidation
	}
	return s.store.SetIncidentStatus(ctx, incidentID, status, actor, reason, s.now().UTC())
}

func (s *Service) Evaluate(ctx context.Context, now time.Time) (Evaluation, error) {
	rules, err := s.store.ListAlertRules(ctx)
	if err != nil {
		return Evaluation{}, err
	}
	report := Evaluation{}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		report.RulesEvaluated++
		count, countErr := s.store.CountAlertMatches(ctx, rule, now.Add(-time.Duration(rule.WindowMinutes)*time.Minute), now)
		if countErr != nil {
			return report, countErr
		}
		if count >= rule.Threshold {
			report.Triggered++
		}
		_, transition, reconcileErr := s.store.ReconcileAlertIncident(ctx, rule, count, now)
		if reconcileErr != nil {
			return report, reconcileErr
		}
		switch transition {
		case "opened":
			report.Opened++
		case "updated":
			report.Updated++
		case "resolved":
			report.Resolved++
		}
	}
	return report, nil
}

func normalizeRule(rule *AlertRule) error {
	rule.Name = strings.TrimSpace(rule.Name)
	rule.Description = strings.TrimSpace(rule.Description)
	rule.Service = strings.TrimSpace(rule.Service)
	rule.Level = strings.ToUpper(strings.TrimSpace(rule.Level))
	rule.EventPrefix = strings.ToLower(strings.TrimSpace(rule.EventPrefix))
	rule.Severity = strings.ToLower(strings.TrimSpace(rule.Severity))
	if rule.Name == "" || len(rule.Name) > 128 || len(rule.Description) > 512 || len(rule.Service) > 128 || len(rule.EventPrefix) > 128 || rule.WindowMinutes < 1 || rule.WindowMinutes > 1440 || rule.Threshold < 1 || rule.Threshold > 1_000_000 {
		return ErrValidation
	}
	if rule.Level != "DEBUG" && rule.Level != "INFO" && rule.Level != "WARN" && rule.Level != "ERROR" {
		return ErrValidation
	}
	if rule.Severity != "warning" && rule.Severity != "critical" {
		return ErrValidation
	}
	return nil
}

// MemoryStore supports isolated API tests and local memory mode.
type MemoryStore struct {
	mu            sync.RWMutex
	rules         []AlertRule
	incidents     []Incident
	counts        map[string]int
	subscriptions []AlertSubscription
	notifications []IncidentNotification
	activities    []IncidentActivity
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{counts: map[string]int{}} }

func (m *MemoryStore) SetCount(ruleID string, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[ruleID] = count
}

func (m *MemoryStore) ListAlertRules(_ context.Context) ([]AlertRule, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]AlertRule{}, m.rules...), nil
}

func (m *MemoryStore) CreateAlertRule(_ context.Context, rule AlertRule, _ string) (AlertRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules = append(m.rules, rule)
	return rule, nil
}

func (m *MemoryStore) SetAlertRuleEnabled(_ context.Context, ruleID string, enabled bool, actor, reason string, now time.Time) (AlertRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range m.rules {
		if m.rules[index].ID == ruleID {
			m.rules[index].Enabled, m.rules[index].UpdatedBy, m.rules[index].UpdatedAt = enabled, actor, now
			if !enabled {
				for incidentIndex := range m.incidents {
					item := &m.incidents[incidentIndex]
					if item.RuleID == ruleID && item.Status != "resolved" {
						item.Status, item.ResolvedAt, item.ResolvedBy, item.Resolution, item.UpdatedAt = "resolved", &now, actor, "rule disabled: "+reason, now
						m.activities = append(m.activities, IncidentActivity{Action: "incident.resolved", ActorType: "operator", Actor: actor, Reason: item.Resolution, OccurredAt: now})
						m.queueNotificationsLocked(*item, "resolved", now)
					}
				}
			}
			return m.rules[index], nil
		}
	}
	return AlertRule{}, ErrNotFound
}

func (m *MemoryStore) CountAlertMatches(_ context.Context, rule AlertRule, _, _ time.Time) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.counts[rule.ID], nil
}

func (m *MemoryStore) ReconcileAlertIncident(_ context.Context, rule AlertRule, count int, now time.Time) (*Incident, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range m.incidents {
		current := &m.incidents[index]
		if current.RuleID != rule.ID || current.Status == "resolved" {
			continue
		}
		if count >= rule.Threshold {
			current.ObservedValue, current.WindowStartedAt, current.WindowEndedAt, current.UpdatedAt = count, now.Add(-time.Duration(rule.WindowMinutes)*time.Minute), now, now
			copy := *current
			return &copy, "updated", nil
		}
		current.Status, current.ResolvedAt, current.ResolvedBy, current.Resolution, current.UpdatedAt = "resolved", &now, "system", "signal returned below threshold", now
		m.activities = append(m.activities, IncidentActivity{Action: "incident.auto_resolved", ActorType: "system", Actor: "alert-evaluator", Reason: current.Resolution, OccurredAt: now})
		m.queueNotificationsLocked(*current, "resolved", now)
		copy := *current
		return &copy, "resolved", nil
	}
	if count < rule.Threshold {
		return nil, "steady", nil
	}
	incidentID, _ := id.New()
	evidence, _ := json.Marshal(map[string]any{"source": "ops.system_logs", "service": rule.Service, "level": rule.Level, "event_prefix": rule.EventPrefix})
	item := Incident{ID: incidentID, RuleID: rule.ID, RuleName: rule.Name, SourceType: "log_rule", SourceID: rule.ID, Status: "open", Severity: rule.Severity, Title: rule.Name, Summary: fmt.Sprintf("%d 条日志达到阈值 %d", count, rule.Threshold), Service: rule.Service, Level: rule.Level, EventPrefix: rule.EventPrefix, ObservedValue: count, Threshold: rule.Threshold, WindowMinutes: rule.WindowMinutes, WindowStartedAt: now.Add(-time.Duration(rule.WindowMinutes) * time.Minute), WindowEndedAt: now, Evidence: evidence, OpenedAt: now, UpdatedAt: now}
	m.incidents = append(m.incidents, item)
	m.activities = append(m.activities, IncidentActivity{Action: "incident.opened", ActorType: "system", Actor: "alert-evaluator", Reason: "threshold reached", OccurredAt: now})
	m.queueNotificationsLocked(item, "opened", now)
	return &item, "opened", nil
}

func (m *MemoryStore) ListIncidents(_ context.Context, filter IncidentFilter) ([]Incident, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Incident, 0)
	for index := len(m.incidents) - 1; index >= 0 && len(items) < filter.Limit; index-- {
		item := m.incidents[index]
		if (filter.Status == "" || item.Status == filter.Status) && (filter.Severity == "" || item.Severity == filter.Severity) && (filter.Source == "" || item.SourceType == filter.Source) {
			items = append(items, item)
		}
	}
	return items, nil
}

func (m *MemoryStore) GetIncident(_ context.Context, incidentID string) (Incident, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, item := range m.incidents {
		if item.ID == incidentID {
			return item, nil
		}
	}
	return Incident{}, ErrNotFound
}

func (m *MemoryStore) SetIncidentStatus(_ context.Context, incidentID, status, actor, reason string, now time.Time) (Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range m.incidents {
		item := &m.incidents[index]
		if item.ID != incidentID {
			continue
		}
		if item.Status == "resolved" || (status == "acknowledged" && item.Status != "open") {
			return Incident{}, ErrConflict
		}
		item.Status, item.UpdatedAt = status, now
		if status == "acknowledged" {
			item.AcknowledgedAt, item.AcknowledgedBy = &now, actor
		} else {
			item.ResolvedAt, item.ResolvedBy, item.Resolution = &now, actor, reason
			m.queueNotificationsLocked(*item, "resolved", now)
		}
		m.activities = append(m.activities, IncidentActivity{Action: "incident." + status, ActorType: "operator", Actor: actor, Reason: reason, OccurredAt: now})
		return *item, nil
	}
	return Incident{}, ErrNotFound
}

func (m *MemoryStore) ListAlertSubscriptions(_ context.Context) ([]AlertSubscription, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]AlertSubscription{}, m.subscriptions...), nil
}

func (m *MemoryStore) CreateAlertSubscription(_ context.Context, item AlertSubscription, _ string) (AlertSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if item.RuleID != "" {
		found := false
		for _, rule := range m.rules {
			found = found || rule.ID == item.RuleID
		}
		if !found {
			return AlertSubscription{}, ErrNotFound
		}
	}
	for _, existing := range m.subscriptions {
		if existing.RuleID == item.RuleID && existing.Channel == item.Channel && existing.Target == item.Target {
			return AlertSubscription{}, ErrConflict
		}
	}
	m.subscriptions = append(m.subscriptions, item)
	return item, nil
}

func (m *MemoryStore) SetAlertSubscriptionEnabled(_ context.Context, subscriptionID string, enabled bool, actor, _ string, now time.Time) (AlertSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range m.subscriptions {
		if m.subscriptions[index].ID == subscriptionID {
			m.subscriptions[index].Enabled, m.subscriptions[index].UpdatedBy, m.subscriptions[index].UpdatedAt = enabled, actor, now
			return m.subscriptions[index], nil
		}
	}
	return AlertSubscription{}, ErrNotFound
}

func (m *MemoryStore) ListIncidentNotifications(_ context.Context, incidentID string) ([]IncidentNotification, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := []IncidentNotification{}
	for _, item := range m.notifications {
		if item.IncidentID == incidentID {
			items = append(items, item)
		}
	}
	return items, nil
}

func (m *MemoryStore) ListIncidentActivities(_ context.Context, _ string) ([]IncidentActivity, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]IncidentActivity{}, m.activities...), nil
}

func (m *MemoryStore) ListIncidentEvidenceLogs(_ context.Context, _ Incident, _ int) ([]EvidenceLog, error) {
	return []EvidenceLog{}, nil
}

func (m *MemoryStore) queueNotificationsLocked(item Incident, transition string, now time.Time) {
	for _, subscription := range m.subscriptions {
		if !subscription.Enabled || (subscription.RuleID != "" && subscription.RuleID != item.RuleID) ||
			(subscription.MinimumSeverity == "critical" && item.Severity != "critical") ||
			(transition == "opened" && !subscription.NotifyOnOpen) || (transition == "resolved" && !subscription.NotifyOnResolved) {
			continue
		}
		key := item.ID + ":" + subscription.ID + ":" + transition
		duplicate := false
		for _, existing := range m.notifications {
			duplicate = duplicate || existing.ID == key
		}
		if duplicate {
			continue
		}
		status := "queued"
		var sentAt *time.Time
		if subscription.Channel == "console" {
			status, sentAt = "sent", &now
		}
		target := subscription.Target
		if subscription.Channel == "email" {
			target = maskEmail(target)
		}
		m.notifications = append(m.notifications, IncidentNotification{ID: key, IncidentID: item.ID, Transition: transition, Channel: subscription.Channel, Target: target, DeliveryID: "memory:" + key, Status: status, CreatedAt: now, SentAt: sentAt})
	}
}

func maskEmail(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "@")
	if len(parts) != 2 || parts[0] == "" {
		return "***"
	}
	visible := parts[0][:1]
	return visible + "***@" + parts[1]
}
