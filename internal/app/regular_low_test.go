package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gabrielMalonso/whatscli/internal/store"
	"github.com/gabrielMalonso/whatscli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// SDK crypto/send confirmation is tested in the SDK. This fake supplies only
// its public result and lifecycle observations to the real persistence pipeline.
type regularLowWA struct {
	*chatStateAgentWA
	policyMu    sync.Mutex
	status      whatsmeow.RegularLowPolicyStatus
	projection  map[types.JID]whatsmeow.RegularLowChatProjection
	identityErr error
	pairResult  *wa.PublicPairResult
	afterPair   func()
	afterSend   func([]any) []any
}

func (f *regularLowWA) RegularLowPolicyStatus() whatsmeow.RegularLowPolicyStatus {
	f.policyMu.Lock()
	defer f.policyMu.Unlock()
	return f.status
}
func (f *regularLowWA) RegularLowChatProjection(jid types.JID) whatsmeow.RegularLowChatProjection {
	f.policyMu.Lock()
	defer f.policyMu.Unlock()
	if !f.status.Valid || !f.status.FetchComplete {
		return whatsmeow.RegularLowChatProjection{}
	}
	return f.projection[jid]
}
func (f *regularLowWA) InvalidateRegularLowPolicy() {
	f.policyMu.Lock()
	defer f.policyMu.Unlock()
	f.status.Valid, f.status.FetchComplete = false, false
}
func (f *regularLowWA) LookupLocalAlias(ctx context.Context, jid types.JID) (types.JID, error) {
	if f.identityErr != nil {
		return types.JID{}, f.identityErr
	}
	return f.fakeWA.LookupLocalAlias(ctx, jid)
}
func (f *regularLowWA) CheckPublicPair(ctx context.Context, pn, lid types.JID) (wa.PublicPairResult, error) {
	if f.pairResult != nil {
		return *f.pairResult, nil
	}
	result, err := f.fakeWA.CheckPublicPair(ctx, pn, lid)
	if err == nil && f.afterPair != nil {
		f.afterPair()
	}
	return result, err
}
func (f *regularLowWA) ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID {
	f.resolves.Add(1)
	return f.fakeWA.ResolveLIDToPN(ctx, jid)
}
func (f *regularLowWA) sendState(ctx context.Context, patch appstate.PatchInfo, boundary func()) ([]any, error) {
	if _, err := f.chatStateAgentWA.sendPatch(ctx, patch, boundary); err != nil {
		return nil, err
	}
	var returned []any
	for _, m := range patch.Mutations {
		jid, err := types.ParseJID(m.Index[1])
		if err != nil {
			return nil, err
		}
		switch {
		case m.Value.GetArchiveChatAction() != nil:
			returned = append(returned, &events.Archive{JID: jid, Action: m.Value.GetArchiveChatAction()})
		case m.Value.GetPinAction() != nil:
			returned = append(returned, &events.Pin{JID: jid, Action: m.Value.GetPinAction()})
		case m.Value.GetMarkChatAsReadAction() != nil:
			returned = append(returned, &events.MarkChatAsRead{JID: jid, Action: m.Value.GetMarkChatAsReadAction()})
		}
	}
	if f.afterSend != nil {
		returned = f.afterSend(returned)
	}
	return returned, nil
}
func (f *regularLowWA) ArchiveChat(ctx context.Context, jid types.JID, archive bool, ts time.Time, key *waCommon.MessageKey, boundary func()) ([]any, error) {
	f.mu.Lock()
	f.archiveCalls = append(f.archiveCalls, fakeArchiveCall{target: jid, archive: archive, lastMsgTS: ts, lastMsgKey: key})
	f.mu.Unlock()
	return f.sendState(ctx, appstate.BuildArchive(jid, archive, ts, key), boundary)
}
func (f *regularLowWA) PinChat(ctx context.Context, jid types.JID, pin bool, boundary func()) ([]any, error) {
	return f.sendState(ctx, appstate.BuildPin(jid, pin), boundary)
}
func (f *regularLowWA) MarkChatAsRead(ctx context.Context, jid types.JID, read bool, ts time.Time, key *waCommon.MessageKey, boundary func()) ([]any, error) {
	return f.sendState(ctx, appstate.BuildMarkChatAsRead(jid, read, ts, key), boundary)
}

