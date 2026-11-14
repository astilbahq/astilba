package agent

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// A local build's image reaches Docker as an archive the build container wrote, so anything
// the build ran may have shaped it. Docker unpacks every layer onto its own storage, where the
// archive's size says little: a sparse file, a highly compressed layer, millions of empty
// files or a layer listed many times unpack to far more than the archive holds. The agent
// therefore reads the archive as Docker will, counts what loading it writes, and passes it to
// Docker through a gate that refuses before Docker could write more than the limit.
const (
	localImageArchiveBytes = 512 << 20
	// What one load may write to Docker's storage, and what it must leave free there, unless the
	// host's owner sets the agent's --image-load-gib and --image-load-reserve-gib.
	DefaultImageLoadBytes        = 8 << 30
	DefaultImageLoadReserveBytes = 4 << 30
	// Files and directories a load may create, which also bounds the agent's memory per layer.
	localImageLoadEntries = 1 << 20
	dockerReserveInodes   = 1 << 18
	// Names and directory headers in one layer, which Docker holds in memory while unpacking it.
	layerMetadataBytes = 128 << 20
	// What Go's tar reader holds for a header besides its names and records, and what Docker's
	// maps of the paths it has unpacked hold for each entry besides its name.
	tarHeaderMemory = 256
	tarEntryMemory  = 128
	// PAX records one file may carry: a few names, owners and times, and its extended attributes.
	layerPAXRecords = 16
	// One entry's headers: its own, one PAX block of at most 64 KiB, and the padding before them.
	layerEntryHeaderBytes = 64<<10 + 3*tarBlockBytes
	// An image has a config, a manifest and a blob per layer; this many members is never one.
	imageMembers = 4096
	// Header blocks, PAX records and long names the gate holds before a member's size is known.
	imageHeaderBytes = 4 << 20
	// Small non-layer members are kept to read the manifests that name layers.
	imageMetadataBytes = 4 << 20
	imageMetadataTotal = 16 << 20
	// Docker's overlay storage stacks at most this many layers.
	imageChainDepth = 128
	imageIndexDepth = 4
	// A layer may end with padding after its end marker, which Docker's legacy image store
	// reads into memory and keeps.
	layerTrailerBytes = 1 << 20
	// zstd's largest standard window, which the zstd tool uses for --ultra and --long.
	layerWindowBytes = 128 << 20
	// Linux refuses longer paths and names, so a layer with them cannot unpack.
	layerPathBytes = 4096
	layerNameBytes = 255
	// Each entry costs its data in whole filesystem blocks plus an inode and a directory entry;
	// a directory or an out-of-inode extended attribute takes a block more.
	storageBlockBytes = 4096
	storageEntryBytes = 512
	// Each layer Docker unpacks has its own directories and bookkeeping files.
	storageLayerBytes   = 64 << 10
	storageLayerEntries = 16
)

// imageLoadLimit is what one load may still write.
type imageLoadLimit struct {
	Bytes, Entries int64
	// Whether the server's free space, rather than the per-load limit, binds each.
	bytesByStorage, entriesByStorage bool
	// The host's settings, which the refusal notes name.
	loadBytes, reserveBytes int64
}

type imageCost struct{ Bytes, Entries int64 }

func (c *imageCost) add(o imageCost) { c.Bytes += o.Bytes; c.Entries += o.Entries }

var (
	errImageOverBytes   = errors.New("image writes more than its load may")
	errImageOverEntries = errors.New("image creates more files than its load may")
)

func (l imageLoadLimit) check(c imageCost) error {
	switch {
	case c.Bytes > l.Bytes:
		return errImageOverBytes
	case c.Entries > l.Entries:
		return errImageOverEntries
	}
	return nil
}

func (l imageLoadLimit) less(c imageCost) imageLoadLimit {
	l.Bytes -= c.Bytes
	l.Entries -= c.Entries
	return l
}

// imageLoadRefused is why the agent did not load or pull an image. Its Error is the note for a
// local build's load; pulled renders the note for a pull.
type imageLoadRefused struct {
	// why completes "<image> was not loaded: ". fix, which may be empty, follows it; {retry}
	// in it names what to do again.
	why, fix string
}

func (r *imageLoadRefused) Error() string {
	return r.render("The built image", "loaded", "build again")
}

// pulled is the note for refusing to pull image.
func (r *imageLoadRefused) pulled(image string) string {
	return r.render("The image "+truncated(image, 300), "pulled", "deploy again")
}

func (r *imageLoadRefused) render(subject, verb, retry string) string {
	note := subject + " was not " + verb + ": " + r.why + "."
	if r.fix != "" {
		note += " " + strings.ReplaceAll(r.fix, "{retry}", retry)
	}
	return note + " Existing workloads are unchanged."
}

