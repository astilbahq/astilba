package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// classicImageStoreNote is preflight's note for a server whose Docker keeps images in its
// classic store, where the agent can only watch a pull rather than feed it.
const classicImageStoreNote = "Docker uses its classic image store, where the agent can only watch a pull: a registry can make one write 1 GiB more than its image and leave a partial download. Switch Docker to the containerd image store for exact bounds."

// pullSlack is how much more than the agent measured a pull may take of Docker's storage, for
// other writers on the server, before the agent stops it: at most half the reserve, so a pull
// the agent stops still leaves half the reserve free.
const pullSlack = 1 << 30

// pullWatched pulls image into Docker's classic image store while watching Docker's storage.
// Docker writes each blob as its registry sends it and checks the blob's size only once it
// ends, so a registry that sends Docker more than the image it names could fill the storage.
// The agent stops the pull once any filesystem Docker writes images to has lost more than it
// measured, and slack for other writers, or once it can no longer see one. A stopped pull's
// partial download is Docker's to remove.
func (e *Engine) pullWatched(ctx context.Context, image, auth string, measured imageCost, limit imageLoadLimit) error {
	slack := min(int64(pullSlack), limit.reserveBytes/2)
	roots, ok := imageStorageRoots(ctx, e.Docker)
	if !ok {
		return &imageLoadRefused{why: "the agent cannot observe this server's Docker storage"}
	}
	before := make([]int64, len(roots))
	for i, root := range roots {
		free, _, known := freeSpace(root)
		if !known {
			return &imageLoadRefused{why: "the agent cannot observe this server's Docker storage"}
		}
		before[i] = free
	}
	pullCtx, stop := context.WithCancel(ctx)
	defer stop()
	var stopped atomic.Pointer[imageLoadRefused]
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
			for i, root := range roots {
				free, _, known := freeSpace(root)
				switch {
				case !known:
					stopped.Store(&imageLoadRefused{why: "the agent could no longer observe this server's Docker storage while Docker pulled it, so the agent stopped the pull"})
				case before[i]-free > measured.Bytes+slack:
					stopped.Store(&imageLoadRefused{why: fmt.Sprintf("Docker's pull took more than the %d MiB the agent measured, and %d MiB more, of this server's Docker storage, so the agent stopped it. Its registry sent more than the image it names, or something else on the server wrote to Docker's storage at the same time", measured.Bytes>>20, slack>>20), fix: "Check the image's registry and {retry}."})
				default:
					continue
				}
				stop()
				return
			}
		}
	}()
	err := e.pullImageAuth(pullCtx, image, auth)
	stop()
	<-watched
	if refused := stopped.Load(); refused != nil {
		return refused
	}
	return err
}