func regularLowFixture(t *testing.T) (*App, *regularLowWA, types.JID, types.JID) {
	t.Helper()
	a, base, _ := chatStateAgentFixture(t)
	pn, _ := types.ParseJID(chatStatePeerPN)
	lid, _ := types.ParseJID(chatStatePeerLID)
	f := &regularLowWA{chatStateAgentWA: base, status: whatsmeow.RegularLowPolicyStatus{Enabled: true, Anchored: true, Valid: true, FetchComplete: true}, projection: make(map[types.JID]whatsmeow.RegularLowChatProjection)}
	a.wa = f
	f.lids[lid] = pn
	if err := a.db.UpsertChat(pn.String(), "dm", "Synthetic", time.Unix(30, 0)); err != nil {
		t.Fatal(err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: pn.String(), MsgID: "anchor", SenderJID: lid.String(), FromMe: true, Timestamp: time.Unix(30, 0), Text: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatUnreadCount(pn.String(), 1); err != nil {
		t.Fatal(err)
	}
	return a, f, pn, lid
}

func TestRegularLowGroupSendRefusalPreservesMirror(t *testing.T) {
	for _, preflight := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_submission", false: "after_submission"}[preflight], func(t *testing.T) {
			a, f, _, _ := regularLowFixture(t)
			group := types.NewJID("123456-789", types.GroupServer)
			if err := a.db.UpsertChat(group.String(), "group", "Synthetic group", time.Unix(30, 0)); err != nil {
				t.Fatal(err)
			}
			f.sendHook = func(_ context.Context, boundary func()) ([]any, error) {
				boundary()
				if preflight {
					return nil, &whatsmeow.RegularLowSendPreflightError{Err: whatsmeow.ErrRegularLowProjectionUnknown}
				}
				f.InvalidateRegularLowPolicy()
				return nil, whatsmeow.ErrRegularLowSendUnconfirmed
			}
			request := ChatStateRequest{Version: 1, StoreRef: a.StoreDir(), Requested: group.String(), Action: ChatStateMarkUnread}
			result, err := a.ApplyAgentChatState(t.Context(), request)
			code, outcome := "chat_state_outcome_uncertain", ChatStateUncertain
			if preflight {
				code, outcome = "not_dispatched", ChatStateNotDispatched
			}
			assertChatStateFailure(t, err, code, outcome)
			row, readErr := a.db.GetChat(group.String())
			debt, debtErr := a.db.AppStateRecoveryRequired("regular_low")
			if readErr != nil || row.Unread || row.UnreadCount != 0 || result.LocalMirror != ChatStateMirrorUnknown || !debt || debtErr != nil || len(f.patches) != 1 {
				t.Fatalf("refused send changed the mirror, lost debt or retried: %+v %+v %v %v", row, result, readErr, debtErr)
			}
			if preflight && (!f.RegularLowPolicyStatus().Valid || !f.RegularLowPolicyStatus().FetchComplete) {
				t.Fatal("preflight refusal invalidated the anchor")
			}
		})
	}
}

func TestRegularLowOwnedResultSurvivesNotificationWithoutClearingNewDebt(t *testing.T) {
	a, f, pn, lid := regularLowFixture(t)
	if err := a.db.SetChatPinned(pn.String(), true); err != nil {
		t.Fatal(err)
	}
	generation, _, err := a.beginAppStateRecovery("regular_low")
	if err != nil {
		t.Fatal(err)
	}
	blocker := a.appStatePersist.reserve()
	fetched := make(chan struct{})
	f.fetchHook = func(context.Context, string, bool, bool) ([]any, error) {
		close(fetched)
		return []any{&events.Archive{JID: lid, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}}}, nil
	}
	done := make(chan error, 1)
	go func() {
		fetchErr, persistErr := a.fetchAndPersistAppState(t.Context(), appstate.WAPatchRegularLow, false, &appStatePersistenceTracker{})
		done <- errors.Join(fetchErr, persistErr)
	}()
	<-fetched
	// The SDK's notification marks the getter incomplete after returning its
	// authenticated result, while the owned ticket is still behind SQLite work.
	f.policyMu.Lock()
	f.status.FetchComplete = false
	f.policyMu.Unlock()
	a.handleAppStateSyncError(t.Context(), &events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: errors.New("notification fetch interrupted")}, nil)
	a.appStatePersist.complete(blocker, func() {})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	row, err := a.db.GetChat(pn.String())
	if err != nil || !row.Archived || !row.Pinned {
		t.Fatalf("owned archive changed unknown pin: %+v %v", row, err)
	}
	if err := a.clearCompletedAppStateRecovery(appstate.WAPatchRegularLow, generation); err == nil {
		t.Fatal("older generation checkpoint succeeded")
	}
	debt, err := a.db.AppStateRecoveryRequired("regular_low")
	if err != nil || !debt {
		t.Fatal("new notification debt lost", err)
	}
	if err := a.persistAppStateEvent(t.Context(), &events.Archive{JID: pn}, nil); !errors.Is(err, whatsmeow.ErrRegularLowProjectionUnknown) {
		t.Fatal("independent callback bypassed masked getter", err)
	}
	if !f.RegularLowPolicyStatus().Valid {
		t.Fatal("incomplete getter invalidated anchor")
	}
}