// refusal is the note for exceeding the limit in over, naming that limit and who can change it.
func (l imageLoadLimit) refusal(over error) error {
	switch {
	case errors.Is(over, errImageOverBytes) && l.bytesByStorage:
		return &imageLoadRefused{fmt.Sprintf("unpacking it would leave less than %d GiB free in this server's Docker storage, which the agent keeps in reserve", l.reserveBytes>>30), "The agent counts every layer of the image, even those Docker already has, so a nearly full server can refuse even a small update. Free space on the server and {retry}; the server's owner sets the reserve with the agent's --image-load-reserve-gib."}
	case errors.Is(over, errImageOverBytes):
		return &imageLoadRefused{fmt.Sprintf("unpacked, it would write more than %d GiB to this server's Docker storage, the most one image may write here", l.loadBytes>>30), "Make the image smaller, for example with a multi-stage build, or ask the server's owner to raise the agent's --image-load-gib."}
	case l.entriesByStorage:
		return &imageLoadRefused{fmt.Sprintf("unpacking it would leave fewer than %d inodes free in this server's Docker storage", dockerReserveInodes), "The agent counts every layer of the image, even those Docker already has, so a nearly full server can refuse even a small update. Free space on the server, such as unused images, and {retry}."}
	}
	return &imageLoadRefused{fmt.Sprintf("unpacked, it would create more than %d files and directories, the most one image may create", localImageLoadEntries), "Make the image smaller, for example with a multi-stage build that copies only what the application needs."}
}

func unmeasurableImage(why string) error {
	return &imageLoadRefused{why: why + ", which the agent cannot measure safely"}
}

// imageArchiveGate is the archive on its way to Docker. What the inspection reads waits in
// pending until the inspection releases it, so Docker never receives a member header before
// its size is counted, nor the archive's end before the whole image is.
type imageArchiveGate struct {
	src     io.Reader
	out     io.Writer
	pending []byte
	head    []byte
	// failed is the stream's own failure, reading the archive or Docker reading it, which
	// takes precedence over what a parser makes of it.
	failed error
}

var errImageHeaders = unmeasurableImage("its archive has more header data than an image needs")

func (g *imageArchiveGate) Read(p []byte) (int, error) {
	if len(g.pending) > imageHeaderBytes {
		return 0, errImageHeaders
	}
	n, err := g.src.Read(p)
	if len(g.head) < 8 {
		g.head = append(g.head, p[:min(n, 8-len(g.head))]...)
	}
	g.pending = append(g.pending, p[:n]...)
	if err != nil && err != io.EOF {
		err = &imageArchiveUnread{err}
		if g.failed == nil {
			g.failed = err
		}
	}
	return n, err
}

// imageArchiveUnread is a failure to read the archive from the build container, as opposed to
// Docker failing to read it from the agent.
type imageArchiveUnread struct{ err error }

func (e *imageArchiveUnread) Error() string { return "reading the image archive: " + e.err.Error() }
func (e *imageArchiveUnread) Unwrap() error { return e.err }

func (g *imageArchiveGate) release() error {
	if len(g.pending) == 0 {
		return nil
	}
	_, err := g.out.Write(g.pending)
	g.pending = g.pending[:0]
	if err != nil && g.failed == nil {
		g.failed = err
	}
	return err
}

// cause is the error to report for err: the stream's own failure, a refusal or a limit as it
// is, and otherwise the refusal why, since the archive itself could not be read.
func (g *imageArchiveGate) cause(err error, why string) error {
	var refused *imageLoadRefused
	switch {
	case g.failed != nil:
		return g.failed
	case errors.As(err, &refused), errors.As(err, new(*layerMetadataRefused)), errors.Is(err, errImageOverBytes), errors.Is(err, errImageOverEntries):
		return err
	}
	return unmeasurableImage(why)
}

// releasing passes a member's content to Docker as it is read. Docker only stores members until
// the archive ends, and the member's full logical size is already counted.
type releasing struct {
	r    io.Reader
	gate *imageArchiveGate
}

func (r releasing) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if released := r.gate.release(); released != nil && err == nil {
		err = released
	}
	return n, err
}

var imageMemberName = regexp.MustCompile(`^(blobs/sha256/([0-9a-f]{64})|index\.json|manifest\.json|oci-layout)$`)

type imageMember struct {
	layer bool
	cost  imageCost
	data  []byte
}

