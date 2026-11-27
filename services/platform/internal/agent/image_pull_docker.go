package agent

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/moby/moby/client"
)

// Docker pulls some images itself: Astilba's own pinned images, such as the test image, the
// builder and the ingress proxy, and, with Docker's classic image store, a deployment's image
// once the agent has measured it. Docker notices a registry that has gone, but not one that is
// there and sends nothing, or a byte now and then, and the agent would wait for it, and every
// command after it, until the agent stops. So the agent reads Docker's reports of a pull and ends
// one whose registry sends less than it asks of an answer it reads itself, see registryProgress,
// and no pull runs longer than dockerPullTime. A command ends the pulls it makes sooner once its
// owner cancels it, or once its own time for pulling runs out, see pullContext.

// dockerPullTime is the longest a pull Docker makes itself may run, so that a registry that keeps
// just above registryProgress's floor can't hold the host for ever. An image of 10 GB arrives
// within the hour at 25 Mbit/s.
const dockerPullTime = time.Hour

// deploymentPullTime is how long a deployment, a protected release or a coordinated phase may
// take to put its images into Docker, whether the agent reads them or Docker pulls them. testPullTime
// is the test command's, for the small test image alone.
const (
	deploymentPullTime = time.Hour
	testPullTime       = 10 * time.Minute
)

// errDockerPullStalled is Docker's pull from a registry that sent less of the image than
// registryProgress asks for. It is a timeout, as errAnswerStalled is.
var errDockerPullStalled error = dockerPullStalled{}

type dockerPullStalled struct{}

func (dockerPullStalled) Error() string { return "the registry stopped sending the image to Docker" }
func (dockerPullStalled) Timeout() bool { return true }

// errPullTimedOut is a pull that ran past its time: dockerPullTime, or the command's own.
var errPullTimedOut error = pullTimedOut{}

type pullTimedOut struct{}

func (pullTimedOut) Error() string { return "the pull ran past the time the agent allows for it" }
func (pullTimedOut) Timeout() bool { return true }

// pullContext is ctx while command c puts images into Docker. It ends once c's owner cancels c,
// with errPullCancelled as its cause, or after limit, with errPullTimedOut, unless e stands in a
// shorter limit for tests.
func (e *Engine) pullContext(ctx context.Context, c api.ExecutionCommand, limit time.Duration) (context.Context, func()) {
	timed, cancel := context.WithTimeoutCause(ctx, e.pullTime(limit), errPullTimedOut)
	cancellable, stop := untilCancelled(timed, func() bool { return e.cancelled(c.Id) }, errPullCancelled)
	return cancellable, func() { stop(); cancel() }
}

// pullTime is limit, or the time tests stand in for every limit on pulls.
func (e *Engine) pullTime(limit time.Duration) time.Duration {
	if e.pullLimit > 0 {
		return e.pullLimit
	}
	return limit
}

// progressFloor is how fast a registry must send an image, see registryProgress.
func (e *Engine) progressFloor() registryProgress {
	if e.registryProgress != nil {
		return *e.registryProgress
	}
	return defaultRegistryProgress
}

func (e *Engine) pullImage(ctx context.Context, image string) error {
	return e.pullImageAuth(ctx, image, "")
}

// pullImageAuth has Docker pull image, with auth, Docker's encoded registry credential, if any.
// It fails with errDockerPullStalled once Docker's reports show that the registry sent too little,
// see dockerPullWatch, and with errPullTimedOut after dockerPullTime. Once ctx ends it fails with
// ctx's cause: the owner's cancel or the command's time, where pullContext gives them.
func (e *Engine) pullImageAuth(ctx context.Context, image, auth string) error {
	ctx, cancelTime := context.WithTimeoutCause(ctx, e.pullTime(dockerPullTime), errPullTimedOut)
	defer cancelTime()
	ctx, stall := context.WithCancelCause(ctx)
	defer stall(nil)
	// Without Docker's info, the agent allows for the larger step.
	info, err := e.Docker.Info(ctx, client.InfoOptions{})
	containerd := err != nil || containerdImageStore(info.Info.DriverStatus)
	// The watch starts before Docker answers: Docker answers once it has begun the pull, which can
	// wait on the registry.
	watch := newDockerPullWatch(e.progressFloor(), containerd, func() { stall(errDockerPullStalled) })
	defer watch.stop()
	err = e.dockerPull(ctx, image, auth, watch)
	if err != nil && ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}