func TestRegularLowArchiveRequiresReturnedUnpin(t *testing.T) {
	for _, unpin := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing_pin", true: "known_unpin"}[unpin], func(t *testing.T) {
			a, f, pn, _ := regularLowFixture(t)
			if err := a.db.SetChatPinned(pn.String(), true); err != nil {
				t.Fatal(err)
			}
			f.afterSend = func(result []any) []any {
				f.policyMu.Lock()
				f.status.FetchComplete = false
				f.policyMu.Unlock()
				if !unpin {
					return result[:1]
				}
				return result
			}
			err := a.ArchiveChat(t.Context(), pn, true)
			row, readErr := a.db.GetChat(pn.String())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if unpin {
				if err != nil || !row.Archived || row.Pinned {
					t.Fatal(row, err)
				}
			} else if !errors.Is(err, whatsmeow.ErrRegularLowProjectionUnknown) || row.Archived || !row.Pinned {
				t.Fatal("unconfirmed archive reached mirror", row, err)
			}
		})
	}
}

func TestRegularLowArchiveRoundTripUsesBoundLID(t *testing.T) {
	for _, agent := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			t.Run(map[bool]string{false: "legacy", true: "agent"}[agent]+map[bool]string{false: "_PN_first", true: "_LID_first"}[reverse], func(t *testing.T) {
				a, f, pn, lid := regularLowFixture(t)
				historyIdentityFixture(t, a, `INSERT INTO whatsmeow_lid_map VALUES('300','15550000001')`)
				if err := a.db.SetChatArchivedProjection(pn.String(), true); err != nil {
					t.Fatal(err)
				}
				f.afterSend = func(echo []any) []any {
					// Authenticated PN shadows stay in the result but have no product
					// authority over the selected LID's archive or included unpin.
					shadow := []any{&events.Archive{JID: pn, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(false)}}, &events.Pin{JID: pn, Action: &waSyncAction.PinAction{Pinned: proto.Bool(true)}}}
					if reverse {
						return append(echo, shadow...)
					}
					return append(shadow, echo...)
				}
				for _, archive := range []bool{false, true} {
					var err error
					before := time.Now().Add(-time.Second)
					if agent {
						action := ChatStateUnarchive
						if archive {
							action = ChatStateArchive
						}
						result, applyErr := a.ApplyAgentChatState(t.Context(), ChatStateRequest{Version: 1, StoreRef: a.StoreDir(), Requested: pn.String(), Action: action})
						err = applyErr
						if err != nil {
							t.Fatal(err)
						}
						if result.LocalMirror != ChatStateMirrorPersisted || result.Observation == nil || result.Observation.Target.JID != pn.String() {
							t.Fatalf("agent target/mirror changed: %+v %v", result, err)
						}
					} else {
						err = a.ArchiveChat(t.Context(), pn, archive)
					}
					if err != nil {
						t.Fatal(err)
					}
					row, err := a.db.GetChat(pn.String())
					if err != nil || row.Archived != archive || row.Pinned {
						t.Fatal("canonical echo lost to PN shadow", row, err)
					}
					patch := f.patches[len(f.patches)-1]
					wantMutations := 1
					if archive {
						wantMutations = 2
					}
					if len(patch.Mutations) != wantMutations {
						t.Fatal("archive/unpin patch changed", patch)
					}
					for _, m := range patch.Mutations {
						if m.Index[1] != lid.String() {
							t.Fatal("archive/unpin wire identities differ", m.Index)
						}
					}
					call := f.archiveCalls[len(f.archiveCalls)-1]
					wantRange := appstate.BuildArchive(pn, archive, call.lastMsgTS, call.lastMsgKey).Mutations[0].Value.GetArchiveChatAction().GetMessageRange()
					if call.lastMsgKey != nil || call.lastMsgTS.Before(before) || !proto.Equal(wantRange, patch.Mutations[0].Value.GetArchiveChatAction().GetMessageRange()) {
						t.Fatal("archive range/key policy changed")
					}
				}
				// Standalone pin shares the verified index; it does not add an
				// unarchive mutation or infer an unarchive in SQLite.
				if err := a.PinChat(t.Context(), pn, true); err != nil {
					t.Fatal(err)
				}
				patch := f.patches[len(f.patches)-1]
				row, err := a.db.GetChat(pn.String())
				if err != nil || !row.Archived || !row.Pinned || len(patch.Mutations) != 1 || patch.Mutations[0].Index[1] != lid.String() {
					t.Fatal("pin changed archive semantics", row, err)
				}
				if f.resolves.Load() != 0 || len(f.readReceiptCalls) != 0 || len(f.patches) != 3 {
					t.Fatal("identity discovery/receipt/retry occurred")
				}
			})
		}
	}
}

