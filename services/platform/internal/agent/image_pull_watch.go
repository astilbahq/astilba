package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// classicImageStoreNote is preflight's note for a server whose Docker keeps images in its
// classic store, where the agent can only watch a pull rather than feed it.
const classicImageStoreNote = "Docker uses its classic image store. The agent bounds what pulling a deployment's image writes there by watching Docker's storage, so a registry can make a pull write up to 1 GiB more than the image before it stops, and a stopped pull can leave its partial download behind. Switch Docker to the containerd image store, its default for new installs, for exact bounds."

// pullSlack is how much more than the agent measured a pull may take of Docker's storage, for
// other writers on the server, before the agent stops it.
const pullSlack = 1 << 30

// pullWatched pulls image into Docker's classic image store while watching Docker's storage.
// Docker writes each blob as its registry sends it and checks the blob's size only once it
// ends, so a registry that sends Docker more than the image it names could fill the storage.
// The agent stops the pull once the storage has lost more than it measured, and slack for
// other writers. A stopped pull's partial download is Docker's to remove.
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
		return &imageLoadRefused{why: fmt.Sprintf("Docker's pull took more than the %d MiB the agent measured, and %d MiB more, of this server's Docker storage, so the agent stopped it. Its registry sent more than the image it names, or something else on the server wrote to Docker's storage at the same time", measured.Bytes>>20, pullSlack>>20), fix: "Check the image's registry and {retry}."}
	}
	return err
}
