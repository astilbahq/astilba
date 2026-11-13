package agent

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
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
		raw.ceiling, raw.over = before+limit.Bytes-cost.Bytes, errImageOverBytes
		if limit.Bytes-cost.Bytes > layerEntryHeaderBytes {
			raw.ceiling, raw.over = before+layerEntryHeaderBytes, errLayerHeaders
		}
		hdr, err := tr.Next()
		cost.Bytes += raw.n - before
		if err == io.EOF {
			break
		}
		if err != nil {
			return cost, headers, err
		}
		headers++
		if err = usualEntry(hdr); err != nil {
			return cost, headers, err
		}
		// Until the layer is unpacked, Docker holds each entry's name in two maps, and a
		// directory's whole header. A name from a PAX record keeps the record's block in memory.
		metadata += tarEntryMemory + 2*int64(len(hdr.Name)+len(hdr.Linkname))
		switch {
		case hdr.Typeflag == tar.TypeDir:
			// Go keeps each record in a map, an extended attribute in a second one too.
			metadata += raw.n - before + tarHeaderMemory + 2*tarEntryMemory*int64(len(hdr.PAXRecords))
		case hdr.PAXRecords["path"] != "" || hdr.PAXRecords["linkpath"] != "":
			metadata += raw.n - before
		}
		if metadata > layerMetadataBytes {
			return cost, headers, &layerMetadataRefused{metadata}
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

// layerMetadataRefused is a layer with more names and headers than Docker should hold in memory
// while unpacking it. The archive's inspection names the layer in the note.
type layerMetadataRefused struct{ metadata int64 }

func (e *layerMetadataRefused) Error() string { return "a layer has too much file metadata" }

func (e *layerMetadataRefused) note(layer string) error {
	return &imageLoadRefused{fmt.Sprintf("The built image was not loaded: its layer %s has at least %d MiB of file names and directory headers, which Docker holds in memory while unpacking it, and the limit is %d MiB a layer. Split large copies, such as dependency directories, across several layers, or build to a registry instead. Existing workloads are unchanged.", truncated(layer, 19), e.metadata>>20, layerMetadataBytes>>20)}
}

// errLayerHeaders refuses an entry with more header data than any image needs, which also
// bounds the memory Docker holds for it.
var errLayerHeaders = unmeasurableImage("a layer has a file with more than 64 KiB of tar headers")

// usualEntry refuses tar features images do not use, rather than measure what Docker does with
// them: entry types other than files, links, directories, devices and pipes, sparse files, PAX
// records other than names, owners, times, sizes and extended attributes, more than a few of
// them, and AUFS's metadata.
func usualEntry(h *tar.Header) error {
	const subject = "a layer"
	switch h.Typeflag {
	case tar.TypeReg, tar.TypeLink, tar.TypeSymlink, tar.TypeChar, tar.TypeBlock, tar.TypeDir, tar.TypeFifo:
	default:
		return unmeasurableImage(fmt.Sprintf("%s has a tar entry of type %q, which images do not use", subject, h.Typeflag))
	}
	if len(h.PAXRecords) > layerPAXRecords {
		return unmeasurableImage(fmt.Sprintf("%s has a file with more than %d PAX records, which images do not need", subject, layerPAXRecords))
	}
	for k := range h.PAXRecords {
		switch {
		case usualPAXRecords[k], strings.HasPrefix(k, "SCHILY.xattr."), strings.HasPrefix(k, "LIBARCHIVE.xattr."):
		case strings.HasPrefix(k, "GNU.sparse."):
			return unmeasurableImage(subject + " has a sparse file, which images do not use")
		default:
			return unmeasurableImage(fmt.Sprintf("%s has the PAX record %q, which images do not need", subject, truncated(k, 64)))
		}
	}
	// Docker's legacy store keeps the headers of AUFS's hard link directory in memory.
	for _, part := range strings.Split(path.Clean("/"+h.Name), "/") {
		if strings.HasPrefix(part, ".wh..wh.") && part != ".wh..wh..opq" {
			return unmeasurableImage(subject + " has AUFS metadata, which images do not use")
		}
	}
	return nil
}

func truncated(s string, n int) string {
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "") + "..."
	}
	return s
}

var usualPAXRecords = map[string]bool{"path": true, "linkpath": true, "size": true, "uid": true, "gid": true, "uname": true, "gname": true, "mtime": true, "atime": true, "ctime": true}

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
		case ok && last && existing.directory != (h.Typeflag == tar.TypeDir):
			// A file written again is counted twice; a directory turned into a file or link,
			// or the reverse, would move what the layer has already written under it.
			return imageCost{}, unmeasurableImage("a layer replaces a directory it writes with a file or link, or the reverse")
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

// entryCost is the storage one tar entry takes once written. Docker's legacy store also records
// each entry, its name again included, in the layer's metadata.
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
