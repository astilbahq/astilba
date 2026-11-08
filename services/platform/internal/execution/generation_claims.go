package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Hosts can honestly be ahead of management's counters, so their claims are bounded rather than
// ignored. A compromised host can spend whatever is accepted, so what a host may spend live is far
// less than what an honest host's export may need to show.
const (
	// networkReceiptMargin is how far ahead a host can be under its current controller. A
	// restriction it has applied but not yet reported can add one generation, and an agent holds
	// at most 128 unacknowledged outcomes. A later controller counts for itself, so nothing a host
	// held before a restore reaches this limit.
	networkReceiptMargin = 128
	// exportClaimAllowance bounds a host export against the issued counters. It is larger because
	// the backup being recovered has forgotten what was issued since.
	exportClaimAllowance = 1 << 44
	// generationClaimCeiling keeps counters that hosts raise below the limit an import accepts,
	// with room for ordinary increments, so a workspace's export stays importable.
	generationClaimCeiling = portability.MaxCounter - exportClaimAllowance
)

const unaccountedNetworkGenerationNote = "This server reported a private network generation far beyond what this installation issued, so private links cannot finish. " + aheadNetworkMemberAdvice

// refusedNetworkConfigureNote keeps a member to removals until the owner saves the network, because
// the host would refuse anything else the same way.
const refusedNetworkConfigureNote = "This server holds a later private network generation than this installation issued and refused a change, so only removals can reach it. " + aheadNetworkMemberAdvice

// routeConflictNetworkNote holds a member to removals at the generation it refused, which would
// be refused the same way again. The next generation tries the whole manifest again, since only
// the host's own ranges stand in its way.
const routeConflictNetworkNote = "This server refused the network change because a container network or route on it overlaps the network prefix. Nothing changed on the server, and removals still reach it. Remove the overlapping network or route on that server, then save the network again."

const aheadNetworkMemberAdvice = "A compromised server can cause this, and so can restoring management without fencing the server. Revoke the server if you do not trust it. Otherwise fence it with prepare-recovery, transfer it, then approve the network again."

// generationClaimLimit is the highest generation a host may claim while management has issued up
// to the given counter. What management issued is never refused, even past the ceiling.
func generationClaimLimit(issued, allowance int64) int64 {
	if issued >= generationClaimCeiling {
		return issued
	}
	return min(issued+allowance, generationClaimCeiling)
}

// issuedRecoveryCounters holds the highest value this installation is known to have issued,
// from current management state and from capsules only management could have sealed.
type issuedRecoveryCounters struct {
	Network      int64
	Environments map[uuid.UUID]int64
	Updates      map[uuid.UUID]int64
	Controllers  map[uuid.UUID]int64
}

func recoveryIssuedCounters(archives ...portability.Archive) (issuedRecoveryCounters, error) {
	out := issuedRecoveryCounters{Environments: map[uuid.UUID]int64{}, Updates: map[uuid.UUID]int64{}, Controllers: map[uuid.UUID]int64{}}
	for _, archive := range archives {
		for _, row := range archive.Tables["workspace_networks"] {
			var generation int64
			if json.Unmarshal(row["generation"], &generation) != nil {
				return out, portability.ErrArchive
			}
			out.Network = max(out.Network, generation)
		}
		for _, row := range archive.Tables["environments"] {
			var id uuid.UUID
			var generation int64
			if json.Unmarshal(row["id"], &id) != nil || json.Unmarshal(row["execution_generation"], &generation) != nil {
				return out, portability.ErrArchive
			}
			out.Environments[id] = max(out.Environments[id], generation)
		}
		for _, row := range archive.Tables["servers"] {
			var id uuid.UUID
			var update, controller int64
			if json.Unmarshal(row["id"], &id) != nil || json.Unmarshal(row["update_generation"], &update) != nil || json.Unmarshal(row["controller_generation"], &controller) != nil {
				return out, portability.ErrArchive
			}
			out.Updates[id] = max(out.Updates[id], update)
			out.Controllers[id] = max(out.Controllers[id], controller)
		}
	}
	return out, nil
}

// checkRecoveryGenerationClaims refuses a host export whose numbers management never issued,
// before anything is written. Exports are unsigned and recovery raises management's counters
// from them, so one forged value could otherwise spend a counter for good. It runs on the
// preview too, and names the host, because the operator can only act on that host.
func checkRecoveryGenerationClaims(ctx context.Context, tx pgx.Tx, before portability.Archive, source recoverySource) error {
	issued, err := recoveryIssuedCounters(before, source.Archive)
	if err != nil {
		return err
	}
	refused := func(server uuid.UUID, counter string) error {
		return fmt.Errorf("host %s claims %s generation beyond what this installation issued; inspect that host before including its export", server, counter)
	}
	limit := func(issued int64) int64 { return generationClaimLimit(issued, exportClaimAllowance) }
	for server, host := range source.Hosts {
		if host.Controller > limit(issued.Controllers[server]) {
			return refused(server, "a controller")
		}
		for _, manifest := range host.Networks {
			if manifest.Generation > limit(issued.Network) {
				return refused(server, "a network")
			}
		}
	}
	// inline-sql: recovery session temporary table
	rows, err := tx.Query(ctx, `SELECT server_id,header,report FROM recovery_input_proofs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var server uuid.UUID
		var headerRaw, reportRaw []byte
		if err = rows.Scan(&server, &headerRaw, &reportRaw); err != nil {
			return err
		}
		var header api.ExecutionCommand
		var report api.AgentReport
		if recoveryDecode(headerRaw, &header) != nil || recoveryDecode(reportRaw, &report) != nil {
			return errors.New("invalid retained recovery header")
		}
		// The same frames decide which counters RaiseEnvironmentExecutionGenerations and
		// RaiseServerUpdateGenerations move.
		if header.EnvironmentId != uuid.Nil && header.Kind != "network" && header.Generation > limit(issued.Environments[header.EnvironmentId]) {
			return refused(server, "an environment")
		}
		if (header.Kind == "network" || header.Kind == "update") && header.Generation > limit(issued.Updates[server]) {
			return refused(server, "an update")
		}
		if report.Network == nil {
			continue
		}
		var receipt hostnetwork.Receipt
		if hostnetwork.Decode(*report.Network, &receipt) != nil {
			return errors.New("invalid retained recovery receipt")
		}
		if receipt.Generation > limit(issued.Network) {
			return refused(server, "a network")
		}
	}
	return rows.Err()
}