// inspectImageArchive reads the archive through gate. It returns nil once the archive's end has
// reached gate.out, after which the caller ends the stream Docker reads; on an error the caller
// must fail that stream instead.
func inspectImageArchive(gate *imageArchiveGate, limit imageLoadLimit) error {
	tr := tar.NewReader(gate)
	var archive, read imageCost // members as Docker stores them; plus each layer once
	members := map[string]*imageMember{}
	directories := map[string]bool{}
	kept := 0
	for first := true; ; first = false {
		hdr, err := tr.Next()
		// Docker decompresses a compressed archive before reading it.
		if first && compressionOf(gate.head) != "" {
			return unmeasurableImage("its archive is compressed")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return gate.cause(err, "its archive is not a valid tar archive")
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		match := imageMemberName.FindStringSubmatch(name)
		switch {
		case name != path.Clean(name) || members[name] != nil || directories[name]:
			return unmeasurableImage("its archive names a member twice or in an unusual form")
		case len(members)+len(directories) >= imageMembers:
			return unmeasurableImage("its archive has more members than an image needs")
		case hdr.Typeflag == tar.TypeDir && (name == "blobs" || name == "blobs/sha256"):
			directories[name] = true
		case hdr.Typeflag == tar.TypeReg && hdr.Name == name && match != nil:
		default:
			return unmeasurableImage("its archive has a member other than OCI image content")
		}
		cost := entryCost(hdr)
		archive.add(cost)
		read.add(cost)
		if err = limit.check(read); err != nil {
			return err
		}
		// BuildKit names archive members in plain headers. A member with PAX or GNU headers is
		// refused, so none of their data stays behind the names Docker keeps while importing.
		if len(hdr.PAXRecords) > 0 || len(gate.pending) > 2*tarBlockBytes {
			return unmeasurableImage("its archive has extended tar headers, which BuildKit does not write")
		}
		if err = gate.release(); err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// containerd stores and finds blobs by their content's digest, so a blob is what its
		// name says or the archive is refused.
		digest := sha256.New()
		member, err := inspectImageMember(io.TeeReader(releasing{tr, gate}, digest), gate, hdr.Size, limit.less(read))
		if metadata := new(layerMetadataRefused); errors.As(err, &metadata) {
			return metadata.note("sha256:" + match[2])
		}
		if err != nil {
			return err
		}
		if match[2] != "" && hex.EncodeToString(digest.Sum(nil)) != match[2] {
			return unmeasurableImage("a blob in its archive differs from its digest")
		}
		if !member.layer {
			// Docker's legacy store writes a config both where it unpacks the archive and
			// where it keeps images.
			archive.add(cost)
			read.add(cost)
		}
		read.add(member.cost)
		if err = limit.check(read); err != nil {
			return err
		}
		if kept += len(member.data); kept > imageMetadataTotal {
			return unmeasurableImage("its archive has more metadata than an image needs")
		}
		members[name] = member
	}
	unpacked, err := imageChainsCost(members, limit.less(archive))
	if err != nil {
		return err
	}
	archive.add(unpacked)
	if err = limit.check(archive); err != nil {
		return err
	}
	// Docker needs nothing after the end marker, so nothing after it is passed on.
	return gate.release()
}

// inspectImageMember counts what Docker writes if it unpacks the member as a layer, and reads
// all of it. Docker recognizes a compressed layer by its magic number, or reads it as a plain
// tar archive; the containerd image store also reads a layer described as uncompressed as plain
// tar, whatever it starts with. A member that is no tar archive either way unpacks to nothing.
func inspectImageMember(r io.Reader, gate *imageArchiveGate, size int64, limit imageLoadLimit) (*imageMember, error) {
	member := &imageMember{}
	buffered := bufio.NewReaderSize(r, 64<<10)
	head, _ := buffered.Peek(tarBlockBytes)
	var err error
	switch compression := compressionOf(head); compression {
	case "gzip", "zstd":
		// A plain tar archive can hide in a compressed stream's header; it would unpack
		// unmeasured where the layer is described as uncompressed.
		if plainTarHeader(head) {
			return nil, unmeasurableImage("a layer reads both as compressed and as a plain tar archive")
		}
		var layer io.Reader
		if compression == "gzip" {
			layer, err = gzip.NewReader(buffered)
		} else {
			var decoder *zstd.Decoder
			decoder, err = zstd.NewReader(buffered, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(layerWindowBytes), zstd.WithDecoderMaxMemory(4<<30))
			if err == nil {
				defer decoder.Close()
				layer = decoder
			}
		}
		if err != nil {
			return nil, gate.cause(err, "a layer's "+compression+" data is invalid")
		}
		if member.cost, _, err = layerCost(layer, limit); err != nil {
			return nil, gate.cause(err, "a compressed layer is not a valid tar archive")
		}
		member.layer = true
	case "":
		var layer io.Reader = buffered
		if size <= imageMetadataBytes {
			if member.data, err = io.ReadAll(buffered); err != nil {
				return nil, gate.cause(err, "its archive is not a valid tar archive")
			}
			layer = bytes.NewReader(member.data)
		}
		var headers int
		member.cost, headers, err = layerCost(layer, limit)
		switch {
		case gate.failed != nil:
			return nil, gate.failed
		case headers == 0 && (errors.Is(err, tar.ErrHeader) || errors.Is(err, io.ErrUnexpectedEOF)):
			// Not a tar archive: Docker's tar reader fails at the same first header.
			member.cost = imageCost{}
		case err != nil:
			return nil, gate.cause(err, "a layer is not a valid tar archive")
		default:
			member.layer, member.data = true, nil
		}
	default:
		return nil, unmeasurableImage("a layer uses " + compression + " compression")
	}
	// Whatever is left is read through, so the archive's next header is what Docker gets next.
	if _, err = io.Copy(io.Discard, buffered); err != nil {
		return nil, gate.cause(err, "its archive is not a valid tar archive")
	}
	return member, nil
}
