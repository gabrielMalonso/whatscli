package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gabrielMalonso/whatscli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const appStateRecoveryStepTimeout = 30 * time.Second

type appStateFailureIntent struct {
	generation   int64
	retentionErr error
}

// renewAppStateFailure requires appStateRecoveryMu. Keep a failed renewal in
// memory so no older checkpoint can retire the remaining durable coverage.
func (a *App) renewAppStateFailure(name string) error {
	intent := a.appStateFailureIntents[name]
	generation, err := a.db.RenewAppStateRecoveryIntent(name, intent.generation)
	if err == nil {
		intent.generation = generation
	}
	intent.retentionErr = err
	if a.appStateFailureIntents == nil {
		a.appStateFailureIntents = make(map[string]appStateFailureIntent)
	}
	a.appStateFailureIntents[name] = intent
	return err
}

// beginAppStateRecovery promotes any uncovered failure to durable debt before
// capturing a generation for new work. Only a later covered replay can clear it.
func (a *App) beginAppStateRecovery(name string) (int64, bool, error) {
	a.appStateRecoveryMu.Lock()
	defer a.appStateRecoveryMu.Unlock()
	if a.appStateFailureIntents[name].retentionErr != nil {
		if err := a.renewAppStateFailure(name); err != nil {
			return 0, false, fmt.Errorf("retain observed WhatsApp app state failure for %s: %w", name, err)
		}
	}
	return a.db.BeginAppStateRecovery(name)
}

func (a *App) handleAppStateSyncError(ctx context.Context, evt *events.AppStateSyncError, recoveries *sync.Map) {
	if evt == nil {
		return
	}
	if a.regularLowEnabled(evt.Name) {
		a.appStateRecoveryMu.Lock()
		defer a.appStateRecoveryMu.Unlock()
		if !a.appStateRecoveryClosing {
			if err := a.renewAppStateFailure(string(evt.Name)); err != nil {
				a.warnAppStateRecovery(string(evt.Name), err)
			}
		}
		return
	}
	code, reason := "app_state_lthash_mismatch", "hit an LTHash mismatch"
	if errors.Is(evt.Error, wa.ErrEmptyAppStateKeyShare) {
		code, reason = "app_state_key_unavailable", "requires a key shared without data"
	} else if !errors.Is(evt.Error, appstate.ErrMismatchingLTHash) {
		return
	}
	name := strings.TrimSpace(string(evt.Name))
	if name == "" {
		return
	}
	if recoveries == nil {
		recoveries = &sync.Map{}
	}
	a.appStateRecoveryMu.Lock()
	defer a.appStateRecoveryMu.Unlock()
	if a.appStateRecoveryClosing {
		return
	}
	// Retain the SDK failure that prompted recovery, even if the refresh succeeds.
	phase := appStateRecoveryDelta
	if evt.FullSync {
		phase = appStateRecoveryFullSync
	}
	recordAppStateRecovery(ctx, name, phase, evt.Error)
	// Debt is independent of the automatic worker budget. Renew only our
	// previous failure intent so repeated errors stay bounded, while a fetch
	// that started earlier cannot checkpoint this newer failure away.
	if err := a.renewAppStateFailure(name); err != nil {
		recordAppStateRecovery(ctx, name, appStateRecoveryPrepare, err)
		a.warnAppStateRecovery(name, err)
		return
	}
	if ctx.Err() != nil {
		return
	}
	// A callback can belong to an independent SDK fetch while our fetch is
	// still returning. Retain it conservatively, but never fetch recursively
	// from a callback under the SDK lock or admit a second recovery worker.
	if a.ownsManualAppStateFetch(evt.Name) {
		return
	}
	// Before login, foreground bootstrap will pick up this durable debt.
	// Do not start recovery traffic or spend its budget on a pairing socket.
	if state := a.sessionState; state != nil {
		state.mu.Lock()
		confirmed := state.confirmed
		state.mu.Unlock()
		if !confirmed {
			return
		}
	}
	if _, loaded := recoveries.LoadOrStore(name, struct{}{}); loaded {
		return
	}

	a.appStateRecoveryWorkers.Go(func() {
		a.emitWarning(code,
			fmt.Sprintf("warning: app state %s %s; attempting full sync", name, reason),
			map[string]any{"name": name})
		a.recoverAppStateCollection(ctx, name, appStateRecoveryStepTimeout)
	})
}

