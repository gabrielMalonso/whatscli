package app

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type recoveryRegistrationWA struct {
	*fakeWA
	app *App
	t   *testing.T
}

func (f *recoveryRegistrationWA) AddEventHandler(handler func(any)) uint32 {
	if !f.app.appStateRecoveryMu.TryLock() {
		f.t.Error("SDK handler registration holds the app-state closing gate")
	} else {
		f.app.appStateRecoveryMu.Unlock()
	}
	return f.fakeWA.AddEventHandler(handler)
}

func TestLateAppStateRecoveryRegistrationReleasesClosingGate(t *testing.T) {
	a := newTestApp(t)
	a.wa = &recoveryRegistrationWA{fakeWA: newFakeWA(), app: a, t: t}
	if _, err := a.newAppStateRecoveryCapture(appstate.WAPatchRegularLow, true); err != nil {
		t.Fatal(err)
	}
	a.Close()
}

func TestLateAppStateRecoveryPersistsSDKEventsWithoutCheckpoint(t *testing.T) {
	a, fixture, chat := sdkMirrorFixture(t)
	a.wa = fixture
	if err := a.db.MarkAppStateRecoveryRequired("regular_low"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if err := a.recoverMismatchingAppState(ctx, appstate.WAPatchRegularLow, &appStatePersistenceTracker{}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recovery error = %v", err)
	}
	// Inject through the pinned SDK after the waiter has returned. There is
	// no global persistence handler to hide a lost late capture.
	injectRecoveryArchive(t, fixture, chat)
	if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, err := a.db.GetChat(chat.String())
	if err != nil || !row.Archived {
		t.Fatalf("late SDK state lost: %+v, %v", row, err)
	}
	if debt, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || !debt {
		t.Fatalf("unattributed completion erased debt: %v, %v", debt, err)
	}
}

func TestLateAppStateRecoveryPreservesDispatchOrderAndNewerDebt(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("15550000013", types.DefaultUserServer)
	if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}
	capture, err := a.newAppStateRecoveryCapture(appstate.WAPatchRegularLow, false)
	if err != nil {
		t.Fatal(err)
	}
	capture.releaseBuffered()
	if err := a.db.MarkAppStateRecoveryRequired("regular_low"); err != nil {
		t.Fatal(err)
	}
	// Hold persistence until both events were dispatched. Replaying a batch
	// only after a sentinel would reverse these writes.
	ticket := a.appStatePersist.reserve()
	f.emit(&events.Mute{JID: chat, Action: &waSyncAction.MuteAction{Muted: proto.Bool(true)}})
	f.emit(&events.MarkChatAsRead{JID: chat, Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(false)}})
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Recovery: true})
	a.handleAppStatePersistenceEvent(t.Context(), &events.MarkChatAsRead{JID: chat, Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(true), MessageRange: &waSyncAction.SyncActionMessageRange{LastMessageTimestamp: proto.Int64(time.Now().Unix() + 1)}}}, nil)
	frontier := a.appStatePersist.complete(ticket, func() {})
	if err := a.appStatePersist.waitThrough(t.Context(), frontier); err != nil {
		t.Fatal(err)
	}
	row, err := a.db.GetChat(chat.String())
	if err != nil || row.Unread || row.MutedUntil != 0 {
		t.Fatalf("late state reordered or wrong collection persisted: %+v, %v", row, err)
	}
	if debt, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || !debt {
		t.Fatalf("newer debt lost: %v, %v", debt, err)
	}
}

func TestLateAppStateRecoveryCaptureLifetime(t *testing.T) {
	for _, ending := range []string{"close", "expire", "replace"} {
		t.Run(ending, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			synctest.Test(t, func(t *testing.T) {
				first, err := a.newAppStateRecoveryCapture(appstate.WAPatchRegularLow, false)
				if err != nil {
					t.Fatal(err)
				}
				switch ending {
				case "close":
					a.Close()
				case "expire":
					time.Sleep(lateAppStateRecoveryMaxWait)
				case "replace":
					if _, err := a.newAppStateRecoveryCapture(appstate.WAPatchRegularLow, false); err != nil {
						t.Fatal(err)
					}
				}
				synctest.Wait()
				first.mu.Lock()
				stopped := first.stopped
				first.mu.Unlock()
				if !stopped {
					t.Fatal("old capture still admits events")
				}
				f.mu.Lock()
				count := len(f.handlers)
				f.mu.Unlock()
				want := 0
				if ending == "replace" {
					want = 1
				}
				if count != want {
					t.Fatalf("handlers = %d, want %d", count, want)
				}
				// A callback copied before removal is harmless after teardown.
				first.handle(&events.MarkChatAsRead{JID: types.NewJID("123", types.DefaultUserServer)})
				a.Close()
			})
		})
	}
}
