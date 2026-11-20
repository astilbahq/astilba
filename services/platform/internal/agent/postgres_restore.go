package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"

	"github.com/containerd/errdefs"
)

// ensureImage pulls a pinned public image the host does not hold, such as the release a restore
// or an upgrade runs when its database's container was removed.
func (e *Engine) ensureImage(ctx context.Context, image string) error {
	_, err := e.Docker.ImageInspect(ctx, image)
	if errdefs.IsNotFound(err) {
		return e.pullImageAuth(ctx, image, "")
	}
	return err
}

// pg_restore prints the versions of the server that was dumped and of the pg_dump that dumped it as
// comments in the SQL preamble. An archive made by an earlier minor, or by the upgrade from 17,
// names other versions than its verification dump, with the same definitions and data. Only those
// two comment lines of the bounded preamble are normalized; COPY data is never touched.
type dumpVersionHeader struct {
	out     io.Writer
	pending []byte
	done    bool
}

var dumpVersionLine = regexp.MustCompile(`(?m)^-- Dumped (from database|by pg_dump) version [0-9][0-9.]*(?: \([^)\n]*\))?$`)

func (w *dumpVersionHeader) Write(b []byte) (int, error) {
	n := len(b)
	if w.done {
		return w.out.Write(b)
	}
	w.pending = append(w.pending, b...)
	if i := bytes.Index(w.pending, []byte("\nSET ")); i >= 0 {
		header := dumpVersionLine.ReplaceAll(w.pending[:i], []byte("-- Dumped $1 version (normalized)"))
		if _, err := w.out.Write(header); err != nil {
			return 0, err
		}
		if _, err := w.out.Write(w.pending[i:]); err != nil {
			return 0, err
		}
		w.pending = nil
		w.done = true
	} else if len(w.pending) > 8192 {
		return 0, fmt.Errorf("unexpected archive preamble")
	}
	return n, nil
}