func TestRegularLowFlagAliasesDoNotChooseStoreWinner(t *testing.T) {
	for _, conflict := range []string{"none", "archive", "pin"} {
		for _, reverse := range []bool{false, true} {
			t.Run(conflict+map[bool]string{false: "_PN_first", true: "_LID_first"}[reverse], func(t *testing.T) {
				a, f, pn, lid := regularLowFixture(t)
				pnFlags := whatsmeow.RegularLowChatProjection{ArchiveKnown: true, Archived: true, PinKnown: true, Pinned: false}
				lidFlags := pnFlags
				lidFlags.Archived = conflict != "archive"
				lidFlags.Pinned = conflict == "pin"
				f.projection[pn], f.projection[lid] = pnFlags, lidFlags
				flagsEvents := func(jid types.JID, p whatsmeow.RegularLowChatProjection) []any {
					return []any{&events.Archive{JID: jid, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(p.Archived)}}, &events.Pin{JID: jid, Action: &waSyncAction.PinAction{Pinned: proto.Bool(p.Pinned)}}}
				}
				returned := append(flagsEvents(pn, pnFlags), flagsEvents(lid, lidFlags)...)
				if reverse {
					for i, j := 0, len(returned)-1; i < j; i, j = i+1, j-1 {
						returned[i], returned[j] = returned[j], returned[i]
					}
				}
				reset := func() {
					if err := a.db.SetChatArchivedProjection(pn.String(), !lidFlags.Archived); err != nil {
						t.Fatal(err)
					}
					if err := a.db.SetChatPinned(pn.String(), !lidFlags.Pinned); err != nil {
						t.Fatal(err)
					}
				}
				assertFields := func(archived, pinned bool) {
					row, err := a.db.GetChat(pn.String())
					if err != nil || row.Archived != archived || row.Pinned != pinned {
						t.Fatalf("field conflict selected a winner: %+v %v", row, err)
					}
				}
				reset()
				if err := a.persistReturnedRegularLow(t.Context(), returned, nil); err != nil {
					t.Fatal(err)
				}
				assertFields(conflict != "archive", conflict == "pin")
				reset()
				for _, event := range returned {
					if err := a.persistAppStateEvent(t.Context(), event, nil); err != nil {
						t.Fatal(err)
					}
				}
				assertFields(conflict != "archive", conflict == "pin")
				if len(f.appStateFetches) != 0 || len(f.patches) != 0 {
					t.Fatal("projection fetched or mutated remote state")
				}
				reset()
				// Command confirmation still needs the selected LID archive and unpin.
				// A PN echo cannot repair an absent or conflicting canonical field.
				f.afterSend = func([]any) []any { return returned }
				err := a.ArchiveChat(t.Context(), pn, true)
				if conflict == "none" {
					if err != nil {
						t.Fatal(err)
					}
					assertFields(true, false)
				} else {
					if !errors.Is(err, whatsmeow.ErrRegularLowProjectionUnknown) {
						t.Fatal("conflicting canonical field confirmed command", err)
					}
					assertFields(!lidFlags.Archived, !lidFlags.Pinned)
				}
			})
		}
	}
}

