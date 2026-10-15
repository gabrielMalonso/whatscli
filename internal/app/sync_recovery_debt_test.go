package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gabrielMalonso/whatscli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestAppStateFailureAfterStartupCheckpointRetainsBoundedDebt(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	for _, name := range mirroredAppStateCollections {
		if err := a.db.MarkAppStateRecoveryRequired(string(name)); err != nil {
			t.Fatal(err)
		}
	}
	var budget sync.Map
	a.syncAppStateDeltas(t.Context(), &budget)
	if got := ReadAppStateDiagnostics(a.db); len(got.PendingCollections) != 0 {
		t.Fatalf("startup did not checkpoint: %+v", got)
	}
	evt := &events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: appstate.ErrMismatchingLTHash}
	a.handleAppStateSyncError(t.Context(), evt, &budget)
	var repeated sync.WaitGroup
	for range 20 {
		repeated.Go(func() { a.handleAppStateSyncError(t.Context(), evt, &budget) })
	}
	repeated.Wait()
	a.appStateRecoveryWorkers.Wait()
	if len(f.appStateFetches) != 3 || len(f.appStateRecoveries) != 0 {
		t.Fatalf("spent budget started more work: fetches=%d snapshots=%d", len(f.appStateFetches), len(f.appStateRecoveries))
	}
	a.Close()
	db, err := store.OpenReadOnly(filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if required, err := db.AppStateRecoveryRequired("regular_low"); err != nil || !required {
		t.Fatalf("new failure did not survive reopen: %v, %v", required, err)
	}
	raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var count int
	if err := raw.QueryRow(`SELECT count(*) FROM app_state_recovery_intents`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("repeated failures grew debt: count=%d error=%v", count, err)
	}
}

func TestAppStateFailureFencesEarlierCheckpoint(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	var budget sync.Map
	budget.Store("regular_low", struct{}{})
	generation, _, err := a.db.BeginAppStateRecovery("regular_low")
	if err != nil {
		t.Fatal(err)
	}
	a.handleAppStateSyncError(t.Context(), &events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: appstate.ErrMismatchingLTHash}, &budget)
	if err := a.clearCompletedAppStateRecovery(appstate.WAPatchRegularLow, generation); err == nil {
		t.Fatal("earlier checkpoint did not detect new failure")
	}
	if required, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || !required {
		t.Fatalf("earlier checkpoint cleared new debt: %v, %v", required, err)
	}
	// A later covered replay can reconcile the renewed failure.
	if err := a.syncAndPersistAppStateDelta(t.Context(), appstate.WAPatchRegularLow, false); err != nil {
		t.Fatal(err)
	}
	if required, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || required {
		t.Fatalf("later replay did not clear covered debt: %v, %v", required, err)
	}
}

func TestAppStateFailureDuringRecoveryPersistenceSurvivesCheckpoint(t *testing.T) {
	a := newTestApp(t)
	synctest.Test(t, func(t *testing.T) {
		f := &appStateContextWA{fakeWA: newFakeWA()}
		f.fetchEvents = func(context.Context, string, bool, bool) ([]any, error) { return nil, nil }
		a.wa = f
		// Hold an earlier persistence task so the full fetch has returned but
		// cannot checkpoint. A newer delta failure must outlive that worker.
		ticket := a.appStatePersist.reserve()
		var budget sync.Map
		evt := &events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: appstate.ErrMismatchingLTHash}
		a.handleAppStateSyncError(t.Context(), evt, &budget)
		synctest.Wait()
		if a.ownsManualAppStateFetch(evt.Name) {
			t.Fatal("fixture did not reach persistence after fetch")
		}
		a.handleAppStateSyncError(t.Context(), evt, &budget)
		a.appStatePersist.complete(ticket, func() {})
		synctest.Wait()
		a.appStateRecoveryWorkers.Wait()
		if required, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || !required {
			t.Fatalf("completed worker erased later failure: %v, %v", required, err)
		}
		a.Close()
	})
}

func TestAppStateCancelledFailureRetainsDebtWithoutWorker(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var budget sync.Map
	for range 5 {
		a.handleAppStateSyncError(ctx, &events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: appstate.ErrMismatchingLTHash}, &budget)
	}
	a.appStateRecoveryWorkers.Wait()
	if len(f.appStateFetches) != 0 || len(f.appStateRecoveries) != 0 {
		t.Fatal("cancelled callback started recovery")
	}
	if required, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || !required {
		t.Fatalf("cancelled callback lost failure: %v, %v", required, err)
	}
}