func (a *App) recoverAppStateCollection(ctx context.Context, name string, timeout time.Duration) {
	if a.regularLowEnabled(appstate.WAPatchName(name)) {
		recoveryCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := a.syncAndPersistAppStateDelta(recoveryCtx, appstate.WAPatchRegularLow, false); err != nil {
			a.warnAppStateRecovery(name, err)
		}
		return
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, timeout)
	finishLock := a.measureSyncStage(lockCtx, "lock_wait", name)
	release, err := a.acquireChatStateSync(lockCtx)
	finishLock(err)
	cancelLock()
	if err != nil {
		recordAppStateRecovery(ctx, name, appStateRecoveryPrepare, err)
		a.warnAppStateRecovery(name, err)
		return
	}
	defer release()

	generation, _, err := a.beginAppStateRecovery(name)
	if err != nil {
		recordAppStateRecovery(ctx, name, appStateRecoveryPrepare, err)
		a.warnAppStateRecovery(name, err)
		return
	}
	collection := appstate.WAPatchName(name)
	tracker := &appStatePersistenceTracker{}
	fetchCtx, cancelFetch := context.WithTimeout(ctx, timeout)
	fetchErr, persistenceErr := a.fetchFullAppStateWithKeyWait(fetchCtx, collection, tracker)
	fetchErr = errors.Join(fetchErr, fetchCtx.Err())
	cancelFetch()
	recordAppStateRecovery(ctx, name, appStateRecoveryFullSync, fetchErr)
	if persistenceErr != nil {
		recordAppStateRecovery(ctx, name, appStateRecoveryPersist, persistenceErr)
		a.warnAppStateRecovery(name, fmt.Errorf("persist full app state replay: %w", persistenceErr))
		return
	}
	if fetchErr == nil {
		err := a.clearCompletedAppStateRecovery(collection, generation)
		recordAppStateRecovery(ctx, name, appStateRecoveryCheckpoint, err)
		if err != nil {
			a.warnAppStateRecovery(name, err)
			return
		}
		a.emitOrPrint("app_state_full_sync_completed", map[string]any{"name": name},
			"\rApp state %s resolved via full sync\n", name)
		return
	}
	if ctx.Err() != nil {
		return
	}
	if errors.Is(fetchErr, appstate.ErrKeyNotFound) {
		// Missing keys are requested by the SDK. Exhausting their delivery
		// window must retain debt without escalating to a fatal snapshot.
		a.warnAppStateRecovery(name, fetchErr)
		return
	}
	a.emitWarning("app_state_full_sync_failed",
		fmt.Sprintf("warning: app state %s full sync failed: %v; requesting recovery snapshot", name, fetchErr),
		map[string]any{"name": name, "error": fetchErr.Error()})

	// A full-fetch timeout must not consume the primary recovery budget.
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, timeout)
	defer cancelRecovery()
	err = a.recoverMismatchingAppState(recoveryCtx, collection, tracker, func(id types.MessageID) {
		if a.eventsEnabled() {
			a.emitSyncObservationEvent(ctx, "app_state_recovery_requested", map[string]any{"name": name, "id": string(id)})
		} else {
			fmt.Fprintf(os.Stderr, "\rRequested app state %s recovery (id %s)\n", name, id)
		}
	})
	recordAppStateRecovery(ctx, name, appStateRecoverySnapshot, err)
	if err != nil {
		a.warnAppStateRecovery(name, err)
	}
}

