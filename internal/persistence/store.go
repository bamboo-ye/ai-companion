package persistence

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/billing"
	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/email"
	"github.com/windcry1/ai-companion/internal/eventbus"
	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/opsauth"
	"github.com/windcry1/ai-companion/internal/persistence/mysqlstore"
	"github.com/windcry1/ai-companion/internal/persistence/postgresstore"
	"github.com/windcry1/ai-companion/internal/planner"
	"github.com/windcry1/ai-companion/internal/reliability"
	"github.com/windcry1/ai-companion/internal/safety"
	"github.com/windcry1/ai-companion/internal/skill"
	"github.com/windcry1/ai-companion/internal/team"
)

// ApplicationStore is the complete durable persistence contract shared by the
// API and background Worker. Keeping the contract in one place prevents a
// database cutover from silently falling back to in-memory storage for one
// domain.
type ApplicationStore interface {
	identity.Store
	identity.AdminStore
	character.Store
	conversation.Store
	memory.Store
	document.Store
	document.IngestStore
	document.CleanupStore
	ledger.Store
	planner.Store
	skill.Store
	team.Store
	email.Store
	billing.Store
	safety.Store
	opsauth.Store
	eventbus.Store
	eventbus.OperationsStore

	EnqueueDueNotifications(context.Context, time.Time, int) (int, error)
	DeliverNotification(context.Context, string, time.Time) error
	ReliabilitySample(context.Context, time.Time) (reliability.Sample, error)
	Close() error
}

var (
	_ ApplicationStore = (*mysqlstore.Store)(nil)
	_ ApplicationStore = (*postgresstore.Store)(nil)
)

func Open(ctx context.Context, driver, mysqlDSN, postgresDSN string) (ApplicationStore, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "mysql":
		return mysqlstore.Open(ctx, mysqlDSN)
	case "postgres", "postgresql", "pgx":
		return postgresstore.Open(ctx, postgresDSN)
	default:
		return nil, fmt.Errorf("unsupported persistent database driver %q", driver)
	}
}
