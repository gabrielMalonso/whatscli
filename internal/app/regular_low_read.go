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
	errRegularLowReadIdentity = errors.New("regular_low read identity unavailable or changed")
	errRegularLowReadMissing  = errors.New("regular_low read requires a verified local PN/LID pair")
)

// Store selects the original message range; Wire selects the mutation index.
// Requested and the connected account remain frozen across delta/send/mirror.
type regularLowReadBinding struct {
	Requested, Store, Wire types.JID
	AccountPN, AccountLID  string
}

type regularLowLocalIdentity interface {
	LookupLocalAlias(context.Context, types.JID) (types.JID, error)
	CheckPublicPair(context.Context, types.JID, types.JID) (wa.PublicPairResult, error)
}

func (a *App) bindRegularLowRead(ctx context.Context, requested types.JID) (regularLowReadBinding, error) {
	client := a.WA()
	if client == nil {
		return regularLowReadBinding{}, errRegularLowReadIdentity
	}
	requested = requested.ToNonAD()
	b := regularLowReadBinding{Requested: requested, Store: requested, Wire: requested, AccountPN: client.LinkedJID(), AccountLID: client.LinkedLID()}
	if err := ctx.Err(); err != nil {
		return b, err
	}
	if b.AccountPN == "" {
		return b, errRegularLowReadIdentity
	}
	if requested.Server != types.DefaultUserServer && requested.Server != types.HiddenUserServer {
		return b, nil
	}
	reader, ok := client.(regularLowLocalIdentity)
	if !ok {
		return b, errRegularLowReadIdentity
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
			return b, errRegularLowReadIdentity
		}
	} else if alias.IsEmpty() {
		if requested.Server == types.HiddenUserServer {
			return b, nil
		}
		return b, errRegularLowReadMissing
	}
	if pn.Server != types.DefaultUserServer || lid.Server != types.HiddenUserServer || pn.User == "" || lid.User == "" || pn.Integrator != 0 || lid.Integrator != 0 {
		return b, errRegularLowReadIdentity
	}
	verified, err := reader.CheckPublicPair(ctx, pn, lid)
	if err != nil {
		return b, err
	}
	if verified == wa.PublicPairContradictory {
		return b, errRegularLowReadIdentity
	}
	// Device's own pair is known even without a public map row. Both map
	// directions were still checked above, so it cannot override a contradiction.
	if !own && verified != wa.PublicPairVerified {
		return b, errRegularLowReadMissing
	}
	b.Store, b.Wire = pn, lid
	return b, nil
}

func (b regularLowReadBinding) revalidate(ctx context.Context, a *App) error {
	client := a.WA()
	if client == nil || !client.IsConnected() || !a.regularLowEnabled(appstate.WAPatchRegularLow) {
		return errRegularLowReadIdentity
	}
	current, err := a.bindRegularLowRead(ctx, b.Requested)
	if err != nil {
		return fmt.Errorf("%w: %w", errRegularLowReadIdentity, err)
	}
	if current != b {
		return errRegularLowReadIdentity
	}
	return nil
}

func (a *App) prepareRegularLowRead(ctx context.Context, jid types.JID) (*regularLowReadBinding, error) {
	if !a.regularLowEnabled(appstate.WAPatchRegularLow) || jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer {
		return nil, nil
	}
	b, err := a.bindRegularLowRead(ctx, jid)
	if err != nil {
		a.handleAppStateSyncError(ctx, &events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: err}, nil)
		return nil, err
	}
	return &b, nil
}
