package agent

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// ingressOnGateway says whether the proxy runs with the dedicated ingress network as its
// default gateway, which a coordinated primary's private links need. ensureProxy and
// ingressNetworkPaths check that the proxy and network are this host's own.
func ingressOnGateway(ctx context.Context, docker *client.Client) bool {
	proxy, err := docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	if err != nil || !proxy.Container.State.Running || proxy.Container.Config.Labels[labelKind] != "ingress-v1" || proxy.Container.NetworkSettings == nil {
		return false
	}
	network := proxy.Container.NetworkSettings.Networks["astilba-ingress-network"]
	return network != nil && network.GwPriority == 100
}

// prepareIngress moves the proxy onto the dedicated gateway, as a reviewed plan asks of its new
// primary during reserve. It first puts back any attempt an interruption left, this one's
// included, so the journal and proxy agree before it decides. A proxy then on the gateway is left
// running untouched. Otherwise this is an attempt like a publish: a failure, or an interruption
// the next attempt finds, puts back the routes and proxy the journal records.
func (e *Engine) prepareIngress(ctx context.Context) error {
	state, err := e.settledRoutes(ctx, false)
	if err != nil {
		return err
	}
	if ingressOnGateway(ctx, e.Docker) {
		return nil
	}
	drift, err := e.ingressDrift(ctx, state)
	if err != nil {
		return err
	}
	if drift {
		return errors.New("ingress changed outside Astilba; explicit reconciliation required")
	}
	next, err := cloneRoutes(state.Current)
	if err != nil {
		return err
	}
	next.ManagedEgress, next.PrivateIngress, next.Revision = true, true, uuid.NewString()
	return e.attemptRoutes(ctx, state, next, false, "", false)
}
