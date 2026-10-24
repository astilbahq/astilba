package backup

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"strings"
)

// ArchiveContents compares the stored file tree independently of tar ordering
// and directory mtimes, which change while children are extracted.
func ArchiveContents(filename string) (string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	type entry struct {
		Type                    byte
		Mode                    int64
		UID, GID                int
		Size                    int64
		Link, Content, Modified string
	}
	entries := map[string]entry{}
	r := tar.NewReader(f)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		name := path.Clean(h.Name)
		if strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") || len(entries) >= 100000 {
			return "", errors.New("unsupported archive path or file count")
		}
		if _, exists := entries[name]; exists {
			return "", errors.New("duplicate archive entry")
		}
		v := entry{Type: h.Typeflag, Mode: h.Mode, UID: h.Uid, GID: h.Gid, Size: h.Size, Link: h.Linkname}
		switch h.Typeflag {
		case tar.TypeDir:
		case tar.TypeSymlink:
			v.Modified = h.ModTime.UTC().String()
		case tar.TypeReg, tar.TypeRegA:
			v.Type = tar.TypeReg
			v.Modified = h.ModTime.UTC().String()
			sum := sha256.New()
			if _, err = io.Copy(sum, r); err != nil {
				return "", err
			}
			v.Content = hex.EncodeToString(sum.Sum(nil))
		default:
			return "", errors.New("file recipe supports regular files, directories and symbolic links")
		}
		entries[name] = v
	}
	return Hash(entries), nil
}
