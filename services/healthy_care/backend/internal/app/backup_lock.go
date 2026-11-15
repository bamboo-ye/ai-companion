package app

import (
	"context"
	"time"
)

// Immutable uploads may proceed during a database snapshot. Physical deletion
// waits for the daily backup's exclusive lock so every frozen DB reference has
// its file until tar has finished.
func (a *App) removeFilesForCleanup(ctx context.Context, user string, ids []string) error {
	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock_shared(626120261019)"); err != nil {
		_ = conn.Conn().Close(context.Background())
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock_shared(626120261019)"); err != nil {
			_ = conn.Conn().Close(context.Background())
		}
	}()
	return a.removeUserFiles(user, ids)
}
