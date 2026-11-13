package agent

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
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
	// What one load may write to Docker's storage: archive members and unpacked layers.
	localImageLoadBytes   = 4 << 30
	localImageLoadEntries = 1 << 20
	// What a load must leave free where Docker keeps images.
	dockerReserveBytes  = 4 << 30
	dockerReserveInodes = 1 << 18
	// Small non-layer members are kept to read the manifests that name layers.
	imageMetadataBytes = 4 << 20
	imageMetadataTotal = 16 << 20
	// A layer may end with padding after its end marker, which Docker's legacy image store
	// reads into memory and keeps.
	layerTrailerBytes = 1 << 20
	// zstd's largest standard window, which the zstd tool uses for --ultra and --long.
	layerWindowBytes = 128 << 20
	imageIndexDepth  = 4
	// Each entry costs its data in whole filesystem blocks plus an inode, a directory entry and
	// layer metadata, and a directory at least one block.
	storageBlockBytes = 4096
	storageEntryBytes = 512
)

// localImageLoads lets one local image load at a time take Docker's storage, so the free space
// each measures is not also spent by another.
var localImageLoads = make(chan struct{}, 1)

// imageLoadLimit is what one load may still write.
type imageLoadLimit struct {
	Bytes, Entries int64
	// Storage is set when the server's free space, not the agent's fixed limit, binds.
	Storage bool
}

type imageCost struct{ Bytes, Entries int64 }

func (c *imageCost) add(o imageCost) { c.Bytes += o.Bytes; c.Entries += o.Entries }

func (c imageCost) exceeds(l imageLoadLimit) bool {
	return c.Bytes > l.Bytes || c.Entries > l.Entries
}

func (l imageLoadLimit) less(c imageCost) imageLoadLimit {
	return imageLoadLimit{Bytes: l.Bytes - c.Bytes, Entries: l.Entries - c.Entries, Storage: l.Storage}
}

// imageLoadRefused is why the agent did not load a built image; its message is the operation's note.
type imageLoadRefused struct{ note string }

func (r *imageLoadRefused) Error() string { return r.note }

var errImageOverLimit = errors.New("image exceeds what its load may write")

func (l imageLoadLimit) refusal() error {
	if l.Storage {
		return &imageLoadRefused{fmt.Sprintf("The built image was not loaded: unpacking it would leave less than %d GiB or %d inodes free in this server's Docker storage. Free space on the server and build again. Existing workloads are unchanged.", dockerReserveBytes>>30, dockerReserveInodes)}
	}
	return &imageLoadRefused{fmt.Sprintf("The built image was not loaded: unpacked, it would take more than %d GiB of this server's Docker storage or more than %d files. Make the image smaller, for example with a multi-stage build. Existing workloads are unchanged.", localImageLoadBytes>>30, localImageLoadEntries)}
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
}

func (g *imageArchiveGate) Read(p []byte) (int, error) {
	n, err := g.src.Read(p)
	if len(g.head) < 8 {
		g.head = append(g.head, p[:min(n, 8-len(g.head))]...)
	}
	g.pending = append(g.pending, p[:n]...)
	return n, err
}

