package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrNotFound         = errors.New("ledger resource not found")
	ErrValidation       = errors.New("ledger validation failed")
	ErrConfirmation     = errors.New("ledger candidate requires clarification")
	ErrIdempotencyKey   = errors.New("idempotency key is required")
	ErrIdempotencyReuse = errors.New("idempotency key was reused for another operation")
)

type Candidate struct {
	ID                 string     `json:"id"`
	UserID             string     `json:"-"`
	SourceMessageID    string     `json:"source_message_id,omitempty"`
	RawText            string     `json:"raw_text"`
	Direction          string     `json:"direction,omitempty"`
	Currency           string     `json:"currency,omitempty"`
	AmountMinor        int64      `json:"amount_minor,omitempty"`
	Category           string     `json:"category"`
	Merchant           string     `json:"merchant,omitempty"`
	OccurredAt         *time.Time `json:"occurred_at,omitempty"`
	Timezone           string     `json:"timezone"`
	TimePrecision      string     `json:"time_precision,omitempty"`
	Confidence         float64    `json:"confidence"`
	NeedsClarification []string   `json:"needs_clarification"`
	Status             string     `json:"status"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type Entry struct {
	ID             string     `json:"id"`
	UserID         string     `json:"-"`
	CandidateID    string     `json:"candidate_id,omitempty"`
	Direction      string     `json:"direction"`
	Currency       string     `json:"currency"`
	AmountMinor    int64      `json:"amount_minor"`
	Category       string     `json:"category"`
	Merchant       string     `json:"merchant,omitempty"`
	OccurredAt     time.Time  `json:"occurred_at"`
	Timezone       string     `json:"timezone"`
	Note           string     `json:"note,omitempty"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
	IdempotencyKey string     `json:"-"`
}

type EntryFilter struct {
	Start     *time.Time
	End       *time.Time
	Direction string
	Category  string
	Limit     int
}

type UpdateInput struct {
	Direction   *string    `json:"direction"`
	Currency    *string    `json:"currency"`
	AmountMinor *int64     `json:"amount_minor"`
	Category    *string    `json:"category"`
	Merchant    *string    `json:"merchant"`
	OccurredAt  *time.Time `json:"occurred_at"`
	Timezone    *string    `json:"timezone"`
	Note        *string    `json:"note"`
}

type CategoryTotal struct {
	Category    string `json:"category"`
	AmountMinor int64  `json:"amount_minor"`
}

type MonthlySummary struct {
	Month             string          `json:"month"`
	Currency          string          `json:"currency"`
	IncomeMinor       int64           `json:"income_minor"`
	ExpenseMinor      int64           `json:"expense_minor"`
	BalanceMinor      int64           `json:"balance_minor"`
	EntryCount        int             `json:"entry_count"`
	ExpenseByCategory []CategoryTotal `json:"expense_by_category"`
	GeneratedAt       time.Time       `json:"generated_at"`
	ReportingTimezone string          `json:"reporting_timezone"`
}