func TestRegularLowFlagProjectionKeepsUnknownLIDFields(t *testing.T) {
	for _, field := range []string{"archive", "pin"} {
		for _, ambiguous := range []bool{false, true} {
			t.Run(field+map[bool]string{false: "_missing", true: "_ambiguous"}[ambiguous], func(t *testing.T) {
				a, f, pn, lid := regularLowFixture(t)
				if err := a.db.SetChatPinned(pn.String(), true); err != nil {
					t.Fatal(err)
				}
				pnArchive := &events.Archive{JID: pn, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}}
				pnPin := &events.Pin{JID: pn, Action: &waSyncAction.PinAction{Pinned: proto.Bool(false)}}
				f.projection[pn] = whatsmeow.RegularLowChatProjection{ArchiveKnown: true, Archived: true, PinKnown: true, Pinned: false}
				returned := []any{pnArchive, pnPin}
				if field == "archive" {
					f.projection[lid] = whatsmeow.RegularLowChatProjection{ArchiveAmbiguous: ambiguous, PinKnown: true, Pinned: false}
					returned = append(returned, &events.Pin{JID: lid, Action: &waSyncAction.PinAction{Pinned: proto.Bool(false)}})
				} else {
					f.projection[lid] = whatsmeow.RegularLowChatProjection{ArchiveKnown: true, Archived: true, PinAmbiguous: ambiguous}
					returned = append(returned, &events.Archive{JID: lid, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}})
				}
				f.fetchHook = func(context.Context, string, bool, bool) ([]any, error) { return returned, nil }
				if err := a.syncAndPersistAppStateDelta(t.Context(), appstate.WAPatchRegularLow, false); err != nil {
					t.Fatal(err)
				}
				for _, event := range []any{pnArchive, pnPin} {
					a.handleAppStatePersistenceEvent(t.Context(), event, nil)
				}
				if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
					t.Fatal(err)
				}
				row, err := a.db.GetChat(pn.String())
				if err != nil || row.Archived != (field == "pin") || row.Pinned != (field == "pin") {
					t.Fatal("unknown canonical field fell back to PN shadow", row, err)
				}
				debt, err := a.db.AppStateRecoveryRequired("regular_low")
				if err != nil || debt {
					t.Fatal("passive unknown created collection debt", err)
				}
			})
		}
	}
}

func TestRegularLowReadBindingRangeAndBoundedMirror(t *testing.T) {
	for _, agent := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "agent"}[agent], func(t *testing.T) {
			a, f, pn, lid := regularLowFixture(t)
			historyIdentityFixture(t, a, `INSERT INTO whatsmeow_lid_map VALUES('300','15550000001')`)
			ts, key, err := a.strictReadMessageRange(pn.String())
			if err != nil {
				t.Fatal(err)
			}
			want := appstate.BuildMarkChatAsRead(pn, true, ts, key).Mutations[0].Value.GetMarkChatAsReadAction().GetMessageRange()
			f.afterSend = func(result []any) []any {
				if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: pn.String(), MsgID: "later", Timestamp: ts, Text: "same second arrival"}); err != nil {
					t.Error(err)
				}
				if err := a.db.IncrementChatUnread(pn.String()); err != nil {
					t.Error(err)
				}
				return result
			}
			if agent {
				_, err = a.ApplyAgentChatState(t.Context(), ChatStateRequest{Version: 1, StoreRef: a.StoreDir(), Requested: pn.String(), Action: ChatStateMarkRead})
			} else {
				err = a.MarkChatRead(t.Context(), pn, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			p := f.patches[0]
			got := p.Mutations[0].Value.GetMarkChatAsReadAction().GetMessageRange()
			row, err := a.db.GetChat(pn.String())
			if err != nil || p.Mutations[0].Index[1] != lid.String() || !proto.Equal(want, got) || !row.Unread || row.UnreadCount != 1 || f.resolves.Load() != 0 || len(f.readReceiptCalls) != 0 {
				t.Fatalf("wire/range/mirror: %+v %v", row, err)
			}
			if got.GetMessages()[0].GetKey().GetRemoteJID() != pn.String() || got.GetMessages()[0].GetKey().GetParticipant() != lid.String() {
				t.Fatal("message key rewritten")
			}
		})
	}
}

