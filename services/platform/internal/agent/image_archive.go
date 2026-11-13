package agent

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
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
	// Files a load may create for each GiB it may write: 1 Mi for the default 8 GiB.
	imageLoadEntriesPerGiB = 128 << 10
	dockerReserveInodes    = 1 << 18
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

// imageLoadRefused is why the agent did not load a built image; its message is the operation's note.
type imageLoadRefused struct{ note string }

func (r *imageLoadRefused) Error() string { return r.note }

// refusal is the note for exceeding the limit in over, naming that limit and who can change it.
func (l imageLoadLimit) refusal(over error) error {
	const tail = " Existing workloads are unchanged."
	switch {
	case errors.Is(over, errImageOverBytes) && l.bytesByStorage:
		return &imageLoadRefused{fmt.Sprintf("The built image was not loaded: unpacking it would leave less than %d GiB free in this server's Docker storage, which the agent keeps in reserve. Free space on the server and build again; the server's owner sets the reserve with the agent's --image-load-reserve-gib.", l.reserveBytes>>30) + tail}
	case errors.Is(over, errImageOverBytes):
		return &imageLoadRefused{fmt.Sprintf("The built image was not loaded: unpacked, it would write more than %d GiB to this server's Docker storage, the most one image load may write here. Make the image smaller, for example with a multi-stage build, or ask the server's owner to raise the agent's --image-load-gib.", l.loadBytes>>30) + tail}
	case l.entriesByStorage:
		return &imageLoadRefused{fmt.Sprintf("The built image was not loaded: unpacking it would leave fewer than %d inodes free in this server's Docker storage. Free space on the server, such as unused images, and build again.", dockerReserveInodes) + tail}
	}
	return &imageLoadRefused{fmt.Sprintf("The built image was not loaded: unpacked, it would create more than %d files and directories, the most one image load may create here (%d for each GiB of the agent's --image-load-gib). Make the image smaller, or ask the server's owner to raise that setting.", l.Entries, imageLoadEntriesPerGiB) + tail}
}

func unmeasurableImage(why string) error {
	return &imageLoadRefused{"The built image was not loaded: " + why + ", which the agent cannot measure safely. Existing workloads are unchanged."}
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
		g.failed = err
	}
	return n, err
}

