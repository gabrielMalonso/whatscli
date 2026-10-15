package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gabrielMalonso/whatscli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestSyncBootstrapWaitsForLogin(t *testing.T) {
	for _, mode := range []string{"qr", "phone", "authenticated"} {
		for _, outcome := range []string{"login", "cancel", "deadline", "revoked", "rejected"} {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				a := newTestApp(t)
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
					defer cancel()
					stop := errors.New("stop after metadata")
					opts := SyncOptions{Mode: SyncModeBootstrap, PresenceMode: SyncPresenceModeQuiet,
						AllowQR: mode == "qr", OnQRCode: func(string) {}, OnPairCode: func(string) {},
						AfterConnect: func(context.Context) error { return stop }}
					if mode == "phone" {
						opts.PairPhoneNumber = "15550000001"
					}
					f := &handshakeWA{fakeWA: newFakeWA()}
					f.connect = func(_ context.Context, got wa.ConnectOptions) error {
						if got.AllowQR != opts.AllowQR || got.PairPhoneNumber != opts.PairPhoneNumber || got.OnQRCode == nil || got.OnPairCode == nil || !got.SuppressInitialAvailablePresence || got.DetachSocket != (mode == "authenticated") {
							t.Errorf("connect options changed: %+v", got)
						}
						f.emit(&events.PairSuccess{})
						// The normal SDK pairing reconnect is a Disconnected event,
						// not a terminal StreamError (including for stream code 515).
						f.emit(&events.Disconnected{})
						f.emit(&events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: appstate.ErrMismatchingLTHash})
						return nil
					}
					a.wa = f
					done := make(chan error, 1)
					go func() { _, err := a.Sync(ctx, opts); done <- err }()
					synctest.Wait()
					select {
					case err := <-done:
						t.Fatalf("bootstrap continued after pairing alone: %v", err)
					default:
					}
					if len(f.appStateFetches) != 0 || len(f.appStateRecoveries) != 0 || a.SyncLiveSnapshot().OwnerReady {
						t.Fatal("bootstrap work started before login")
					}
					want := ""
					switch outcome {
					case "login":
						f.emit(&events.Connected{})
					case "cancel":
						cancel()
						want = "context canceled"
					case "deadline":
						time.Sleep(time.Minute)
						want = "deadline exceeded"
					case "revoked":
						f.emit(&events.LoggedOut{Reason: events.ConnectFailureLoggedOut})
						f.emit(&events.Connected{})
						want = "revoked"
					case "rejected":
						f.emit(&events.ConnectFailure{Reason: events.ConnectFailureServiceUnavailable})
						want = "login failed"
					}
					synctest.Wait()
					err := <-done
					if want == "" && !errors.Is(err, stop) || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
						t.Fatalf("Sync error = %v, want %q", err, want)
					}
					if want != "" && (len(f.appStateFetches) != 0 || len(f.appStateRecoveries) != 0) {
						t.Fatal("failed login started metadata recovery")
					}
					a.Close()
				})
			})
		}
	}
}

func TestConnectForSyncReusesOnlyCurrentConfirmation(t *testing.T) {
	a := newTestApp(t)
	synctest.Test(t, func(t *testing.T) {
		f := &handshakeWA{fakeWA: newFakeWA()}
		f.connect = func(context.Context, wa.ConnectOptions) error { return nil }
		a.wa = f
		if err := a.OpenWA(); err != nil {
			t.Fatal(err)
		}
		f.connected = true
		f.emit(&events.Connected{})
		if cleanup, err := a.connectForSync(t.Context(), SyncOptions{}); err != nil || !cleanup {
			t.Fatal(err)
		}
		f.connected = false // stale confirmation from a previous socket
		done := make(chan error, 1)
		go func() { _, err := a.connectForSync(t.Context(), SyncOptions{}); done <- err }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("reopened socket reused stale login: %v", err)
		default:
		}
		f.emit(&events.Connected{})
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		a.Close()
	})
}

func TestSyncLoginWaitFailureCleansUpOpenedTransport(t *testing.T) {
	for _, presence := range []SyncPresenceMode{SyncPresenceModeNormal, SyncPresenceModeQuiet} {
		for _, outcome := range []string{"cancelled", "deadline", "rejected", "revoked"} {
			t.Run(string(presence)+"/"+outcome, func(t *testing.T) {
				a := newTestApp(t)
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), time.Second)
					defer cancel()
					f := &handshakeWA{fakeWA: newFakeWA()}
					f.connect = func(ctx context.Context, opts wa.ConnectOptions) error {
						if !opts.DetachSocket {
							t.Fatal("authenticated sync did not detach the socket")
						}
						f.connected = true
						// The authenticated wrapper can send initial availability
						// before the session observer receives Connected.
						if !opts.SuppressInitialAvailablePresence {
							if err := f.SendPresence(ctx, types.PresenceAvailable); err != nil {
								t.Fatal(err)
							}
						}
						switch outcome {
						case "cancelled":
							cancel()
						case "rejected":
							f.emit(&events.ConnectFailure{Reason: events.ConnectFailureGeneric})
						case "revoked":
							f.emit(&events.LoggedOut{Reason: events.ConnectFailureLoggedOut})
						}
						return nil
					}
					a.wa = f
					_, err := a.Sync(ctx, SyncOptions{Mode: SyncModeOnce, PresenceMode: presence,
						AfterConnect: func(context.Context) error {
							t.Error("failed login reached metadata completion")
							return nil
						}})
					if err == nil {
						t.Fatal("login wait unexpectedly succeeded")
					}
					if presence == SyncPresenceModeNormal {
						assertPresenceCalls(t, f.fakeWA, types.PresenceAvailable, types.PresenceUnavailable)
					} else {
						assertPresenceCalls(t, f.fakeWA, types.PresenceUnavailable)
					}
					if len(f.appStateFetches) != 0 || len(f.appStateRecoveries) != 0 {
						t.Fatal("failed login started metadata traffic")
					}
					if required, err := a.db.AppStateRecoveryRequired("regular_low"); err != nil || !required {
						t.Fatalf("login cleanup did not retain replay debt: %v, %v", required, err)
					}
					a.Close()
				})
			})
		}
	}
}
