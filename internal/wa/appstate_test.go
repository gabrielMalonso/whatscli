package wa

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestHasLocalAppStateUsesExactCollectionCursor(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if has, err := c.HasLocalAppState(t.Context(), "regular_low"); err != nil || has {
		t.Fatalf("fresh collection = %v, %v", has, err)
	}
	if err := c.client.Store.AppState.PutAppStateVersion(t.Context(), "regular_low", 1173, [128]byte{}); err != nil {
		t.Fatal(err)
	}
	if has, err := c.HasLocalAppState(t.Context(), "regular_low"); err != nil || !has {
		t.Fatalf("restored collection = %v, %v", has, err)
	}
	if has, err := c.HasLocalAppState(t.Context(), "regular_high"); err != nil || has {
		t.Fatalf("unrelated collection = %v, %v", has, err)
	}
	if err := c.client.Store.AppState.DeleteAppStateVersion(t.Context(), "regular_low"); err != nil {
		t.Fatal(err)
	}
	if has, err := c.HasLocalAppState(t.Context(), "regular_low"); err != nil || has {
		t.Fatalf("reset collection = %v, %v", has, err)
	}
	c.Close()
	if _, err := c.HasLocalAppState(context.Background(), "regular_low"); err == nil {
		t.Fatal("closed session reported a usable cursor")
	}
}

func TestChatSettingsCacheExactJIDAndLifetime(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pn := types.NewJID("15550000001", types.DefaultUserServer)
	lid := types.NewJID("300", types.HiddenUserServer)
	if err := c.client.Store.ChatSettings.PutArchived(t.Context(), pn, true); err != nil {
		t.Fatal(err)
	}
	if err := c.client.Store.ChatSettings.PutPinned(t.Context(), pn, true); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetChatSettings(t.Context(), pn)
	if err != nil || !got.Found || !got.Archived || !got.Pinned {
		t.Fatal(got, err)
	}
	got, err = c.GetChatSettings(t.Context(), lid)
	if err != nil || got.Found {
		t.Fatalf("guessed missing alias: %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.GetChatSettings(ctx, pn); err == nil {
		t.Fatal("cancelled cache read succeeded")
	}
	original := c.client.Store.ChatSettings
	c.client.Store.ChatSettings = nil
	if _, err := c.GetChatSettings(t.Context(), pn); err == nil {
		t.Fatal("missing SDK store invented settings")
	}
	c.client.Store.ChatSettings = original
	c.Close()
	if _, err := c.GetChatSettings(t.Context(), pn); err == nil {
		t.Fatal("closed SDK invented settings")
	}
}