func (g *imageArchiveGate) release() error {
	if len(g.pending) == 0 {
		return nil
	}
	_, err := g.out.Write(g.pending)
	g.pending = g.pending[:0]
	if err != nil {
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
	case errors.As(err, &refused), errors.Is(err, errImageOverBytes), errors.Is(err, errImageOverEntries):
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

const tarBlockBytes = 512

// boundedReader counts what it reads and fails past ceiling, so a parser cannot read without
// limit before returning anything to count.
type boundedReader struct {
	r          io.Reader
	n, ceiling int64
	over       error
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.n >= b.ceiling {
		return 0, b.over
	}
	n, err := b.r.Read(p[:min(int64(len(p)), b.ceiling-b.n)])
	b.n += int64(n)
	return n, err
}

// layerCost counts what unpacking the tar archive r writes, and how many headers it has.
func layerCost(r io.Reader, limit imageLoadLimit) (imageCost, int, error) {
	raw := &boundedReader{r: r, over: errImageOverBytes}
	tr := tar.NewReader(raw)
	directories := layerDirectories{}
	var cost imageCost
	headers := 0
	for {
		// Docker's legacy store keeps every byte between entries' data: headers, including meta
		// headers Go's reader folds into the next entry, long names and padding.
		before := raw.n
		raw.ceiling = before + limit.Bytes - cost.Bytes
		hdr, err := tr.Next()
		cost.Bytes += raw.n - before
		if err == io.EOF {
			break
		}
		if err != nil {
			return cost, headers, err
		}
		headers++
		entry, err := directories.entry(hdr)
		if err != nil {
			return cost, headers, err
		}
		cost.add(entry)
		if err = limit.check(cost); err != nil {
			return cost, headers, err
		}
		// The entry's data is read here, not in Next, so Next reads only what is counted above.
		// It is at most the entry's counted size.
		raw.ceiling = math.MaxInt64
		if _, err = io.Copy(io.Discard, tr); err != nil {
			return cost, headers, err
		}
	}
	// Docker's legacy image store reads what follows the end marker into memory and keeps it.
	raw.ceiling, raw.over = raw.n+layerTrailerBytes+1, io.EOF
	n, err := io.Copy(io.Discard, raw)
	if err != nil {
		return cost, headers, err
	}
	if n > layerTrailerBytes {
		return cost, headers, unmeasurableImage("a layer has data after its end")
	}
	cost.Bytes += n
	return cost, headers, limit.check(cost)
}

// layerDirectories is the directories one layer has made, explicitly or as the parents Docker
// creates for an entry whose directory the layer does not have.
type layerDirectories map[layerPathKey]int32

type layerPathKey struct {
	parent int32
	name   string
}

var directoryCost = imageCost{Bytes: storageEntryBytes + storageBlockBytes, Entries: 1}

// entry is what writing one tar entry costs, including directories it needs Docker to create.
func (d layerDirectories) entry(h *tar.Header) (imageCost, error) {
	if len(h.Name) > layerPathBytes || len(h.Linkname) > layerPathBytes {
		return imageCost{}, unmeasurableImage("a layer has a path longer than Linux allows")
	}
	parts := strings.Split(strings.TrimPrefix(path.Clean("/"+h.Name), "/"), "/")
	cost := entryCost(h)
	parent := int32(0)
	for i, part := range parts {
		if len(part) > layerNameBytes {
			return imageCost{}, unmeasurableImage("a layer has a file name longer than Linux allows")
		}
		if i == len(parts)-1 && h.Typeflag != tar.TypeDir {
			break
		}
		key := layerPathKey{parent, part}
		id, ok := d[key]
		if !ok {
			id = int32(len(d) + 1)
			d[layerPathKey{parent, strings.Clone(part)}] = id
			if i < len(parts)-1 {
				cost.add(directoryCost)
			}
		}
		parent = id
	}
	return cost, nil
}

// entryCost is the storage one tar entry takes once written. Its size is the logical one, so a
// sparse file counts in full, as Docker writes it.
func entryCost(h *tar.Header) imageCost {
	bytes := int64(storageEntryBytes)
	xattrs := false
	for k := range h.PAXRecords {
		xattrs = xattrs || strings.HasPrefix(k, "SCHILY.xattr.") || strings.HasPrefix(k, "LIBARCHIVE.xattr.")
	}
	if xattrs {
		bytes += storageBlockBytes
	}
	if h.Typeflag == tar.TypeDir {
		bytes += storageBlockBytes
	}
	size := min(max(h.Size, 0), 1<<60)
	return imageCost{Bytes: bytes + (size+storageBlockBytes-1)/storageBlockBytes*storageBlockBytes, Entries: 1}
}

// compressionOf names the compression Docker detects from a stream's first bytes, which is
// gzip, zstd (also behind a skippable frame), bzip2 or xz, or "" for none.
func compressionOf(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return "gzip"
	case bytes.HasPrefix(head, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return "zstd"
	case len(head) >= 4 && binary.LittleEndian.Uint32(head)&0xfffffff0 == 0x184d2a50:
		return "zstd"
	case bytes.HasPrefix(head, []byte("BZh")):
		return "bzip2"
	case bytes.HasPrefix(head, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}):
		return "xz"
	}
	return ""
}

// plainTarHeader reports whether Docker's tar reader accepts head's first block as a header.
func plainTarHeader(head []byte) bool {
	if len(head) < tarBlockBytes {
		return false
	}
	_, err := tar.NewReader(bytes.NewReader(head[:tarBlockBytes])).Next()
	return !errors.Is(err, tar.ErrHeader)
}

// imageChainsCost counts the layers Docker unpacks for every image the archive's manifests
// describe. Docker's legacy store reads manifest.json and the containerd store also index.json,
// so both count. Docker unpacks a layer once for each distinct chain of layers below and
// including it, so a layer listed twice, or below different layers, counts each time, as does
// each layer's own bookkeeping.
func imageChainsCost(members map[string]*imageMember, limit imageLoadLimit) (imageCost, error) {
	var chains [][]string
	if m := members["manifest.json"]; m != nil {
		var images []jsontext.Value
		if m.data == nil || json.Unmarshal(m.data, &images) != nil {
			return imageCost{}, unmeasurableImage("its manifest.json is not valid")
		}
		for _, image := range images {
			lists, err := foldedMembers(image, "Layers")
			if err != nil {
				return imageCost{}, err
			}
			for _, list := range lists {
				var layers []string
				if json.Unmarshal(list, &layers) != nil {
					return imageCost{}, unmeasurableImage("its manifest.json is not valid")
				}
				chain := make([]string, len(layers))
				for i, layer := range layers {
					// Docker's legacy store resolves each path inside the archive.
					chain[i] = strings.TrimPrefix(path.Clean("/"+layer), "/")
				}
				chains = append(chains, chain)
			}
		}
	}
	if members["index.json"] != nil {
		visited := map[string]bool{}
		if err := indexChains(members, "index.json", 0, visited, &chains); err != nil {
			return imageCost{}, err
		}
	}
	type node struct {
		parent int32
		name   string
	}
	nodes := map[node]int32{}
	var cost imageCost
	for _, chain := range chains {
		if len(chain) > imageChainDepth {
			return imageCost{}, unmeasurableImage("an image in its archive has more layers than Docker can stack")
		}
		parent := int32(0)
		for _, name := range chain {
			m := members[name]
			if m == nil {
				return imageCost{}, unmeasurableImage("its manifests name a layer its archive does not have")
			}
			key := node{parent, name}
			id, ok := nodes[key]
			if !ok {
				id = int32(len(nodes) + 1)
				nodes[key] = id
				cost.add(m.cost)
				cost.add(imageCost{Bytes: storageLayerBytes, Entries: storageLayerEntries})
				if err := limit.check(cost); err != nil {
					return cost, err
				}
			}
			parent = id
		}
	}
	return cost, nil
}

// indexChains follows an OCI index or manifest to the layer chains it describes. It reads
// each document's manifests and layers whatever its media type says, as either may be used.
func indexChains(members map[string]*imageMember, name string, depth int, visited map[string]bool, chains *[][]string) error {
	if visited[name] {
		return nil
	}
	visited[name] = true
	m := members[name]
	switch {
	case m == nil:
		return unmeasurableImage("its image index names a document its archive does not have")
	case m.data == nil:
		return unmeasurableImage("its image index names a document the agent cannot read")
	case depth > imageIndexDepth:
		return unmeasurableImage("its image index is nested too deeply")
	}
	manifests, err := foldedMembers(m.data, "manifests")
	if err != nil {
		return err
	}
	for _, list := range manifests {
		blobs, err := descriptorBlobs(list)
		if err != nil {
			return err
		}
		for _, blob := range blobs {
			if err = indexChains(members, blob, depth+1, visited, chains); err != nil {
				return err
			}
		}
	}
	layers, err := foldedMembers(m.data, "layers")
	if err != nil {
		return err
	}
	for _, list := range layers {
		chain, err := descriptorBlobs(list)
		if err != nil {
			return err
		}
		*chains = append(*chains, chain)
	}
	return nil
}

// descriptorBlobs names the archive members a list of OCI descriptors points at.
func descriptorBlobs(list jsontext.Value) ([]string, error) {
	var descriptors []jsontext.Value
	if json.Unmarshal(list, &descriptors) != nil {
		return nil, unmeasurableImage("its image index is not valid")
	}
	blobs := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		digests, err := foldedMembers(descriptor, "digest")
		if err != nil {
			return nil, err
		}
		var digest string
		if len(digests) != 1 || json.Unmarshal(digests[0], &digest) != nil {
			return nil, unmeasurableImage("its image index is not valid")
		}
		algorithm, encoded, _ := strings.Cut(digest, ":")
		blobs = append(blobs, "blobs/"+algorithm+"/"+encoded)
	}
	return blobs, nil
}

// foldedMembers returns the values of an object's members named name, ignoring case as Docker's
// and containerd's JSON decoding does.
func foldedMembers(raw []byte, name string) ([]jsontext.Value, error) {
	var object map[string]jsontext.Value
	if json.Unmarshal(raw, &object) != nil {
		return nil, unmeasurableImage("its image metadata is not valid JSON")
	}
	var values []jsontext.Value
	for key, value := range object {
		if strings.EqualFold(key, name) && string(value) != "null" {
			values = append(values, value)
		}
	}
	return values, nil
}