// dockerPullMessage is what the agent reads of one of Docker's reports of a pull.
type dockerPullMessage struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Progress *struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
	Error       string `json:"error"`
	ErrorDetail *struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
}

func (e *Engine) dockerPull(ctx context.Context, image, auth string, watch *dockerPullWatch) error {
	stream, err := e.Docker.ImagePull(ctx, image, client.ImagePullOptions{RegistryAuth: auth})
	if err != nil {
		return err
	}
	defer stream.Close()
	decoder := jsontext.NewDecoder(stream)
	for {
		var message dockerPullMessage
		err := json.UnmarshalDecode(decoder, &message)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if message.Error != "" || message.ErrorDetail != nil && message.ErrorDetail.Message != "" {
			return errors.New("registry rejected the image pull")
		}
		watch.report(message)
	}
}

// dockerDownloads is how many layers Docker downloads at once, its default
// max-concurrent-downloads.
const dockerDownloads = 3

// reportStep is the most of a layer of total bytes, zero when unknown, that Docker receives before
// it reports the layer's progress. With its containerd image store, Docker reports what containerd
// has written of the layer, which containerd writes a mebibyte at a time. The classic store
// reports every 512 KiB, or every 1% of the layer when that is less.
func reportStep(containerd bool, total int64) int64 {
	switch {
	case containerd && total > 0:
		return min(1<<20, total)
	case containerd:
		return 1 << 20
	case total > 0:
		return min(512<<10, total/100)
	}
	return 512 << 10
}

// dockerPullWatch ends a pull Docker makes itself once Docker's reports show that the registry
// sent less than progress asks for, the floor the agent's own reads keep to, see slowAnswer. Time
// counts while the registry owes Docker some of the image: until Docker has downloaded every layer
// it named, including before it names any, while it reads the image's manifests. Time Docker
// spends unpacking downloaded layers does not count.
//
// Docker reports a layer only once it has received a step of it, see reportStep, so it can
// hold up to a step of each layer it is downloading without saying so. A link that keeps up with
// the floor can therefore show less in a window than the floor asks for, and make it up later. So
// the registry may fall behind the floor by what Docker can hold unreported, the largest steps of
// the layers it hasn't finished, as many as it downloads at once, before the pull ends. What it
// sends beyond the floor in one window does not make up for a later one. A registry that stops is
// caught once it is that far behind: with the containerd image store within five windows for a
// layer of over 1 MiB on its own, and thirteen for three at once; with the classic store, within
// three and seven. A host whose owner lets Docker download more layers at once than dockerDownloads
// holds up to a step more of each, and a link just above the floor could fall short.
type dockerPullWatch struct {
	progress registryProgress
	// containerd is whether Docker keeps images in containerd, see reportStep.
	containerd bool
	stall      func()
	// mu guards what follows, which the timer also reads and changes.
	mu sync.Mutex
	// timer ends the window while the registry owes Docker some of the image.
	timer *time.Timer
	// layers are the layers Docker has named, by its ID for each.
	layers map[string]*pulledLayer
	// since is when the time counted in the window was last brought up to date, zero while the
	// registry owes nothing. waited is the window's time so far, and sent what Docker reported
	// receiving in it.
	since  time.Time
	waited time.Duration
	sent   int64
	// behind is how far the registry has fallen behind the floor in the windows so far.
	behind  int64
	stalled bool
	stopped bool
}

// pulledLayer is what Docker has reported of a layer.
type pulledLayer struct {
	// downloaded is whether Docker has the whole layer.
	downloaded bool
	// current is how much of the layer Docker has reported receiving, of total, zero until it
	// reports one.
	current, total int64
}

