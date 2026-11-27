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

// bindPolicyDestination admits policy p onto its destination, and binds an S3 destination no
// policy has used yet to p's server. Policy saves in a workspace are taken one at a time, so a
// second policy saved at once sees the first's binding. creating says p is new.
func bindPolicyDestination(ctx context.Context, tx pgx.Tx, ws uuid.UUID, p backup.Policy, creating bool) error {
	row, err := db.New(tx).BackupDestinationBinding(ctx, db.BackupDestinationBindingParams{WorkspaceID: ws, ID: p.DestinationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotFound
	}
	if err != nil {
		return err
	}
	var d backup.Destination
	if err = json.Unmarshal(row.Configuration, &d); err != nil {
		return err
	}
	if err = requireBackupPrefixes(ctx, tx, ws, p.ServerID, d); err != nil {
		return err
	}
	switch {
	case d.Kind != "s3":
		return nil
	case row.Shared && creating:
		return errDestinationShared
	case row.Shared:
		// One of the policies that shared it before destinations were bound. It never changes
		// server or destination.
		return nil
	case row.ServerID == nil:
		return db.New(tx).BindBackupDestination(ctx, db.BindBackupDestinationParams{ServerID: &p.ServerID, WorkspaceID: ws, ID: p.DestinationID})
	case *row.ServerID != p.ServerID:
		return errDestinationServer
	}
	return nil
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

// admitDestination refuses new S3 destination d for server unless server is a deployment server
// that can take it and d has a part of its bucket to itself.
func (s *Service) admitDestination(ctx context.Context, tx pgx.Tx, ws, server uuid.UUID, d backup.Destination) error {
	// A server that is missing, revoked, a build server or not ready can't take it.
	var refused *domain.Error
	if err := s.deploymentServer(ctx, tx, ws, server); errors.Is(err, pgx.ErrNoRows) || errors.As(err, &refused) {
		return domain.Invalid("serverId")
	} else if err != nil {
		return err
	}
	if err := requireBackupPrefixes(ctx, tx, ws, server, d); err != nil {
		return err
	}
	return requireOwnScope(ctx, tx, ws, d)
}

// errDestinationOverlaps refuses an S3 destination whose repositories would share part of a
// bucket with another destination's, so neither's keys could be limited to its own.
var errDestinationOverlaps = &domain.Error{Code: "invalid_input", Message: "Another destination keeps its backups in this part of the bucket, or around it, so keys limited to one would reach the other's. Choose a prefix that neither contains nor is inside another destination's, or another bucket.", Fields: []string{"prefix"}}

// requireOwnScope refuses S3 destination d if it overlaps one of the workspace's destinations.
func requireOwnScope(ctx context.Context, tx pgx.Tx, ws uuid.UUID, d backup.Destination) error {
	rows, err := db.New(tx).ListBackupDestinations(ctx, ws)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var o backup.Destination
		if err = json.Unmarshal(row.Configuration, &o); err != nil {
			return err
		}
		if d.Overlaps(o) {
			return errDestinationOverlaps
		}
	}
	return nil
}
