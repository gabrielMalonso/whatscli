package main

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestReactionTargetDirect(t *testing.T) {
	chat, sender, err := reactionTarget("+15551234567", "")
	if err != nil {
		t.Fatalf("reactionTarget: %v", err)
	}
	if chat.String() != "15551234567@s.whatsapp.net" {
		t.Fatalf("chat = %q", chat.String())
	}
	if !sender.IsEmpty() {
		t.Fatalf("sender = %q, want empty", sender.String())
	}
}

func TestReactionTargetGroupRequiresSender(t *testing.T) {
	_, _, err := reactionTarget("12345@g.us", "")
	if err == nil || !strings.Contains(err.Error(), "--sender is required") {
		t.Fatalf("expected sender error, got %v", err)
	}
}

func TestReactionTargetGroupSender(t *testing.T) {
	chat, sender, err := reactionTarget("12345@g.us", "+15551234567")
	if err != nil {
		t.Fatalf("reactionTarget: %v", err)
	}
	if chat.Server != types.GroupServer {
		t.Fatalf("chat = %q, want group", chat.String())
	}
	if sender.String() != "15551234567@s.whatsapp.net" {
		t.Fatalf("sender = %q", sender.String())
	}
}
