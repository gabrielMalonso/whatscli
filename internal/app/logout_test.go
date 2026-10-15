package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabrielMalonso/whatscli/internal/wa"
	"go.mau.fi/whatsmeow/types/events"
)

type logoutWA struct {
	*handshakeWA
	remoteCalls, deleteCalls int
	remoteErr, deleteErr     error
}

func (f *logoutWA) Logout(ctx context.Context) error {
	f.remoteCalls++
	return f.remoteErr
}

func (f *logoutWA) DeleteSession(ctx context.Context) error {
	f.deleteCalls++
	return f.deleteErr
}

func TestLogoutSessionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		revoked, missing, ro    bool
		connectErr              error
		rejectLogin             bool
		remoteErr, deleteErr    error
		wantErr                 string
		connects, remote, local int
	}{
		{name: "recorded revocation", revoked: true, local: 1},
		{name: "already deleted revoked device", revoked: true, missing: true, local: 1},
		{name: "valid session", connects: 1, remote: 1},
		{name: "missing session", missing: true, wantErr: "not authenticated"},
		{name: "network failure retains session", connectErr: errors.New("offline"), connects: 1, wantErr: "offline"},
		{name: "remote logout failure retains session", remoteErr: errors.New("logout refused"), connects: 1, remote: 1, wantErr: "logout refused"},
		{name: "new revocation requires fresh client", rejectLogin: true, connects: 1, wantErr: "auth logout"},
		{name: "delete failure is reported", revoked: true, local: 1, deleteErr: errors.New("disk failure"), wantErr: "disk failure"},
		{name: "readonly revoked session", ro: true, revoked: true, wantErr: "read-only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			a.opts.ReadOnly = tc.ro
			if tc.revoked {
				if err := MarkSessionRevoked(a.StoreDir(), "401: logged out from another device"); err != nil {
					t.Fatal(err)
				}
			}
			f := &logoutWA{handshakeWA: &handshakeWA{fakeWA: newFakeWA()}, remoteErr: tc.remoteErr, deleteErr: tc.deleteErr}
			f.authed = !tc.missing
			connects := 0
			f.connect = func(_ context.Context, opts wa.ConnectOptions) error {
				connects++
				if opts.AllowQR || opts.OnQRCode != nil {
					t.Error("logout allowed pairing")
				}
				if tc.rejectLogin {
					f.emit(&events.LoggedOut{Reason: events.ConnectFailureLoggedOut})
				} else if tc.connectErr == nil {
					f.emit(&events.Connected{})
				}
				return tc.connectErr
			}
			a.opts.WAFactory = func(wa.Options) (WAClient, error) { return f, nil }
			err := a.Logout(t.Context())
			if (tc.wantErr == "" && err != nil) || (tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr))) {
				t.Fatalf("Logout = %v, want %q", err, tc.wantErr)
			}
			if connects != tc.connects || f.remoteCalls != tc.remote || f.deleteCalls != tc.local {
				t.Fatalf("connect/remote/local = %d/%d/%d, want %d/%d/%d", connects, f.remoteCalls, f.deleteCalls, tc.connects, tc.remote, tc.local)
			}
			revoked, err := SessionRevoked(a.StoreDir())
			if err != nil || revoked != (tc.revoked || tc.rejectLogin) {
				t.Fatalf("revocation evidence = %t, %v", revoked, err)
			}
		})
	}
}

func TestLogoutRefusesUnreadableRevocationAndCancelledContext(t *testing.T) {
	a := newTestApp(t)
	marker := filepath.Join(a.StoreDir(), sessionRevokedFilename)
	if err := os.Symlink(sessionRevokedFilename, marker); err != nil {
		t.Skipf("cannot create unreadable marker fixture: %v", err)
	}
	a.opts.WAFactory = func(wa.Options) (WAClient, error) {
		t.Fatal("opened session despite failed prerequisite")
		return nil, nil
	}
	if err := a.Logout(t.Context()); err == nil || !strings.Contains(err.Error(), "read session revoked marker") {
		t.Fatalf("Logout = %v, want marker read failure", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := a.Logout(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Logout = %v, want cancellation", err)
	}
}
