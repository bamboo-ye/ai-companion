package worker

import (
	"context"
	"log/slog"
	"time"
)

type Runner struct {
	logger   *slog.Logger
	interval time.Duration
}

func New(logger *slog.Logger, interval time.Duration) *Runner {
	return &Runner{logger: logger, interval: interval}
}

func (r *Runner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.logger.Info("worker ready")

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.logger.Debug("worker heartbeat")
		}
	}
}