func newDockerPullWatch(progress registryProgress, containerd bool, stall func()) *dockerPullWatch {
	w := &dockerPullWatch{progress: progress, containerd: containerd, stall: stall, layers: map[string]*pulledLayer{}}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.since = time.Now()
	w.timer = time.AfterFunc(progress.window, w.windowEnds)
	return w
}

// report reads one of Docker's reports of the pull.
func (w *dockerPullWatch) report(m dockerPullMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stalled || w.stopped {
		return
	}
	now := time.Now()
	if w.count(now); w.stalled {
		return
	}
	if m.ID != "" {
		w.apply(m)
	}
	w.resume(now)
}

// apply records m, a report on a layer.
func (w *dockerPullWatch) apply(m dockerPullMessage) {
	layer := func() *pulledLayer {
		l := w.layers[m.ID]
		if l == nil {
			l = &pulledLayer{}
			w.layers[m.ID] = l
		}
		return l
	}
	switch {
	case m.Status == "Pulling fs layer" || m.Status == "Waiting":
		layer()
	case m.Status == "Downloading":
		l := layer()
		if m.Progress == nil {
			return
		}
		// A download Docker begins again counts from where it begins.
		if m.Progress.Current > l.current {
			w.sent += m.Progress.Current - l.current
		}
		l.current = m.Progress.Current
		if m.Progress.Total > 0 {
			l.total = m.Progress.Total
		}
	case strings.HasPrefix(m.Status, "Retrying in "):
		// Docker downloads the rest of the layer again, and reports it from zero.
		layer().current = 0
	case m.Status == "Verifying Checksum" || m.Status == "Download complete" || m.Status == "Extracting" || m.Status == "Pull complete" || m.Status == "Already exists":
		layer().downloaded = true
	}
	// Any other report, such as which repository Docker pulls from, says nothing of a layer.
}

// owed reports whether the registry owes Docker some of the image.
func (w *dockerPullWatch) owed() bool {
	if len(w.layers) == 0 {
		return true
	}
	for _, l := range w.layers {
		if !l.downloaded {
			return true
		}
	}
	return false
}

// unreported is the most Docker can have received without reporting it, see dockerPullWatch.
func (w *dockerPullWatch) unreported() int64 {
	if len(w.layers) == 0 {
		return reportStep(w.containerd, 0)
	}
	var steps []int64
	for _, l := range w.layers {
		if !l.downloaded {
			steps = append(steps, reportStep(w.containerd, l.total))
		}
	}
	slices.SortFunc(steps, func(a, b int64) int { return cmp.Compare(b, a) })
	var most int64
	for _, step := range steps[:min(len(steps), dockerDownloads)] {
		most += step
	}
	return most
}

// count brings the window's time up to now, ending each window that has been waited out.
func (w *dockerPullWatch) count(now time.Time) {
	if w.since.IsZero() {
		return
	}
	w.waited += now.Sub(w.since)
	w.since = now
	for w.waited >= w.progress.window {
		w.behind = max(0, w.behind+w.progress.least-w.sent)
		w.waited -= w.progress.window
		w.sent = 0
		if w.behind > w.unreported() {
			w.stalled = true
			w.timer.Stop()
			w.stall()
			return
		}
	}
}

// resume runs the window's time while the registry owes Docker some of the image, and stops it
// otherwise.
func (w *dockerPullWatch) resume(now time.Time) {
	if !w.owed() {
		w.since = time.Time{}
		w.timer.Stop()
		return
	}
	if w.since.IsZero() {
		w.since = now
	}
	w.timer.Reset(w.progress.window - w.waited)
}

// windowEnds is the window ending while the registry owes Docker some of the image.
func (w *dockerPullWatch) windowEnds() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stalled || w.stopped {
		return
	}
	now := time.Now()
	if w.count(now); !w.stalled {
		w.resume(now)
	}
}

func (w *dockerPullWatch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	w.timer.Stop()
}