type ExportJob struct {
	ID          string     `json:"id"`
	UserID      string     `json:"-"`
	Month       string     `json:"month"`
	Currency    string     `json:"currency"`
	Timezone    string     `json:"timezone"`
	Status      string     `json:"status"`
	FileName    string     `json:"file_name,omitempty"`
	MediaType   string     `json:"media_type,omitempty"`
	SizeBytes   int64      `json:"size_bytes,omitempty"`
	SHA256      string     `json:"sha256,omitempty"`
	StorageKey  string     `json:"-"`
	WorkerID    string     `json:"-"`
	FailureCode string     `json:"failure_code,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type Store interface {
	CreateCandidate(context.Context, Candidate) error
	GetCandidate(context.Context, string, string) (Candidate, error)
	ConfirmCandidate(context.Context, Candidate, Entry, string, time.Time) (Entry, bool, error)
	ListEntries(context.Context, string, EntryFilter) ([]Entry, error)
	GetEntry(context.Context, string, string) (Entry, error)
	UpdateEntry(context.Context, Entry) error
	DeleteEntry(context.Context, string, string, time.Time) error
	CreateExport(context.Context, ExportJob, string) (ExportJob, bool, error)
	GetExport(context.Context, string, string) (ExportJob, error)
	ClaimExportByID(context.Context, string, string, time.Time, time.Duration) (ExportJob, error)
	ClaimExport(context.Context, string, time.Time, time.Duration) (ExportJob, error)
	CompleteExport(context.Context, ExportJob) error
	FailExport(context.Context, string, string, string, time.Time) error
	ShareExportWithWorkspace(context.Context, string, string, string, time.Time) error
	ListWorkspaceExports(context.Context, string, int) ([]ExportJob, error)
	GetWorkspaceExport(context.Context, string, string) (ExportJob, error)
}

type ExportFileStore interface {
	Put(context.Context, string, string, []byte) (string, error)
	Get(context.Context, string) ([]byte, error)
}

type Service struct {
	store    Store
	exporter Exporter
	files    ExportFileStore
	now      func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, files: NewMemoryExportFileStore(), now: time.Now}
}

func (s *Service) SetExporter(exporter Exporter) { s.exporter = exporter }
func (s *Service) SetExportFileStore(files ExportFileStore) {
	if files != nil {
		s.files = files
	}
}

func (s *Service) QueueExport(ctx context.Context, userID, requestKey, month, timezone, currency string) (ExportJob, bool, error) {
	requestKey = strings.TrimSpace(requestKey)
	if requestKey == "" || len(requestKey) > 191 {
		return ExportJob{}, false, ErrIdempotencyKey
	}
	if _, _, _, err := monthRange(month, timezone); err != nil {
		return ExportJob{}, false, err
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "CNY"
	}
	if currency != "CNY" && currency != "USD" {
		return ExportJob{}, false, fmt.Errorf("%w: unsupported currency", ErrValidation)
	}
	exportID, err := id.New()
	if err != nil {
		return ExportJob{}, false, err
	}
	now := s.now().UTC()
	job := ExportJob{ID: exportID, UserID: userID, Month: month, Currency: currency, Timezone: timezone, Status: "queued", CreatedAt: now, UpdatedAt: now}
	return s.store.CreateExport(ctx, job, requestKey)
}

func (s *Service) GetExport(ctx context.Context, userID, exportID string) (ExportJob, error) {
	return s.store.GetExport(ctx, userID, exportID)
}

func (s *Service) DownloadExport(ctx context.Context, userID, exportID string) (ExportJob, []byte, error) {
	job, err := s.store.GetExport(ctx, userID, exportID)
	if err != nil || job.Status != "completed" || job.StorageKey == "" {
		if err == nil {
			err = ErrNotFound
		}
		return ExportJob{}, nil, err
	}
	data, err := s.files.Get(ctx, job.StorageKey)
	return job, data, err
}

func (s *Service) ShareExportWithWorkspace(ctx context.Context, userID, workspaceID, exportID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	exportID = strings.TrimSpace(exportID)
	if workspaceID == "" || exportID == "" {
		return ErrValidation
	}
	return s.store.ShareExportWithWorkspace(ctx, userID, workspaceID, exportID, s.now().UTC())
}

func (s *Service) ListWorkspaceExports(ctx context.Context, workspaceID string) ([]ExportJob, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, ErrValidation
	}
	return s.store.ListWorkspaceExports(ctx, workspaceID, 200)
}

func (s *Service) DownloadWorkspaceExport(ctx context.Context, workspaceID, exportID string) (ExportJob, []byte, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	exportID = strings.TrimSpace(exportID)
	if workspaceID == "" || exportID == "" {
		return ExportJob{}, nil, ErrValidation
	}
	job, err := s.store.GetWorkspaceExport(ctx, workspaceID, exportID)
	if err != nil || job.Status != "completed" || job.StorageKey == "" {
		if err == nil {
			err = ErrNotFound
		}
		return ExportJob{}, nil, err
	}
	data, err := s.files.Get(ctx, job.StorageKey)
	return job, data, err
}

func (s *Service) RunExport(ctx context.Context, exportID, workerID string, lease time.Duration) (bool, error) {
	job, err := s.store.ClaimExportByID(ctx, exportID, workerID, s.now().UTC(), lease)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.executeExport(ctx, job, workerID)
}

func (s *Service) RunNextExport(ctx context.Context, workerID string, lease time.Duration) (bool, error) {
	job, err := s.store.ClaimExport(ctx, workerID, s.now().UTC(), lease)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.executeExport(ctx, job, workerID)
}

func (s *Service) RunExportReconciler(ctx context.Context, workerID string, lease, interval time.Duration) error {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		processed, _ := s.RunNextExport(ctx, workerID, lease)
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Service) executeExport(ctx context.Context, job ExportJob, workerID string) (bool, error) {
	job.WorkerID = workerID
	data, fileName, err := s.Export(ctx, job.UserID, job.Month, job.Timezone, job.Currency)
	if err != nil {
		_ = s.store.FailExport(ctx, job.ID, workerID, "export_failed", s.now().UTC())
		return true, nil
	}
	key, err := s.files.Put(ctx, job.UserID, job.ID, data)
	if err != nil {
		_ = s.store.FailExport(ctx, job.ID, workerID, "file_write_failed", s.now().UTC())
		return true, nil
	}
	digest := sha256.Sum256(data)
	now := s.now().UTC()
	job.Status, job.StorageKey, job.FileName = "completed", key, fileName
	job.MediaType, job.SizeBytes, job.SHA256 = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", int64(len(data)), hex.EncodeToString(digest[:])
	job.UpdatedAt, job.CompletedAt = now, &now
	if err = s.store.CompleteExport(ctx, job); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Service) ParseCandidate(ctx context.Context, userID, sourceMessageID, text, timezone string) (Candidate, error) {
	parsed, err := Parse(text, timezone, s.now().UTC())
	if err != nil {
		return Candidate{}, err
	}
	candidateID, err := id.New()
	if err != nil {
		return Candidate{}, err
	}
	now := s.now().UTC()
	parsed.ID = candidateID
	parsed.UserID = userID
	parsed.SourceMessageID = sourceMessageID
	parsed.CreatedAt = now
	parsed.UpdatedAt = now
	if err = s.store.CreateCandidate(ctx, parsed); err != nil {
		return Candidate{}, err
	}
	return parsed, nil
}

func (s *Service) Confirm(ctx context.Context, userID, candidateID, idempotencyKey, note string) (Entry, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 191 {
		return Entry{}, false, ErrIdempotencyKey
	}
	candidate, err := s.store.GetCandidate(ctx, userID, candidateID)
	if err != nil {
		return Entry{}, false, err
	}
	if candidate.Status == "needs_clarification" || candidate.OccurredAt == nil || len(candidate.NeedsClarification) > 0 {
		return Entry{}, false, ErrConfirmation
	}
	entryID, err := id.New()
	if err != nil {
		return Entry{}, false, err
	}
	now := s.now().UTC()
	entry := Entry{
		ID: entryID, UserID: userID, CandidateID: candidate.ID, Direction: candidate.Direction,
		Currency: candidate.Currency, AmountMinor: candidate.AmountMinor, Category: candidate.Category,
		Merchant: candidate.Merchant, OccurredAt: candidate.OccurredAt.UTC(), Timezone: candidate.Timezone,
		Note: strings.TrimSpace(note), Status: "active", CreatedAt: now, UpdatedAt: now, IdempotencyKey: idempotencyKey,
	}
	return s.store.ConfirmCandidate(ctx, candidate, entry, idempotencyKey, now)
}

func (s *Service) List(ctx context.Context, userID string, filter EntryFilter) ([]Entry, error) {
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 200
	}
	if filter.Direction != "" && filter.Direction != "income" && filter.Direction != "expense" {
		return nil, fmt.Errorf("%w: invalid direction", ErrValidation)
	}
	return s.store.ListEntries(ctx, userID, filter)
}

func (s *Service) Get(ctx context.Context, userID, entryID string) (Entry, error) {
	return s.store.GetEntry(ctx, userID, entryID)
}

func (s *Service) Update(ctx context.Context, userID, entryID string, input UpdateInput) (Entry, error) {
	item, err := s.store.GetEntry(ctx, userID, entryID)
	if err != nil {
		return Entry{}, err
	}
	if input.Direction != nil {
		item.Direction = strings.TrimSpace(*input.Direction)
	}
	if input.Currency != nil {
		item.Currency = strings.ToUpper(strings.TrimSpace(*input.Currency))
	}
	if input.AmountMinor != nil {
		item.AmountMinor = *input.AmountMinor
	}
	if input.Category != nil {
		item.Category = strings.TrimSpace(*input.Category)
	}
	if input.Merchant != nil {
		item.Merchant = strings.TrimSpace(*input.Merchant)
	}
	if input.OccurredAt != nil {
		item.OccurredAt = input.OccurredAt.UTC()
	}
	if input.Timezone != nil {
		item.Timezone = strings.TrimSpace(*input.Timezone)
	}
	if input.Note != nil {
		item.Note = strings.TrimSpace(*input.Note)
	}
	if err = validateEntry(item); err != nil {
		return Entry{}, err
	}
	item.UpdatedAt = s.now().UTC()
	if err = s.store.UpdateEntry(ctx, item); err != nil {
		return Entry{}, err
	}
	return item, nil
}

func (s *Service) Delete(ctx context.Context, userID, entryID string) error {
	return s.store.DeleteEntry(ctx, userID, entryID, s.now().UTC())
}

func (s *Service) Summary(ctx context.Context, userID, month, timezone, currency string) (MonthlySummary, error) {
	location, start, end, err := monthRange(month, timezone)
	if err != nil {
		return MonthlySummary{}, err
	}
	items, err := s.store.ListEntries(ctx, userID, EntryFilter{Start: &start, End: &end, Limit: 500})
	if err != nil {
		return MonthlySummary{}, err
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "CNY"
	}
	categoryTotals := map[string]int64{}
	result := MonthlySummary{Month: month, Currency: currency, GeneratedAt: s.now().UTC(), ReportingTimezone: location.String()}
	for _, item := range items {
		if item.Currency != currency {
			continue
		}
		result.EntryCount++
		if item.Direction == "income" {
			result.IncomeMinor += item.AmountMinor
		} else {
			result.ExpenseMinor += item.AmountMinor
			categoryTotals[item.Category] += item.AmountMinor
		}
	}
	result.BalanceMinor = result.IncomeMinor - result.ExpenseMinor
	for category, amount := range categoryTotals {
		result.ExpenseByCategory = append(result.ExpenseByCategory, CategoryTotal{Category: category, AmountMinor: amount})
	}
	sortCategoryTotals(result.ExpenseByCategory)
	return result, nil
}

func (s *Service) Export(ctx context.Context, userID, month, timezone, currency string) ([]byte, string, error) {
	if s.exporter == nil {
		return nil, "", ErrExporterUnavailable
	}
	_, start, end, err := monthRange(month, timezone)
	if err != nil {
		return nil, "", err
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "CNY"
	}
	if currency != "CNY" && currency != "USD" {
		return nil, "", fmt.Errorf("%w: unsupported currency", ErrValidation)
	}
	items, err := s.store.ListEntries(ctx, userID, EntryFilter{Start: &start, End: &end, Limit: 500})
	if err != nil {
		return nil, "", err
	}
	filtered := make([]Entry, 0, len(items))
	for _, item := range items {
		if item.Currency == currency {
			filtered = append(filtered, item)
		}
	}
	data, err := s.exporter.Export(ctx, ExportPayload{Month: month, Currency: currency, Timezone: timezone, GeneratedAt: s.now().UTC(), Entries: filtered})
	if err != nil {
		return nil, "", err
	}
	return data, fmt.Sprintf("ledger-%s-%s.xlsx", month, currency), nil
}

func validateEntry(item Entry) error {
	if item.Direction != "income" && item.Direction != "expense" {
		return fmt.Errorf("%w: direction must be income or expense", ErrValidation)
	}
	if item.AmountMinor <= 0 {
		return fmt.Errorf("%w: amount_minor must be positive", ErrValidation)
	}
	if item.Currency != "CNY" && item.Currency != "USD" {
		return fmt.Errorf("%w: unsupported currency", ErrValidation)
	}
	if item.Category == "" || len([]rune(item.Category)) > 64 {
		return fmt.Errorf("%w: category is required", ErrValidation)
	}
	if _, err := time.LoadLocation(item.Timezone); err != nil {
		return fmt.Errorf("%w: invalid timezone", ErrValidation)
	}
	if len([]rune(item.Note)) > 1000 || len([]rune(item.Merchant)) > 255 {
		return fmt.Errorf("%w: merchant or note is too long", ErrValidation)
	}
	return nil
}

func monthRange(month, timezone string) (*time.Location, time.Time, time.Time, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, time.Time{}, time.Time{}, fmt.Errorf("%w: invalid timezone", ErrValidation)
	}
	value, err := time.ParseInLocation("2006-01", month, location)
	if err != nil {
		return nil, time.Time{}, time.Time{}, fmt.Errorf("%w: month must use YYYY-MM", ErrValidation)
	}
	start := time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, location).UTC()
	return location, start, time.Date(value.Year(), value.Month()+1, 1, 0, 0, 0, 0, location).UTC(), nil
}
