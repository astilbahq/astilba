package backup

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoredFileTreeComparison(t *testing.T) {
	write := func(name, data string, dirTime int64, mode int64) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		w := tar.NewWriter(f)
		for _, h := range []*tar.Header{{Name: "./", Typeflag: tar.TypeDir, Mode: 0755, ModTime: time.Unix(dirTime, 0)}, {Name: "./data", Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(data)), Uid: 1000, Gid: 1000, ModTime: time.Unix(1, 0)}} {
			if err = w.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if h.Typeflag == tar.TypeReg {
				if _, err = w.Write([]byte(data)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}
	source, err := ArchiveContents(write("source", "record", 1, 0640))
	if err != nil {
		t.Fatal(err)
	}
	extracted, err := ArchiveContents(write("extracted", "record", 2, 0640))
	if err != nil || source != extracted {
		t.Fatal("directory extraction time changed a valid file tree")
	}
	for _, p := range []string{write("corrupt", "changed", 2, 0640), write("permissions", "record", 2, 0644)} {
		got, err := ArchiveContents(p)
		if err != nil || got == source {
			t.Fatal("restored content or permissions mismatch was ignored")
		}
	}
}
