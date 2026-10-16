package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/gabrielMalonso/whatscli/internal/wa"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type regularLowPolicyClient interface {
	RegularLowPolicyStatus() whatsmeow.RegularLowPolicyStatus
	RegularLowChatProjection(types.JID) whatsmeow.RegularLowChatProjection
	InvalidateRegularLowPolicy()
}

func (a *App) regularLowPolicyStatus() whatsmeow.RegularLowPolicyStatus {
	if client, ok := a.WA().(regularLowPolicyClient); ok {
		return client.RegularLowPolicyStatus()
	}
	return whatsmeow.RegularLowPolicyStatus{}
}

func (a *App) regularLowEnabled(collection appstate.WAPatchName) bool {
	return collection == appstate.WAPatchRegularLow && a.regularLowPolicyStatus().Enabled
}

func (a *App) regularLowProjection(jid types.JID) whatsmeow.RegularLowChatProjection {
	if client, ok := a.WA().(regularLowPolicyClient); ok {
		return client.RegularLowChatProjection(jid)
	}
	return whatsmeow.RegularLowChatProjection{}
}

func (a *App) invalidateRegularLow() {
	if client, ok := a.WA().(regularLowPolicyClient); ok {
		client.InvalidateRegularLowPolicy()
	}
}

func (a *App) validateRegularLowStartup(fullSync bool) error {
	if !fullSync {
		return nil
	}
	if a.ReadOnly() {
		return fmt.Errorf("read-only mode: regular_low full sync resets local collection state")
	}
	if !a.regularLowPolicyStatus().Enabled {
		return fmt.Errorf("--regular-low-full-sync requires WHATSMEOW_REGULAR_LOW_WEB_COMPAT=1 before client construction")
	}
	return nil
}

// An explicit native full sync establishes this session's anchor once. Neither
// debt nor a failed delta may request another reset or primary recovery.
func (a *App) startRegularLowAnchor(ctx context.Context) error {
	generation, _, err := a.beginAppStateRecovery(string(appstate.WAPatchRegularLow))
	if err != nil {
		return err
	}
	if err := a.fetchRegularLow(ctx, true, &appStatePersistenceTracker{}); err != nil {
		return err
	}
	status := a.regularLowPolicyStatus()
	if !status.Anchored || !status.Valid {
		return whatsmeow.ErrRegularLowSessionInvalid
	}
	// The owned snapshot was mirrored even if a newer notification is fetching.
	// That newer work keeps its own debt and must complete before an action.
	if !status.FetchComplete {
		return nil
	}
	return a.clearCompletedAppStateRecovery(appstate.WAPatchRegularLow, generation)
}

func (a *App) fetchRegularLow(ctx context.Context, fullSync bool, tracker *appStatePersistenceTracker) error {
	fetchErr, persistErr := a.fetchAndPersistAppState(ctx, appstate.WAPatchRegularLow, fullSync, tracker)
	if persistErr != nil {
		a.invalidateRegularLow()
	}
	return errors.Join(fetchErr, persistErr, ctx.Err())
}

func (a *App) syncRegularLow(ctx context.Context, generation int64, tracker *appStatePersistenceTracker) error {
	status := a.regularLowPolicyStatus()
	if !status.Anchored {
		return whatsmeow.ErrRegularLowAnchorRequired
	}
	if !status.Valid {
		return whatsmeow.ErrRegularLowSessionInvalid
	}
	if err := a.fetchRegularLow(ctx, false, tracker); err != nil {
		return err
	}
	status = a.regularLowPolicyStatus()
	if !status.Anchored || !status.Valid {
		return whatsmeow.ErrRegularLowSessionInvalid
	}
	if !status.FetchComplete {
		return whatsmeow.ErrRegularLowFetchRequired
	}
	return a.clearCompletedAppStateRecovery(appstate.WAPatchRegularLow, generation)
}

func (a *App) persistRegularLowFlags(chat string, p whatsmeow.RegularLowChatProjection) error {
	if p.ArchiveKnown {
		if err := a.db.SetChatArchivedProjection(chat, p.Archived); err != nil {
			return err
		}
	}
	if p.PinKnown {
		return a.db.SetChatPinned(chat, p.Pinned)
	}
	return nil
}

// Only an owned fetch/send ticket can use its returned authenticated result.
// A notification can mask the live getter between return and SQLite persistence.
// It cannot replace these observations; invalidation still revokes them.
func (a *App) returnedRegularLowProjection(jid types.JID, returned []any) (whatsmeow.RegularLowChatProjection, error) {
	projections, err := a.returnedRegularLowProjections(returned)
	return projections[jid], err
}

