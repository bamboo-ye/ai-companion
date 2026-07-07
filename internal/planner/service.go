package planner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrNotFound         = errors.New("planner resource not found")
	ErrValidation       = errors.New("planner validation failed")
	ErrConfirmation     = errors.New("reminder requires clarification")
	ErrIdempotencyKey   = errors.New("idempotency key is required")
	ErrIdempotencyReuse = errors.New("idempotency key was reused")
)

type Plan struct {
	ID        string     `json:"id"`
	UserID    string     `json:"-"`
	Title     string     `json:"title"`
	LocalDate string     `json:"local_date"`
	Timezone  string     `json:"timezone"`
	Status    string     `json:"status"`
	Items     []PlanItem `json:"items"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type PlanItem struct {
	ID              string     `json:"id"`
	PlanID          string     `json:"plan_id"`
	Title           string     `json:"title"`
	Priority        string     `json:"priority"`
	EstimatedMinute int        `json:"estimated_minutes"`
	StartsAt        *time.Time `json:"starts_at,omitempty"`
	EndsAt          *time.Time `json:"ends_at,omitempty"`
	Location        string     `json:"location,omitempty"`
	Status          string     `json:"status"`
	Source          string     `json:"source"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type PlanInput struct {
	Title     string          `json:"title"`
	LocalDate string          `json:"local_date"`
	Timezone  string          `json:"timezone"`
	Items     []PlanItemInput `json:"items"`
}

type PlanItemInput struct {
	Title            string     `json:"title"`
	Priority         string     `json:"priority"`
	EstimatedMinutes int        `json:"estimated_minutes"`
	StartsAt         *time.Time `json:"starts_at"`
	EndsAt           *time.Time `json:"ends_at"`
	Location         string     `json:"location"`
	Source           string     `json:"source"`
}

type Reminder struct {
	ID                 string     `json:"id"`
	UserID             string     `json:"-"`
	PlanItemID         string     `json:"plan_item_id,omitempty"`
	SourceMessageID    string     `json:"source_message_id,omitempty"`
	RawText            string     `json:"raw_text"`
	Title              string     `json:"title"`
	DueAt              *time.Time `json:"due_at,omitempty"`
	LocalDue           string     `json:"local_due,omitempty"`
	Timezone           string     `json:"timezone"`
	TimePrecision      string     `json:"time_precision,omitempty"`
	Recurrence         string     `json:"recurrence"`
	NeedsClarification []string   `json:"needs_clarification"`
	Status             string     `json:"status"`
	SystemSyncStatus   string     `json:"system_sync_status"`
	ExternalProvider   string     `json:"external_provider,omitempty"`
	ExternalID         string     `json:"external_id,omitempty"`
	ExternalRevision   string     `json:"external_revision,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type SyncResult struct {
	Status     string `json:"status"`
	Provider   string `json:"provider"`
	ExternalID string `json:"external_id"`
	Revision   string `json:"revision"`
	ErrorCode  string `json:"error_code"`
}

type Store interface {
	CreatePlan(context.Context, Plan) error
	ListPlans(context.Context, string, string) ([]Plan, error)
	CreateReminder(context.Context, Reminder) error
	GetReminder(context.Context, string, string) (Reminder, error)
	ConfirmReminder(context.Context, Reminder, string, time.Time) (Reminder, bool, error)
	ListReminders(context.Context, string, *time.Time, *time.Time, int) ([]Reminder, error)
	CompleteReminder(context.Context, string, string, time.Time) error
	UpdateReminderSync(context.Context, string, string, SyncResult, time.Time) (Reminder, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service { return &Service{store: store, now: time.Now} }

func (s *Service) CreatePlan(ctx context.Context, userID string, input PlanInput) (Plan, error) {
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || len([]rune(input.Title)) > 160 {
		return Plan{}, fmt.Errorf("%w: title is required", ErrValidation)
	}
	location, err := time.LoadLocation(input.Timezone)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: invalid timezone", ErrValidation)
	}
	if _, err = time.ParseInLocation("2006-01-02", input.LocalDate, location); err != nil {
		return Plan{}, fmt.Errorf("%w: local_date must use YYYY-MM-DD", ErrValidation)
	}
	planID, err := id.New()
	if err != nil {
		return Plan{}, err
	}
	now := s.now().UTC()
	plan := Plan{ID: planID, UserID: userID, Title: input.Title, LocalDate: input.LocalDate, Timezone: input.Timezone, Status: "active", CreatedAt: now, UpdatedAt: now}
	for _, itemInput := range input.Items {
		item, itemErr := buildPlanItem(planID, itemInput, now)
		if itemErr != nil {
			return Plan{}, itemErr
		}
		plan.Items = append(plan.Items, item)
	}
	if err = s.store.CreatePlan(ctx, plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (s *Service) ListPlans(ctx context.Context, userID, localDate string) ([]Plan, error) {
	return s.store.ListPlans(ctx, userID, localDate)
}

func (s *Service) ParseReminder(ctx context.Context, userID, sourceMessageID, text, timezone string) (Reminder, error) {
	item, err := ParseReminder(text, timezone, s.now().UTC())
	if err != nil {
		return Reminder{}, err
	}
	item.ID, err = id.New()
	if err != nil {
		return Reminder{}, err
	}
	item.UserID = userID
	item.SourceMessageID = sourceMessageID
	item.CreatedAt = s.now().UTC()
	item.UpdatedAt = item.CreatedAt
	if err = s.store.CreateReminder(ctx, item); err != nil {
		return Reminder{}, err
	}
	return item, nil
}

func (s *Service) ConfirmReminder(ctx context.Context, userID, reminderID, idempotencyKey string) (Reminder, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 191 {
		return Reminder{}, false, ErrIdempotencyKey
	}
	item, err := s.store.GetReminder(ctx, userID, reminderID)
	if err != nil {
		return Reminder{}, false, err
	}
	if item.DueAt == nil || len(item.NeedsClarification) > 0 || item.Status == "needs_clarification" {
		return Reminder{}, false, ErrConfirmation
	}
	return s.store.ConfirmReminder(ctx, item, idempotencyKey, s.now().UTC())
}

func (s *Service) ListReminders(ctx context.Context, userID string, start, end *time.Time, limit int) ([]Reminder, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	return s.store.ListReminders(ctx, userID, start, end, limit)
}

func (s *Service) CompleteReminder(ctx context.Context, userID, reminderID string) error {
	return s.store.CompleteReminder(ctx, userID, reminderID, s.now().UTC())
}

func (s *Service) ReportSystemSync(ctx context.Context, userID, reminderID string, result SyncResult) (Reminder, error) {
	allowed := map[string]bool{"synced": true, "permission_denied": true, "failed": true, "conflict": true, "disconnected": true}
	if !allowed[result.Status] {
		return Reminder{}, fmt.Errorf("%w: invalid sync status", ErrValidation)
	}
	if result.Status == "synced" && (result.Provider == "" || result.ExternalID == "") {
		return Reminder{}, fmt.Errorf("%w: synced reminders require provider and external_id", ErrValidation)
	}
	return s.store.UpdateReminderSync(ctx, userID, reminderID, result, s.now().UTC())
}

func buildPlanItem(planID string, input PlanItemInput, now time.Time) (PlanItem, error) {
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || input.EstimatedMinutes < 0 || input.EstimatedMinutes > 1440 {
		return PlanItem{}, fmt.Errorf("%w: invalid plan item", ErrValidation)
	}
	if input.StartsAt != nil && input.EndsAt != nil && !input.EndsAt.After(*input.StartsAt) {
		return PlanItem{}, fmt.Errorf("%w: plan item end must follow start", ErrValidation)
	}
	priority := input.Priority
	if priority == "" {
		priority = "medium"
	}
	if priority != "low" && priority != "medium" && priority != "high" {
		return PlanItem{}, fmt.Errorf("%w: invalid priority", ErrValidation)
	}
	itemID, err := id.New()
	if err != nil {
		return PlanItem{}, err
	}
	return PlanItem{ID: itemID, PlanID: planID, Title: input.Title, Priority: priority, EstimatedMinute: input.EstimatedMinutes, StartsAt: input.StartsAt, EndsAt: input.EndsAt, Location: strings.TrimSpace(input.Location), Status: "pending", Source: strings.TrimSpace(input.Source), CreatedAt: now, UpdatedAt: now}, nil
}
