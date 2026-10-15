package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	appPkg "github.com/gabrielMalonso/whatscli/internal/app"
	"github.com/gabrielMalonso/whatscli/internal/config"
	"github.com/gabrielMalonso/whatscli/internal/lock"
	"github.com/gabrielMalonso/whatscli/internal/sqliteutil"
	"github.com/gabrielMalonso/whatscli/internal/store"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
)

func seedLogoutSession(t *testing.T, dir string) {
	t.Helper()
	c, err := sqlstore.New(t.Context(), "sqlite3", sqliteutil.FileURI(filepath.Join(dir, "session.db"), "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	d := c.NewDevice()
	d.ID = &types.JID{User: "15550000001", Device: 7, Server: types.DefaultUserServer}
	d.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := d.Save(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// Compare retained records rather than SQLite page/header bookkeeping from
// opening the writable archive and refreshing its schema/FTS support.
func logoutArchiveRows(t *testing.T, dir string) map[string][][]any {
	t.Helper()
	db, err := sql.Open("sqlite3", readOnlySessionSQLiteURI(filepath.Join(dir, "wacli.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result := make(map[string][][]any)
	for _, table := range []string{"messages", "drafts", "draft_revisions", "outbound_operations", "outbound_observations", "change_feed_identity", "archive_changes"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			result[table] = append(result[table], values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	return result
}

func TestAuthLogoutRecoversSelectedRevokedAccountAndPreservesArchive(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(config.EnvStoreDir, "")
	t.Setenv("WACLI_READONLY", "0")
	dir, other := t.TempDir(), t.TempDir()
	cfg := &config.AccountsConfig{DefaultAccount: "other", Accounts: map[string]config.AccountEntry{
		"selected": {Store: dir}, "other": {Store: other},
	}}
	if err := config.SaveAccountsConfig(config.DefaultConfigPath(), cfg); err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(config.DefaultConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	db, _ := outboundCLISeed(t, dir, 1)
	at := time.Unix(1700000000, 0)
	if err := db.UpsertChat(localReadLID, "dm", "Retained chat", at); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: localReadLID, MsgID: "retained", SenderJID: localReadLID, Text: "retained history", Timestamp: at}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordChangeReceipts(t.Context(), localReadLID, []string{"retained"}, false, store.ChangeReceipt{Type: "read", ActorJID: localReadLID, EventAt: at}); err != nil {
		t.Fatal(err)
	}
	page, err := db.ListChanges(t.Context(), dir, 100, "")
	if err != nil || len(page.Changes) < 2 {
		t.Fatalf("seed changes = %+v, %v", page, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	otherDB, _ := outboundCLISeed(t, other, 2)
	if err := otherDB.Close(); err != nil {
		t.Fatal(err)
	}
	seedLogoutSession(t, dir)
	seedLogoutSession(t, other)
	if err := appPkg.MarkSessionRevoked(dir, "401: logged out from another device"); err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(dir, "processed-page.checkpoint")
	if err := os.WriteFile(checkpoint, []byte(page.NextCursor), 0o600); err != nil {
		t.Fatal(err)
	}
	archiveBefore := logoutArchiveRows(t, dir)
	otherBefore := snapshotLocalStore(t, other)
	markerBefore, err := os.ReadFile(filepath.Join(dir, "SESSION_REVOKED"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		stdout := captureRootStdout(t, func() {
			if err := execute([]string{"-a", "selected", "--json", "auth", "logout"}); err != nil {
				t.Fatalf("recover selected revoked session: %v", err)
			}
		})
		if !strings.Contains(stdout, `"logged_out":true`) {
			t.Fatalf("stdout = %q", stdout)
		}
	}
	for path, want := range map[string][]byte{
		filepath.Join(dir, "SESSION_REVOKED"): markerBefore,
		config.DefaultConfigPath():            configBefore,
		checkpoint:                            []byte(page.NextCursor),
	} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("recovery changed retained data %s: %v", filepath.Base(path), err)
		}
	}
	if !reflect.DeepEqual(archiveBefore, logoutArchiveRows(t, dir)) {
		t.Fatal("recovery changed retained archive records")
	}
	if !reflect.DeepEqual(otherBefore, snapshotLocalStore(t, other)) {
		t.Fatal("recovery changed another account")
	}
	session, err := sql.Open("sqlite3", filepath.Join(dir, "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var devices int
	if err := session.QueryRow("SELECT count(*) FROM whatsmeow_device").Scan(&devices); err != nil || devices != 0 {
		t.Fatalf("retained devices = %d, %v", devices, err)
	}
	authed, _, err := readOnlyAuthStatus(dir)
	if err != nil || authed {
		t.Fatalf("status after recovery = %t, %v", authed, err)
	}
	resumed, err := store.OpenReadOnly(filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if next, err := resumed.ListChanges(t.Context(), dir, 100, page.NextCursor); err != nil || len(next.Changes) != 0 {
		t.Fatalf("processed checkpoint cannot resume: %+v, %v", next, err)
	}
}

func TestAuthLogoutRespectsReadOnlyLockAndSelection(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(config.EnvStoreDir, "")
	t.Setenv("WACLI_READONLY", "0")
	dir := t.TempDir()
	seedLogoutSession(t, dir)
	if err := appPkg.MarkSessionRevoked(dir, "logged_out"); err != nil {
		t.Fatal(err)
	}
	lk, err := lock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	before := snapshotLocalStore(t, dir)
	for _, tc := range []struct {
		args      []string
		env, want string
	}{
		{args: []string{"--store", dir, "--read-only", "auth", "logout"}, want: "read-only"},
		{args: []string{"--store", dir, "auth", "logout"}, env: "1", want: "read-only"},
		{args: []string{"--store", dir, "--lock-wait", "1ms", "auth", "logout"}, want: "store is locked"},
		{args: []string{"-a", "missing", "auth", "logout"}, want: "Selected local archive"},
	} {
		t.Setenv("WACLI_READONLY", tc.env)
		if err := execute(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v = %v, want %q", tc.args, err, tc.want)
		}
		if !reflect.DeepEqual(before, snapshotLocalStore(t, dir)) {
			t.Fatal("refused logout modified session/store")
		}
	}
}

func TestAuthLogoutReportsMissingSessionAndCleanupFailure(t *testing.T) {
	t.Setenv("WACLI_READONLY", "0")
	for _, revoked := range []bool{false, true} {
		dir := t.TempDir()
		want := "not authenticated"
		if revoked {
			seedLogoutSession(t, dir)
			if err := appPkg.MarkSessionRevoked(dir, "logged_out"); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite3", filepath.Join(dir, "session.db"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`CREATE TRIGGER refuse_device_delete BEFORE DELETE ON whatsmeow_device BEGIN SELECT RAISE(ABORT, 'fixture deletion refused'); END`)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			want = "fixture deletion refused"
		}
		var logoutErr error
		stdout := captureRootStdout(t, func() {
			captureRootStderr(t, func() {
				logoutErr = execute([]string{"--store", dir, "--json", "auth", "logout"})
			})
		})
		if logoutErr == nil || !strings.Contains(logoutErr.Error(), want) || stdout != "" {
			t.Fatalf("logout revoked=%t: stdout=%q, err=%v", revoked, stdout, logoutErr)
		}
		if got, err := appPkg.SessionRevoked(dir); err != nil || got != revoked {
			t.Fatalf("revocation after failed logout = %t, %v", got, err)
		}
	}
}
