package studio

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Caller holds a.mu. This short preflight avoids external moderation or edit
// preparation for already restricted workspaces. Every final acceptance still
// repeats the same gate inside its business transaction. Management being
// disabled must never remove an existing persistent restriction.
func (a *App) checkGenerationAccessLocked(workspaceID string) error {
	if a.store == nil {
		return nil
	}
	return a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = checkWorkspaceGenerationSQL(ctx, tx, workspaceID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}
