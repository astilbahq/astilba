package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/klauspost/compress/zstd"
)

// testImage builds the archive BuildKit's docker exporter writes: blobs, then index.json,
// manifest.json and oci-layout.
type testImage struct {
	names []string
	data  map[string][]byte
}

func newTestImage() *testImage { return &testImage{data: map[string][]byte{}} }

func (m *testImage) add(name string, data []byte) {
	if _, ok := m.data[name]; !ok {
		m.names = append(m.names, name)
	}
	m.data[name] = data
}

func (m *testImage) blob(data []byte) string {
	sum := sha256.Sum256(data)
	m.add("blobs/sha256/"+hex.EncodeToString(sum[:]), data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func blobName(digest string) string { return "blobs/sha256/" + strings.TrimPrefix(digest, "sha256:") }

// image adds one image whose manifest.json and OCI manifest name the given layers. The
// descriptors claim every layer is one byte, which nothing may rely on.
func (m *testImage) image(dockerLayers, ociLayers []string) {
	config := m.blob([]byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers"}}`))
	descriptors := []string{}
	for _, layer := range ociLayers {
		descriptors = append(descriptors, fmt.Sprintf(`{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":%q,"size":1}`, layer))
	}
	manifest := m.blob(fmt.Appendf(nil, `{"schemaVersion":2,"config":{"digest":%q,"size":1},"layers":[%s]}`, config, strings.Join(descriptors, ",")))
	m.add("index.json", fmt.Appendf(nil, `{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":1}]}`, manifest))
	paths := []string{}
	for _, layer := range dockerLayers {
		paths = append(paths, strconv.Quote(blobName(layer)))
	}
	m.add("manifest.json", fmt.Appendf(nil, `[{"Config":%q,"RepoTags":["astilba-build:built"],"Layers":[%s]}]`, blobName(config), strings.Join(paths, ",")))
	m.add("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
}

func (m *testImage) archive(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for _, dir := range []string{"blobs/", "blobs/sha256/"} {
		if err := w.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range m.names {
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0444, Size: int64(len(m.data[name]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(m.data[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

type testFile struct {
	name string
	size int
}

func tarOf(t *testing.T, files ...testFile) []byte {
	t.Helper()
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for _, f := range files {
		if err := w.WriteHeader(&tar.Header{Name: f.name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(f.size)}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(make([]byte, f.size)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func gzipOf(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// tarBlock is one ustar header block, written by hand for headers Go's tar writer refuses.
func tarBlock(name string, typeflag byte, size int64) []byte {
	block := make([]byte, 512)
	copy(block, name)
	copy(block[100:], "0000644\x00")
	copy(block[108:], "0000000\x00")
	copy(block[116:], "0000000\x00")
	copy(block[124:], fmt.Sprintf("%011o\x00", size))
	copy(block[136:], "00000000000\x00")
	block[156] = typeflag
	copy(block[257:], "ustar\x0000")
	copy(block[148:], "        ")
	sum := 0
	for _, b := range block {
		sum += int(b)
	}
	copy(block[148:], fmt.Sprintf("%06o\x00 ", sum))
	return block
}

func padded(data []byte) []byte {
	return append(data, make([]byte, (512-len(data)%512)%512)...)
}

// sparseEntry is one PAX sparse file of realSize bytes, all hole: about 2 KiB that Docker
// writes as realSize zero bytes.
func sparseEntry(name string, realSize int64) []byte {
	records := ""
	for _, kv := range [][2]string{{"GNU.sparse.major", "1"}, {"GNU.sparse.minor", "0"}, {"GNU.sparse.name", name}, {"GNU.sparse.realsize", strconv.FormatInt(realSize, 10)}} {
		record := " " + kv[0] + "=" + kv[1] + "\n"
		n := len(record) + 1
		for len(strconv.Itoa(n))+len(record) != n {
			n++
		}
		records += strconv.Itoa(n) + record
	}
	var out []byte
	out = append(out, tarBlock("PaxHeaders/sparse", tar.TypeXHeader, int64(len(records)))...)
	out = append(out, padded([]byte(records))...)
	out = append(out, tarBlock("GNUSparseFile.0/sparse", tar.TypeReg, 512)...)
	return append(out, padded([]byte("0\n"))...)
}

// gzipTarPolyglot is valid gzip of a small tar archive and, read as plain tar, an 8 GiB
// sparse file hidden in gzip's extra field. The containerd store reads a layer described as
// uncompressed the second way.
func gzipTarPolyglot(t *testing.T) []byte {
	t.Helper()
	hidden := append(sparseEntry("hidden", 8<<30), make([]byte, 1024)...)
	extra := len(hidden) - 12
	copy(hidden, []byte{0x1f, 0x8b, 0x08, 0x04, 0, 0, 0, 0, 0, 0xff, byte(extra), byte(extra >> 8)})
	copy(hidden[148:156], "        ")
	sum := 0
	for _, b := range hidden[:512] {
		sum += int(b)
	}
	copy(hidden[148:], fmt.Sprintf("%06o\x00 ", sum))
	polyglot := append(hidden, gzipOf(t, tarOf(t, testFile{"app", 10}))[10:]...)
	if _, err := tar.NewReader(bytes.NewReader(polyglot)).Next(); err != nil {
		t.Fatal("fixture is not a tar archive", err)
	}
	if _, err := gzip.NewReader(bytes.NewReader(polyglot)); err != nil {
		t.Fatal("fixture is not gzip", err)
	}
	return polyglot
}

func sparseTar(realSize int64) []byte {
	return append(sparseEntry("sparse", realSize), make([]byte, 1024)...)
}

var testLimit = imageLoadLimit{Bytes: 64 << 20, Entries: 1000}

func inspectTest(archive []byte, limit imageLoadLimit) ([]byte, error) {
	var docker bytes.Buffer
	err := inspectImageArchive(&imageArchiveGate{src: bytes.NewReader(archive), out: &docker}, limit)
	return docker.Bytes(), err
}

func TestImageArchivePassesUnchanged(t *testing.T) {
	image := newTestImage()
	base := image.blob(gzipOf(t, tarOf(t, testFile{"bin/app", 300 << 10}, testFile{"etc/config", 10})))
	app := image.blob(gzipOf(t, tarOf(t, testFile{"srv/index.js", 5000})))
	image.image([]string{base, app}, []string{base, app})
	archive := append(image.archive(t), make([]byte, 4096)...)
	docker, err := inspectTest(archive, testLimit)
	if err != nil {
		t.Fatal(err)
	}
	// Docker needs nothing after the end marker, so the padding after it is not passed on.
	if !bytes.Equal(docker, archive[:len(archive)-4096]) {
		t.Fatal("Docker received an archive other than the one the build wrote")
	}
	// The same archive with zstd and plain tar layers, which base images may bring.
	image = newTestImage()
	var zstdLayer bytes.Buffer
	encoder, _ := zstd.NewWriter(&zstdLayer)
	_, _ = encoder.Write(tarOf(t, testFile{"usr/lib/library", 100 << 10}))
	_ = encoder.Close()
	zstdDigest := image.blob(zstdLayer.Bytes())
	plain := image.blob(tarOf(t, testFile{"srv/app", 1000}))
	image.image([]string{zstdDigest, plain}, []string{zstdDigest, plain})
	if _, err = inspectTest(image.archive(t), testLimit); err != nil {
		t.Fatal("zstd and uncompressed layers", err)
	}
}

// The limit holds on what Docker writes, whatever the archive's size or the sizes its
// descriptors claim, and Docker never receives the archive's end once it is exceeded.
func TestImageArchiveRefusesWhatUnpacksBeyondItsLimit(t *testing.T) {
	refused := func(t *testing.T, archive []byte, limit imageLoadLimit) []byte {
		t.Helper()
		docker, err := inspectTest(archive, limit)
		if !errors.Is(err, errImageOverBytes) && !errors.Is(err, errImageOverEntries) {
			t.Fatal("not refused for its size", err)
		}
		if len(docker) >= len(archive)-1024 {
			t.Fatal("Docker received the archive's end")
		}
		return docker
	}
	t.Run("compressed zeros", func(t *testing.T) {
		image := newTestImage()
		layer := image.blob(gzipOf(t, tarOf(t, testFile{"zeros", 96 << 20})))
		image.image([]string{layer}, []string{layer})
		archive := image.archive(t)
		if len(archive) > 1<<20 {
			t.Fatal("fixture is not small", len(archive))
		}
		refused(t, archive, testLimit)
	})
	t.Run("zstd zeros", func(t *testing.T) {
		image := newTestImage()
		var layer bytes.Buffer
		encoder, _ := zstd.NewWriter(&layer)
		_, _ = encoder.Write(tarOf(t, testFile{"zeros", 96 << 20}))
		_ = encoder.Close()
		digest := image.blob(layer.Bytes())
		image.image([]string{digest}, []string{digest})
		refused(t, image.archive(t), testLimit)
	})
	t.Run("many files", func(t *testing.T) {
		files := []testFile{}
		for i := range 1001 {
			files = append(files, testFile{fmt.Sprintf("f%d", i), 0})
		}
		image := newTestImage()
		layer := image.blob(gzipOf(t, tarOf(t, files...)))
		image.image([]string{layer}, []string{layer})
		refused(t, image.archive(t), testLimit)
	})
	t.Run("parent directories Docker creates", func(t *testing.T) {
		var out bytes.Buffer
		w := tar.NewWriter(&out)
		for _, top := range []string{"a", "b"} {
			name := top + strings.Repeat("/d", 600) + "/f"
			_ = w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644})
		}
		_ = w.Close()
		image := newTestImage()
		layer := image.blob(gzipOf(t, out.Bytes()))
		image.image([]string{layer}, []string{layer})
		refused(t, image.archive(t), testLimit)
	})
	t.Run("header bytes spread over entries", func(t *testing.T) {
		// Docker's legacy store keeps every header byte, so 900 files with a 60 KiB extended
		// attribute each count for their headers, not their few blocks.
		var out bytes.Buffer
		w := tar.NewWriter(&out)
		for i := range 900 {
			_ = w.WriteHeader(&tar.Header{Name: fmt.Sprintf("f%d", i), Typeflag: tar.TypeReg, Mode: 0644, PAXRecords: map[string]string{"SCHILY.xattr.user.big": strings.Repeat(string(rune('a'+i%26)), 60<<10)}})
		}
		_ = w.Close()
		image := newTestImage()
		layer := image.blob(gzipOf(t, out.Bytes()))
		image.image([]string{layer}, []string{layer})
		refused(t, image.archive(t), imageLoadLimit{Bytes: 32 << 20, Entries: 10000})
	})
	t.Run("the name tar-split records again", func(t *testing.T) {
		// 600 entries with 3 KiB paths: about 4 MiB of headers, but Docker's legacy store
		// writes each name a second time in the layer's metadata.
		var out bytes.Buffer
		w := tar.NewWriter(&out)
		for i := range 600 {
			_ = w.WriteHeader(&tar.Header{Name: fmt.Sprintf("%sf%03d", strings.Repeat(strings.Repeat("n", 250)+"/", 12), i), Typeflag: tar.TypeReg, Mode: 0644})
		}
		_ = w.Close()
		image := newTestImage()
		layer := image.blob(gzipOf(t, out.Bytes()))
		image.image([]string{layer}, []string{layer})
		refused(t, image.archive(t), imageLoadLimit{Bytes: 5 << 20, Entries: 100000})
	})
	layer := tarOf(t, testFile{"data", 20 << 20})
	t.Run("a layer listed again", func(t *testing.T) {
		image := newTestImage()
		digest := image.blob(gzipOf(t, layer))
		image.image([]string{digest}, []string{digest})
		if _, err := inspectTest(image.archive(t), testLimit); err != nil {
			t.Fatal("one listing", err)
		}
		image.image([]string{digest, digest, digest, digest}, []string{digest})
		refused(t, image.archive(t), testLimit)
	})
	t.Run("index and manifest naming different chains", func(t *testing.T) {
		image := newTestImage()
		first := image.blob(gzipOf(t, layer))
		second := image.blob(gzipOf(t, tarOf(t, testFile{"other", 20 << 20})))
		image.image([]string{first, second}, []string{second, first})
		refused(t, image.archive(t), testLimit)
	})
	t.Run("a manifest's layers under another name", func(t *testing.T) {
		image := newTestImage()
		digest := image.blob(gzipOf(t, layer))
		image.image([]string{digest}, []string{digest})
		// Docker's JSON decoding matches names regardless of case.
		image.add("manifest.json", fmt.Appendf(nil, `[{"layers":[%q,%q,%q,"./%s"]}]`, blobName(digest), blobName(digest), blobName(digest), blobName(digest)))
		refused(t, image.archive(t), testLimit)
	})
	t.Run("free space below the fixed limit", func(t *testing.T) {
		image := newTestImage()
		digest := image.blob(gzipOf(t, layer))
		image.image([]string{digest}, []string{digest})
		refused(t, image.archive(t), imageLoadLimit{Bytes: 16 << 20, Entries: 1000, bytesByStorage: true})
	})
}

func TestImageArchiveRefusesWhatItCannotMeasure(t *testing.T) {
	valid := func() *testImage {
		image := newTestImage()
		layer := image.blob(gzipOf(t, tarOf(t, testFile{"app", 10})))
		image.image([]string{layer}, []string{layer})
		return image
	}
	cases := map[string]func() []byte{
		"compressed archive": func() []byte { return gzipOf(t, valid().archive(t)) },
		// Docker decompresses an archive that starts like gzip; a PAX header's name can.
		"archive starting like gzip": func() []byte {
			return append(tarBlock("\x1f\x8b\x08pax", tar.TypeXHeader, 0), valid().archive(t)...)
		},
		"manifest the agent did not keep": func() []byte {
			image := valid()
			layer := image.blob(gzipOf(t, tarOf(t, testFile{"app", 10})))
			manifest := image.blob(append(fmt.Appendf(nil, `{"layers":[{"digest":%q}]}`, layer), bytes.Repeat([]byte(" "), imageMetadataBytes)...))
			image.add("index.json", fmt.Appendf(nil, `{"manifests":[{"digest":%q}]}`, manifest))
			return image.archive(t)
		},
		"blob that differs from its digest": func() []byte {
			image := valid()
			image.add("blobs/sha256/"+strings.Repeat("c", 64), gzipOf(t, tarOf(t, testFile{"app", 10})))
			return image.archive(t)
		},
		"layer missing from the archive": func() []byte {
			image := valid()
			image.image([]string{"sha256:" + strings.Repeat("d", 64)}, nil)
			return image.archive(t)
		},
		// Docker resolves a path through the layer's own links, which the agent does not follow.
		"layer writing through its own link": func() []byte {
			var out bytes.Buffer
			w := tar.NewWriter(&out)
			_ = w.WriteHeader(&tar.Header{Name: "target/", Typeflag: tar.TypeDir, Mode: 0755})
			_ = w.WriteHeader(&tar.Header{Name: "a", Typeflag: tar.TypeSymlink, Linkname: "target"})
			_ = w.WriteHeader(&tar.Header{Name: "a/x/f", Typeflag: tar.TypeReg, Mode: 0644})
			_ = w.Close()
			image := valid()
			layer := image.blob(gzipOf(t, out.Bytes()))
			image.image([]string{layer}, []string{layer})
			return image.archive(t)
		},
		"layer replacing a directory with a file": func() []byte {
			var out bytes.Buffer
			w := tar.NewWriter(&out)
			_ = w.WriteHeader(&tar.Header{Name: "a/", Typeflag: tar.TypeDir, Mode: 0755})
			_ = w.WriteHeader(&tar.Header{Name: "a", Typeflag: tar.TypeReg, Mode: 0644})
			_ = w.Close()
			image := valid()
			layer := image.blob(gzipOf(t, out.Bytes()))
			image.image([]string{layer}, []string{layer})
			return image.archive(t)
		},
		"sparse file in a layer": func() []byte {
			image := valid()
			layer := image.blob(gzipOf(t, sparseTar(8<<30)))
			image.image([]string{layer}, []string{layer})
			return image.archive(t)
		},
		"PAX record images do not use": func() []byte {
			var out bytes.Buffer
			w := tar.NewWriter(&out)
			_ = w.WriteHeader(&tar.Header{Name: "f", Typeflag: tar.TypeReg, Mode: 0644, PAXRecords: map[string]string{"comment": "c"}})
			_ = w.Close()
			image := valid()
			layer := image.blob(gzipOf(t, out.Bytes()))
			image.image([]string{layer}, []string{layer})
			return image.archive(t)
		},
		// Docker keeps every member's name while importing, with any PAX block behind it.
		"archive member with PAX headers": func() []byte {
			image := valid()
			var out bytes.Buffer
			w := tar.NewWriter(&out)
			data := []byte("x")
			sum := sha256.Sum256(data)
			_ = w.WriteHeader(&tar.Header{Name: "blobs/sha256/" + hex.EncodeToString(sum[:]), Typeflag: tar.TypeReg, Mode: 0444, Size: 1, Uname: strings.Repeat("u", 1000)})
			_, _ = w.Write(data)
			_ = w.Flush()
			return append(out.Bytes(), image.archive(t)...)
		},
		"AUFS metadata": func() []byte {
			image := valid()
			layer := image.blob(gzipOf(t, tarOf(t, testFile{".wh..wh.plnk/f", 1})))
			image.image([]string{layer}, []string{layer})
			return image.archive(t)
		},
		"file with many PAX records": func() []byte {
			records := map[string]string{}
			for i := range layerPAXRecords + 1 {
				records[fmt.Sprintf("SCHILY.xattr.user.%d", i)] = "v"
			}
			var out bytes.Buffer
			w := tar.NewWriter(&out)
			_ = w.WriteHeader(&tar.Header{Name: "d/", Typeflag: tar.TypeDir, Mode: 0755, PAXRecords: records})
			_ = w.Close()
			image := valid()
			layer := image.blob(gzipOf(t, out.Bytes()))
			image.image([]string{layer}, []string{layer})
			return image.archive(t)
		},
		"tar entry type images do not use": func() []byte {
			var out bytes.Buffer
			w := tar.NewWriter(&out)
			_ = w.WriteHeader(&tar.Header{Name: "g", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"mtime": "1"}})
			_ = w.WriteHeader(&tar.Header{Name: "f", Typeflag: tar.TypeReg, Mode: 0644})
			_ = w.Close()
			image := valid()
			layer := image.blob(gzipOf(t, out.Bytes()))
			image.image([]string{layer}, []string{layer})
			return image.archive(t)
		},
		// Go folds superseded long names into the next entry, but Docker's legacy store keeps them.
		"file with more than 64 KiB of headers": func() []byte {
			var raw []byte
			for range 80 {
				raw = append(raw, tarBlock("././@LongLink", tar.TypeGNULongName, 1<<20)...)
				raw = append(raw, make([]byte, 1<<20)...)
			}
			raw = append(append(raw, tarBlock("f", tar.TypeReg, 0)...), make([]byte, 1024)...)
			image := valid()
			layer := image.blob(gzipOf(t, raw))
			image.image([]string{layer}, []string{layer})
			return image.archive(t)
		},
		"index nested too deeply": func() []byte {
			image := valid()
			child := image.blob([]byte(`{"layers":[]}`))
			for range imageIndexDepth + 1 {
				child = image.blob(fmt.Appendf(nil, `{"manifests":[{"digest":%q}]}`, child))
			}
			image.add("index.json", fmt.Appendf(nil, `{"manifests":[{"digest":%q}]}`, child))
			return image.archive(t)
		},
		"unexpected member": func() []byte {
			image := valid()
			image.add("repositories", []byte(`{}`))
			return image.archive(t)
		},
		"bzip2 layer": func() []byte {
			image := valid()
			layer := image.blob(append([]byte("BZh91AY&SY"), make([]byte, 100)...))
			image.image([]string{layer}, []string{layer})
			return image.archive(t)
		},
		"layer that is also a plain tar archive": func() []byte {
			image := valid()
			layer := image.blob(gzipTarPolyglot(t))
			image.image([]string{layer}, []string{layer})
			return image.archive(t)
		},
		"gzip layer that is not tar": func() []byte {
			image := valid()
			layer := image.blob(gzipOf(t, bytes.Repeat([]byte("not a tar archive "), 100)))
			image.image([]string{layer}, []string{layer})
			return image.archive(t)
		},
		"data after a layer's end": func() []byte {
			image := valid()
			layer := image.blob(gzipOf(t, append(tarOf(t, testFile{"app", 10}), make([]byte, 2<<20)...)))
			image.image([]string{layer}, []string{layer})
			return image.archive(t)
		},
		"symlink member": func() []byte {
			var out bytes.Buffer
			w := tar.NewWriter(&out)
			_ = w.WriteHeader(&tar.Header{Name: "blobs/sha256/" + strings.Repeat("b", 64), Typeflag: tar.TypeSymlink, Linkname: "/dev/zero"})
			_ = w.Close()
			return out.Bytes()
		},
		"duplicate member": func() []byte {
			archive := valid().archive(t)
			var out bytes.Buffer
			w := tar.NewWriter(&out)
			_ = w.WriteHeader(&tar.Header{Name: "index.json", Typeflag: tar.TypeReg, Size: 2})
			_, _ = w.Write([]byte("{}"))
			_ = w.Flush()
			return append(out.Bytes(), archive...)
		},
		"duplicate JSON names": func() []byte {
			image := valid()
			image.add("index.json", []byte(`{"manifests":[],"manifests":[]}`))
			return image.archive(t)
		},
	}
	for name, archive := range cases {
		t.Run(name, func(t *testing.T) {
			data := archive()
			docker, err := inspectTest(data, testLimit)
			var refused *imageLoadRefused
			if !errors.As(err, &refused) {
				t.Fatal("not refused", err)
			}
			if bytes.Equal(docker, data) {
				t.Fatal("Docker received the whole archive")
			}
		})
	}
}

// Docker holds every directory's header until its layer is unpacked, within any storage limit.
func TestImageArchiveRefusesMoreMetadataThanDockerHolds(t *testing.T) {
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for i := range 2200 {
		_ = w.WriteHeader(&tar.Header{Name: fmt.Sprintf("d%d/", i), Typeflag: tar.TypeDir, Mode: 0755, PAXRecords: map[string]string{"SCHILY.xattr.user.big": strings.Repeat("x", 60<<10)}})
	}
	_ = w.Close()
	image := newTestImage()
	layer := image.blob(gzipOf(t, out.Bytes()))
	image.image([]string{layer}, []string{layer})
	_, err := inspectTest(image.archive(t), imageLoadLimit{Bytes: DefaultImageLoadBytes, Entries: localImageLoadEntries})
	var refused *imageLoadRefused
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "its layer sha256:"+strings.TrimPrefix(layer, "sha256:")[:12]) {
		t.Fatal("not refused for its layer's metadata", err)
	}
}

// Docker's untar writes a sparse member's holes from its header alone, so the header is held.
func TestImageArchiveHoldsARefusedMembersHeader(t *testing.T) {
	archive := append(sparseEntry("blobs/sha256/"+strings.Repeat("a", 64), 8<<30), make([]byte, 1024)...)
	docker, err := inspectTest(archive, imageLoadLimit{Bytes: DefaultImageLoadBytes, Entries: localImageLoadEntries})
	if err == nil || len(docker) != 0 {
		t.Fatal("Docker received a refused member's header", err)
	}
}

// bsdtar records an extended attribute both as LIBARCHIVE.xattr and as SCHILY.xattr, and an
// opaque directory's whiteout is how a layer replaces a directory's contents.
func TestImageArchiveAcceptsBsdtarAttributesAndOpaqueDirectories(t *testing.T) {
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	_ = w.WriteHeader(&tar.Header{Name: "bin/ping", Typeflag: tar.TypeReg, Mode: 0755, PAXRecords: map[string]string{"LIBARCHIVE.xattr.security.capability": "AQAAAgAgAAAAAAAAAAAAAAAAAAA=", "SCHILY.xattr.security.capability": "\x01"}})
	_ = w.WriteHeader(&tar.Header{Name: "etc/.wh..wh..opq", Typeflag: tar.TypeReg, Mode: 0644})
	_ = w.Close()
	image := newTestImage()
	layer := image.blob(gzipOf(t, out.Bytes()))
	image.image([]string{layer}, []string{layer})
	if _, err := inspectTest(image.archive(t), testLimit); err != nil {
		t.Fatal(err)
	}
}

// Flattened base images can write a file again; Docker keeps the later one.
func TestImageArchiveAcceptsAFileWrittenTwice(t *testing.T) {
	image := newTestImage()
	layer := image.blob(gzipOf(t, tarOf(t, testFile{"a", 1}, testFile{"./a", 1})))
	image.image([]string{layer}, []string{layer})
	if _, err := inspectTest(image.archive(t), testLimit); err != nil {
		t.Fatal(err)
	}
}

// A whiteout's name is four bytes longer than the file it removes, which may itself be as long
// as Linux allows.
func TestImageArchiveAcceptsAWhiteoutForALongName(t *testing.T) {
	image := newTestImage()
	layer := image.blob(gzipOf(t, tarOf(t, testFile{"dir/.wh." + strings.Repeat("n", layerNameBytes), 0})))
	image.image([]string{layer}, []string{layer})
	if _, err := inspectTest(image.archive(t), testLimit); err != nil {
		t.Fatal(err)
	}
}

// A failure reading the archive from the build container is told apart from Docker's.
func TestImageArchiveReportsAnUnreadArchive(t *testing.T) {
	image := newTestImage()
	layer := image.blob(gzipOf(t, tarOf(t, testFile{"app", 100 << 10})))
	image.image([]string{layer}, []string{layer})
	archive := image.archive(t)
	var docker bytes.Buffer
	err := inspectImageArchive(&imageArchiveGate{src: io.MultiReader(bytes.NewReader(archive[:len(archive)/2]), iotest.ErrReader(errors.New("container exited"))), out: &docker}, testLimit)
	var unread *imageArchiveUnread
	if !errors.As(err, &unread) {
		t.Fatal("an unreadable archive was reported as", err)
	}
}

// Docker imports on the archive's end marker, so nothing after it reaches Docker, and the load
// ends there.
func TestImageArchiveEndsAtItsEndMarker(t *testing.T) {
	image := newTestImage()
	layer := image.blob(gzipOf(t, tarOf(t, testFile{"app", 10})))
	image.image([]string{layer}, []string{layer})
	archive := image.archive(t)
	docker, err := inspectTest(append(append([]byte{}, archive...), "more"...), testLimit)
	if err != nil || !bytes.Equal(docker, archive) {
		t.Fatal("data after the end marker reached Docker", err)
	}
}

// containerd follows nested indexes to every manifest, so their layers count.
func TestImageArchiveCountsNestedIndexes(t *testing.T) {
	image := newTestImage()
	layer := image.blob(gzipOf(t, tarOf(t, testFile{"data", 20 << 20})))
	image.image([]string{layer}, []string{layer})
	if _, err := inspectTest(image.archive(t), testLimit); err != nil {
		t.Fatal(err)
	}
	descriptor := fmt.Sprintf(`{"digest":%q}`, layer)
	manifest := image.blob(fmt.Appendf(nil, `{"layers":[%s,%s,%s,%s]}`, descriptor, descriptor, descriptor, descriptor))
	nested := image.blob(fmt.Appendf(nil, `{"manifests":[{"digest":%q}]}`, manifest))
	image.add("index.json", fmt.Appendf(nil, `{"manifests":[{"digest":%q}]}`, nested))
	if _, err := inspectTest(image.archive(t), testLimit); !errors.Is(err, errImageOverBytes) {
		t.Fatal("a nested manifest's layers were not counted", err)
	}
}
