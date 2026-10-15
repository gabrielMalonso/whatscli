package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gabrielMalonso/whatscli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

const backgroundAppStateRecoveryInterval = 6 * time.Hour

// Keep the in-memory result even if SQLite fails, but retry persistence on
// every full-sync result. Serialize both so an older write cannot win on disk.
func (a *App) setAppStateFullSyncResult(collection appstate.WAPatchName, broken bool) {
	a.appStateDegradedMu.Lock()
	defer a.appStateDegradedMu.Unlock()
	a.loadAppStateFullSyncResultsLocked()
	if a.appStateFullSyncBroken == nil {
		a.appStateFullSyncBroken = make(map[string]bool)
	}
	a.appStateFullSyncBroken[string(collection)] = broken
	if err := a.db.SetAppStateFullSyncBroken(string(collection), broken); err != nil {
		a.warnAppStateRecovery(string(collection), fmt.Errorf("persist full sync verification flag: %w", err))
	}
}

func (a *App) isAppStateFullSyncBroken(collection appstate.WAPatchName) bool {
	a.appStateDegradedMu.Lock()
	defer a.appStateDegradedMu.Unlock()
	a.loadAppStateFullSyncResultsLocked()
	return a.appStateFullSyncBroken[string(collection)]
}

func (a *App) loadAppStateFullSyncResultsLocked() {
	if a.appStateFullSyncLoaded {
		return
	}
	collections, err := a.db.AppStateFullSyncBrokenCollections()
	if err != nil {
		a.emitWarning("app_state_full_sync_flags_load_failed", fmt.Sprintf("warning: failed to load app state full sync verification flags: %v", err), nil)
		return
	}
	a.appStateFullSyncLoaded = true
	if a.appStateFullSyncBroken == nil {
		a.appStateFullSyncBroken = make(map[string]bool)
	}
	for _, name := range collections {
		// A result recorded during a failed load takes precedence on retry.
		if _, observed := a.appStateFullSyncBroken[name]; !observed {
			a.appStateFullSyncBroken[name] = true
		}
	}
}

// A verified incremental read can admit a write on a known-broken collection
// with an existing SDK cursor. It cannot replay mutations already consumed by
// that cursor, so older debt and post-send intents remain durable.
func (a *App) degradedAppStateReplay(ctx context.Context, collection appstate.WAPatchName, generation int64, tracker *appStatePersistenceTracker) (bool, error) {
	if !a.isAppStateFullSyncBroken(collection) {
		return false, nil
	}
	hasLocal, err := a.wa.HasLocalAppState(ctx, string(collection))
	if err != nil || !hasLocal {
		return false, nil
	}
	fetchErr, persistErr := a.fetchAndPersistAppState(ctx, collection, false, tracker)
	if persistErr != nil {
		return true, fmt.Errorf("persist incremental app state %s: %w", collection, persistErr)
	}
	if fetchErr != nil {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	a.appStateRecoveryMu.Lock()
	failure := a.appStateFailureIntents[string(collection)]
	if failure.retentionErr != nil || failure.generation > generation {
		a.appStateRecoveryMu.Unlock()
		return true, fmt.Errorf("WhatsApp app state failure changed during %s incremental synchronization: %w", collection, errors.Join(failure.retentionErr, wa.ErrAppStateCompletionUnconfirmed))
	}
	// This exact marker belongs to the pre-write read, not older missing
	// events or the dependent send's separate intent.
	err = a.db.ClearAppStateRecoveryIntent(string(collection), generation)
	a.appStateRecoveryMu.Unlock()
	if err != nil {
		return true, err
	}
	a.emitWarning("app_state_degraded_replay", fmt.Sprintf("app state %s: full sync is known not to verify; proceeding after incremental sync and retaining older replay debt", collection), map[string]any{"name": string(collection)})
	a.requestBackgroundAppStateRecovery(collection)
	return true, nil
}

// Register persistence before sending, including for standalone callers. This
// fork cannot attribute public snapshot completion to the request; background
// responses persist events but never clear older generations.
func (a *App) requestBackgroundAppStateRecovery(collection appstate.WAPatchName) {
	name := string(collection)
	a.appStateDegradedMu.Lock()
	if last, ok := a.appStateRecoveryRequested[name]; ok && time.Since(last) < backgroundAppStateRecoveryInterval {
		a.appStateDegradedMu.Unlock()
		return
	}
	if a.appStateRecoveryRequested == nil {
		a.appStateRecoveryRequested = make(map[string]time.Time)
	}
	a.appStateRecoveryRequested[name] = time.Now()
	a.appStateDegradedMu.Unlock()
	capture, err := a.newAppStateRecoveryCapture(collection, true)
	if err != nil {
		return
	}
	a.appStateRecoveryMu.Lock()
	defer a.appStateRecoveryMu.Unlock()
	if a.appStateRecoveryClosing {
		return
	}
	a.appStateRecoveryWorkers.Go(func() {
		ctx, cancel := context.WithTimeout(capture.ctx, appStateRecoveryStepTimeout)
		defer cancel()
		exchange, err := a.wa.RequestAppStateRecoveryObserved(ctx, name, func(id types.MessageID) {
			a.emitWarning("app_state_recovery_requested_background", fmt.Sprintf("app state %s: requested primary recovery in the background", collection), map[string]any{"name": name, "id": string(id)})
		})
		a.emitOrPrint("app_state_recovery_observed", map[string]any{"requested_collection": name, "ack_confirmed": exchange.ACKConfirmed, "response_received": exchange.ResponseReceived, "completion": "unconfirmed"}, "\rApp state %s background recovery: ack confirmed=%t, response received=%t; completion unconfirmed\n", collection, exchange.ACKConfirmed, exchange.ResponseReceived)
		if err != nil && !errors.Is(err, wa.ErrAppStateCompletionUnconfirmed) && !errors.Is(err, context.Canceled) {
			a.warnAppStateRecovery(name, err)
		}
	})
}
