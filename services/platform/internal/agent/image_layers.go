package agent

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"errors"
	"hash/maphash"
	"io"
	"math"
	"path"
	"strings"
)

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
	paths := layerPaths{seed: maphash.MakeSeed(), ids: map[uint64]layerPath{}}
	var cost imageCost
	var metadata int64
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
		// Docker holds each entry's name, and a directory's whole header, in memory until the
		// layer is unpacked.
		metadata += int64(len(hdr.Name) + len(hdr.Linkname))
		for k, v := range hdr.PAXRecords {
			metadata += int64(len(k) + len(v))
		}
		if metadata > layerMetadataBytes {
			return cost, headers, unmeasurableImage("a layer has more file metadata than Docker can hold while unpacking it")
		}
		entry, err := paths.entry(hdr)
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

// layerPaths is every path one layer writes, by its parent's id and its name, whether as an
// entry or as a parent directory Docker creates for one. Keys are hashed with a random seed, so
// memory per path is fixed and no archive can aim a collision.
type layerPaths struct {
	seed maphash.Seed
	ids  map[uint64]layerPath
}

type layerPath struct {
	id        uint32
	directory bool
}

var directoryCost = imageCost{Bytes: storageEntryBytes + storageBlockBytes, Entries: 1}

func (l layerPaths) key(parent uint32, name string) uint64 {
	var h maphash.Hash
	h.SetSeed(l.seed)
	_ = binary.Write(&h, binary.LittleEndian, parent)
	_, _ = h.WriteString(name)
	return h.Sum64()
}

// entry is what writing one tar entry costs, including directories it needs Docker to create.
// Docker resolves an entry's parents through links the same layer writes, so a layer that writes
// through its own link or file, or writes one path twice, is refused rather than measured.
func (l layerPaths) entry(h *tar.Header) (imageCost, error) {
	name := path.Clean("/" + h.Name)
	// A whiteout names a file it removes, and Docker stores it under that file's name.
	stored := strings.TrimPrefix(path.Base(name), ".wh.")
	if len(h.Name)-(len(path.Base(name))-len(stored)) > layerPathBytes || len(h.Linkname) > layerPathBytes {
		return imageCost{}, unmeasurableImage("a layer has a path longer than Linux allows")
	}
	parts := strings.Split(strings.TrimPrefix(name, "/"), "/")
	cost := entryCost(h)
	parent := uint32(0)
	for i, part := range parts {
		last := i == len(parts)-1
		if !last && len(part) > layerNameBytes || last && len(stored) > layerNameBytes {
			return imageCost{}, unmeasurableImage("a layer has a file name longer than Linux allows")
		}
		key := l.key(parent, part)
		existing, ok := l.ids[key]
		switch {
		case ok && !last && !existing.directory:
			return imageCost{}, unmeasurableImage("a layer writes through a link or file it also writes")
		case ok && last && !(existing.directory && h.Typeflag == tar.TypeDir):
			return imageCost{}, unmeasurableImage("a layer writes the same path twice")
		case !ok:
			existing = layerPath{id: uint32(len(l.ids) + 1), directory: !last || h.Typeflag == tar.TypeDir}
			l.ids[key] = existing
			if !last {
				cost.add(directoryCost)
			}
		}
		parent = existing.id
	}
	return cost, nil
}

// entryCost is the storage one tar entry takes once written. Its size is the logical one, so a
// sparse file counts in full, as Docker writes it. Docker's legacy store also records each
// entry, its name again included, in the layer's metadata.
func entryCost(h *tar.Header) imageCost {
	bytes := int64(storageEntryBytes + 2*(len(h.Name)+len(h.Linkname)))
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
