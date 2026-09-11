package performance

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

const (
	DimensionAgentVersion = "agent_version"
	DimensionModelProfile = "model_profile"
)

var (
	ErrInvalid              = errors.New("invalid performance query")
	ErrNotFound             = errors.New("performance budget not found")
	ErrConflict             = errors.New("performance budget conflict")
	ErrRecommendationStale  = errors.New("performance budget recommendation is stale")
	ErrEffectNotReviewable  = errors.New("performance budget policy effect is not reviewable")
	ErrEffectReviewConflict = errors.New("performance budget policy effect review conflict")
)

// Store exposes the persisted operational projections used by the cost and
// quality workspace. It intentionally contains no user prompt or model output.
type Store interface {
	ListVersionMetrics(context.Context, Filter) ([]VersionMetrics, error)
	ListModelMetrics(context.Context, Filter) ([]ModelMetrics, error)
	MeasureWindow(context.Context, WindowFilter) (WindowMetrics, error)
	ListTrend(context.Context, TrendFilter) ([]TrendPoint, error)
	ListBudgets(context.Context) ([]Budget, error)
	GetBudget(context.Context, string) (Budget, error)
	ListBudgetForecastOutcomes(context.Context, ForecastHistoryFilter) ([]BudgetForecastOutcome, error)
	ListBudgetPolicyDecisions(context.Context, string) ([]BudgetPolicyDecision, error)
	SaveBudgetPolicyDecision(context.Context, BudgetPolicyDecision) (BudgetPolicyDecision, error)
	AcknowledgeBudgetPolicyEffect(context.Context, BudgetPolicyEffect, BudgetPolicyEffectReview) (BudgetPolicyEffectReview, error)
	CloseBudgetPolicyEffect(context.Context, BudgetPolicyEffect, string, string, time.Time) (BudgetPolicyEffectReview, error)
	CreateBudget(context.Context, Budget, string) error
	UpdateBudget(context.Context, Budget, int, string) (Budget, error)
	ReconcileBudgetIncident(context.Context, BudgetStatus, time.Time) (string, error)
}

type Filter struct {
	Dimension string
	Module    string
	From      time.Time
	To        time.Time
	Limit     int
}

type WindowFilter struct {
	Module string
	From   time.Time
	To     time.Time
}

type TrendFilter struct {
	Module string
	From   time.Time
	To     time.Time
	Bucket string
}

type TrendPoint struct {
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	Runs          int       `json:"runs"`
	SampleSize    int       `json:"sample_size"`
	Completed     int       `json:"completed"`
	Failed        int       `json:"failed"`
	SuccessRate   float64   `json:"success_rate"`
	ModelCalls    int64     `json:"model_calls"`
	CostMicros    int64     `json:"cost_micros"`
	P95DurationMS float64   `json:"p95_duration_ms"`
}

