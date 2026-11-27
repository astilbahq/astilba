package execution

import (
	"context"
	"encoding/json/v2"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// An S3 destination's storage keys are given to one server, the one it is bound to, and only
// policies on that server use it. The owner limits the keys to the destination's prefix or bucket,
// so a compromised server can't delete or overwrite another server's backups. A destination used
// by policies on several servers before destinations were bound is shared: its policies keep
// using it, and no new policy can. See docs/backups.md#destination-setup.

// errDestinationServer refuses a policy on an S3 destination bound to another server.
var errDestinationServer = &domain.Error{Code: "invalid_input", Message: "This destination's storage keys belong to another server. Add a destination for this server, with keys limited to its own prefix or bucket.", Fields: []string{"destination.server"}}

// errDestinationShared refuses a new policy on a shared S3 destination.
var errDestinationShared = &domain.Error{Code: "invalid_input", Message: "This destination's storage keys are held by more than one server, so each can delete the others' backups. Add a destination for this server, with keys limited to its own prefix or bucket.", Fields: []string{"destination.shared"}}

// bindPolicyDestination admits policy p onto its destination, which it locks until the policy is
// saved, and binds an S3 destination no policy has used yet to p's server. The lock orders two
// policies saved at once, so the second sees the first's binding. creating says p is new.
func bindPolicyDestination(ctx context.Context, tx pgx.Tx, ws uuid.UUID, p backup.Policy, creating bool) (backup.Destination, error) {
	var d backup.Destination
	row, err := db.New(tx).LockBackupDestinationBinding(ctx, db.LockBackupDestinationBindingParams{WorkspaceID: ws, ID: p.DestinationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return d, domain.NotFound
	}
	if err != nil {
		return d, err
	}
	if err = json.Unmarshal(row.Configuration, &d); err != nil {
		return d, err
	}
	if err = requireBackupPrefixes(ctx, tx, ws, p.ServerID, d); err != nil {
		return d, err
	}
	switch {
	case d.Kind != "s3":
		return d, nil
	case row.Shared && creating:
		return d, errDestinationShared
	case row.Shared:
		// One of the policies that shared it before destinations were bound. It never changes
		// server or destination.
		return d, nil
	case row.ServerID == nil:
		return d, db.New(tx).BindBackupDestination(ctx, db.BindBackupDestinationParams{ServerID: &p.ServerID, WorkspaceID: ws, ID: p.DestinationID})
	case *row.ServerID != p.ServerID:
		return d, errDestinationServer
	}
	return d, nil
}

// destinationAdmits says whether an S3 destination bound to bound, or shared, gives its keys to
// server. A filesystem destination has none to give.
func destinationAdmits(kind string, bound *uuid.UUID, shared bool, server uuid.UUID) bool {
	return kind != "s3" || shared || bound != nil && *bound == server
}

// backupPrefixesField marks a refusal an agent update clears.
const backupPrefixesField = "server.capabilities.backupPrefixes"

// requireBackupPrefixes refuses an S3 destination with a prefix on a server whose agent would
// look for its repositories at the bucket's root.
func requireBackupPrefixes(ctx context.Context, q db.DBTX, ws, server uuid.UUID, d backup.Destination) error {
	if d.Prefix == "" {
		return nil
	}
	raw, err := db.New(q).ServerCapabilities(ctx, db.ServerCapabilitiesParams{WorkspaceID: ws, ID: server})
	if err != nil {
		return err
	}
	var c api.AgentCapabilities
	if json.Unmarshal(raw, &c) == nil && c.BackupPrefixes != nil && *c.BackupPrefixes {
		return nil
	}
	return &domain.Error{Code: "invalid_input", Message: "Update this server's agent before using a destination with a prefix: its agent would keep the backups at the bucket's root.", Fields: []string{backupPrefixesField}}
}
