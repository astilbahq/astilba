package agent

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// A deployment's image comes from a registry, where anyone who can push to it, or who runs it,
// shapes what pulling it writes. Docker unpacks every layer it pulls onto its storage, so a
// layer of a sparse file, a highly compressed layer, millions of empty files or a layer listed
// many times fills that storage however small the image is in the registry. Before Docker
// pulls an image, the agent therefore reads what Docker will pull, from the same registry,
// and counts what pulling it writes as it counts a local build's load (image_archive.go). The
// image is named by digest and Docker verifies every blob it pulls, so Docker unpacks exactly
// what the agent counted.

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

// pullMeasuredImage pulls image, which names its content by digest, once the agent has counted
// what pulling it writes to Docker's storage within the host's limits. credential is the
// registry's, if it needs one.
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
	if !ok || pinned.Digest().Algorithm() != digest.SHA256 || pinned.Digest().Validate() != nil {
		return refuse(&imageLoadRefused{why: "it is not named by its sha256 digest"})
	}
	limit, err := e.imageLoadLimit(ctx)
	if err != nil {
		return refuse(err)
	}
	info, err := e.Docker.Info(ctx, client.InfoOptions{})
	if err != nil {
		return refuse(&imageLoadRefused{why: "the agent cannot read Docker's registry settings"})
	}
	sources, err := registryImages(ctx, info.Info.RegistryConfig, named, credential)
	if err != nil {
		return refuse(&imageLoadRefused{why: err.Error()})
	}
	// Docker pulls the image for its own platform, which is the agent's.
	platforms := []string{runtime.GOARCH, dockerArchitecture(info.Info.Architecture)}
	var fetched error
	var measured imageCost
	for _, source := range sources {
		measure := imageMeasure{source: source, limit: limit, platforms: platforms, cache: e.layerCosts()}
		fetched, measured = measure.image(ctx, pinned.Digest()), measure.total
		var unread *imageArchiveUnread
		if !errors.As(fetched, &unread) {
			break
		}
	}
	if err = fetched; err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var unread *imageArchiveUnread
		if errors.As(err, &unread) {
			err = registryUnreachable(unread.err)
		}
		return refuse(err)
	}
	var auth string
	if credential != nil {
		auth = encodedRegistryAuth(*credential)
	}
	return refuse(e.pullWatched(ctx, image, auth, measured))
}

// pullSlack is how much more than the agent measured a pull may take of Docker's storage, for
// other writers on the server, before the agent stops it.
const pullSlack = 1 << 30

// pullWatched pulls image while watching Docker's storage. Docker writes each blob as its
// registry sends it and checks the blob's size only once it ends, so a registry that sends
// Docker more than the image it names could fill the storage. The agent stops the pull once
// the storage has lost more than it measured, and slack for other writers.
func (e *Engine) pullWatched(ctx context.Context, image, auth string, measured imageCost) error {
	roots, ok := imageStorageRoots(ctx, e.Docker)
	before, _, known := storageFree(roots)
	if !ok || !known {
		return &imageLoadRefused{why: "the agent cannot observe this server's Docker storage"}
	}
	floor := before - measured.Bytes - pullSlack
	pullCtx, stop := context.WithCancel(ctx)
	defer stop()
	var exceeded atomic.Bool
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-pullCtx.Done():
				return
			case <-ticker.C:
			}
			if free, _, ok := storageFree(roots); ok && free < floor {
				exceeded.Store(true)
				stop()
				return
			}
		}
	}()
	err := e.pullImageAuth(pullCtx, image, auth)
	stop()
	<-watched
	if exceeded.Load() {
		return &imageLoadRefused{why: fmt.Sprintf("Docker's pull took more than the %d MiB the agent measured, and %d MiB more, of this server's Docker storage, so the agent stopped it. Its registry sent more than the image it names, or something else on the server wrote to Docker's storage at the same time", measured.Bytes>>20, pullSlack>>20), fix: "Check the image's registry and {retry}. The server's owner can remove a stopped pull's partial download from containerd's ingest directory."}
	}
	return err
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
