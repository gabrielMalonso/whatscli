package app

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gabrielMalonso/whatscli/internal/out"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func failureFenceSQL(t *testing.T, a *App) *sql.DB {
	t.Helper()
	raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return raw
}

func blockAppStateIntentWrites(t *testing.T, raw *sql.DB) {
	t.Helper()
	if _, err := raw.Exec(`CREATE TRIGGER fail_failure_intent BEFORE INSERT ON app_state_recovery_intents BEGIN SELECT RAISE(FAIL, 'synthetic marker failure'); END`); err != nil {
		t.Fatal(err)
	}
}

func restoreAppStateIntentWrites(t *testing.T, raw *sql.DB) {
	t.Helper()
	if _, err := raw.Exec(`DROP TRIGGER fail_failure_intent`); err != nil {
		t.Fatal(err)
	}
}

func TestAppStateFailedRenewalFencesCheckpointAndMutation(t *testing.T) {
	for _, mode := range []string{"delta", "full", "mutation"} {
		t.Run(mode, func(t *testing.T) {
			a := newTestApp(t)
			raw := failureFenceSQL(t, a)
			var output bytes.Buffer
			a.opts.Events = out.NewEventWriter(&output, true)
			var budget sync.Map
			budget.Store("regular_low", struct{}{})
			fetches := 0
			f := &appStateContextWA{fakeWA: newFakeWA()}
			f.fetchEvents = func(ctx context.Context, name string, full, _ bool) ([]any, error) {
				fetches++
				if fetches == 1 {
					blockAppStateIntentWrites(t, raw)
					a.handleAppStateSyncError(ctx, &events.AppStateSyncError{Name: appstate.WAPatchName(name), Error: appstate.ErrMismatchingLTHash}, &budget)
					// Even if SQLite recovers before checkpointing, this fetch
					// cannot certify coverage of the newly observed failure.
					restoreAppStateIntentWrites(t, raw)
				} else if !full {
					t.Error("later work did not replay the uncovered failure")
				}
				return nil, nil
			}
			a.wa = f
			switch mode {
			case "delta":
				if err := a.syncChatStateBeforeWrite(t.Context(), appstate.WAPatchRegularLow); err == nil {
					t.Fatal("unretained failure authorized pre-write success")
				}
			case "full":
				a.recoverAppStateCollection(t.Context(), "regular_low", time.Second)
				if strings.Contains(output.String(), "app_state_full_sync_completed") {
					t.Fatal("unretained failure reported full recovery success")
				}
			case "mutation":
				if err := a.ArchiveChat(t.Context(), types.NewJID("15550000002", types.DefaultUserServer), true); err == nil || len(f.archiveCalls) != 0 {
					t.Fatalf("unretained failure crossed mutation boundary: error=%v calls=%d", err, len(f.archiveCalls))
				}
			}
			if required, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || !required {
				t.Fatalf("older checkpoint erased durable coverage: %v, %v", required, err)
			}
			// Persist the fence before the next replay. That later successful
			// full fetch may reconcile it without another automatic worker.
			if err := a.syncAndPersistAppStateDelta(t.Context(), appstate.WAPatchRegularLow, false); err != nil {
				t.Fatal(err)
			}
			if required, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || required || fetches != 2 {
				t.Fatalf("later replay did not reconcile: required=%v error=%v fetches=%d", required, err, fetches)
			}
		})
	}
}

func TestAppStateFailedRenewalAfterFetchFencesWaitingCheckpoint(t *testing.T) {
	a := newTestApp(t)
	raw := failureFenceSQL(t, a)
	synctest.Test(t, func(t *testing.T) {
		f := &appStateContextWA{fakeWA: newFakeWA()}
		f.fetchEvents = func(context.Context, string, bool, bool) ([]any, error) { return nil, nil }
		a.wa = f
		ticket := a.appStatePersist.reserve()
		var budget sync.Map
		evt := &events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: appstate.ErrMismatchingLTHash}
		a.handleAppStateSyncError(t.Context(), evt, &budget)
		synctest.Wait()
		if a.ownsManualAppStateFetch(evt.Name) {
			t.Fatal("fixture did not reach persistence after fetch")
		}
		blockAppStateIntentWrites(t, raw)
		a.handleAppStateSyncError(t.Context(), evt, &budget)
		restoreAppStateIntentWrites(t, raw)
		a.appStatePersist.complete(ticket, func() {})
		synctest.Wait()
		a.appStateRecoveryWorkers.Wait()
		if required, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || !required {
			t.Fatalf("waiting checkpoint erased uncovered failure: %v, %v", required, err)
		}
		a.Close()
	})
}

func TestAppStateFailedRenewalBlocksFetchUntilDurable(t *testing.T) {
	a := newTestApp(t)
	raw := failureFenceSQL(t, a)
	f := newFakeWA()
	a.wa = f
	var budget sync.Map
	budget.Store("regular_low", struct{}{})
	blockAppStateIntentWrites(t, raw)
	for range 3 {
		a.handleAppStateSyncError(t.Context(), &events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: appstate.ErrMismatchingLTHash}, &budget)
		if err := a.syncAndPersistAppStateDelta(t.Context(), appstate.WAPatchRegularLow, false); err == nil {
			t.Fatal("failed retention allowed another fetch")
		}
	}
	if len(f.appStateFetches) != 0 || len(f.appStateRecoveries) != 0 {
		t.Fatal("failed retention started SDK work")
	}
	restoreAppStateIntentWrites(t, raw)
	if err := a.syncAndPersistAppStateDelta(t.Context(), appstate.WAPatchRegularLow, false); err != nil {
		t.Fatal(err)
	}
	if len(f.appStateFetches) != 1 || !f.appStateFetches[0].fullSync {
		t.Fatal("restored persistence did not require a full replay")
	}
}

func TestAppStateFailedRenewalIsCollectionScopedAndSurvivesCancellation(t *testing.T) {
	a := newTestApp(t)
	raw := failureFenceSQL(t, a)
	f := newFakeWA()
	a.wa = f
	low, _, err := a.beginAppStateRecovery("regular_low")
	if err != nil {
		t.Fatal(err)
	}
	high, _, err := a.beginAppStateRecovery("regular_high")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var budget sync.Map
	blockAppStateIntentWrites(t, raw)
	evt := &events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: appstate.ErrMismatchingLTHash}
	a.handleAppStateSyncError(ctx, evt, &budget)
	restoreAppStateIntentWrites(t, raw)
	if err := a.clearCompletedAppStateRecovery(appstate.WAPatchRegularHigh, high); err != nil {
		t.Fatalf("unrelated collection was fenced: %v", err)
	}
	if err := a.clearCompletedAppStateRecovery(appstate.WAPatchRegularLow, low); err == nil {
		t.Fatal("cancellation discarded the failed-renewal fence")
	}
	// A later error can make all prior failure debt durable. Its generation
	// must still fence the original checkpoint without starting a worker.
	a.handleAppStateSyncError(ctx, evt, &budget)
	if err := a.clearCompletedAppStateRecovery(appstate.WAPatchRegularLow, low); err == nil {
		t.Fatal("renewed generation did not fence the original checkpoint")
	}
	if len(f.appStateFetches) != 0 {
		t.Fatal("cancelled failure started a worker")
	}
	if err := a.syncAndPersistAppStateDelta(t.Context(), appstate.WAPatchRegularLow, false); err != nil {
		t.Fatal(err)
	}
}
