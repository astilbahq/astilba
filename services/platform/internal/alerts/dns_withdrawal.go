package alerts

import (
	"context"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const heldTitle = "DNS records may resolve to addresses Astilba no longer controls: the operator must review them with `astilba dns withdrawals`"

// dnsWithdrawalHeld raises dns_withdrawal_held, for the workspace itself, while records of its
// servers that the zone sweep would withdraw are held for the installation operator's confirmation, because the
// zone sweep would have withdrawn too many records at once. Only on a self-hosted installation,
// whose owners are its operators: on the hosted service owners can't confirm, and the management
// log tells its operator. Confirmation, or the records coming back under the threshold, resolves it.
func (s *Service) dnsWithdrawalHeld(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	value := good
	if !s.Store.Hosted {
		held, err := db.New(tx).WorkspaceHasHeldDNSRecords(ctx, &ws)
		if err != nil {
			return err
		}
		if held {
			value = bad
		}
	}
	return s.condition(ctx, tx, ws, api.DnsWithdrawalHeld, target{resource: ws}, value)
}
