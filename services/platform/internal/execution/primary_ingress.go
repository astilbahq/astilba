package execution

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var primaryIngressUnsupported = &domain.Error{Code: "invalid_input", Message: "This server's agent can't prepare the ingress a primary needs. Update the agent on this server, then plan again.", Fields: []string{"placement.preparePrimaryIngress"}}

// preparePrimaryIngress decides how the primary's host gets the dedicated ingress gateway its
// private links need. Only a primary whose proxy reaches another host needs one, and a host that
// has it changes nothing. Otherwise its agent moves the proxy onto the gateway during reserve.
// That restarts the proxy, so the effect names every environment whose routes it serves, letting
// the owner pick a quiet time.
func preparePrimaryIngress(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent *deployment.Plan, caps api.AgentCapabilities, host api.CrossHostInventory) error {
	if host.IngressReady != nil && *host.IngressReady {
		return nil
	}
	for i := range parent.Distributed.Units {
		local := parent.Distributed.Units[i].Plan.Local
		if !local.Primary || !slices.ContainsFunc(local.Network.Egress, func(e hostnetwork.Egress) bool { return e.Ingress }) {
			continue
		}
		if caps.PrimaryIngress == nil || !*caps.PrimaryIngress {
			return primaryIngressUnsupported
		}
		effect, err := primaryIngressEffect(ctx, db.New(tx), ws, parent.ServerID)
		if err != nil {
			return err
		}
		local.PrepareIngress = true
		parent.Effects = append(parent.Effects, effect)
	}
	return nil
}

// primaryIngressEffect names the environments routed through server, the planned one
// included.
func primaryIngressEffect(ctx context.Context, q *db.Queries, ws, server uuid.UUID) (string, error) {
	name, err := q.ServerName(ctx, db.ServerNameParams{WorkspaceID: ws, ID: server})
	if err != nil {
		return "", err
	}
	rows, err := q.ServerRoutedEnvironments(ctx, db.ServerRoutedEnvironmentsParams{WorkspaceID: ws, ServerID: server})
	if err != nil {
		return "", err
	}
	labels := make([]string, 0, len(rows))
	for _, row := range rows {
		label := row.Project + " / " + row.Environment
		if row.Purpose == "preview" {
			label += " (preview)"
		}
		labels = append(labels, label)
	}
	return ingressRestartEffect(name, labels), nil
}

// maxEffectLength is the most characters the contract lets one plan effect have.
const maxEffectLength = 300

// ingressRestartEffect names as many of the routed environments as one effect has room for,
// and counts the rest.
func ingressRestartEffect(server string, routed []string) string {
	effect := fmt.Sprintf("The ingress proxy on %s restarts on a dedicated gateway before the deployment starts.", server)
	if len(routed) == 0 {
		return effect + " Nothing routes through it yet."
	}
	const ending = " drops for a few seconds."
	for named := len(routed); named > 0; named-- {
		text := effect + " Traffic to " + strings.Join(routed[:named], ", ")
		if rest := len(routed) - named; rest > 0 {
			text += fmt.Sprintf(" and %d more", rest)
		}
		if text += ending; utf8.RuneCountInString(text) <= maxEffectLength {
			return text
		}
	}
	if len(routed) == 1 {
		return effect + " Traffic to 1 environment" + ending
	}
	return effect + fmt.Sprintf(" Traffic to %d environments", len(routed)) + ending
}