func TestRegularLowChatIdentityAndEchoRefusals(t *testing.T) {
	for _, action := range []string{"read", "archive", "pin"} {
		for _, reason := range []string{"missing", "local_error", "one_way", "contradiction", "changed_during_delta", "changed_after_IQ", "account_after_IQ", "policy_after_IQ", "wrong_wire_echo", "wrong_echo_range"} {
			if reason == "wrong_echo_range" && action != "read" {
				continue
			}
			t.Run(action+"/"+reason, func(t *testing.T) {
				a, f, pn, lid := regularLowFixture(t)
				remap := func() {
					f.mu.Lock()
					delete(f.lids, lid)
					f.lids[types.NewJID("301", types.HiddenUserServer)] = pn
					f.mu.Unlock()
				}
				switch reason {
				case "missing":
					delete(f.lids, lid)
				case "local_error":
					f.identityErr = errors.New("local mapping unavailable")
				case "one_way":
					value := wa.PublicPairUnverified
					f.pairResult = &value
				case "contradiction":
					value := wa.PublicPairContradictory
					f.pairResult = &value
				case "changed_during_delta":
					f.fetchHook = func(context.Context, string, bool, bool) ([]any, error) { remap(); return nil, nil }
				case "changed_after_IQ":
					f.afterSend = func(result []any) []any { remap(); return result }
				case "account_after_IQ":
					f.afterSend = func(result []any) []any { f.mu.Lock(); f.linkedLID = "801@lid"; f.mu.Unlock(); return result }
				case "policy_after_IQ":
					f.afterSend = func(result []any) []any {
						f.policyMu.Lock()
						f.status.Enabled = false
						f.policyMu.Unlock()
						return result
					}
				case "wrong_wire_echo":
					f.afterSend = func(result []any) []any {
						for _, event := range result {
							switch v := event.(type) {
							case *events.Archive:
								v.JID = pn
							case *events.Pin:
								v.JID = pn
							case *events.MarkChatAsRead:
								v.JID = pn
							}
						}
						return result
					}
				case "wrong_echo_range":
					f.afterSend = func(result []any) []any {
						v := result[0].(*events.MarkChatAsRead)
						v.Action.MessageRange.LastMessageTimestamp = proto.Int64(31)
						return result
					}
				}
				var applyErr error
				switch action {
				case "read":
					applyErr = a.MarkChatRead(t.Context(), pn, true)
				case "archive":
					applyErr = a.ArchiveChat(t.Context(), pn, true)
				case "pin":
					applyErr = a.PinChat(t.Context(), pn, true)
				}
				if applyErr == nil {
					t.Fatal("chat state refusal became success")
				}
				row, err := a.db.GetChat(pn.String())
				if err != nil || !row.Unread || row.UnreadCount != 1 || row.Archived || row.Pinned {
					t.Fatal("refused action changed mirror", row, err)
				}
				debt, err := a.db.AppStateRecoveryRequired("regular_low")
				if err != nil || !debt || f.resolves.Load() != 0 {
					t.Fatal("lost debt or performed discovery", err)
				}
				sends := 0
				if reason == "changed_after_IQ" || reason == "account_after_IQ" || reason == "policy_after_IQ" || reason == "wrong_wire_echo" || reason == "wrong_echo_range" {
					sends = 1
				}
				if len(f.patches) != sends {
					t.Fatal("unexpected mutation/retry", len(f.patches))
				}
				if reason == "missing" || reason == "local_error" || reason == "one_way" || reason == "contradiction" {
					if len(f.appStateFetches) != 0 {
						t.Fatal("invalid pair reached delta")
					}
				}
			})
		}
	}
}

func TestRegularLowAgentObservationMustMatchBinding(t *testing.T) {
	for _, requestedLID := range []bool{false, true} {
		t.Run(map[bool]string{false: "PN", true: "LID"}[requestedLID], func(t *testing.T) {
			a, f, pn, lid := regularLowFixture(t)
			requested, mapping := pn, `INSERT INTO whatsmeow_lid_map VALUES('301','15550000001')`
			if requestedLID {
				requested, mapping = lid, `INSERT INTO whatsmeow_lid_map VALUES('300','15550000002')`
			}
			// The SDK's frozen pair and a later public session snapshot can
			// disagree after a remap. Neither observation may silently win.
			historyIdentityFixture(t, a, mapping)
			request := ChatStateRequest{Version: 1, StoreRef: a.StoreDir(), Requested: requested.String(), Action: ChatStateMarkUnread}
			result, err := a.ApplyAgentChatState(t.Context(), request)
			assertChatStateFailure(t, err, "identity_unavailable", ChatStateNotDispatched)
			row, readErr := a.db.GetChat(pn.String())
			debt, debtErr := a.db.AppStateRecoveryRequired("regular_low")
			if readErr != nil || !row.Unread || row.UnreadCount != 1 || result.Observation != nil || !debt || debtErr != nil || len(f.patches) != 0 || f.resolves.Load() != 0 {
				t.Fatalf("mismatched observation mutated or lost debt: %+v %+v %v %v", row, result, readErr, debtErr)
			}
		})
	}
}

