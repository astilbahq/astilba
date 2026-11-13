package agent

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Docker reads exactly the archive the agent measured: code in the build container that grows
// or swaps it afterwards fails the load instead of feeding Docker more.
func TestImageArchiveIsReadAsMeasured(t *testing.T) {
	read := func(data string, size int64) (string, error) {
		var out bytes.Buffer
		_, err := io.Copy(&out, &measuredReader{r: strings.NewReader(data), remaining: size})
		return out.String(), err
	}
	if got, err := read("archive", 7); err != nil || got != "archive" {
		t.Fatalf("a measured archive read %q, %v", got, err)
	}
	if _, err := read("archive and more", 7); err == nil {
		t.Fatal("an archive that grew after it was measured was read")
	}
	if _, err := read("arch", 7); err != io.ErrUnexpectedEOF {
		t.Fatal("an archive that shrank after it was measured was read", err)
	}
}
