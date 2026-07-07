package reliability

import (
	"fmt"
	"sync"
	"time"
)

type Level int

const (
	L0 Level = iota
	L1
	L2
	L3
)

func (l Level) String() string { return fmt.Sprintf("L%d", int(l)) }

type Sample struct {
	QueueLag       int
	OldestJobAge   time.Duration
	ModelErrorRate float64
	P95Latency     time.Duration
}

type Policy struct {
	UseFullRAG          bool   `json:"use_full_rag"`
	ExtractMemory       bool   `json:"extract_memory"`
	PreferredModelClass string `json:"preferred_model_class"`
	LongSkillsQueued    bool   `json:"long_skills_queued"`
	AcceptOnly          bool   `json:"accept_only"`
}

type Snapshot struct {
	Level            string    `json:"level"`
	Reason           string    `json:"reason"`
	ObservedAt       time.Time `json:"observed_at"`
	ChangedAt        time.Time `json:"changed_at"`
	CandidateLevel   string    `json:"candidate_level"`
	CandidateSamples int       `json:"candidate_samples"`
	QueueLag         int       `json:"queue_lag"`
	OldestJobAgeMS   int64     `json:"oldest_job_age_ms"`
	ModelErrorRate   float64   `json:"model_error_rate"`
	P95LatencyMS     int64     `json:"p95_latency_ms"`
	Policy           Policy    `json:"policy"`
}

type Config struct {
	EnterSamples   int
	RecoverSamples int
	MinDwell       time.Duration
}

type Controller struct {
	mu             sync.RWMutex
	config         Config
	level          Level
	reason         string
	changedAt      time.Time
	observedAt     time.Time
	last           Sample
	candidate      Level
	candidateCount int
}

func NewController(config Config) *Controller {
	if config.EnterSamples <= 0 {
		config.EnterSamples = 3
	}
	if config.RecoverSamples <= 0 {
		config.RecoverSamples = 5
	}
	if config.MinDwell <= 0 {
		config.MinDwell = 30 * time.Second
	}
	return &Controller{config: config, level: L0, candidate: L0, reason: "signals healthy"}
}

func (c *Controller) Observe(sample Sample, now time.Time) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	now = now.UTC()
	target, reason := classify(sample)
	c.last, c.observedAt = sample, now
	if c.changedAt.IsZero() {
		c.changedAt = now
	}
	if target == c.level {
		c.candidate, c.candidateCount, c.reason = c.level, 0, reason
		return c.snapshotLocked()
	}
	candidate, required := target, c.config.EnterSamples
	if target < c.level {
		candidate = c.level - 1
		required = c.config.RecoverSamples
		if now.Sub(c.changedAt) < c.config.MinDwell {
			c.candidate, c.candidateCount = candidate, 0
			return c.snapshotLocked()
		}
	}
	if c.candidate != candidate {
		c.candidate, c.candidateCount = candidate, 1
	} else {
		c.candidateCount++
	}
	if c.candidateCount >= required {
		c.level, c.reason, c.changedAt = candidate, reason, now
		c.candidate, c.candidateCount = c.level, 0
	}
	return c.snapshotLocked()
}

func (c *Controller) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshotLocked()
}

func (c *Controller) Policy() Policy {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return policyFor(c.level)
}

func (c *Controller) snapshotLocked() Snapshot {
	return Snapshot{
		Level: c.level.String(), Reason: c.reason, ObservedAt: c.observedAt, ChangedAt: c.changedAt,
		CandidateLevel: c.candidate.String(), CandidateSamples: c.candidateCount,
		QueueLag: c.last.QueueLag, OldestJobAgeMS: c.last.OldestJobAge.Milliseconds(), ModelErrorRate: c.last.ModelErrorRate, P95LatencyMS: c.last.P95Latency.Milliseconds(),
		Policy: policyFor(c.level),
	}
}

func classify(sample Sample) (Level, string) {
	if sample.ModelErrorRate >= 0.80 || sample.OldestJobAge >= 2*time.Minute {
		return L3, "dependency unavailable or oldest job exceeds protection threshold"
	}
	if sample.QueueLag >= 500 || sample.OldestJobAge >= time.Minute || sample.P95Latency >= 10*time.Second || sample.ModelErrorRate >= 0.50 {
		return L2, "queue or dependency signals exceed heavy-degradation threshold"
	}
	if sample.QueueLag >= 100 || sample.OldestJobAge >= 15*time.Second || sample.P95Latency >= 3*time.Second || sample.ModelErrorRate >= 0.20 {
		return L1, "queue or dependency signals exceed light-degradation threshold"
	}
	return L0, "signals healthy"
}

func policyFor(level Level) Policy {
	switch level {
	case L1:
		return Policy{UseFullRAG: false, ExtractMemory: false, PreferredModelClass: "small", LongSkillsQueued: true}
	case L2:
		return Policy{UseFullRAG: false, ExtractMemory: false, PreferredModelClass: "low_latency", LongSkillsQueued: true}
	case L3:
		return Policy{UseFullRAG: false, ExtractMemory: false, PreferredModelClass: "local_only", LongSkillsQueued: true, AcceptOnly: true}
	default:
		return Policy{UseFullRAG: true, ExtractMemory: true, PreferredModelClass: "primary", LongSkillsQueued: true}
	}
}