// Confirmation still needs the exact wire JID. A second authenticated alias
// cannot silently replace that observation when both materialize one store row.
func (a *App) returnedRegularLowFlags(ctx context.Context, chat string, jid types.JID, returned []any) (whatsmeow.RegularLowChatProjection, error) {
	projections, err := a.returnedRegularLowProjections(returned)
	if err != nil {
		return whatsmeow.RegularLowChatProjection{}, err
	}
	p := projections[jid]
	merged := a.regularLowFlagsByStore(ctx, projections)[chat]
	p.ArchiveKnown = p.ArchiveKnown && !merged.ArchiveAmbiguous
	p.PinKnown = p.PinKnown && !merged.PinAmbiguous
	return p, nil
}

// Missing is not false. Conflicts are sticky per field, so neither map nor
// event order chooses a winner; concordant fields remain independently usable.
func mergeRegularLowFlags(current, observed whatsmeow.RegularLowChatProjection) whatsmeow.RegularLowChatProjection {
	if current.ArchiveAmbiguous || observed.ArchiveAmbiguous || current.ArchiveKnown && observed.ArchiveKnown && current.Archived != observed.Archived {
		current.ArchiveKnown, current.ArchiveAmbiguous = false, true
	} else if observed.ArchiveKnown {
		current.ArchiveKnown, current.Archived = true, observed.Archived
	}
	if current.PinAmbiguous || observed.PinAmbiguous || current.PinKnown && observed.PinKnown && current.Pinned != observed.Pinned {
		current.PinKnown, current.PinAmbiguous = false, true
	} else if observed.PinKnown {
		current.PinKnown, current.Pinned = true, observed.Pinned
	}
	return current
}

func (a *App) regularLowFlagsByStore(ctx context.Context, projections map[types.JID]whatsmeow.RegularLowChatProjection) map[string]whatsmeow.RegularLowChatProjection {
	flags := make(map[string]whatsmeow.RegularLowChatProjection)
	for jid, p := range projections {
		if p.ArchiveKnown || p.PinKnown || p.ArchiveAmbiguous || p.PinAmbiguous {
			chat := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
			flags[chat] = mergeRegularLowFlags(flags[chat], p)
		}
	}
	return flags
}

func (a *App) returnedRegularLowProjections(returned []any) (map[types.JID]whatsmeow.RegularLowChatProjection, error) {
	status := a.regularLowPolicyStatus()
	if !status.Anchored || !status.Valid {
		return nil, whatsmeow.ErrRegularLowSessionInvalid
	}
	projections := make(map[types.JID]whatsmeow.RegularLowChatProjection)
	for _, event := range returned {
		var jid types.JID
		switch v := event.(type) {
		case *events.Archive:
			if v == nil || v.Action == nil || v.Action.Archived == nil {
				return nil, whatsmeow.ErrRegularLowProjectionUnknown
			}
			jid = v.JID
			p := projections[jid]
			if p.ArchiveKnown {
				return nil, whatsmeow.ErrRegularLowProjectionUnknown
			}
			p.ArchiveKnown, p.Archived = true, v.Action.GetArchived()
			projections[jid] = p
		case *events.Pin:
			if v == nil || v.Action == nil || v.Action.Pinned == nil {
				return nil, whatsmeow.ErrRegularLowProjectionUnknown
			}
			jid = v.JID
			p := projections[jid]
			if p.PinKnown {
				return nil, whatsmeow.ErrRegularLowProjectionUnknown
			}
			p.PinKnown, p.Pinned = true, v.Action.GetPinned()
			projections[jid] = p
		case *events.MarkChatAsRead:
			if v == nil || v.Action == nil || v.Action.Read == nil {
				return nil, whatsmeow.ErrRegularLowProjectionUnknown
			}
			jid = v.JID
			p := projections[jid]
			if p.ReadKnown {
				return nil, whatsmeow.ErrRegularLowProjectionUnknown
			}
			if v.Action.GetRead() {
				through, _ := markChatAsReadPosition(&events.MarkChatAsRead{Action: v.Action})
				if through.Unix() <= 0 {
					return nil, whatsmeow.ErrRegularLowProjectionUnknown
				}
			}
			p.ReadKnown, p.Read = true, v.Action.GetRead()
			p.ReadAction = proto.Clone(v.Action).(*waSyncAction.MarkChatAsReadAction)
			projections[jid] = p
		default:
			return nil, whatsmeow.ErrRegularLowProjectionUnknown
		}
		if jid.IsEmpty() {
			return nil, whatsmeow.ErrRegularLowProjectionUnknown
		}
	}
	return projections, nil
}