func (a *App) warnAppStateRecovery(name string, err error) {
	if errors.Is(err, wa.ErrAppStateCompletionUnconfirmed) {
		a.emitWarning("app_state_recovery_unconfirmed", fmt.Sprintf("warning: app state %s recovery completion unconfirmed; replay debt retained", name), map[string]any{"name": name})
		return
	}
	a.emitWarning("app_state_recovery_failed",
		fmt.Sprintf("warning: app state %s recovery failed: %v", name, err),
		map[string]any{"name": name, "error": err.Error()})
}

func (a *App) syncAppStateDeltas(ctx context.Context, recoveries *sync.Map) {
	pending, err := a.db.AppStateRecoveryCollections()
	if err != nil {
		a.emitWarning("app_state_sync_failed", fmt.Sprintf("warning: cannot inspect app state recovery: %v", err),
			map[string]any{"error": err.Error()})
		return
	}
	for _, name := range pending {
		if _, loaded := recoveries.LoadOrStore(name, struct{}{}); !loaded {
			a.recoverAppStateCollection(ctx, name, appStateRecoveryStepTimeout)
		}
	}
	for _, name := range mirroredAppStateCollections {
		if _, recovering := recoveries.Load(string(name)); recovering {
			continue
		}
		fullSync := name == appstate.WAPatchRegular
		if err := a.syncAndPersistAppStateDelta(ctx, name, fullSync); err != nil {
			recordAppStateRecovery(ctx, string(name), appStateRecoveryDelta, err)
			if errors.Is(err, wa.ErrEmptyAppStateKeyShare) || errors.Is(err, appstate.ErrMismatchingLTHash) {
				a.handleAppStateSyncError(ctx, &events.AppStateSyncError{Name: name, FullSync: fullSync, Error: err}, recoveries)
				continue
			}
			a.emitWarning("app_state_sync_failed",
				fmt.Sprintf("warning: failed to sync WhatsApp app state %s: %v", name, err),
				map[string]any{"name": string(name), "error": err.Error()})
		}
	}
}

func (a *App) syncAndPersistAppStateDelta(ctx context.Context, name appstate.WAPatchName, fullSync bool) (syncErr error) {
	finish := a.measureSyncStage(ctx, "delta", string(name))
	defer func() { finish(syncErr) }()
	finishLock := a.measureSyncStage(ctx, "lock_wait", string(name))
	release, err := a.acquireChatStateSync(ctx)
	finishLock(err)
	if err != nil {
		return err
	}
	defer release()
	generation, required, err := a.beginAppStateRecovery(string(name))
	if err != nil {
		return err
	}
	tracker := &appStatePersistenceTracker{}
	if a.regularLowEnabled(name) {
		return a.syncRegularLow(ctx, generation, tracker)
	}
	if required {
		ctx, cancel := context.WithTimeout(ctx, appStateRecoveryStepTimeout)
		defer cancel()
		return a.replayRequiredAppState(ctx, name, generation, tracker)
	}
	// The SDK commits each page before returning its collected events. A later
	// page error can discard them all, so the intent must precede the fetch.
	fetchErr, persistenceErr := a.fetchAndPersistAppState(ctx, name, fullSync, tracker)
	if err := errors.Join(fetchErr, persistenceErr, ctx.Err()); err != nil {
		return err
	}
	return a.clearCompletedAppStateRecovery(name, generation)
}

func (a *App) warnEmptyAppStateKey(evt *wa.AppStateKeyUnavailable) {
	if evt == nil {
		return
	}
	keyID := fmt.Sprintf("%X", evt.KeyID)
	a.emitWarning("app_state_key_unavailable",
		fmt.Sprintf("warning: primary device shared app state key %s without data; collections that cannot read it will attempt snapshot recovery", keyID),
		map[string]any{"key_id": keyID})
}
