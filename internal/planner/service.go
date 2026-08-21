package planner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrNotFound          = errors.New("planner resource not found")
	ErrValidation        = errors.New("planner validation failed")
	ErrConfirmation      = errors.New("reminder requires clarification")
	ErrIdempotencyKey    = errors.New("idempotency key is required")
	ErrIdempotencyReuse  = errors.New("idempotency key was reused")
	ErrStaleUpdate       = errors.New("planner resource changed before confirmation")
	ErrDayPeriodRequired = errors.New("schedule requires morning or afternoon")
	ErrPastSchedule      = errors.New("schedule time has already passed")
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
	LocalDate       string     `json:"local_date,omitempty"`
	Overdue         bool       `json:"overdue,omitempty"`
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

type TodayPlan struct {
	LocalDate string     `json:"local_date"`
	Timezone  string     `json:"timezone"`
	Items     []PlanItem `json:"items"`
	UpdatedAt time.Time  `json:"updated_at"`
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
	AddPlanItem(context.Context, string, string, PlanItem) error
	FindPlanItemBySource(context.Context, string, string, string) (PlanItem, error)
	GetPlanItem(context.Context, string, string) (PlanItem, Plan, error)
	UpdatePlanItemSchedule(context.Context, string, PlanItem, *time.Time, time.Time) error
	CompletePlanItem(context.Context, string, string, time.Time) error
	CreateReminder(context.Context, Reminder) error
	GetReminder(context.Context, string, string) (Reminder, error)
	FindReminderBySourceMessage(context.Context, string, string) (Reminder, error)
	ConfirmReminder(context.Context, Reminder, string, time.Time) (Reminder, bool, error)
	ListReminders(context.Context, string, *time.Time, *time.Time, int) ([]Reminder, error)
	ListTodayReminders(context.Context, string, time.Time, time.Time, int) ([]Reminder, error)
	CompleteReminder(context.Context, string, string, time.Time) error
	RescheduleReminder(context.Context, Reminder, *time.Time, time.Time) (Reminder, error)
	UpdateReminderSync(context.Context, string, string, SyncResult, time.Time) (Reminder, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service { return NewServiceWithClock(store, time.Now) }

func NewServiceWithClock(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

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

func (s *Service) AddTodayItem(ctx context.Context, userID, localDate, timezone, title, source string) (PlanItem, error) {
	title = strings.TrimSpace(title)
	source = strings.TrimSpace(source)
	if title == "" || len([]rune(title)) > 255 {
		return PlanItem{}, fmt.Errorf("%w: plan item title is required and must not exceed 255 characters", ErrValidation)
	}
	if len([]rune(source)) > 64 {
		return PlanItem{}, fmt.Errorf("%w: plan item source must not exceed 64 characters", ErrValidation)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return PlanItem{}, fmt.Errorf("%w: invalid timezone", ErrValidation)
	}
	if _, err = time.ParseInLocation("2006-01-02", localDate, location); err != nil {
		return PlanItem{}, fmt.Errorf("%w: local_date must use YYYY-MM-DD", ErrValidation)
	}
	// Generic UI origins describe where an item came from; they are not stable
	// idempotency keys. Chat sources include the message ID and can safely
	// deduplicate a replay of the same confirmed action.
	if strings.HasPrefix(source, "chat:") {
		existing, findErr := s.store.FindPlanItemBySource(ctx, userID, localDate, source)
		if findErr == nil {
			return existing, nil
		}
		if !errors.Is(findErr, ErrNotFound) {
			return PlanItem{}, findErr
		}
	}
	plans, err := s.store.ListPlans(ctx, userID, localDate)
	if err != nil {
		return PlanItem{}, err
	}
	if len(plans) == 0 {
		plan, createErr := s.CreatePlan(ctx, userID, PlanInput{
			Title: "今日计划", LocalDate: localDate, Timezone: timezone,
			Items: []PlanItemInput{{Title: title, Priority: "medium", EstimatedMinutes: 0, Source: source}},
		})
		if createErr != nil {
			return PlanItem{}, createErr
		}
		return plan.Items[0], nil
	}
	now := s.now().UTC()
	item, err := buildPlanItem(plans[0].ID, PlanItemInput{Title: title, Priority: "medium", Source: source}, now)
	if err != nil {
		return PlanItem{}, err
	}
	if err = s.store.AddPlanItem(ctx, userID, plans[0].ID, item); err != nil {
		return PlanItem{}, err
	}
	return item, nil
}

func (s *Service) Today(ctx context.Context, userID, localDate, timezone string) (TodayPlan, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return TodayPlan{}, fmt.Errorf("%w: invalid timezone", ErrValidation)
	}
	date, err := time.ParseInLocation("2006-01-02", localDate, location)
	if err != nil {
		return TodayPlan{}, fmt.Errorf("%w: local_date must use YYYY-MM-DD", ErrValidation)
	}
	plans, err := s.store.ListPlans(ctx, userID, localDate)
	if err != nil {
		return TodayPlan{}, err
	}
	result := TodayPlan{LocalDate: localDate, Timezone: timezone, Items: []PlanItem{}, UpdatedAt: s.now().UTC()}
	planItemIDs := map[string]bool{}
	for _, plan := range plans {
		if plan.UpdatedAt.After(result.UpdatedAt) {
			result.UpdatedAt = plan.UpdatedAt
		}
		for _, item := range plan.Items {
			if item.Status == "cancelled" || item.Status == "skipped" {
				continue
			}
			item.LocalDate = plan.LocalDate
			result.Items = append(result.Items, item)
			planItemIDs[item.ID] = true
		}
	}
	start, end := date.UTC(), date.AddDate(0, 0, 1).UTC()
	reminders, err := s.store.ListTodayReminders(ctx, userID, start, end, 500)
	if err != nil {
		return TodayPlan{}, err
	}
	for _, reminder := range reminders {
		if reminder.Status != "active" && reminder.Status != "completed" {
			continue
		}
		if reminder.Status == "completed" && (reminder.DueAt == nil || reminder.DueAt.Before(start)) {
			continue
		}
		if reminder.PlanItemID != "" && planItemIDs[reminder.PlanItemID] {
			continue
		}
		status := "pending"
		if reminder.Status == "completed" {
			status = "completed"
		}
		var startsAt *time.Time
		if reminder.TimePrecision == "minute" {
			startsAt = reminder.DueAt
		}
		reminderDate := reminderLocalDate(reminder, location)
		result.Items = append(result.Items, PlanItem{
			ID: reminder.ID, Title: reminder.Title, LocalDate: reminderDate, Overdue: reminder.Status == "active" && reminderDate < localDate, Priority: "medium", StartsAt: startsAt,
			Status: status, Source: "reminder", CreatedAt: reminder.CreatedAt, UpdatedAt: reminder.UpdatedAt,
		})
		if reminder.UpdatedAt.After(result.UpdatedAt) {
			result.UpdatedAt = reminder.UpdatedAt
		}
	}
	sort.SliceStable(result.Items, func(i, j int) bool {
		left, right := result.Items[i], result.Items[j]
		if left.Status == "completed" && right.Status != "completed" {
			return false
		}
		if left.Status != "completed" && right.Status == "completed" {
			return true
		}
		if left.Overdue != right.Overdue {
			return left.Overdue
		}
		if left.Overdue && left.LocalDate != right.LocalDate {
			return left.LocalDate < right.LocalDate
		}
		if left.StartsAt == nil {
			return right.StartsAt == nil && left.CreatedAt.Before(right.CreatedAt)
		}
		if right.StartsAt == nil {
			return true
		}
		return left.StartsAt.Before(*right.StartsAt)
	})
	return result, nil
}

func reminderLocalDate(item Reminder, location *time.Location) string {
	if len(item.LocalDue) >= len("2006-01-02") {
		return item.LocalDue[:len("2006-01-02")]
	}
	if item.DueAt != nil {
		return item.DueAt.In(location).Format("2006-01-02")
	}
	return ""
}

func (s *Service) ParseReminder(ctx context.Context, userID, sourceMessageID, text, timezone string) (Reminder, error) {
	return s.parseReminder(ctx, userID, sourceMessageID, text, "", "", timezone)
}

func (s *Service) ParseReminderWithSlots(ctx context.Context, userID, sourceMessageID, text, title, dateHint, timezone string) (Reminder, error) {
	return s.parseReminder(ctx, userID, sourceMessageID, text, title, dateHint, timezone)
}

func (s *Service) parseReminder(ctx context.Context, userID, sourceMessageID, text, title, dateHint, timezone string) (Reminder, error) {
	if sourceMessageID != "" {
		existing, err := s.store.FindReminderBySourceMessage(ctx, userID, sourceMessageID)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Reminder{}, err
		}
	}
	item, err := ParseReminderWithSlots(text, title, dateHint, timezone, s.now().UTC())
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

func (s *Service) CompletePlanItem(ctx context.Context, userID, itemID string) error {
	return s.store.CompletePlanItem(ctx, userID, itemID, s.now().UTC())
}

func (s *Service) SchedulePlanItem(ctx context.Context, userID, itemID string, startsAt time.Time, expectedUpdatedAt *time.Time) (PlanItem, error) {
	item, plan, err := s.store.GetPlanItem(ctx, userID, itemID)
	if err != nil {
		return PlanItem{}, err
	}
	if item.Status != "pending" && item.Status != "in_progress" {
		return PlanItem{}, ErrNotFound
	}
	location, err := time.LoadLocation(plan.Timezone)
	if err != nil {
		return PlanItem{}, fmt.Errorf("%w: invalid plan timezone", ErrValidation)
	}
	startsAt = startsAt.UTC()
	if startsAt.In(location).Format("2006-01-02") != plan.LocalDate {
		return PlanItem{}, fmt.Errorf("%w: today plan time must remain on %s", ErrValidation, plan.LocalDate)
	}
	if item.StartsAt != nil && item.StartsAt.Equal(startsAt) {
		item.LocalDate = plan.LocalDate
		return item, nil
	}
	if !startsAt.After(s.now().UTC()) {
		return PlanItem{}, ErrPastSchedule
	}
	if expectedUpdatedAt != nil && !item.UpdatedAt.Equal(*expectedUpdatedAt) {
		return PlanItem{}, ErrStaleUpdate
	}
	var endsAt *time.Time
	if item.StartsAt != nil && item.EndsAt != nil {
		shifted := startsAt.Add(item.EndsAt.Sub(*item.StartsAt))
		endsAt = &shifted
	}
	item.StartsAt = &startsAt
	item.EndsAt = endsAt
	now := s.now().UTC()
	item.UpdatedAt = now
	if err = s.store.UpdatePlanItemSchedule(ctx, userID, item, expectedUpdatedAt, now); err != nil {
		return PlanItem{}, err
	}
	item.LocalDate = plan.LocalDate
	return item, nil
}

func (s *Service) RescheduleReminder(ctx context.Context, userID, reminderID, localDue, timezone string, expectedUpdatedAt *time.Time) (Reminder, error) {
	current, err := s.store.GetReminder(ctx, userID, reminderID)
	if err != nil {
		return Reminder{}, err
	}
	if current.Status != "active" || current.DueAt == nil {
		return Reminder{}, ErrNotFound
	}
	if strings.TrimSpace(localDue) == current.LocalDue && timezone == current.Timezone {
		return current, nil
	}
	schedule, err := ParseSchedule(localDue, timezone, s.now().UTC(), nil)
	if err != nil {
		return Reminder{}, err
	}
	if current.DueAt.Equal(schedule.DueAt) && current.Timezone == timezone && current.TimePrecision == schedule.TimePrecision {
		return current, nil
	}
	if expectedUpdatedAt != nil && !current.UpdatedAt.Equal(*expectedUpdatedAt) {
		return Reminder{}, ErrStaleUpdate
	}
	current.DueAt = &schedule.DueAt
	current.LocalDue = schedule.LocalDue
	current.Timezone = timezone
	current.TimePrecision = schedule.TimePrecision
	if current.ExternalID != "" {
		current.SystemSyncStatus = "pending"
	} else {
		current.SystemSyncStatus = "not_requested"
	}
	return s.store.RescheduleReminder(ctx, current, expectedUpdatedAt, s.now().UTC())
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
