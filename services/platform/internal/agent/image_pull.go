package agent

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// A deployment's image comes from a registry, where anyone who can push to it, or who runs it,
// shapes what putting it into Docker writes. Docker unpacks every layer onto its storage, so a
// layer of a sparse file, a highly compressed layer, millions of empty files or a layer listed
// many times fills that storage however small the image is in the registry, and a registry can
// send Docker more for a blob than the image says it holds. The agent therefore reads the image
// from its registry itself and counts what Docker writes for it as it counts a local build's
// load (image_archive.go), before Docker writes it: with Docker's containerd image store by
// loading what it read (image_pull_load.go), and with the classic store by letting Docker pull
// what it measured while watching Docker's storage (image_pull_watch.go).

// imagePullRefused is why the agent did not pull an image; its message is the operation's note.
type imagePullRefused struct{ note string }

func (r *imagePullRefused) Error() string { return r.note }

// imagePullNote is the note for failing to pull an image: the refusal's note followed by
// after when the agent refused it, and otherwise otherwise.
func imagePullNote(err error, otherwise, after string) string {
	if refused := new(imagePullRefused); errors.As(err, &refused) {
		return refused.note + after
	}
	return otherwise
}

// layerCosts remembers what unpacking each layer the agent has measured writes, by its digest,
// so a new version of an image does not download its unchanged layers twice.
type layerCosts struct {
	mu    sync.Mutex
	costs map[digest.Digest]imageCost
}

const layerCostEntries = 4096

func (c *layerCosts) get(d digest.Digest) (imageCost, bool) {
	if c == nil {
		return imageCost{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cost, ok := c.costs[d]
	return cost, ok
}

func (c *layerCosts) put(d digest.Digest, cost imageCost) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.costs == nil || len(c.costs) >= layerCostEntries {
		c.costs = map[digest.Digest]imageCost{}
	}
	c.costs[d] = cost
}

// pullMeasuredImage puts image, which names its content by digest, into Docker once the agent
// has counted what that writes to Docker's storage within the host's limits. With Docker's
// containerd image store the agent reads the image and loads it; with the classic store it
// measures the image, then Docker pulls it while the agent watches Docker's storage.
// credential is the registry's, if it needs one; the agent keeps it only for this pull.
func (e *Engine) pullMeasuredImage(ctx context.Context, image string, credential *pipeline.Credential) error {
	refuse := func(err error) error {
		var refused *imageLoadRefused
		if errors.As(err, &refused) {
			return &imagePullRefused{refused.pulled(image)}
		}
		return err
	}
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return refuse(&imageLoadRefused{why: "its reference is not valid"})
	}
	pinned, ok := named.(reference.Digested)
	if !ok || pinned.Digest().Algorithm() != digest.SHA256 {
		return refuse(&imageLoadRefused{why: "it is not named by its sha256 digest"})
	}
	canonical, err := reference.WithDigest(reference.TrimNamed(named), pinned.Digest())
	if err != nil {
		return refuse(&imageLoadRefused{why: "its reference is not valid"})
	}
	limit, err := e.imageLoadLimit(ctx)
	if err != nil {
		return refuse(err)
	}
	info, err := e.Docker.Info(ctx, client.InfoOptions{})
	if err != nil {
		return refuse(&imageLoadRefused{why: "the agent cannot read Docker's registry settings"})
	}
	if info.Info.OSType != "linux" {
		return refuse(&imageLoadRefused{why: "Docker on this server runs " + truncated(info.Info.OSType, 32) + " images"})
	}
	sources, err := registryImages(ctx, info.Info, named, credential)
	if err != nil {
		return refuse(err)
	}
	// Docker pulls the image for its own platform.
	platform := ocispec.Platform{OS: "linux", Architecture: dockerArchitecture(info.Info.Architecture)}
	plan, source, err := planFromSources(ctx, sources, pinned.Digest(), platform, limit)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var unread *imageArchiveUnread
		if errors.As(err, &unread) {
			err = registryUnreachable(unread.err)
		}
		return refuse(err)
	}
	if containerdImageStore(info.Info.DriverStatus) {
		return refuse(e.loadRegistryImage(ctx, canonical, source, plan, limit))
	}
	measured, err := e.measureRegistryImage(ctx, source, plan, limit)
	if err != nil {
		var unread *imageArchiveUnread
		if errors.As(err, &unread) {
			err = registryUnreachable(unread.err)
		}
		return refuse(err)
	}
	// Measuring read the whole image, while other writers may have filled Docker's storage.
	if limit, err = e.imageLoadLimit(ctx); err != nil {
		return refuse(err)
	}
	if err = limit.check(measured); err != nil {
		return refuse(limit.refusal(err))
	}
	var auth string
	if credential != nil {
		auth = encodedRegistryAuth(*credential)
	}
	if err = e.pullWatched(ctx, image, auth, measured); err != nil {
		return refuse(err)
	}
	// The classic store names an image by its config's digest. One that differs is an image the
	// agent did not measure.
	pulled, err := e.Docker.ImageInspect(ctx, image)
	if err != nil || pulled.ID != plan.config.desc.Digest.String() {
		_, _ = e.Docker.ImageRemove(ctx, image, client.ImageRemoveOptions{})
		return refuse(&imageLoadRefused{why: "Docker pulled a different image for this server than the agent measured"})
	}
	return nil
}

