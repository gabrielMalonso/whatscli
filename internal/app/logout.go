package app

import (
	"context"
	"fmt"
)

// Logout clears a known revoked local session without trying to log it in.
// Other sessions still require confirmed login and successful remote logout.
// The caller owns the store lock; archive data and revocation evidence remain.
func (a *App) Logout(ctx context.Context) error {
	if a.opts.ReadOnly {
		return fmt.Errorf("read-only mode: command would log out of WhatsApp")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	revoked, err := SessionRevoked(a.opts.StoreDir)
	if err != nil {
		return err
	}
	if err := a.OpenWA(); err != nil {
		return err
	}
	if revoked {
		return a.wa.DeleteSession(ctx)
	}
	if err := a.EnsureAuthedWithoutMigration(ctx); err != nil {
		return err
	}
	if err := a.Connect(ctx, false, nil); err != nil {
		// A newly observed logout belongs to this SDK client's lifetime. Retry
		// with a fresh client instead of racing the SDK's own device deletion.
		if revoked, markerErr := SessionRevoked(a.opts.StoreDir); markerErr == nil && revoked {
			return fmt.Errorf("%w; %s", err, loggedOutRecoveryHint)
		}
		return err
	}
	return a.wa.Logout(ctx)
}
