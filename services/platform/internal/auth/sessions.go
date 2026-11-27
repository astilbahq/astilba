package auth

import (
	"context"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CurrentSession refuses to sign out the session that asks, which logs out instead.
var CurrentSession = &domain.Error{Code: "invalid_input", Message: "This is the session you are using. Log out to end it.", Fields: []string{"sessionId"}}

// SignOutSessions ends another of the account's browser sessions, or every other one when
// lineage is nil, each across every token that renewed it, freeing any authenticator enrolment
// it began. It needs recent authentication, so a stolen session cannot sign its holder out.
//
// It holds the account's row, so it orders with a credential change, and rechecks the session
// that asks under it, as those changes do. Signing out every other session ends one a credential
// change has moved on from but not yet renewed, so that renewal refuses. A session past its
// expiry is signed out already, so it is not found and ends nothing. Ending any records an
// account security event and queues its notice in the same transaction.
func (s *Service) SignOutSessions(ctx context.Context, p store.Principal, lineage *uuid.UUID) error {
	if !p.Recent() {
		return domain.RecentRequired
	}
	return s.Store.TransactionTx(ctx, func(tx pgx.Tx, q *db.Queries) error {
		_, current, err := lockCurrentLineage(ctx, q, p)
		if err != nil {
			return err
		}
		// Recent is decided when it is asked, and the account's row may have taken a while.
		if !p.Recent() {
			return domain.RecentRequired
		}
		var ending []uuid.UUID
		switch {
		case lineage == nil:
			if ending, err = q.AccountSessionLineages(ctx, db.AccountSessionLineagesParams{AccountID: p.Account.ID, Kept: current}); err != nil {
				return err
			}
		case *lineage == current:
			return CurrentSession
		default:
			held, err := q.AccountHasSessionLineage(ctx, db.AccountHasSessionLineageParams{AccountID: p.Account.ID, Lineage: *lineage})
			if err != nil {
				return err
			}
			if !held {
				return domain.NotFound
			}
			ending = []uuid.UUID{*lineage}
		}
		if len(ending) == 0 {
			return nil
		}
		// In lineage order, though the account's row already keeps two of these apart.
		for _, l := range ending {
			if err := store.EndLineage(ctx, q, l); err != nil {
				return err
			}
		}
		return s.securityEvent(ctx, tx, p.Account.ID, SessionsSignedOut)
	})
}
