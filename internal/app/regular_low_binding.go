package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/gabrielMalonso/whatscli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

var (
	errRegularLowChatIdentity = errors.New("regular_low chat identity unavailable or changed")
	errRegularLowChatMissing  = errors.New("regular_low chat state requires a verified local PN/LID pair")
)

// Store preserves the archive identity and original range; Wire selects the index.
// Requested and the connected account remain frozen across delta/send/mirror.
type regularLowChatBinding struct {
	Requested, Store, Wire types.JID
	AccountPN, AccountLID  string
}

type regularLowLocalIdentity interface {
	LookupLocalAlias(context.Context, types.JID) (types.JID, error)
	CheckPublicPair(context.Context, types.JID, types.JID) (wa.PublicPairResult, error)
}

func (a *App) bindRegularLowChat(ctx context.Context, requested types.JID) (regularLowChatBinding, error) {
	client := a.WA()
	if client == nil {
		return regularLowChatBinding{}, errRegularLowChatIdentity
	}
	requested = requested.ToNonAD()
	b := regularLowChatBinding{Requested: requested, Store: requested, Wire: requested, AccountPN: client.LinkedJID(), AccountLID: client.LinkedLID()}
	if err := ctx.Err(); err != nil {
		return b, err
	}
	if b.AccountPN == "" {
		return b, errRegularLowChatIdentity
	}
	if requested.Server != types.DefaultUserServer && requested.Server != types.HiddenUserServer {
		return b, nil
	}
	reader, ok := client.(regularLowLocalIdentity)
	if !ok {
		return b, errRegularLowChatIdentity
	}
	// These APIs are local only. ResolvePNToLID may fall back to network USync.
	alias, err := reader.LookupLocalAlias(ctx, requested)
	if err != nil {
		return b, err
	}
	alias = alias.ToNonAD()
	ownPN, pnErr := ParseHistoryJID(b.AccountPN)
	ownLID, lidErr := ParseHistoryJID(b.AccountLID)
	own := pnErr == nil && lidErr == nil && ownPN.Server == types.DefaultUserServer && ownLID.Server == types.HiddenUserServer && (requested == ownPN || requested == ownLID)
	pn, lid := requested, alias
	if requested.Server == types.HiddenUserServer {
		pn, lid = alias, requested
	}
	if own {
		pn, lid = ownPN, ownLID
		if !alias.IsEmpty() && (requested == pn && alias != lid || requested == lid && alias != pn) {
			return b, errRegularLowChatIdentity
		}
	} else if alias.IsEmpty() {
		if requested.Server == types.HiddenUserServer {
			return b, nil
		}
		return b, errRegularLowChatMissing
	}
	if pn.Server != types.DefaultUserServer || lid.Server != types.HiddenUserServer || pn.User == "" || lid.User == "" || pn.Integrator != 0 || lid.Integrator != 0 {
		return b, errRegularLowChatIdentity
	}
	verified, err := reader.CheckPublicPair(ctx, pn, lid)
	if err != nil {
		return b, err
	}
	if verified == wa.PublicPairContradictory {
		return b, errRegularLowChatIdentity
	}
	// Device's own pair is known even without a public map row. Both map
	// directions were still checked above, so it cannot override a contradiction.
	if !own && verified != wa.PublicPairVerified {
		return b, errRegularLowChatMissing
	}
	b.Store, b.Wire = pn, lid
	return b, nil
}

func (b regularLowChatBinding) revalidate(ctx context.Context, a *App) error {
	client := a.WA()
	if client == nil || !client.IsConnected() || !a.regularLowEnabled(appstate.WAPatchRegularLow) {
		return errRegularLowChatIdentity
	}
	current, err := a.bindRegularLowChat(ctx, b.Requested)
	if err != nil {
		return fmt.Errorf("%w: %w", errRegularLowChatIdentity, err)
	}
	if current != b {
		return errRegularLowChatIdentity
	}
	return nil
}

func (a *App) prepareRegularLowChat(ctx context.Context, jid types.JID) (*regularLowChatBinding, error) {
	if !a.regularLowEnabled(appstate.WAPatchRegularLow) || jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer {
		return nil, nil
	}
	b, err := a.bindRegularLowChat(ctx, jid)
	if err != nil {
		a.handleAppStateSyncError(ctx, &events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: err}, nil)
		return nil, err
	}
	return &b, nil
}

func (a *App) regularLowProjectionBinding(ctx context.Context, jid types.JID, frozen *regularLowChatBinding) (regularLowChatBinding, error) {
	var binding regularLowChatBinding
	var err error
	if frozen != nil && (jid == frozen.Requested || jid == frozen.Store || jid == frozen.Wire) {
		binding = *frozen
	} else {
		binding, err = a.bindRegularLowChat(ctx, jid)
	}
	if err != nil {
		return binding, err
	}
	return binding, binding.revalidate(ctx, a)
}