func TestRegularLowReadProjectionUsesCanonicalLID(t *testing.T) {
	for _, field := range []string{"known", "missing", "ambiguous"} {
		for _, reverse := range []bool{false, true} {
			t.Run(field+map[bool]string{false: "_PN_first", true: "_LID_first"}[reverse], func(t *testing.T) {
				a, f, pn, lid := regularLowFixture(t)
				pnRead := &events.MarkChatAsRead{JID: pn, Action: appstate.BuildMarkChatAsRead(pn, true, time.Unix(30, 0), nil).Mutations[0].Value.GetMarkChatAsReadAction()}
				lidUnread := &events.MarkChatAsRead{JID: lid, Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(false)}}
				returned := []any{pnRead}
				if field == "known" {
					returned = append(returned, lidUnread)
					f.projection[lid] = whatsmeow.RegularLowChatProjection{ReadKnown: true, Read: false, ReadAction: lidUnread.Action}
				}
				if field == "ambiguous" {
					f.projection[lid] = whatsmeow.RegularLowChatProjection{ReadAmbiguous: true}
				}
				if reverse && len(returned) == 2 {
					returned[0], returned[1] = returned[1], returned[0]
				}
				f.fetchHook = func(context.Context, string, bool, bool) ([]any, error) { return returned, nil }
				if err := a.syncAndPersistAppStateDelta(t.Context(), appstate.WAPatchRegularLow, false); err != nil {
					t.Fatal(err)
				}
				for _, event := range []any{pnRead, lidUnread} {
					a.handleAppStatePersistenceEvent(t.Context(), event, nil)
				}
				if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
					t.Fatal(err)
				}
				row, err := a.db.GetChat(pn.String())
				if err != nil || !row.Unread || row.UnreadCount != 1 {
					t.Fatal("PN shadow changed canonical field", row, err)
				}
				debt, err := a.db.AppStateRecoveryRequired("regular_low")
				if err != nil || debt {
					t.Fatal("passive missing field created debt", err)
				}
			})
		}
	}
}

func TestRegularLowCallbackRetainsPairChangeDebt(t *testing.T) {
	for _, field := range []string{"archive", "pin", "read"} {
		t.Run(field, func(t *testing.T) {
			a, f, pn, lid := regularLowFixture(t)
			f.projection[lid] = whatsmeow.RegularLowChatProjection{ArchiveKnown: true, Archived: true, PinKnown: true, Pinned: true, ReadKnown: true, Read: true, ReadAction: appstate.BuildMarkChatAsRead(lid, true, time.Unix(30, 0), nil).Mutations[0].Value.GetMarkChatAsReadAction()}
			f.afterPair = func() { f.afterPair = nil; f.mu.Lock(); delete(f.lids, lid); f.mu.Unlock() }
			var event any
			switch field {
			case "archive":
				event = &events.Archive{JID: pn}
			case "pin":
				event = &events.Pin{JID: pn}
			case "read":
				event = &events.MarkChatAsRead{JID: pn}
			}
			a.handleAppStatePersistenceEvent(t.Context(), event, nil)
			if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
				t.Fatal(err)
			}
			row, err := a.db.GetChat(pn.String())
			if err != nil || row.Archived || row.Pinned || !row.Unread {
				t.Fatal("changed pair reached callback mirror", row, err)
			}
			debt, err := a.db.AppStateRecoveryRequired("regular_low")
			if err != nil || !debt {
				t.Fatal("changed verified pair became passive missing", err)
			}
		})
	}
}

func TestRegularLowBindingOwnPairExplicitLIDAndDefault(t *testing.T) {
	a, f, pn, lid := regularLowFixture(t)
	delete(f.lids, lid)
	b, err := a.bindRegularLowChat(t.Context(), lid)
	if err != nil || b.Store != lid || b.Wire != lid {
		t.Fatal("unpaired explicit LID changed", b, err)
	}
	if err := a.MarkChatRead(t.Context(), lid, false); err != nil {
		t.Fatal("explicit unpaired LID unread failed", err)
	}
	row, err := a.db.GetChat(lid.String())
	if err != nil || !row.Unread || f.patches[0].Mutations[0].Index[1] != lid.String() {
		t.Fatal("explicit LID unread used a PN mirror", row, err)
	}
	own, _ := types.ParseJID(f.LinkedJID())
	ownLID, _ := types.ParseJID(f.LinkedLID())
	b, err = a.bindRegularLowChat(t.Context(), own)
	if err != nil || b.Store != own || b.Wire != ownLID {
		t.Fatal("known Device pair required invented map row", b, err)
	}
	f.lids[types.NewJID("999", types.HiddenUserServer)] = own
	if _, err = a.bindRegularLowChat(t.Context(), own); !errors.Is(err, errRegularLowChatIdentity) {
		t.Fatal("own Device overrode local contradiction", err)
	}
	group := types.NewJID("400", types.GroupServer)
	if b, err := a.prepareRegularLowChat(t.Context(), group); err != nil || b != nil {
		t.Fatal("group acquired new identity policy", b, err)
	}
	f.status.Enabled = false
	if b, err := a.prepareRegularLowChat(t.Context(), pn); err != nil || b != nil {
		t.Fatal("default acquired compatibility policy", b, err)
	}
}