func (g *imageArchiveGate) release() error {
	if len(g.pending) == 0 {
		return nil
	}
	_, err := g.out.Write(g.pending)
	g.pending = g.pending[:0]
	return err
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

var imageMemberName = regexp.MustCompile(`^(blobs/sha256/[0-9a-f]{64}|index\.json|manifest\.json|oci-layout)$`)

type imageMember struct {
	layer bool
	cost  imageCost
	data  []byte
}

// inspectImageArchive reads the archive through gate and returns nil only once the whole archive
// has reached gate.out. On an error the caller must fail the stream Docker reads.
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
			return unmeasurableImage("its archive is not a valid tar archive")
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		switch {
		case name != path.Clean(name) || members[name] != nil || directories[name]:
			return unmeasurableImage("its archive names a member twice or in an unusual form")
		case hdr.Typeflag == tar.TypeDir && (name == "blobs" || name == "blobs/sha256"):
			directories[name] = true
		case hdr.Typeflag == tar.TypeReg && hdr.Name == name && imageMemberName.MatchString(name):
		default:
			return unmeasurableImage("its archive has a member other than OCI image content")
		}
		cost := entryCost(hdr)
		archive.add(cost)
		read.add(cost)
		if read.exceeds(limit) {
			return errImageOverLimit
		}
		if err = gate.release(); err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		member, err := inspectImageMember(releasing{tr, gate}, hdr.Size, limit.less(read))
		if err != nil {
			return err
		}
		read.add(member.cost)
		if kept += len(member.data); kept > imageMetadataTotal {
			return unmeasurableImage("its archive has more metadata than an image needs")
		}
		members[name] = member
	}
	unpacked, err := imageChainsCost(members)
	if err != nil {
		return err
	}
	archive.add(unpacked)
	if archive.exceeds(limit) {
		return errImageOverLimit
	}
	if err = gate.release(); err != nil {
		return err
	}
	// Only padding may follow the end marker; Docker needs it no more than the agent does.
	buffer := make([]byte, 32<<10)
	for {
		n, err := gate.src.Read(buffer)
		if bytes.ContainsFunc(buffer[:n], func(r rune) bool { return r != 0 }) {
			return unmeasurableImage("its archive has data after its end")
		}
		if _, written := gate.out.Write(buffer[:n]); written != nil {
			return written
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// inspectImageMember counts what Docker writes if it unpacks the member as a layer. Docker
// recognizes a compressed layer by its magic number, or reads it as a plain tar archive; the
// containerd image store also reads a layer described as uncompressed as plain tar, whatever it
// starts with. A member that is no tar archive either way unpacks to nothing.
func inspectImageMember(r io.Reader, size int64, limit imageLoadLimit) (*imageMember, error) {
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
			return nil, unmeasurableImage("a layer's " + compression + " data is invalid")
		}
		member.cost, _, err = layerCost(layer, limit)
		var refused *imageLoadRefused
		if errors.Is(err, errImageOverLimit) || errors.As(err, &refused) {
			return nil, err
		}
		if err != nil {
			return nil, unmeasurableImage("a compressed layer is not a valid tar archive")
		}
		member.layer = true
	case "":
		var layer io.Reader = buffered
		if size <= imageMetadataBytes {
			if member.data, err = io.ReadAll(buffered); err != nil {
				return nil, err
			}
			layer = bytes.NewReader(member.data)
		}
		var headers int
		member.cost, headers, err = layerCost(layer, limit)
		switch {
		case errors.Is(err, errImageOverLimit):
			return nil, err
		case headers == 0 && (errors.Is(err, tar.ErrHeader) || errors.Is(err, io.ErrUnexpectedEOF)):
			// Not a tar archive: Docker's tar reader fails at the same first header.
			member.cost = imageCost{}
		case err != nil:
			return nil, unmeasurableImage("a layer is not a valid tar archive")
		default:
			member.layer, member.data = true, nil
		}
	default:
		return nil, unmeasurableImage("a layer uses " + compression + " compression")
	}
	// Whatever is left is read through, so the archive's next header is what Docker gets next.
	if _, err = io.Copy(io.Discard, buffered); err != nil {
		return nil, err
	}
	return member, nil
}

const tarBlockBytes = 512

// layerCost counts what unpacking the tar archive r writes, and how many headers it has.
func layerCost(r io.Reader, limit imageLoadLimit) (imageCost, int, error) {
	tr := tar.NewReader(r)
	var cost imageCost
	headers := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return cost, headers, err
		}
		headers++
		cost.add(entryCost(hdr))
		if cost.exceeds(limit) {
			return cost, headers, errImageOverLimit
		}
	}
	// Docker's legacy image store reads what follows the end marker into memory and keeps it.
	n, err := io.Copy(io.Discard, io.LimitReader(r, layerTrailerBytes+1))
	if err != nil {
		return cost, headers, err
	}
	if n > layerTrailerBytes {
		return cost, headers, unmeasurableImage("a layer has data after its end")
	}
	cost.Bytes += n
	if cost.exceeds(limit) {
		return cost, headers, errImageOverLimit
	}
	return cost, headers, nil
}

// entryCost is the storage one tar entry takes once written. Its size is the logical one, so a
// sparse file counts in full, as Docker writes it.
func entryCost(h *tar.Header) imageCost {
	bytes := int64(storageEntryBytes + len(h.Name) + len(h.Linkname))
	for k, v := range h.PAXRecords {
		bytes += int64(len(k) + len(v))
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
// including it, so a layer listed twice, or below different layers, counts each time.
func imageChainsCost(members map[string]*imageMember) (imageCost, error) {
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
	type node map[string]node
	root := node{}
	var cost imageCost
	for _, chain := range chains {
		at := root
		for _, name := range chain {
			next, ok := at[name]
			if !ok {
				next = node{}
				at[name] = next
				if m := members[name]; m != nil && m.layer {
					cost.add(m.cost)
				}
			}
			at = next
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
	if m == nil || m.data == nil {
		// Docker cannot read a missing or non-JSON document as a manifest either.
		return nil
	}
	if depth > imageIndexDepth {
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