type Budget struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Module                string    `json:"module,omitempty"`
	Period                string    `json:"period"`
	CostLimitMicros       int64     `json:"cost_limit_micros"`
	WarningRatio          float64   `json:"warning_ratio"`
	Enabled               bool      `json:"enabled"`
	ForecastAlertsEnabled bool      `json:"forecast_alerts_enabled"`
	ForecastLookbackDays  int       `json:"forecast_lookback_days"`
	ForecastMinSamples    int       `json:"forecast_min_samples"`
	EffectObservationDays int       `json:"effect_observation_days"`
	EffectMinSamples      int       `json:"effect_min_samples"`
	Revision              int       `json:"revision"`
	CreatedBy             string    `json:"created_by"`
	UpdatedBy             string    `json:"updated_by"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type BudgetInput struct {
	Name                  string  `json:"name"`
	Module                string  `json:"module"`
	Period                string  `json:"period"`
	CostLimitMicros       int64   `json:"cost_limit_micros"`
	WarningRatio          float64 `json:"warning_ratio"`
	Enabled               bool    `json:"enabled"`
	ForecastAlertsEnabled *bool   `json:"forecast_alerts_enabled"`
	ForecastLookbackDays  int     `json:"forecast_lookback_days"`
	ForecastMinSamples    int     `json:"forecast_min_samples"`
	EffectObservationDays int     `json:"effect_observation_days"`
	EffectMinSamples      int     `json:"effect_min_samples"`
}

type BudgetStatus struct {
	Budget
	WindowFrom      time.Time       `json:"window_from"`
	WindowTo        time.Time       `json:"window_to"`
	UsedCostMicros  int64           `json:"used_cost_micros"`
	RemainingMicros int64           `json:"remaining_micros"`
	Utilization     float64         `json:"utilization"`
	Status          string          `json:"status"`
	AlertStatus     string          `json:"-"`
	Forecast        *BudgetForecast `json:"-"`
}

type BudgetEvaluation struct {
	BudgetsEvaluated  int `json:"budgets_evaluated"`
	Warning           int `json:"warning"`
	Exceeded          int `json:"exceeded"`
	ProjectedExceeded int `json:"projected_exceeded"`
	Opened            int `json:"opened"`
	Escalated         int `json:"escalated"`
	Resolved          int `json:"resolved"`
}

type BudgetImpactProjection struct {
	Enabled                  bool       `json:"enabled"`
	ForecastAlertsEnabled    bool       `json:"forecast_alerts_enabled"`
	ForecastLookbackDays     int        `json:"forecast_lookback_days"`
	ForecastMinSamples       int        `json:"forecast_min_samples"`
	Status                   string     `json:"status"`
	Signal                   string     `json:"signal"`
	Severity                 string     `json:"severity"`
	WillAlert                bool       `json:"will_alert"`
	UsedCostMicros           int64      `json:"used_cost_micros"`
	CostLimitMicros          int64      `json:"cost_limit_micros"`
	Utilization              float64    `json:"utilization"`
	ForecastRisk             string     `json:"forecast_risk"`
	ForecastConfidence       string     `json:"forecast_confidence"`
	SampleSize               int        `json:"sample_size"`
	ProjectedCostMicros      int64      `json:"projected_cost_micros"`
	ProjectedUtilization     float64    `json:"projected_utilization"`
	ProjectedLimitExceededAt *time.Time `json:"projected_limit_exceeded_at,omitempty"`
	Reason                   string     `json:"reason"`
}

type BudgetImpactPreview struct {
	BudgetID    string                 `json:"budget_id"`
	GeneratedAt time.Time              `json:"generated_at"`
	Current     BudgetImpactProjection `json:"current"`
	Proposed    BudgetImpactProjection `json:"proposed"`
	Change      string                 `json:"change"`
	Note        string                 `json:"note"`
}

type ForecastHistoryFilter struct {
	BudgetID string
	From     time.Time
	To       time.Time
	Limit    int
}

type BudgetForecastOutcome struct {
	IncidentID               string     `json:"incident_id"`
	BudgetID                 string     `json:"budget_id"`
	BudgetName               string     `json:"budget_name"`
	Module                   string     `json:"module,omitempty"`
	Period                   string     `json:"period"`
	Outcome                  string     `json:"outcome"`
	OpenedAt                 time.Time  `json:"opened_at"`
	OutcomeAt                *time.Time `json:"outcome_at,omitempty"`
	DurationMinutes          int64      `json:"duration_minutes"`
	InitialUtilization       float64    `json:"initial_utilization"`
	ProjectedUtilization     float64    `json:"projected_utilization"`
	ProjectedCostMicros      int64      `json:"projected_cost_micros"`
	ForecastSampleSize       int        `json:"forecast_sample_size"`
	PredictedLimitExceededAt *time.Time `json:"predicted_limit_exceeded_at,omitempty"`
	BudgetRevision           int        `json:"budget_revision"`
}

type BudgetForecastHistorySummary struct {
	BudgetID               string  `json:"budget_id"`
	BudgetName             string  `json:"budget_name"`
	Module                 string  `json:"module,omitempty"`
	Period                 string  `json:"period"`
	Predictions            int     `json:"predictions"`
	Hits                   int     `json:"hits"`
	Cleared                int     `json:"cleared"`
	Observing              int     `json:"observing"`
	Decided                int     `json:"decided"`
	HitRate                float64 `json:"hit_rate"`
	AverageLeadTimeMinutes float64 `json:"average_lead_time_minutes"`
}

type BudgetForecastHistory struct {
	GeneratedAt            time.Time                      `json:"generated_at"`
	From                   time.Time                      `json:"from"`
	To                     time.Time                      `json:"to"`
	Predictions            int                            `json:"predictions"`
	Hits                   int                            `json:"hits"`
	Cleared                int                            `json:"cleared"`
	Observing              int                            `json:"observing"`
	Decided                int                            `json:"decided"`
	HitRate                float64                        `json:"hit_rate"`
	AverageLeadTimeMinutes float64                        `json:"average_lead_time_minutes"`
	Budgets                []BudgetForecastHistorySummary `json:"budgets"`
	Recommendations        []BudgetPolicyRecommendation   `json:"recommendations"`
	Effects                []BudgetPolicyEffect           `json:"effects"`
	Items                  []BudgetForecastOutcome        `json:"items"`
	Note                   string                         `json:"note"`
}

type BudgetPolicyRecommendation struct {
	BudgetID               string                `json:"budget_id"`
	BudgetName             string                `json:"budget_name"`
	Module                 string                `json:"module,omitempty"`
	Period                 string                `json:"period"`
	Action                 string                `json:"action"`
	Confidence             string                `json:"confidence"`
	Decided                int                   `json:"decided"`
	HitRate                float64               `json:"hit_rate"`
	AverageLeadTimeMinutes float64               `json:"average_lead_time_minutes"`
	CurrentLookbackDays    int                   `json:"current_lookback_days"`
	CurrentMinSamples      int                   `json:"current_min_samples"`
	ProposedLookbackDays   int                   `json:"proposed_lookback_days"`
	ProposedMinSamples     int                   `json:"proposed_min_samples"`
	Reason                 string                `json:"reason"`
	RequiresImpactPreview  bool                  `json:"requires_impact_preview"`
	RecommendationKey      string                `json:"recommendation_key"`
	Feedback               *BudgetPolicyDecision `json:"feedback,omitempty"`
	BudgetRevision         int                   `json:"budget_revision"`
}

type BudgetPolicyDecision struct {
	ID                     string                    `json:"id"`
	BudgetID               string                    `json:"budget_id"`
	RecommendationKey      string                    `json:"recommendation_key"`
	Action                 string                    `json:"action"`
	Decision               string                    `json:"decision"`
	Confidence             string                    `json:"confidence"`
	OutcomeCount           int                       `json:"outcome_count"`
	HitRate                float64                   `json:"hit_rate"`
	AverageLeadTimeMinutes float64                   `json:"average_lead_time_minutes"`
	CurrentLookbackDays    int                       `json:"current_lookback_days"`
	CurrentMinSamples      int                       `json:"current_min_samples"`
	ProposedLookbackDays   int                       `json:"proposed_lookback_days"`
	ProposedMinSamples     int                       `json:"proposed_min_samples"`
	RecommendationReason   string                    `json:"recommendation_reason"`
	Reason                 string                    `json:"reason"`
	DecidedBy              string                    `json:"decided_by"`
	DecidedAt              time.Time                 `json:"decided_at"`
	BudgetRevision         int                       `json:"budget_revision"`
	AppliedAt              *time.Time                `json:"applied_at,omitempty"`
	AppliedBy              string                    `json:"applied_by,omitempty"`
	AppliedBudgetRevision  int                       `json:"applied_budget_revision,omitempty"`
	EffectObservationDays  int                       `json:"effect_observation_days,omitempty"`
	EffectMinSamples       int                       `json:"effect_min_samples,omitempty"`
	EffectReview           *BudgetPolicyEffectReview `json:"effect_review,omitempty"`
}

type BudgetPolicyDecisionInput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type BudgetPolicyEffectReview struct {
	Status                 string     `json:"status"`
	Disposition            string     `json:"disposition"`
	Reason                 string     `json:"reason"`
	ReviewedBy             string     `json:"reviewed_by"`
	ReviewedAt             time.Time  `json:"reviewed_at"`
	RollbackAppliedAt      *time.Time `json:"rollback_applied_at,omitempty"`
	RollbackAppliedBy      string     `json:"rollback_applied_by,omitempty"`
	RollbackBudgetRevision int        `json:"rollback_budget_revision,omitempty"`
	ClosedReason           string     `json:"closed_reason,omitempty"`
	ClosedBy               string     `json:"closed_by,omitempty"`
	ClosedAt               *time.Time `json:"closed_at,omitempty"`
}

type BudgetPolicyEffectReviewInput struct {
	Disposition string `json:"disposition"`
	Reason      string `json:"reason"`
}

type BudgetPolicyEffectCloseInput struct {
	Reason string `json:"reason"`
}

type BudgetPolicyEffectMetrics struct {
	Predictions            int     `json:"predictions"`
	Hits                   int     `json:"hits"`
	Cleared                int     `json:"cleared"`
	Observing              int     `json:"observing"`
	Decided                int     `json:"decided"`
	HitRate                float64 `json:"hit_rate"`
	AverageLeadTimeMinutes float64 `json:"average_lead_time_minutes"`
}

type BudgetPolicyRollbackVerification struct {
	BudgetRevision        int                       `json:"budget_revision"`
	AppliedBy             string                    `json:"applied_by"`
	AppliedAt             time.Time                 `json:"applied_at"`
	ObservationDays       int                       `json:"observation_days"`
	MinimumDecidedSamples int                       `json:"minimum_decided_samples"`
	ObservationEndsAt     time.Time                 `json:"observation_ends_at"`
	ObservationComplete   bool                      `json:"observation_complete"`
	After                 BudgetPolicyEffectMetrics `json:"after"`
	Status                string                    `json:"status"`
	Note                  string                    `json:"note"`
}

type BudgetPolicyEffect struct {
	DecisionID            string                            `json:"decision_id"`
	BudgetID              string                            `json:"budget_id"`
	BudgetName            string                            `json:"budget_name"`
	Module                string                            `json:"module,omitempty"`
	Period                string                            `json:"period"`
	RecommendationKey     string                            `json:"recommendation_key"`
	Action                string                            `json:"action"`
	CurrentLookbackDays   int                               `json:"current_lookback_days"`
	CurrentMinSamples     int                               `json:"current_min_samples"`
	ProposedLookbackDays  int                               `json:"proposed_lookback_days"`
	ProposedMinSamples    int                               `json:"proposed_min_samples"`
	AppliedBy             string                            `json:"applied_by"`
	AppliedAt             time.Time                         `json:"applied_at"`
	AppliedBudgetRevision int                               `json:"applied_budget_revision"`
	ObservationDays       int                               `json:"observation_days"`
	MinimumDecidedSamples int                               `json:"minimum_decided_samples"`
	ObservationEndsAt     time.Time                         `json:"observation_ends_at"`
	ObservationComplete   bool                              `json:"observation_complete"`
	Before                BudgetPolicyEffectMetrics         `json:"before"`
	After                 BudgetPolicyEffectMetrics         `json:"after"`
	Status                string                            `json:"status"`
	Note                  string                            `json:"note"`
	RecommendRollback     bool                              `json:"recommend_rollback"`
	RollbackLookbackDays  int                               `json:"rollback_lookback_days,omitempty"`
	RollbackMinSamples    int                               `json:"rollback_min_samples,omitempty"`
	RollbackReason        string                            `json:"rollback_reason,omitempty"`
	RollbackCanApply      bool                              `json:"rollback_can_apply"`
	RollbackVerification  *BudgetPolicyRollbackVerification `json:"rollback_verification,omitempty"`
	Review                *BudgetPolicyEffectReview         `json:"review,omitempty"`
}

type VersionMetrics struct {
	Dimension          string  `json:"dimension"`
	GroupID            string  `json:"group_id"`
	Key                string  `json:"key"`
	Label              string  `json:"label"`
	VersionID          string  `json:"version_id,omitempty"`
	Version            int     `json:"version,omitempty"`
	Revision           int     `json:"revision,omitempty"`
	ConfigVersion      string  `json:"config_version,omitempty"`
	Runs               int     `json:"runs"`
	SampleSize         int     `json:"sample_size"`
	Completed          int     `json:"completed"`
	Failed             int     `json:"failed"`
	Cancelled          int     `json:"cancelled"`
	QualityFailures    int     `json:"quality_failures"`
	SuccessRate        float64 `json:"success_rate"`
	ErrorRate          float64 `json:"error_rate"`
	QualityFailureRate float64 `json:"quality_failure_rate"`
	ModelCalls         int64   `json:"model_calls"`
	PromptTokens       int64   `json:"prompt_tokens"`
	CompletionTokens   int64   `json:"completion_tokens"`
	CostMicros         int64   `json:"cost_micros"`
	AverageCostMicros  float64 `json:"average_cost_micros"`
	AverageDurationMS  float64 `json:"average_duration_ms"`
	P50DurationMS      float64 `json:"p50_duration_ms"`
	P95DurationMS      float64 `json:"p95_duration_ms"`
}

type ModelMetrics struct {
	Provider          string  `json:"provider"`
	Model             string  `json:"model"`
	Calls             int     `json:"calls"`
	Succeeded         int     `json:"succeeded"`
	Failed            int     `json:"failed"`
	ErrorRate         float64 `json:"error_rate"`
	PromptTokens      int64   `json:"prompt_tokens"`
	CompletionTokens  int64   `json:"completion_tokens"`
	CachedTokens      int64   `json:"cached_tokens"`
	CostMicros        int64   `json:"cost_micros"`
	AverageCostMicros float64 `json:"average_cost_micros"`
	AverageLatencyMS  float64 `json:"average_latency_ms"`
	P95LatencyMS      float64 `json:"p95_latency_ms"`
}

type WindowMetrics struct {
	From               time.Time `json:"from"`
	To                 time.Time `json:"to"`
	Runs               int       `json:"runs"`
	SampleSize         int       `json:"sample_size"`
	Completed          int       `json:"completed"`
	Failed             int       `json:"failed"`
	QualityFailures    int       `json:"quality_failures"`
	SuccessRate        float64   `json:"success_rate"`
	ErrorRate          float64   `json:"error_rate"`
	QualityFailureRate float64   `json:"quality_failure_rate"`
	ModelCalls         int64     `json:"model_calls"`
	CostMicros         int64     `json:"cost_micros"`
	AverageCostMicros  float64   `json:"average_cost_micros"`
	P95DurationMS      float64   `json:"p95_duration_ms"`
}

type Anomaly struct {
	Metric   string  `json:"metric"`
	Severity string  `json:"severity"`
	Current  float64 `json:"current"`
	Baseline float64 `json:"baseline"`
	Change   float64 `json:"change"`
	Message  string  `json:"message"`
}

type Analysis struct {
	Status      string        `json:"status"`
	GeneratedAt time.Time     `json:"generated_at"`
	Current     WindowMetrics `json:"current"`
	Baseline    WindowMetrics `json:"baseline"`
	Anomalies   []Anomaly     `json:"anomalies"`
	Note        string        `json:"note,omitempty"`
}

type ForecastFilter struct {
	Module string
	From   time.Time
	To     time.Time
}

type BudgetForecast struct {
	BudgetID                 string     `json:"budget_id"`
	Name                     string     `json:"name"`
	Module                   string     `json:"module,omitempty"`
	Period                   string     `json:"period"`
	CurrentStatus            string     `json:"current_status"`
	Risk                     string     `json:"risk"`
	Confidence               string     `json:"confidence"`
	SampleSize               int        `json:"sample_size"`
	ObservedHours            float64    `json:"observed_hours"`
	UsedCostMicros           int64      `json:"used_cost_micros"`
	CostLimitMicros          int64      `json:"cost_limit_micros"`
	BurnRateMicrosPerHour    float64    `json:"burn_rate_micros_per_hour"`
	ProjectedCostMicros      int64      `json:"projected_cost_micros"`
	ProjectedUtilization     float64    `json:"projected_utilization"`
	RequiredSavingsMicros    int64      `json:"required_savings_micros"`
	PeriodEndsAt             time.Time  `json:"period_ends_at"`
	ProjectedWarningAt       *time.Time `json:"projected_warning_at,omitempty"`
	ProjectedLimitExceededAt *time.Time `json:"projected_limit_exceeded_at,omitempty"`
}

type ModelRecommendation struct {
	SourceProvider                   string  `json:"source_provider"`
	SourceModel                      string  `json:"source_model"`
	TargetProvider                   string  `json:"target_provider"`
	TargetModel                      string  `json:"target_model"`
	SourceCalls                      int     `json:"source_calls"`
	TargetCalls                      int     `json:"target_calls"`
	EstimatedSavingsMicrosPer1000    int64   `json:"estimated_savings_micros_per_1000_calls"`
	EstimatedSavingsRatio            float64 `json:"estimated_savings_ratio"`
	ErrorRateDelta                   float64 `json:"error_rate_delta"`
	P95LatencyDeltaMS                float64 `json:"p95_latency_delta_ms"`
	Confidence                       string  `json:"confidence"`
	Reason                           string  `json:"reason"`
	RequiresValidationBeforeAdoption bool    `json:"requires_validation_before_adoption"`
}

type Forecast struct {
	Status               string                `json:"status"`
	GeneratedAt          time.Time             `json:"generated_at"`
	HistoryFrom          time.Time             `json:"history_from"`
	HistoryTo            time.Time             `json:"history_to"`
	BudgetForecasts      []BudgetForecast      `json:"budget_forecasts"`
	ModelRecommendations []ModelRecommendation `json:"model_recommendations"`
	Note                 string                `json:"note"`
}

type Service struct {
	store Store
}

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) Versions(ctx context.Context, filter Filter) ([]VersionMetrics, error) {
	if err := validateFilter(filter); err != nil {
		return nil, err
	}
	return s.store.ListVersionMetrics(ctx, filter)
}

func (s *Service) Models(ctx context.Context, filter Filter) ([]ModelMetrics, error) {
	filter.Dimension = DimensionAgentVersion
	if err := validateFilter(filter); err != nil {
		return nil, err
	}
	return s.store.ListModelMetrics(ctx, filter)
}

func (s *Service) Trend(ctx context.Context, filter TrendFilter) ([]TrendPoint, error) {
	filter.Module = strings.TrimSpace(filter.Module)
	if !validModule(filter.Module) || filter.From.IsZero() || filter.To.IsZero() || !filter.From.Before(filter.To) || filter.To.Sub(filter.From) > 90*24*time.Hour || (filter.Bucket != "hour" && filter.Bucket != "day") {
		return nil, ErrInvalid
	}
	return s.store.ListTrend(ctx, filter)
}

func (s *Service) BudgetStatuses(ctx context.Context, now time.Time) ([]BudgetStatus, error) {
	budgets, err := s.store.ListBudgets(ctx)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	items := make([]BudgetStatus, 0, len(budgets))
	for _, budget := range budgets {
		item, statusErr := s.budgetStatus(ctx, budget, now)
		if statusErr != nil {
			return nil, statusErr
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) budgetStatus(ctx context.Context, budget Budget, now time.Time) (BudgetStatus, error) {
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if budget.Period == "monthly" {
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	usage, err := s.store.MeasureWindow(ctx, WindowFilter{Module: budget.Module, From: from, To: now})
	if err != nil {
		return BudgetStatus{}, err
	}
	utilization := float64(usage.CostMicros) / float64(budget.CostLimitMicros)
	status := "normal"
	if !budget.Enabled {
		status = "disabled"
	} else if utilization >= 1 {
		status = "exceeded"
	} else if utilization >= budget.WarningRatio {
		status = "warning"
	}
	return BudgetStatus{
		Budget: budget, WindowFrom: from, WindowTo: now, UsedCostMicros: usage.CostMicros,
		RemainingMicros: max(0, budget.CostLimitMicros-usage.CostMicros), Utilization: utilization, Status: status,
	}, nil
}

func (s *Service) withBudgetForecast(ctx context.Context, item BudgetStatus, now time.Time) (BudgetStatus, error) {
	if !item.Enabled || !item.ForecastAlertsEnabled {
		return item, nil
	}
	lookback := time.Duration(item.ForecastLookbackDays) * 24 * time.Hour
	recent, err := s.store.MeasureWindow(ctx, WindowFilter{Module: item.Module, From: now.Add(-lookback), To: now})
	if err != nil {
		return BudgetStatus{}, err
	}
	forecast := forecastBudget(item, recent, ForecastFilter{Module: item.Module, From: now.Add(-lookback), To: now})
	item.Forecast = &forecast
	return item, nil
}

func budgetAlertSignal(item BudgetStatus) string {
	if item.Status == "warning" || item.Status == "exceeded" || item.Status == "disabled" {
		return item.Status
	}
	if item.Forecast != nil && item.Forecast.Risk == "projected_exceeded" && item.Forecast.SampleSize >= item.ForecastMinSamples {
		return "projected_exceeded"
	}
	return item.Status
}

func (s *Service) EvaluateBudgets(ctx context.Context, now time.Time) (BudgetEvaluation, error) {
	now = now.UTC()
	statuses, err := s.BudgetStatuses(ctx, now)
	if err != nil {
		return BudgetEvaluation{}, err
	}
	report := BudgetEvaluation{BudgetsEvaluated: len(statuses)}
	for _, item := range statuses {
		item, err = s.withBudgetForecast(ctx, item, now)
		if err != nil {
			return report, err
		}
		item.AlertStatus = budgetAlertSignal(item)
		if item.AlertStatus == "projected_exceeded" {
			report.ProjectedExceeded++
		}
		switch item.Status {
		case "warning":
			report.Warning++
		case "exceeded":
			report.Exceeded++
		}
		transition, reconcileErr := s.store.ReconcileBudgetIncident(ctx, item, now)
		if reconcileErr != nil {
			return report, reconcileErr
		}
		switch transition {
		case "opened":
			report.Opened++
		case "escalated":
			report.Escalated++
		case "resolved":
			report.Resolved++
		}
	}
	return report, nil
}

func (s *Service) PreviewBudgetImpact(ctx context.Context, budgetID string, input BudgetInput, now time.Time) (BudgetImpactPreview, error) {
	budgetID = strings.TrimSpace(budgetID)
	proposed := budgetFromInput(input)
	if budgetID == "" || !validBudgetDefinition(proposed) {
		return BudgetImpactPreview{}, ErrInvalid
	}
	current, err := s.store.GetBudget(ctx, budgetID)
	if err != nil {
		return BudgetImpactPreview{}, err
	}
	proposed.ID = current.ID
	now = now.UTC()
	currentStatus, err := s.budgetStatus(ctx, current, now)
	if err != nil {
		return BudgetImpactPreview{}, err
	}
	currentStatus, err = s.withBudgetForecast(ctx, currentStatus, now)
	if err != nil {
		return BudgetImpactPreview{}, err
	}
	proposedStatus, err := s.budgetStatus(ctx, proposed, now)
	if err != nil {
		return BudgetImpactPreview{}, err
	}
	proposedStatus, err = s.withBudgetForecast(ctx, proposedStatus, now)
	if err != nil {
		return BudgetImpactPreview{}, err
	}
	currentProjection := budgetImpactProjection(currentStatus)
	proposedProjection := budgetImpactProjection(proposedStatus)
	return BudgetImpactPreview{
		BudgetID: current.ID, GeneratedAt: now, Current: currentProjection, Proposed: proposedProjection,
		Change: compareBudgetImpact(currentProjection, proposedProjection),
		Note:   "仅使用已持久化的实际调用数据进行试算；不会保存预算、创建事故或发送通知。",
	}, nil
}

func (s *Service) ForecastHistory(ctx context.Context, filter ForecastHistoryFilter, now time.Time) (BudgetForecastHistory, error) {
	filter.BudgetID = strings.TrimSpace(filter.BudgetID)
	if filter.From.IsZero() || filter.To.IsZero() || !filter.From.Before(filter.To) || filter.To.Sub(filter.From) > 365*24*time.Hour {
		return BudgetForecastHistory{}, ErrInvalid
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 500 {
		return BudgetForecastHistory{}, ErrInvalid
	}
	filter.From, filter.To = filter.From.UTC(), filter.To.UTC()
	items, err := s.store.ListBudgetForecastOutcomes(ctx, filter)
	if err != nil {
		return BudgetForecastHistory{}, err
	}
	budgets, err := s.store.ListBudgets(ctx)
	if err != nil {
		return BudgetForecastHistory{}, err
	}
	report := BudgetForecastHistory{
		GeneratedAt: now.UTC(), From: filter.From, To: filter.To,
		Budgets: make([]BudgetForecastHistorySummary, 0), Recommendations: make([]BudgetPolicyRecommendation, 0), Effects: make([]BudgetPolicyEffect, 0), Items: items,
		Note: "命中表示预测事故随后升级为实际超限；提前解除可能来自主动节省或消耗变化，不等同于误报。观察中的记录不参与命中率计算。策略建议只读，至少需要 5 条已有结论的记录，应用前必须先预览影响；应用与回滚后的效果均只统计对应预算 revision 和固化观察期内的新预测，所有结论都不会自动修改配置。",
	}
	summaries := make(map[string]*BudgetForecastHistorySummary)
	var totalLeadMinutes int64
	for _, item := range items {
		report.Predictions++
		summary := summaries[item.BudgetID]
		if summary == nil {
			summary = &BudgetForecastHistorySummary{BudgetID: item.BudgetID, BudgetName: item.BudgetName, Module: item.Module, Period: item.Period}
			summaries[item.BudgetID] = summary
		}
		summary.Predictions++
		switch item.Outcome {
		case "hit":
			report.Hits++
			summary.Hits++
			totalLeadMinutes += item.DurationMinutes
			summary.AverageLeadTimeMinutes += float64(item.DurationMinutes)
		case "cleared":
			report.Cleared++
			summary.Cleared++
		case "observing":
			report.Observing++
			summary.Observing++
		}
	}
	report.Decided = report.Hits + report.Cleared
	if report.Decided > 0 {
		report.HitRate = float64(report.Hits) / float64(report.Decided)
	}
	if report.Hits > 0 {
		report.AverageLeadTimeMinutes = float64(totalLeadMinutes) / float64(report.Hits)
	}
	for _, summary := range summaries {
		summary.Decided = summary.Hits + summary.Cleared
		if summary.Decided > 0 {
			summary.HitRate = float64(summary.Hits) / float64(summary.Decided)
		}
		if summary.Hits > 0 {
			summary.AverageLeadTimeMinutes /= float64(summary.Hits)
		}
		report.Budgets = append(report.Budgets, *summary)
	}
	sort.Slice(report.Budgets, func(i, j int) bool {
		if report.Budgets[i].Predictions == report.Budgets[j].Predictions {
			return report.Budgets[i].BudgetName < report.Budgets[j].BudgetName
		}
		return report.Budgets[i].Predictions > report.Budgets[j].Predictions
	})
	decisions, err := s.store.ListBudgetPolicyDecisions(ctx, filter.BudgetID)
	if err != nil {
		return BudgetForecastHistory{}, err
	}
	decisionsByKey := make(map[string]BudgetPolicyDecision, len(decisions))
	for _, decision := range decisions {
		decisionsByKey[decision.RecommendationKey] = decision
	}
	for _, budget := range budgets {
		if filter.BudgetID != "" && budget.ID != filter.BudgetID {
			continue
		}
		recommendation := recommendBudgetPolicy(budget, summaries[budget.ID])
		recommendation.BudgetRevision = budget.Revision
		recommendation.RecommendationKey = budgetPolicyRecommendationKey(recommendation)
		if decision, exists := decisionsByKey[recommendation.RecommendationKey]; exists {
			decisionCopy := decision
			recommendation.Feedback = &decisionCopy
		}
		report.Recommendations = append(report.Recommendations, recommendation)
	}
	sort.Slice(report.Recommendations, func(i, j int) bool {
		left, right := recommendationPriority(report.Recommendations[i].Action), recommendationPriority(report.Recommendations[j].Action)
		if left == right {
			return report.Recommendations[i].BudgetName < report.Recommendations[j].BudgetName
		}
		return left < right
	})
	effectItems := items
	effectFrom := filter.From
	for _, decision := range decisions {
		if decision.AppliedAt == nil || decision.EffectReview == nil || decision.EffectReview.RollbackAppliedAt == nil {
			continue
		}
		rollbackInRange := !decision.EffectReview.RollbackAppliedAt.Before(filter.From) && decision.EffectReview.RollbackAppliedAt.Before(filter.To)
		if rollbackInRange && decision.AppliedAt.Before(effectFrom) {
			effectFrom = *decision.AppliedAt
		}
	}
	oldestEffectFrom := filter.To.Add(-365 * 24 * time.Hour)
	if effectFrom.Before(oldestEffectFrom) {
		effectFrom = oldestEffectFrom
	}
	if effectFrom.Before(filter.From) {
		effectFilter := filter
		effectFilter.From, effectFilter.Limit = effectFrom, 500
		effectItems, err = s.store.ListBudgetForecastOutcomes(ctx, effectFilter)
		if err != nil {
			return BudgetForecastHistory{}, err
		}
	}
	report.Effects = budgetPolicyEffects(decisions, effectItems, budgets, filter, now.UTC())
	if len(report.Items) > filter.Limit {
		report.Items = report.Items[:filter.Limit]
	}
	return report, nil
}

func (s *Service) DecideBudgetPolicyRecommendation(ctx context.Context, filter ForecastHistoryFilter, budgetID, recommendationKey string, input BudgetPolicyDecisionInput, actor string, now time.Time) (BudgetPolicyDecision, error) {
	budgetID, recommendationKey = strings.TrimSpace(budgetID), strings.ToLower(strings.TrimSpace(recommendationKey))
	input.Decision, input.Reason, actor = strings.TrimSpace(input.Decision), strings.TrimSpace(input.Reason), strings.TrimSpace(actor)
	if budgetID == "" || !validRecommendationKey(recommendationKey) || (input.Decision != "accepted" && input.Decision != "rejected") || len(input.Reason) < 2 || len(input.Reason) > 512 || actor == "" {
		return BudgetPolicyDecision{}, ErrInvalid
	}
	if _, err := s.store.GetBudget(ctx, budgetID); err != nil {
		return BudgetPolicyDecision{}, err
	}
	filter.BudgetID = budgetID
	report, err := s.ForecastHistory(ctx, filter, now)
	if err != nil {
		return BudgetPolicyDecision{}, err
	}
	var recommendation *BudgetPolicyRecommendation
	for index := range report.Recommendations {
		if report.Recommendations[index].RecommendationKey == recommendationKey {
			recommendation = &report.Recommendations[index]
			break
		}
	}
	if recommendation == nil || !recommendation.RequiresImpactPreview {
		return BudgetPolicyDecision{}, ErrRecommendationStale
	}
	generated, err := id.New()
	if err != nil {
		return BudgetPolicyDecision{}, err
	}
	decision := BudgetPolicyDecision{
		ID: generated, BudgetID: budgetID, RecommendationKey: recommendation.RecommendationKey,
		Action: recommendation.Action, Decision: input.Decision, Confidence: recommendation.Confidence,
		OutcomeCount: recommendation.Decided, HitRate: recommendation.HitRate, AverageLeadTimeMinutes: recommendation.AverageLeadTimeMinutes,
		CurrentLookbackDays: recommendation.CurrentLookbackDays, CurrentMinSamples: recommendation.CurrentMinSamples,
		ProposedLookbackDays: recommendation.ProposedLookbackDays, ProposedMinSamples: recommendation.ProposedMinSamples,
		RecommendationReason: recommendation.Reason, Reason: input.Reason, DecidedBy: actor, DecidedAt: now.UTC(),
		BudgetRevision: recommendation.BudgetRevision,
	}
	return s.store.SaveBudgetPolicyDecision(ctx, decision)
}

func (s *Service) AcknowledgeBudgetPolicyEffect(ctx context.Context, budgetID, decisionID string, input BudgetPolicyEffectReviewInput, actor string, now time.Time) (BudgetPolicyEffectReview, error) {
	budgetID, decisionID = strings.TrimSpace(budgetID), strings.TrimSpace(decisionID)
	input.Disposition, input.Reason, actor = strings.TrimSpace(input.Disposition), strings.TrimSpace(input.Reason), strings.TrimSpace(actor)
	if budgetID == "" || decisionID == "" || (input.Disposition != "rollback_planned" && input.Disposition != "continue_observing") || len(input.Reason) < 2 || len(input.Reason) > 512 || actor == "" {
		return BudgetPolicyEffectReview{}, ErrInvalid
	}
	effect, err := s.budgetPolicyEffectForReview(ctx, budgetID, decisionID, now)
	if err != nil {
		return BudgetPolicyEffectReview{}, err
	}
	if !effect.RecommendRollback {
		return BudgetPolicyEffectReview{}, ErrEffectNotReviewable
	}
	if effect.Review != nil && effect.Review.Status == "closed" {
		return BudgetPolicyEffectReview{}, ErrEffectReviewConflict
	}
	if effect.Review != nil && effect.Review.RollbackAppliedAt != nil && input.Disposition != "rollback_planned" {
		return BudgetPolicyEffectReview{}, ErrEffectReviewConflict
	}
	review := BudgetPolicyEffectReview{Status: "acknowledged", Disposition: input.Disposition, Reason: input.Reason, ReviewedBy: actor, ReviewedAt: now.UTC()}
	if effect.Review != nil {
		review.RollbackAppliedAt = effect.Review.RollbackAppliedAt
		review.RollbackAppliedBy = effect.Review.RollbackAppliedBy
		review.RollbackBudgetRevision = effect.Review.RollbackBudgetRevision
	}
	return s.store.AcknowledgeBudgetPolicyEffect(ctx, effect, review)
}

func (s *Service) CloseBudgetPolicyEffect(ctx context.Context, budgetID, decisionID string, input BudgetPolicyEffectCloseInput, actor string, now time.Time) (BudgetPolicyEffectReview, error) {
	budgetID, decisionID, input.Reason, actor = strings.TrimSpace(budgetID), strings.TrimSpace(decisionID), strings.TrimSpace(input.Reason), strings.TrimSpace(actor)
	if budgetID == "" || decisionID == "" || len(input.Reason) < 2 || len(input.Reason) > 512 || actor == "" {
		return BudgetPolicyEffectReview{}, ErrInvalid
	}
	effect, err := s.budgetPolicyEffectForReview(ctx, budgetID, decisionID, now)
	if err != nil {
		return BudgetPolicyEffectReview{}, err
	}
	if effect.Review == nil || effect.Review.Status != "acknowledged" {
		return BudgetPolicyEffectReview{}, ErrEffectReviewConflict
	}
	return s.store.CloseBudgetPolicyEffect(ctx, effect, actor, input.Reason, now.UTC())
}

func (s *Service) budgetPolicyEffectForReview(ctx context.Context, budgetID, decisionID string, now time.Time) (BudgetPolicyEffect, error) {
	if _, err := s.store.GetBudget(ctx, budgetID); err != nil {
		return BudgetPolicyEffect{}, err
	}
	now = now.UTC()
	report, err := s.ForecastHistory(ctx, ForecastHistoryFilter{BudgetID: budgetID, From: now.Add(-365 * 24 * time.Hour), To: now, Limit: 500}, now)
	if err != nil {
		return BudgetPolicyEffect{}, err
	}
	for _, effect := range report.Effects {
		if effect.DecisionID == decisionID {
			return effect, nil
		}
	}
	return BudgetPolicyEffect{}, ErrEffectNotReviewable
}

func budgetPolicyRecommendationKey(item BudgetPolicyRecommendation) string {
	payload := fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%d\x00%.12g\x00%.12g\x00%d\x00%d\x00%d\x00%d", item.BudgetID, item.BudgetRevision, item.Action, item.Confidence, item.Decided, item.HitRate, item.AverageLeadTimeMinutes, item.CurrentLookbackDays, item.CurrentMinSamples, item.ProposedLookbackDays, item.ProposedMinSamples)
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", digest)
}

func budgetPolicyEffects(decisions []BudgetPolicyDecision, outcomes []BudgetForecastOutcome, budgets []Budget, filter ForecastHistoryFilter, now time.Time) []BudgetPolicyEffect {
	items := make([]BudgetPolicyEffect, 0)
	budgetsByID := make(map[string]Budget, len(budgets))
	for _, budget := range budgets {
		budgetsByID[budget.ID] = budget
	}
	for _, decision := range decisions {
		if decision.AppliedAt == nil || decision.AppliedBudgetRevision < 1 {
			continue
		}
		appliedInRange := !decision.AppliedAt.Before(filter.From) && decision.AppliedAt.Before(filter.To)
		rollbackInRange := decision.EffectReview != nil && decision.EffectReview.RollbackAppliedAt != nil &&
			!decision.EffectReview.RollbackAppliedAt.Before(filter.From) && decision.EffectReview.RollbackAppliedAt.Before(filter.To)
		if !appliedInRange && !rollbackInRange {
			continue
		}
		beforeHits := int(math.Round(decision.HitRate * float64(decision.OutcomeCount)))
		before := BudgetPolicyEffectMetrics{Predictions: decision.OutcomeCount, Hits: beforeHits, Cleared: max(0, decision.OutcomeCount-beforeHits), Decided: decision.OutcomeCount, HitRate: decision.HitRate, AverageLeadTimeMinutes: decision.AverageLeadTimeMinutes}
		observationDays := decision.EffectObservationDays
		if observationDays == 0 {
			observationDays = 30
		}
		minimumSamples := decision.EffectMinSamples
		if minimumSamples == 0 {
			minimumSamples = 5
		}
		observationEndsAt := decision.AppliedAt.Add(time.Duration(observationDays) * 24 * time.Hour)
		observationComplete := !now.Before(observationEndsAt)
		after := budgetPolicyEffectMetrics(outcomes, decision.BudgetID, decision.AppliedBudgetRevision, *decision.AppliedAt, observationEndsAt)
		status, note := assessBudgetPolicyEffect(decision.Action, before, after, minimumSamples, observationComplete)
		budget := budgetsByID[decision.BudgetID]
		recommendRollback := observationComplete && status == "regressed"
		rollbackCanApply := decision.EffectReview != nil && decision.EffectReview.Status == "acknowledged" &&
			decision.EffectReview.Disposition == "rollback_planned" && decision.EffectReview.RollbackAppliedAt == nil &&
			budget.Enabled && budget.ForecastAlertsEnabled && budget.Revision == decision.AppliedBudgetRevision &&
			budget.ForecastLookbackDays == decision.ProposedLookbackDays && budget.ForecastMinSamples == decision.ProposedMinSamples
		rollbackLookbackDays, rollbackMinSamples, rollbackReason := 0, 0, ""
		if recommendRollback {
			rollbackLookbackDays, rollbackMinSamples = decision.CurrentLookbackDays, decision.CurrentMinSamples
			rollbackReason = fmt.Sprintf("观察期已结束且指标明确变差，建议人工预览后恢复为 %d 天历史窗口、至少 %d 个预测样本。", decision.CurrentLookbackDays, decision.CurrentMinSamples)
		}
		var rollbackVerification *BudgetPolicyRollbackVerification
		if decision.EffectReview != nil && decision.EffectReview.RollbackAppliedAt != nil && decision.EffectReview.RollbackBudgetRevision > decision.AppliedBudgetRevision {
			rollbackEndsAt := decision.EffectReview.RollbackAppliedAt.Add(time.Duration(observationDays) * 24 * time.Hour)
			rollbackComplete := !now.Before(rollbackEndsAt)
			rollbackAfter := budgetPolicyEffectMetrics(outcomes, decision.BudgetID, decision.EffectReview.RollbackBudgetRevision, *decision.EffectReview.RollbackAppliedAt, rollbackEndsAt)
			rollbackStatus, rollbackNote := assessBudgetPolicyRollback(before, after, rollbackAfter, minimumSamples, rollbackComplete)
			rollbackVerification = &BudgetPolicyRollbackVerification{
				BudgetRevision: decision.EffectReview.RollbackBudgetRevision, AppliedBy: decision.EffectReview.RollbackAppliedBy, AppliedAt: *decision.EffectReview.RollbackAppliedAt,
				ObservationDays: observationDays, MinimumDecidedSamples: minimumSamples, ObservationEndsAt: rollbackEndsAt, ObservationComplete: rollbackComplete,
				After: rollbackAfter, Status: rollbackStatus, Note: rollbackNote,
			}
		}
		items = append(items, BudgetPolicyEffect{
			DecisionID: decision.ID, BudgetID: decision.BudgetID, BudgetName: budget.Name, Module: budget.Module, Period: budget.Period,
			RecommendationKey: decision.RecommendationKey, Action: decision.Action, CurrentLookbackDays: decision.CurrentLookbackDays, CurrentMinSamples: decision.CurrentMinSamples,
			ProposedLookbackDays: decision.ProposedLookbackDays, ProposedMinSamples: decision.ProposedMinSamples,
			AppliedBy: decision.AppliedBy, AppliedAt: *decision.AppliedAt, AppliedBudgetRevision: decision.AppliedBudgetRevision,
			ObservationDays: observationDays, MinimumDecidedSamples: minimumSamples, ObservationEndsAt: observationEndsAt, ObservationComplete: observationComplete,
			Before: before, After: after, Status: status, Note: note, RecommendRollback: recommendRollback,
			RollbackLookbackDays: rollbackLookbackDays, RollbackMinSamples: rollbackMinSamples, RollbackReason: rollbackReason,
			RollbackCanApply: rollbackCanApply, RollbackVerification: rollbackVerification, Review: decision.EffectReview,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AppliedAt.After(items[j].AppliedAt) })
	return items
}

func budgetPolicyEffectMetrics(outcomes []BudgetForecastOutcome, budgetID string, revision int, from, to time.Time) BudgetPolicyEffectMetrics {
	metrics := BudgetPolicyEffectMetrics{}
	var totalLeadMinutes int64
	for _, outcome := range outcomes {
		if outcome.BudgetID != budgetID || outcome.BudgetRevision != revision || outcome.OpenedAt.Before(from) || !outcome.OpenedAt.Before(to) {
			continue
		}
		metrics.Predictions++
		switch outcome.Outcome {
		case "hit":
			metrics.Hits++
			totalLeadMinutes += outcome.DurationMinutes
		case "cleared":
			metrics.Cleared++
		case "observing":
			metrics.Observing++
		}
	}
	metrics.Decided = metrics.Hits + metrics.Cleared
	if metrics.Decided > 0 {
		metrics.HitRate = float64(metrics.Hits) / float64(metrics.Decided)
	}
	if metrics.Hits > 0 {
		metrics.AverageLeadTimeMinutes = float64(totalLeadMinutes) / float64(metrics.Hits)
	}
	return metrics
}

func assessBudgetPolicyRollback(target, regressed, after BudgetPolicyEffectMetrics, minimumSamples int, observationComplete bool) (string, string) {
	if after.Decided < minimumSamples {
		if observationComplete {
			return "insufficient_data", fmt.Sprintf("回滚观察期已结束，但仅有 %d 条结论，未达到 %d 条最低样本门槛。", after.Decided, minimumSamples)
		}
		return "collecting", fmt.Sprintf("回滚后已有 %d 条结论，至少积累 %d 条后再判断恢复效果。", after.Decided, minimumSamples)
	}
	hitDeltaFromRegression := after.HitRate - regressed.HitRate
	leadComparable := regressed.Hits > 0 && after.Hits > 0
	leadDeltaFromRegression := after.AverageLeadTimeMinutes - regressed.AverageLeadTimeMinutes
	hitRecovered := after.HitRate >= target.HitRate-.1
	leadRecovered := target.Hits == 0 || (after.Hits > 0 && after.AverageLeadTimeMinutes >= target.AverageLeadTimeMinutes-30)
	if hitRecovered && leadRecovered {
		return "recovered", "回滚后的命中率与提前量已恢复到原始基线保护区间。"
	}
	hitImproved, hitWorse := hitDeltaFromRegression >= .1, hitDeltaFromRegression <= -.1
	leadImproved, leadWorse := leadComparable && leadDeltaFromRegression >= 30, leadComparable && leadDeltaFromRegression <= -30
	if (hitImproved || leadImproved) && (hitWorse || leadWorse) {
		return "mixed", "回滚后部分指标改善，但另有指标继续变差，建议保留处置并复核。"
	}
	if hitImproved || leadImproved {
		return "improving", "回滚后的指标较异常版本已有改善，但尚未恢复到原始基线保护区间。"
	}
	return "not_recovered", "回滚后已有足够样本，但指标尚未较异常版本明显改善。"
}

func assessBudgetPolicyEffect(action string, before, after BudgetPolicyEffectMetrics, minimumSamples int, observationComplete bool) (string, string) {
	if after.Decided < minimumSamples {
		if observationComplete {
			return "insufficient_data", fmt.Sprintf("观察期已结束，但仅有 %d 条结论，未达到 %d 条最低样本门槛，不生成回滚建议。", after.Decided, minimumSamples)
		}
		return "collecting", fmt.Sprintf("观察期内已有 %d 条结论，至少积累 %d 条后再判断效果。", after.Decided, minimumSamples)
	}
	hitRateDelta := after.HitRate - before.HitRate
	leadDelta := after.AverageLeadTimeMinutes - before.AverageLeadTimeMinutes
	if action == "increase_sample_gate" {
		switch {
		case hitRateDelta >= .1:
			return "improved", "提高样本门槛后预测命中率明显提升。"
		case hitRateDelta <= -.1:
			return "regressed", "提高样本门槛后预测命中率下降，需要人工复核。"
		default:
			return "stable", "命中率变化尚不明显，建议继续观察。"
		}
	}
	switch {
	case hitRateDelta < -.1 || leadDelta <= -30:
		return "regressed", "降低样本门槛后的命中率或提前量变差，需要人工复核。"
	case leadDelta >= 30 && hitRateDelta >= -.1:
		return "improved", "降低样本门槛后提前量增加，且命中率保持在保护线内。"
	case leadDelta > 0 || hitRateDelta > 0:
		return "mixed", "部分指标改善，但尚未同时满足提前量和命中率目标。"
	default:
		return "stable", "应用后的变化尚不明显，建议继续观察。"
	}
}

func validRecommendationKey(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func recommendBudgetPolicy(budget Budget, summary *BudgetForecastHistorySummary) BudgetPolicyRecommendation {
	item := BudgetPolicyRecommendation{
		BudgetID: budget.ID, BudgetName: budget.Name, Module: budget.Module, Period: budget.Period,
		Action: "collect_more_data", Confidence: "none",
		CurrentLookbackDays: budget.ForecastLookbackDays, CurrentMinSamples: budget.ForecastMinSamples,
		ProposedLookbackDays: budget.ForecastLookbackDays, ProposedMinSamples: budget.ForecastMinSamples,
		Reason: "至少积累 5 条已有结论的预测记录后再调整策略。",
	}
	if !budget.Enabled {
		item.Action, item.Reason = "not_applicable", "预算当前已停用，不生成预测策略调整建议。"
		return item
	}
	if !budget.ForecastAlertsEnabled {
		item.Action, item.Reason = "not_applicable", "提前预警当前已关闭，不根据旧记录建议自动开启。"
		return item
	}
	if summary == nil {
		return item
	}
	item.Decided, item.HitRate, item.AverageLeadTimeMinutes = summary.Decided, summary.HitRate, summary.AverageLeadTimeMinutes
	item.Confidence = recommendationConfidenceFromDecisions(summary.Decided)
	if summary.Decided < 5 {
		item.Reason = fmt.Sprintf("目前只有 %d 条已有结论的预测记录，至少需要 5 条才能形成调优建议。", summary.Decided)
		return item
	}
	if summary.HitRate < .35 && summary.Cleared >= 3 && budget.ForecastMinSamples < 10_000 {
		proposed := int(math.Ceil(float64(budget.ForecastMinSamples) * 1.25))
		proposed = min(10_000, max(budget.ForecastMinSamples+1, proposed))
		item.Action, item.ProposedMinSamples, item.RequiresImpactPreview = "increase_sample_gate", proposed, true
		item.Reason = fmt.Sprintf("%d 条已有结论的记录中命中率为 %.0f%%；建议先将样本门槛从 %d 提高到 %d，减少低样本波动。", summary.Decided, summary.HitRate*100, budget.ForecastMinSamples, proposed)
		return item
	}
	if summary.HitRate >= .70 && summary.AverageLeadTimeMinutes < 120 && budget.ForecastMinSamples > 5 {
		proposed := max(5, int(math.Floor(float64(budget.ForecastMinSamples)*.8)))
		item.Action, item.ProposedMinSamples, item.RequiresImpactPreview = "decrease_sample_gate", proposed, true
		item.Reason = fmt.Sprintf("命中率为 %.0f%%，但平均只提前 %.0f 分钟；建议将样本门槛从 %d 降到 %d，试算能否更早预警。", summary.HitRate*100, summary.AverageLeadTimeMinutes, budget.ForecastMinSamples, proposed)
		return item
	}
	item.Action = "keep_policy"
	item.Reason = fmt.Sprintf("当前 %d 条已有结论记录的命中率与平均提前量未达到安全调整门槛，建议保持现有策略。", summary.Decided)
	return item
}

func recommendationConfidenceFromDecisions(decided int) string {
	switch {
	case decided >= 20:
		return "high"
	case decided >= 10:
		return "medium"
	case decided >= 5:
		return "low"
	default:
		return "none"
	}
}

func recommendationPriority(action string) int {
	if action == "increase_sample_gate" || action == "decrease_sample_gate" {
		return 0
	}
	if action == "keep_policy" {
		return 1
	}
	if action == "collect_more_data" {
		return 2
	}
	return 3
}

func budgetImpactProjection(item BudgetStatus) BudgetImpactProjection {
	projection := BudgetImpactProjection{
		Enabled: item.Enabled, ForecastAlertsEnabled: item.ForecastAlertsEnabled,
		ForecastLookbackDays: item.ForecastLookbackDays, ForecastMinSamples: item.ForecastMinSamples,
		Status: item.Status, Signal: "safe", Severity: "none", UsedCostMicros: item.UsedCostMicros,
		CostLimitMicros: item.CostLimitMicros, Utilization: item.Utilization,
		ForecastRisk: "not_evaluated", ForecastConfidence: "none", Reason: "当前不会触发预算事故。",
	}
	if item.Forecast != nil {
		projection.ForecastRisk = item.Forecast.Risk
		projection.ForecastConfidence = item.Forecast.Confidence
		projection.SampleSize = item.Forecast.SampleSize
		projection.ProjectedCostMicros = item.Forecast.ProjectedCostMicros
		projection.ProjectedUtilization = item.Forecast.ProjectedUtilization
		projection.ProjectedLimitExceededAt = item.Forecast.ProjectedLimitExceededAt
	}
	switch {
	case !item.Enabled:
		projection.Signal, projection.Reason = "disabled", "预算已停用，不会参与事故评估。"
	case item.Status == "exceeded":
		projection.Signal, projection.Severity, projection.WillAlert = "actual_exceeded", "critical", true
		projection.Reason = "当前周期的实际成本已经超过预算上限。"
	case item.Status == "warning":
		projection.Signal, projection.Severity, projection.WillAlert = "actual_warning", "warning", true
		projection.Reason = "当前周期的实际成本已经达到预警线。"
	case !item.ForecastAlertsEnabled:
		projection.Reason = "提前预警已关闭；实际成本达到预警线或上限时仍会告警。"
	case item.Forecast != nil && item.Forecast.Risk == "projected_exceeded" && item.Forecast.SampleSize < item.ForecastMinSamples:
		projection.Signal = "insufficient_samples"
		projection.Reason = fmt.Sprintf("预测显示可能超限，但只有 %d 个样本，未达到 %d 个样本的门槛。", item.Forecast.SampleSize, item.ForecastMinSamples)
	case item.Forecast != nil && item.Forecast.Risk == "insufficient_data":
		projection.Signal = "insufficient_samples"
		projection.Reason = "尚无足够的实际调用数据形成预算预测。"
	case budgetAlertSignal(item) == "projected_exceeded":
		projection.Signal, projection.Severity, projection.WillAlert = "projected_exceeded", "warning", true
		projection.Reason = "按当前消耗速度，本周期预计会超过预算上限。"
	}
	return projection
}

func compareBudgetImpact(current, proposed BudgetImpactProjection) string {
	if !current.WillAlert && proposed.WillAlert {
		return "would_start_alerting"
	}
	if current.WillAlert && !proposed.WillAlert {
		return "would_stop_alerting"
	}
	severityRank := map[string]int{"none": 0, "warning": 1, "critical": 2}
	if severityRank[proposed.Severity] > severityRank[current.Severity] {
		return "would_escalate"
	}
	if severityRank[proposed.Severity] < severityRank[current.Severity] {
		return "would_deescalate"
	}
	if current.Signal != proposed.Signal {
		return "would_change_signal"
	}
	return "unchanged"
}

func (s *Service) CreateBudget(ctx context.Context, input BudgetInput, actor, reason string, now time.Time) (Budget, error) {
	item := budgetFromInput(input)
	if !validBudget(item, actor, reason) {
		return Budget{}, ErrInvalid
	}
	generated, err := id.New()
	if err != nil {
		return Budget{}, err
	}
	item.ID, item.Revision = generated, 1
	item.CreatedBy, item.UpdatedBy = strings.TrimSpace(actor), strings.TrimSpace(actor)
	item.CreatedAt, item.UpdatedAt = now.UTC(), now.UTC()
	if err = s.store.CreateBudget(ctx, item, strings.TrimSpace(reason)); err != nil {
		return Budget{}, err
	}
	return item, nil
}

func (s *Service) UpdateBudget(ctx context.Context, budgetID string, revision int, input BudgetInput, actor, reason string, now time.Time) (Budget, error) {
	item := budgetFromInput(input)
	item.ID, item.Revision, item.UpdatedBy, item.UpdatedAt = strings.TrimSpace(budgetID), revision+1, strings.TrimSpace(actor), now.UTC()
	if item.ID == "" || revision < 1 || !validBudget(item, actor, reason) {
		return Budget{}, ErrInvalid
	}
	return s.store.UpdateBudget(ctx, item, revision, strings.TrimSpace(reason))
}

func budgetFromInput(input BudgetInput) Budget {
	warning := input.WarningRatio
	if warning == 0 {
		warning = .8
	}
	period := strings.TrimSpace(input.Period)
	forecastEnabled := true
	if input.ForecastAlertsEnabled != nil {
		forecastEnabled = *input.ForecastAlertsEnabled
	}
	lookback := input.ForecastLookbackDays
	if lookback == 0 {
		lookback = 7
		if period == "monthly" {
			lookback = 30
		}
	}
	minimumSamples := input.ForecastMinSamples
	if minimumSamples == 0 {
		minimumSamples = 20
	}
	effectObservationDays := input.EffectObservationDays
	if effectObservationDays == 0 {
		effectObservationDays = 30
	}
	effectMinSamples := input.EffectMinSamples
	if effectMinSamples == 0 {
		effectMinSamples = 5
	}
	return Budget{
		Name: strings.TrimSpace(input.Name), Module: strings.TrimSpace(input.Module), Period: period,
		CostLimitMicros: input.CostLimitMicros, WarningRatio: warning, Enabled: input.Enabled,
		ForecastAlertsEnabled: forecastEnabled, ForecastLookbackDays: lookback, ForecastMinSamples: minimumSamples,
		EffectObservationDays: effectObservationDays, EffectMinSamples: effectMinSamples,
	}
}

func validBudget(item Budget, actor, reason string) bool {
	return validBudgetDefinition(item) && len(strings.TrimSpace(actor)) > 0 && len(strings.TrimSpace(reason)) >= 2 && len(strings.TrimSpace(reason)) <= 512
}

func validBudgetDefinition(item Budget) bool {
	return len(item.Name) >= 2 && len(item.Name) <= 128 && validModule(item.Module) && (item.Period == "daily" || item.Period == "monthly") && item.CostLimitMicros > 0 && item.CostLimitMicros <= 1_000_000_000_000_000 && !math.IsNaN(item.WarningRatio) && item.WarningRatio > 0 && item.WarningRatio < 1 && item.ForecastLookbackDays >= 1 && item.ForecastLookbackDays <= 90 && item.ForecastMinSamples >= 5 && item.ForecastMinSamples <= 10_000 && item.EffectObservationDays >= 7 && item.EffectObservationDays <= 180 && item.EffectMinSamples >= 5 && item.EffectMinSamples <= 100
}

func (s *Service) Analyze(ctx context.Context, now time.Time, currentDuration, baselineDuration time.Duration, module string) (Analysis, error) {
	module = strings.TrimSpace(module)
	if !validModule(module) || currentDuration <= 0 || baselineDuration <= 0 || currentDuration > 24*time.Hour || baselineDuration > 30*24*time.Hour {
		return Analysis{}, ErrInvalid
	}
	now = now.UTC()
	currentFrom := now.Add(-currentDuration)
	baselineFrom := currentFrom.Add(-baselineDuration)
	current, err := s.store.MeasureWindow(ctx, WindowFilter{Module: module, From: currentFrom, To: now})
	if err != nil {
		return Analysis{}, err
	}
	baseline, err := s.store.MeasureWindow(ctx, WindowFilter{Module: module, From: baselineFrom, To: currentFrom})
	if err != nil {
		return Analysis{}, err
	}
	result := Analysis{
		Status: "stable", GeneratedAt: now, Current: current, Baseline: baseline,
		Anomalies: make([]Anomaly, 0),
	}
	if current.SampleSize < 5 || baseline.SampleSize < 20 {
		result.Status = "insufficient_data"
		result.Note = fmt.Sprintf("异常判断至少需要当前窗口 5 个完成样本和基线窗口 20 个完成样本；当前为 %d / %d", current.SampleSize, baseline.SampleSize)
		return result, nil
	}
	result.Anomalies = append(result.Anomalies,
		rateAnomaly("error_rate", "错误率", current.ErrorRate, baseline.ErrorRate, .10, .25, .15, .30)...,
	)
	result.Anomalies = append(result.Anomalies,
		rateAnomaly("quality_failure_rate", "质量失败率", current.QualityFailureRate, baseline.QualityFailureRate, .08, .18, .10, .22)...,
	)
	result.Anomalies = append(result.Anomalies,
		ratioAnomaly("p95_duration_ms", "P95 耗时", current.P95DurationMS, baseline.P95DurationMS, 1.5, 2.0)...,
	)
	result.Anomalies = append(result.Anomalies,
		ratioAnomaly("average_cost_micros", "平均成本", current.AverageCostMicros, baseline.AverageCostMicros, 1.5, 2.0)...,
	)
	for _, item := range result.Anomalies {
		if item.Severity == "critical" {
			result.Status = "critical"
			return result, nil
		}
		result.Status = "warning"
	}
	return result, nil
}

func (s *Service) Forecast(ctx context.Context, filter ForecastFilter) (Forecast, error) {
	filter.Module = strings.TrimSpace(filter.Module)
	if !validModule(filter.Module) || filter.From.IsZero() || filter.To.IsZero() || !filter.From.Before(filter.To) || filter.To.Sub(filter.From) > 90*24*time.Hour {
		return Forecast{}, ErrInvalid
	}
	filter.From, filter.To = filter.From.UTC(), filter.To.UTC()
	result := Forecast{
		Status: "ready", GeneratedAt: filter.To, HistoryFrom: filter.From, HistoryTo: filter.To,
		BudgetForecasts: make([]BudgetForecast, 0), ModelRecommendations: make([]ModelRecommendation, 0),
		Note: "预测基于所选历史窗口的实际消耗速度；模型建议只用于缩小评测范围，不会自动修改生产配置。",
	}
	statuses, err := s.BudgetStatuses(ctx, filter.To)
	if err != nil {
		return Forecast{}, err
	}
	for _, status := range statuses {
		if filter.Module != "" && status.Module != filter.Module {
			continue
		}
		recent, measureErr := s.store.MeasureWindow(ctx, WindowFilter{Module: status.Module, From: filter.From, To: filter.To})
		if measureErr != nil {
			return Forecast{}, measureErr
		}
		result.BudgetForecasts = append(result.BudgetForecasts, forecastBudget(status, recent, filter))
	}
	models, err := s.Models(ctx, Filter{Dimension: DimensionAgentVersion, Module: filter.Module, From: filter.From, To: filter.To, Limit: 100})
	if err != nil {
		return Forecast{}, err
	}
	result.ModelRecommendations = recommendModels(models, 5)
	if len(result.BudgetForecasts) == 0 && len(result.ModelRecommendations) == 0 {
		result.Status = "insufficient_data"
	}
	return result, nil
}

func forecastBudget(status BudgetStatus, recent WindowMetrics, filter ForecastFilter) BudgetForecast {
	periodEnd := status.WindowFrom.Add(24 * time.Hour)
	if status.Period == "monthly" {
		periodEnd = time.Date(status.WindowFrom.Year(), status.WindowFrom.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	}
	observedHours := filter.To.Sub(filter.From).Hours()
	burnRate := float64(recent.CostMicros) / observedHours
	remainingHours := max(0, periodEnd.Sub(filter.To).Hours())
	projectedCost := status.UsedCostMicros + int64(math.Round(burnRate*remainingHours))
	projectedCost = max(projectedCost, status.UsedCostMicros)
	projectedUtilization := float64(projectedCost) / float64(status.CostLimitMicros)
	item := BudgetForecast{
		BudgetID: status.ID, Name: status.Name, Module: status.Module, Period: status.Period,
		CurrentStatus: status.Status, Risk: "on_track", Confidence: forecastConfidence(recent.SampleSize),
		SampleSize: recent.SampleSize, ObservedHours: observedHours, UsedCostMicros: status.UsedCostMicros,
		CostLimitMicros: status.CostLimitMicros, BurnRateMicrosPerHour: burnRate,
		ProjectedCostMicros: projectedCost, ProjectedUtilization: projectedUtilization,
		RequiredSavingsMicros: max(0, projectedCost-status.CostLimitMicros), PeriodEndsAt: periodEnd,
	}
	if !status.Enabled {
		item.Risk = "disabled"
		return item
	}
	if status.Status == "exceeded" {
		item.Risk = "exceeded"
	} else if recent.SampleSize == 0 && status.UsedCostMicros == 0 {
		item.Risk = "insufficient_data"
	} else if projectedUtilization >= 1 {
		item.Risk = "projected_exceeded"
	} else if projectedUtilization >= status.WarningRatio {
		item.Risk = "projected_warning"
	}
	item.ProjectedWarningAt = projectedThresholdAt(status.UsedCostMicros, int64(math.Round(float64(status.CostLimitMicros)*status.WarningRatio)), burnRate, filter.To, periodEnd)
	item.ProjectedLimitExceededAt = projectedThresholdAt(status.UsedCostMicros, status.CostLimitMicros, burnRate, filter.To, periodEnd)
	return item
}

func projectedThresholdAt(used, threshold int64, burnRate float64, now, periodEnd time.Time) *time.Time {
	if used >= threshold {
		value := now
		return &value
	}
	if burnRate <= 0 {
		return nil
	}
	hours := float64(threshold-used) / burnRate
	if hours > periodEnd.Sub(now).Hours() {
		return nil
	}
	value := now.Add(time.Duration(hours * float64(time.Hour)))
	return &value
}

func forecastConfidence(samples int) string {
	switch {
	case samples >= 100:
		return "high"
	case samples >= 20:
		return "medium"
	case samples > 0:
		return "low"
	default:
		return "none"
	}
}

func recommendModels(models []ModelMetrics, limit int) []ModelRecommendation {
	items := make([]ModelRecommendation, 0)
	for _, source := range models {
		if source.Calls < 20 || source.AverageCostMicros <= 0 || source.P95LatencyMS <= 0 || source.Provider == "unknown" || source.Model == "unknown" {
			continue
		}
		var best *ModelRecommendation
		for _, target := range models {
			if target.Calls < 20 || target.P95LatencyMS <= 0 || (source.Provider == target.Provider && source.Model == target.Model) {
				continue
			}
			savingsRatio := (source.AverageCostMicros - target.AverageCostMicros) / source.AverageCostMicros
			if savingsRatio < .10 || target.ErrorRate > source.ErrorRate+.02 || target.P95LatencyMS > source.P95LatencyMS*1.25 {
				continue
			}
			candidate := ModelRecommendation{
				SourceProvider: source.Provider, SourceModel: source.Model, TargetProvider: target.Provider, TargetModel: target.Model,
				SourceCalls: source.Calls, TargetCalls: target.Calls,
				EstimatedSavingsMicrosPer1000: int64(math.Round((source.AverageCostMicros - target.AverageCostMicros) * 1000)),
				EstimatedSavingsRatio:         savingsRatio, ErrorRateDelta: target.ErrorRate - source.ErrorRate,
				P95LatencyDeltaMS:                target.P95LatencyMS - source.P95LatencyMS,
				Confidence:                       recommendationConfidence(source.Calls, target.Calls),
				Reason:                           fmt.Sprintf("历史样本中单次成本预计降低 %.0f%%，错误率和 P95 延迟仍在保护范围内", savingsRatio*100),
				RequiresValidationBeforeAdoption: true,
			}
			if best == nil || candidate.EstimatedSavingsMicrosPer1000 > best.EstimatedSavingsMicrosPer1000 {
				copy := candidate
				best = &copy
			}
		}
		if best != nil {
			items = append(items, *best)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].EstimatedSavingsMicrosPer1000 > items[j].EstimatedSavingsMicrosPer1000
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func recommendationConfidence(sourceCalls, targetCalls int) string {
	samples := min(sourceCalls, targetCalls)
	if samples >= 100 {
		return "high"
	}
	if samples >= 50 {
		return "medium"
	}
	return "low"
}

func validateFilter(filter Filter) error {
	if filter.Dimension != DimensionAgentVersion && filter.Dimension != DimensionModelProfile {
		return ErrInvalid
	}
	if !validModule(strings.TrimSpace(filter.Module)) || filter.From.IsZero() || filter.To.IsZero() || !filter.From.Before(filter.To) || filter.To.Sub(filter.From) > 90*24*time.Hour || filter.Limit < 1 || filter.Limit > 100 {
		return ErrInvalid
	}
	return nil
}

func validModule(module string) bool {
	return module == "" || module == "companion" || module == "life" || module == "work"
}

func rateAnomaly(metric, label string, current, baseline, warningDelta, criticalDelta, warningFloor, criticalFloor float64) []Anomaly {
	delta := current - baseline
	severity := ""
	if current >= criticalFloor && delta >= criticalDelta {
		severity = "critical"
	} else if current >= warningFloor && delta >= warningDelta {
		severity = "warning"
	}
	if severity == "" {
		return nil
	}
	return []Anomaly{{Metric: metric, Severity: severity, Current: current, Baseline: baseline, Change: delta, Message: fmt.Sprintf("%s较基线上升 %.1f 个百分点", label, delta*100)}}
}

func ratioAnomaly(metric, label string, current, baseline, warningRatio, criticalRatio float64) []Anomaly {
	if current <= 0 || baseline <= 0 {
		return nil
	}
	ratio := current / baseline
	severity := ""
	if ratio >= criticalRatio {
		severity = "critical"
	} else if ratio >= warningRatio {
		severity = "warning"
	}
	if severity == "" {
		return nil
	}
	return []Anomaly{{Metric: metric, Severity: severity, Current: current, Baseline: baseline, Change: ratio - 1, Message: fmt.Sprintf("%s达到基线的 %.2f 倍", label, ratio)}}
}
