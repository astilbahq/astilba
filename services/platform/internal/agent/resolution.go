package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
)

// resolveVisual resolves a plan's images and reports what applying it would change on the
// host, for the owner who applies it to review.
func (e *Engine) resolveVisual(ctx context.Context, p deployment.Payload) (deployment.Observation, error) {
	observed, err := e.resolveVisualImages(ctx, p, nil)
	if err != nil {
		return observed, err
	}
	if observed.HostChanges, err = e.hostChanges(p.Plan); err != nil {
		return observed, errors.New("Host ingress cannot accept this plan: " + err.Error())
	}
	observed.HostChangesReported = true
	return observed, nil
}

// hostChanges is what publishing p's routes would change on the host itself, beyond its own
// unit: newly published ports, the certificate contact email and managed egress. It asks
// candidateRoutes what an approved plan would write, so it follows the same rules as a
// deploy, and reports the difference for an owner to review before the plan is applied.
func (e *Engine) hostChanges(p deployment.Plan) ([]deployment.HostChange, error) {
	state, err := e.Journal.routing()
	if err != nil {
		return nil, err
	}
	current := state.Current
	// publishRoutes leaves a host with no routes, before or after, untouched.
	if len(p.Specification.Routes) == 0 && len(current.Sets) == 0 {
		return nil, nil
	}
	p.HostChanges = true
	next, err := e.candidateRoutes(current, p)
	if err != nil {
		return nil, err
	}
	changes := []deployment.HostChange{}
	published := map[int]bool{}
	for _, port := range current.Ports {
		published[port] = true
	}
	added := []int{}
	for _, port := range next.Ports {
		if !published[port] {
			added = append(added, port)
		}
	}
	// routePorts keeps every port the host already publishes, so a change only ever adds.
	if len(added) > 0 {
		changes = append(changes, deployment.HostChange{Kind: deployment.HostChangePorts, Ports: added})
	}
	if next.Email != current.Email {
		changes = append(changes, deployment.HostChange{Kind: deployment.HostChangeCertificateEmail})
	}
	if next.ManagedEgress != current.ManagedEgress {
		changes = append(changes, deployment.HostChange{Kind: deployment.HostChangeManagedEgress})
	}
	return changes, nil
}

// withHostChanges reports whether an observation reports host changes, even none.
func withHostChanges(observation *jsontext.Value) bool {
	var observed deployment.Observation
	return observation != nil && json.Unmarshal(*observation, &observed) == nil && (observed.HostChangesReported || len(observed.HostChanges) > 0)
}

// withoutHostChanges is an observation as a controller that reviews no host changes accepts it.
func withoutHostChanges(observation *jsontext.Value) *jsontext.Value {
	var observed deployment.Observation
	if observation == nil || json.Unmarshal(*observation, &observed) != nil || !observed.HostChangesReported && len(observed.HostChanges) == 0 {
		return observation
	}
	observed.HostChanges, observed.HostChangesReported = nil, false
	raw, err := stablejson.Marshal(observed)
	if err != nil {
		return observation
	}
	value := jsontext.Value(raw)
	return &value
}