func TestAppStateFailureMarkerErrorDoesNotStartRecovery(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER fail_intent BEFORE INSERT ON app_state_recovery_intents BEGIN SELECT RAISE(FAIL, 'synthetic marker failure'); END`); err != nil {
		t.Fatal(err)
	}
	ctx, run := ingestionTestRun(t, a)
	var budget sync.Map
	a.handleAppStateSyncError(ctx, &events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: appstate.ErrMismatchingLTHash}, &budget)
	a.appStateRecoveryWorkers.Wait()
	if len(f.appStateFetches) != 0 || len(run.recovery.snapshot()) != 2 {
		t.Fatal("marker failure must be observed without advancing SDK state")
	}
}

func TestAppStateManualFetchErrorKeepsItsOwnDebt(t *testing.T) {
	a := newTestApp(t)
	var budget sync.Map
	f := &appStateContextWA{fakeWA: newFakeWA()}
	f.fetchEvents = func(ctx context.Context, name string, _, _ bool) ([]any, error) {
		a.handleAppStateSyncError(ctx, &events.AppStateSyncError{Name: appstate.WAPatchName(name), Error: appstate.ErrMismatchingLTHash}, &budget)
		return nil, appstate.ErrMismatchingLTHash
	}
	a.wa = f
	if err := a.syncAndPersistAppStateDelta(t.Context(), appstate.WAPatchRegularLow, false); !errors.Is(err, appstate.ErrMismatchingLTHash) {
		t.Fatal(err)
	}
	a.appStateRecoveryWorkers.Wait()
	if _, started := budget.Load("regular_low"); started {
		t.Fatal("manual fetch callback admitted a second worker")
	}
	if required, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || !required {
		t.Fatalf("manual fetch lost write-ahead debt: %v, %v", required, err)
	}
}

func TestAppStateErrorDuringManualFetchCannotBeCheckpointed(t *testing.T) {
	a := newTestApp(t)
	var budget sync.Map
	f := &appStateContextWA{fakeWA: newFakeWA()}
	f.fetchEvents = func(ctx context.Context, name string, _, _ bool) ([]any, error) {
		// SDK callbacks do not identify which fetch produced the error. Even
		// if our own fetch succeeds, it cannot dismiss this observed failure.
		a.handleAppStateSyncError(ctx, &events.AppStateSyncError{Name: appstate.WAPatchName(name), Error: appstate.ErrMismatchingLTHash}, &budget)
		return nil, nil
	}
	a.wa = f
	if err := a.syncAndPersistAppStateDelta(t.Context(), appstate.WAPatchRegularLow, false); err == nil {
		t.Fatal("manual fetch checkpoint dismissed an uncorrelated SDK failure")
	}
	if _, started := budget.Load("regular_low"); started {
		t.Fatal("manual callback admitted another worker")
	}
	if required, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || !required {
		t.Fatalf("manual checkpoint cleared observed failure: %v, %v", required, err)
	}
}

func TestStartupFullFetchWaitsForMissingKeysWithinBudget(t *testing.T) {
	for _, outcome := range []string{"delivered", "deadline", "cancelled", "mismatch"} {
		t.Run(outcome, func(t *testing.T) {
			a := newTestApp(t)
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				fetches, snapshots := 0, 0
				f := &appStateContextWA{fakeWA: newFakeWA()}
				f.fetchEvents = func(context.Context, string, bool, bool) ([]any, error) {
					fetches++
					if outcome == "cancelled" {
						cancel()
					}
					if fetches == 1 || outcome == "deadline" || outcome == "cancelled" {
						return nil, appstate.ErrKeyNotFound
					}
					if outcome == "mismatch" {
						return nil, appstate.ErrMismatchingLTHash
					}
					return nil, nil
				}
				f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
					snapshots++
					return "", errors.New("synthetic snapshot refusal")
				}
				a.wa = f
				var budget sync.Map
				budget.Store("regular_low", struct{}{})
				a.recoverAppStateCollection(ctx, "regular_low", time.Second)
				wantFetches, wantSnapshots := 2, 0
				if outcome == "deadline" {
					wantFetches = 3
				}
				if outcome == "cancelled" {
					wantFetches = 1
				}
				if outcome == "mismatch" {
					wantSnapshots = 1
				}
				if fetches != wantFetches || snapshots != wantSnapshots {
					t.Fatalf("fetches=%d snapshots=%d; want %d, %d", fetches, snapshots, wantFetches, wantSnapshots)
				}
				if required, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || required != (outcome != "delivered") {
					t.Fatalf("debt=%v error=%v for %s", required, err, outcome)
				}
				a.Close()
			})
		})
	}
}