// PN read shadows never compete with the verified LID in this same batch,
// regardless of event ordering. Missing read fields leave SQLite untouched.
func (a *App) persistReturnedRegularLow(ctx context.Context, returned []any, frozen *regularLowReadBinding) error {
	if frozen != nil {
		if err := frozen.revalidate(ctx, a); err != nil {
			return err
		}
	}
	projections, err := a.returnedRegularLowProjections(returned)
	if err != nil {
		return err
	}
	// Resolve all field conflicts before the first SQLite write.
	for chat, p := range a.regularLowFlagsByStore(ctx, projections) {
		if err := a.persistRegularLowFlags(chat, p); err != nil {
			return err
		}
	}
	for jid, p := range projections {
		if !p.ReadKnown {
			continue
		}
		var binding regularLowReadBinding
		var err error
		if frozen != nil && (jid == frozen.Requested || jid == frozen.Store || jid == frozen.Wire) {
			binding = *frozen
		} else {
			binding, err = a.bindRegularLowRead(ctx, jid)
		}
		if errors.Is(err, errRegularLowReadMissing) {
			continue
		}
		if err != nil {
			return err
		}
		if binding.Wire != jid {
			continue
		}
		if err := binding.revalidate(ctx, a); err != nil {
			return err
		}
		if err := a.persistRegularLowRead(binding.Store.String(), p); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) persistRegularLowRead(chat string, p whatsmeow.RegularLowChatProjection) error {
	if !p.ReadKnown || p.ReadAction == nil {
		return whatsmeow.ErrRegularLowProjectionUnknown
	}
	if !p.Read {
		return a.db.SetChatUnread(chat, true)
	}
	through, ids := markChatAsReadPosition(&events.MarkChatAsRead{Action: p.ReadAction})
	if through.Unix() <= 0 {
		return whatsmeow.ErrRegularLowProjectionUnknown
	}
	return a.db.ClearChatUnreadThrough(chat, through, ids)
}

// Independent callbacks observe the current masked getter, never their stale
// event payload. A missing canonical LID field is passive unknown, not false.
func (a *App) persistLiveRegularLow(ctx context.Context, event any) error {
	status := a.regularLowPolicyStatus()
	if !status.Anchored || !status.Valid || !status.FetchComplete {
		return whatsmeow.ErrRegularLowProjectionUnknown
	}
	var jid types.JID
	switch v := event.(type) {
	case *events.Archive:
		if v == nil {
			return whatsmeow.ErrRegularLowProjectionUnknown
		}
		jid = v.JID
	case *events.Pin:
		if v == nil {
			return whatsmeow.ErrRegularLowProjectionUnknown
		}
		jid = v.JID
	case *events.MarkChatAsRead:
		if v == nil {
			return whatsmeow.ErrRegularLowProjectionUnknown
		}
		binding, err := a.bindRegularLowRead(ctx, v.JID)
		if errors.Is(err, errRegularLowReadMissing) {
			return nil
		}
		if err != nil {
			return err
		}
		p := a.regularLowProjection(binding.Wire)
		if !p.ReadKnown || p.ReadAction == nil {
			return nil
		}
		if err := binding.revalidate(ctx, a); err != nil {
			return err
		}
		return a.persistRegularLowRead(binding.Store.String(), p)
	}
	if jid.IsEmpty() {
		return whatsmeow.ErrRegularLowProjectionUnknown
	}
	p := a.regularLowProjection(jid)
	// Alias reads are local, corroborated in both directions, and used only
	// to mask conflicting fields. They never supply a missing exact-JID field.
	if jid.Server == types.DefaultUserServer || jid.Server == types.HiddenUserServer {
		reader, ok := a.WA().(regularLowLocalIdentity)
		if !ok {
			return whatsmeow.ErrRegularLowProjectionUnknown
		}
		alias, err := reader.LookupLocalAlias(ctx, jid)
		if err != nil {
			return err
		}
		alias = alias.ToNonAD()
		if !alias.IsEmpty() {
			verified, err := reader.CheckPublicPair(ctx, jid, alias)
			if err != nil {
				return err
			}
			if verified != wa.PublicPairVerified {
				return whatsmeow.ErrRegularLowProjectionUnknown
			}
			merged := mergeRegularLowFlags(p, a.regularLowProjection(alias))
			p.ArchiveKnown = p.ArchiveKnown && !merged.ArchiveAmbiguous
			p.PinKnown = p.PinKnown && !merged.PinAmbiguous
			p.ArchiveAmbiguous, p.PinAmbiguous = merged.ArchiveAmbiguous, merged.PinAmbiguous
		}
	}
	if a.regularLowPolicyStatus() != status {
		return whatsmeow.ErrRegularLowProjectionUnknown
	}
	switch event.(type) {
	case *events.Archive:
		if !p.ArchiveKnown && !p.ArchiveAmbiguous {
			return whatsmeow.ErrRegularLowProjectionUnknown
		}
	case *events.Pin:
		if !p.PinKnown && !p.PinAmbiguous {
			return whatsmeow.ErrRegularLowProjectionUnknown
		}
	}
	return a.persistRegularLowFlags(canonicalJIDString(a.canonicalStoreJID(ctx, jid)), p)
}
