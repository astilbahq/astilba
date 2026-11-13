package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// loadPipelineImage loads the archive the build container wrote, measuring it on the way. See
// image_archive.go.
func (e *Engine) loadPipelineImage(ctx context.Context, c api.ExecutionCommand, size int64) error {
	// The agent runs one operation at a time, so nothing else of its own writes to Docker's
	// storage between measuring it here and the load.
	limit, err := e.imageLoadLimit(ctx)
	if err != nil {
		return err
	}
	cli := e.DockerCLI
	if cli == "" {
		cli = "docker"
	}
	cmd := exec.CommandContext(ctx, cli, "exec", pipelineName(c), "cat", "/workspace/image.tar")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_HOST=" + e.Docker.DaemonHost()}
	if v := os.Getenv("DOCKER_CONFIG"); v != "" {
		cmd.Env = append(cmd.Env, "DOCKER_CONFIG="+v)
	}
	stream, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	// Docker reads the archive only through the gate. A refusal fails the stream it reads, so
	// Docker never receives a complete archive to import.
	reader, writer := io.Pipe()
	inspected := make(chan error, 1)
	go func() {
		err := inspectImageArchive(&imageArchiveGate{src: &measuredReader{r: stream, remaining: size}, out: writer}, limit)
		if errors.Is(err, errImageOverBytes) || errors.Is(err, errImageOverEntries) {
			err = limit.refusal(err)
		}
		_ = writer.CloseWithError(err)
		inspected <- err
	}()
	response, loaded := e.Docker.ImageLoad(ctx, reader)
	if loaded == nil {
		loaded = imageLoadOutcome(response)
		_ = response.Close()
	}
	// Docker has stopped reading, so the inspection's next write fails rather than waits, and
	// whatever the build container still sends after the archive's end is not needed.
	_ = reader.CloseWithError(errors.New("Docker stopped reading the image archive"))
	var checked error
	var unread *imageArchiveUnread
	select {
	case checked = <-inspected:
	case <-time.After(5 * time.Second):
		// The build container stopped sending. Ending it ends the inspection, and the archive
		// it then cannot read is the agent's doing, not the build's.
		_ = cmd.Process.Kill()
		if checked = <-inspected; errors.As(checked, &unread) {
			checked = errors.New("the build container stopped sending the image archive")
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	var refused *imageLoadRefused
	switch {
	case errors.As(checked, &refused):
		return refused
	case errors.As(checked, &unread):
		return errors.New("The built image was not loaded: the agent could not read its archive from the build container. Existing workloads are unchanged.")
	case loaded != nil:
		return fmt.Errorf("The built image was not loaded: Docker's image load failed: %s. Existing workloads are unchanged.", firstLine(loaded.Error(), 300))
	case checked != nil:
		return errors.New("The built image was not loaded: Docker stopped reading its archive. Existing workloads are unchanged.")
	}
	return nil
}

// imageLoadOutcome reads Docker's progress messages for a load, which carry its failure: Docker
// answers before it has read the archive, so the HTTP status cannot.
func imageLoadOutcome(r io.Reader) error {
	decoder := jsontext.NewDecoder(r)
	var failed error
	for {
		var message struct {
			Error string `json:"error"`
		}
		err := json.UnmarshalDecode(decoder, &message)
		if errors.Is(err, io.EOF) {
			return failed
		}
		if err != nil {
			return err
		}
		if message.Error != "" && failed == nil {
			failed = errors.New(message.Error)
		}
	}
}

func firstLine(s string, limit int) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > limit {
		s = strings.ToValidUTF8(s[:limit], "")
	}
	return s
}

// imageLoadLimit is what a load may write now: the host's per-load limit, or less where Docker's
// storage would otherwise drop below its reserve.
func (e *Engine) imageLoadLimit(ctx context.Context) (imageLoadLimit, error) {
	load, reserve := e.ImageLoadBytes, e.ImageLoadReserveBytes
	if load <= 0 {
		load = DefaultImageLoadBytes
	}
	if reserve <= 0 {
		reserve = DefaultImageLoadReserveBytes
	}
	bytes, inodes, ok := imageStorage(ctx, e.Docker)
	if !ok {
		return imageLoadLimit{}, &imageLoadRefused{why: "the agent cannot observe this server's Docker storage"}
	}
	limit := imageLoadLimit{Bytes: load, Entries: localImageLoadEntries, loadBytes: load, reserveBytes: reserve}
	if room := bytes - reserve; room < limit.Bytes {
		limit.Bytes, limit.bytesByStorage = room, true
	}
	if room := inodes - dockerReserveInodes; inodes >= 0 && room < limit.Entries {
		limit.Entries, limit.entriesByStorage = room, true
	}
	if limit.Bytes <= 0 {
		return limit, limit.refusal(errImageOverBytes)
	}
	if limit.Entries <= 0 {
		return limit, limit.refusal(errImageOverEntries)
	}
	return limit, nil
}

// measuredReader reads exactly remaining bytes, and fails if r has more.
type measuredReader struct {
	r         io.Reader
	remaining int64
}

func (m *measuredReader) Read(p []byte) (int, error) {
	if m.remaining == 0 {
		if n, _ := m.r.Read(make([]byte, 1)); n > 0 {
			return 0, errors.New("the image archive grew after it was measured")
		}
		return 0, io.EOF
	}
	n, err := m.r.Read(p[:min(int64(len(p)), m.remaining)])
	m.remaining -= int64(n)
	if err == io.EOF && m.remaining > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
