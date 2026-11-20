package agent

import (
	"bytes"
	"context"
	"net/netip"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

var responseStatus = regexp.MustCompile(`HTTP/1\.[01] (\d{3})`)

// TestNativeOwnerAllowlistTurnsAwayOtherUnits publishes a route admitted only to its owner's own
// address, as a staged catalog entry's is until the owner has created its administrator, and
// requests it from a container in another unit on the same host, which reaches the proxy over
// its own unit's network. The proxy turns it away. A private allowlist wide enough to hold that
// container's address admits it, which is why such an entry is refused: the container's address
// lies in the ranges the agent reports, and the shared allowlist check refuses the wide entry
// against them while it accepts the owner's address.
func TestNativeOwnerAllowlistTurnsAwayOtherUnits(t *testing.T) {
	h := newIsolationHost(t)
	target, other := h.f.unit("e", "target"), h.f.unit("f", "other")
	h.removeProxy()
	owner := target.set().Routes[0]
	owner.AllowCIDRs = []string{"203.0.113.7/32"}
	publish := func(allow ...string) {
		t.Helper()
		route := owner
		route.AllowCIDRs = allow
		if err := h.e.publishVisualRoutes(t.Context(), h.plan(target, []domain.Route{route})); err != nil {
			t.Fatal(err)
		}
	}
	publish(owner.AllowCIDRs...)
	// The other unit has a route of its own, so the proxy joins its network, as for any
	// deployment that publishes one.
	if err := h.e.publishVisualRoutes(t.Context(), h.plan(other, other.set().Routes)); err != nil {
		t.Fatal(err)
	}
	h.f.answers(proxyName, other.domain, "other")
	proxy, err := h.f.docker.ContainerInspect(t.Context(), proxyName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := proxy.Container.NetworkSettings.Networks[deployment.NetworkName(other.id)]
	if endpoint == nil || !endpoint.IPAddress.IsValid() {
		t.Fatal("the proxy did not join the other unit's network")
	}
	// fetch requests the target's route from the other unit's container, through the proxy's
	// address on that unit's network, and returns the response status, or 0 for none.
	fetch := func() int {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		call, err := h.f.docker.ExecCreate(ctx, other.container, client.ExecCreateOptions{AttachStdout: true, AttachStderr: true, Cmd: []string{"wget", "-S", "-T", "5", "-O", "/dev/null", "--header", "Host: " + target.domain, "http://" + endpoint.IPAddress.String() + ":80/"}})
		if err != nil {
			t.Fatal(err)
		}
		attached, err := h.f.docker.ExecAttach(ctx, call.ID, client.ExecAttachOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer attached.Close()
		var output bytes.Buffer
		if _, err = stdcopy.StdCopy(&output, &output, attached.Reader); err != nil {
			t.Fatal(err)
		}
		m := responseStatus.FindSubmatch(output.Bytes())
		if m == nil {
			return 0
		}
		code, _ := strconv.Atoi(string(m[1]))
		return code
	}
	// Each publish is verified before it returns, but the proxy may still be reloading.
	await := func(want int) {
		t.Helper()
		got := 0
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if got = fetch(); got == want {
				return
			}
		}
		t.Fatalf("the other unit's request answered %d, want %d", got, want)
	}
	await(403)

	inspect, err := h.f.docker.ContainerInspect(t.Context(), other.container, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := inspect.Container.NetworkSettings.Networks[deployment.NetworkName(other.id)].IPAddress
	reported, err := h.e.containerRanges(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ranges := []domain.ContainerRange{}
	covered := false
	for _, r := range reported {
		c, ok := domain.ParseContainerRange(r.Prefix, string(r.Source))
		if !ok {
			t.Fatalf("management would not read %+v", r)
		}
		ranges = append(ranges, c)
		covered = covered || c.Prefix.Contains(source)
	}
	if !covered {
		t.Fatalf("the other unit's address %s is outside the reported container ranges %+v", source, reported)
	}
	wide := netip.PrefixFrom(source, 8).Masked().String()
	if domain.ValidateOwnerAllowlist([]string{wide}, ranges) == nil {
		t.Fatalf("%s, which admits the other unit, was not refused", wide)
	}
	if err := domain.ValidateOwnerAllowlist(owner.AllowCIDRs, ranges); err != nil {
		t.Fatal("the owner's own address was refused:", err)
	}
	// A private entry that holds the other unit's address admits it.
	publish(wide)
	await(200)
}