func TestRegularLowStartupAndRecoveryStayExplicit(t *testing.T) {
	for _, mode := range []string{"default", "opt_in_without_anchor", "explicit_anchor", "anchor_error", "read_only"} {
		t.Run(mode, func(t *testing.T) {
			a := newTestApp(t)
			f := &regularLowWA{chatStateAgentWA: &chatStateAgentWA{fakeWA: newFakeWA()}, status: whatsmeow.RegularLowPolicyStatus{Enabled: mode != "default"}}
			a.wa = f
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fulls := 0
			f.fetchHook = func(_ context.Context, name string, full, _ bool) ([]any, error) {
				if name == "regular_low" && full {
					fulls++
					if mode == "anchor_error" {
						return nil, appstate.ErrMismatchingLTHash
					}
					f.policyMu.Lock()
					f.status.Anchored = true
					f.status.Valid = true
					f.status.FetchComplete = true
					f.status.Degraded = true
					f.policyMu.Unlock()
				}
				return nil, nil
			}
			if mode == "read_only" {
				a.opts.ReadOnly = true
			}
			_, err := a.Sync(ctx, SyncOptions{Mode: SyncModeOnce, RegularLowFullSync: mode == "explicit_anchor" || mode == "anchor_error" || mode == "read_only", AfterConnect: func(context.Context) error { cancel(); return nil }})
			want := 0
			if mode == "explicit_anchor" || mode == "anchor_error" {
				want = 1
			}
			if fulls != want {
				t.Fatal("implicit/reset retry", fulls, want, err)
			}
			if len(f.appStateRecoveries) != 0 {
				t.Fatal("compatibility requested primary recovery", f.appStateRecoveries)
			}
			if mode == "anchor_error" && !errors.Is(err, appstate.ErrMismatchingLTHash) {
				t.Fatal("anchor error hidden", err)
			}
			if mode == "read_only" && (err == nil || f.connectCalls != 0) {
				t.Fatal("read-only startup connected", err)
			}
			if mode == "explicit_anchor" {
				status := a.SyncAdmissionSnapshot()
				if status.RegularLowPolicy == nil || !status.RegularLowPolicy.Degraded || status.Ready || status.Authenticated != "unknown" {
					t.Fatal("degraded conflated with readiness", status)
				}
			}
		})
	}
}

func TestRegularLowRecoveryRetainsTimeout(t *testing.T) {
	for _, blocked := range []string{"lock", "fetch"} {
		t.Run(blocked, func(t *testing.T) {
			a, f, _, _ := regularLowFixture(t)
			if _, err := a.db.MarkAppStateRecoveryGeneration("regular_low"); err != nil {
				t.Fatal(err)
			}
			if blocked == "lock" {
				release, err := a.acquireChatStateSync(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			fetches := 0
			f.fetchHook = func(ctx context.Context, name string, full, _ bool) ([]any, error) {
				fetches++
				if name != "regular_low" || full {
					t.Error("recovery reset or changed collection")
				}
				<-ctx.Done()
				return nil, ctx.Err()
			}
			// The parent is a watchdog; recovery must exhaust its own budget
			// without waiting for the owner lifetime to end.
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			a.recoverAppStateCollection(ctx, "regular_low", 10*time.Millisecond)
			if ctx.Err() != nil {
				t.Fatal("recovery ignored its timeout and waited for the parent")
			}
			debt, err := a.db.AppStateRecoveryRequired("regular_low")
			wantFetches := 0
			if blocked == "fetch" {
				wantFetches = 1
			}
			if err != nil || !debt || fetches != wantFetches || len(f.appStateRecoveries) != 0 || len(f.patches) != 0 {
				t.Fatalf("timeout lost debt, reset, retried or sent: %v %v %d", debt, err, fetches)
			}
		})
	}
}