// planFromSources plans the image from the first of sources that can be read: a mirror the
// agent cannot read gives way to the next, and an insecure registry it cannot reach over HTTPS
// to plain HTTP. A registry that answered over HTTPS is not asked again in the clear.
func planFromSources(ctx context.Context, sources []registryImage, root digest.Digest, platform ocispec.Platform, limit imageLoadLimit) (*registryPlan, registryImage, error) {
	var err error
	for _, source := range sources {
		var answered *errcode.ErrorResponse
		if plainHTTPFallback(source) && errors.As(err, &answered) {
			break
		}
		var plan *registryPlan
		plan, err = planRegistryImage(ctx, source, root, platform, limit)
		var unread *imageArchiveUnread
		if !errors.As(err, &unread) {
			return plan, source, err
		}
	}
	return nil, nil, err
}

// containerdImageStore reports whether Docker keeps images in containerd, as its info's driver
// status says.
func containerdImageStore(status [][2]string) bool {
	for _, pair := range status {
		if pair[0] == "driver-type" && pair[1] == "io.containerd.snapshotter.v1" {
			return true
		}
	}
	return false
}

// measureRegistryImage reads each distinct layer of plan from source, unless the agent measured it
// before, and returns what pulling the image writes.
func (e *Engine) measureRegistryImage(ctx context.Context, source registryImage, plan *registryPlan, limit imageLoadLimit) (imageCost, error) {
	cache := e.layerCosts()
	running := plan.stored
	unpacked := map[string]imageCost{}
	for _, layer := range plan.layers {
		if _, done := unpacked[layer.Digest.String()]; done {
			continue
		}
		cost, ok := cache.get(layer.Digest)
		if !ok {
			var err error
			if cost, err = measureRegistryLayer(ctx, source, layer, nil, limit.less(running)); err != nil {
				return imageCost{}, err
			}
			cache.put(layer.Digest, cost)
		}
		unpacked[layer.Digest.String()] = cost
		running.add(cost)
		running.add(imageCost{Bytes: storageLayerBytes, Entries: storageLayerEntries})
		if err := limit.check(running); err != nil {
			return imageCost{}, limit.refusal(err)
		}
	}
	return registryImageCost(plan, unpacked, limit)
}

func (e *Engine) layerCosts() *layerCosts {
	e.layerCostsOnce.Do(func() { e.measuredLayers = &layerCosts{} })
	return e.measuredLayers
}

// registryUnreachable is the refusal for an image the agent could not read from its registry.
func registryUnreachable(err error) error {
	var response *errcode.ErrorResponse
	why := "the agent could not read it from its registry to measure what pulling it writes"
	switch {
	case errors.Is(err, errLongerThanMeasured):
		why = "its registry sent more for a blob than the image's manifest says the blob holds"
	case errors.As(err, &response) && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden):
		why = "its registry refused to let the agent read it, so the agent cannot measure what pulling it writes"
	case errors.As(err, &response) && response.StatusCode == http.StatusNotFound:
		why = "its registry does not have it"
	}
	return &imageLoadRefused{why: why, fix: "Check the image's registry, its credentials and that this server can reach it, then {retry}."}
}

// dockerArchitecture is the GOARCH name for an architecture as Docker's info reports it.
func dockerArchitecture(arch string) string {
	switch arch {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	}
	return arch
}

// encodedRegistryAuth is a credential as Docker's API takes it.
func encodedRegistryAuth(c pipeline.Credential) string {
	raw, _ := json.Marshal(map[string]string{"username": c.Username, "password": c.Password})
	return base64.URLEncoding.EncodeToString(raw)
}

func (e *Engine) pullVisualImages(ctx context.Context, p deployment.Payload) error {
	images := make([]string, 0, len(p.Plan.Images)+1)
	for _, image := range p.Plan.Images {
		images = append(images, image)
	}
	images = append(images, execution.TestImage)
	for _, image := range images {
		key := ""
		for k, v := range p.Plan.Images {
			if v == image {
				key = k
				break
			}
		}
		if a, ok := p.Plan.Artifacts[key]; ok && a.ServerID != nil {
			v, err := e.Docker.ImageInspect(ctx, image)
			if err != nil || v.ID != a.Digest || v.Architecture != a.Architecture {
				return errors.New("local artifact identity is unavailable")
			}
			continue
		}
		if _, err := e.Docker.ImageInspect(ctx, image); err == nil {
			continue
		} else if !errdefs.IsNotFound(err) {
			return errors.New("cannot inspect reviewed image")
		}
		if _, planned := p.Plan.Images[key]; !planned {
			// Astilba's own images are pinned by digest in the agent itself.
			if err := e.pullImageAuth(ctx, image, ""); err != nil {
				return errors.New("image pull failed or was interrupted")
			}
			continue
		}
		var credential *pipeline.Credential
		if c, ok := p.RegistryAccess[key]; ok {
			credential = &c
		}
		if err := e.pullMeasuredImage(ctx, image, credential); err != nil {
			var refused *imagePullRefused
			if errors.As(err, &refused) {
				return err
			}
			return errors.New("image pull failed or was interrupted")
		}
	}

	for key, artifact := range p.Plan.Artifacts {
		v, err := e.Docker.ImageInspect(ctx, p.Plan.Images[key])
		if err != nil || v.Architecture != artifact.Architecture {
			return errors.New("artifact architecture differs from the reviewed target")
		}
	}
	return nil
}

func (e *Engine) pullImage(ctx context.Context, image string) error {
	return e.pullImageAuth(ctx, image, "")
}
func (e *Engine) pullImageAuth(ctx context.Context, image, auth string) error {
	stream, err := e.Docker.ImagePull(ctx, image, client.ImagePullOptions{RegistryAuth: auth})
	if err != nil {
		return err
	}
	defer stream.Close()
	decoder := jsontext.NewDecoder(stream)
	for {
		var message struct {
			Error string `json:"error"`
		}
		err := json.UnmarshalDecode(decoder, &message)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if message.Error != "" {
			return errors.New("registry rejected the image pull")
		}
	}
}
