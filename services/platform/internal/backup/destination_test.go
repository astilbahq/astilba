package backup

import (
	"testing"

	"github.com/google/uuid"
)

// A destination's prefix puts each policy's repository under it, so the destination's keys can be
// limited to it. Without one, a repository stays where it always was.
func TestDestinationPrefixPlacesRepositories(t *testing.T) {
	p := Policy{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000b1"), WorkspaceID: uuid.MustParse("00000000-0000-0000-0000-0000000000a1")}
	d := Destination{Kind: "s3", Endpoint: "https://storage.example.test", Bucket: "backups"}
	if got, want := d.Repository(p), "s3:https://storage.example.test/backups/astilba/00000000-0000-0000-0000-0000000000a1/00000000-0000-0000-0000-0000000000b1"; got != want {
		t.Fatalf("repository without a prefix = %q, want %q", got, want)
	}
	d.Prefix = "servers/web-1"
	if got, want := d.Repository(p), "s3:https://storage.example.test/backups/servers/web-1/astilba/00000000-0000-0000-0000-0000000000a1/00000000-0000-0000-0000-0000000000b1"; got != want {
		t.Fatalf("repository with a prefix = %q, want %q", got, want)
	}
	fs := Destination{Kind: "filesystem", Root: "/srv/backups"}
	if got, want := fs.Repository(p), "/srv/backups/00000000-0000-0000-0000-0000000000a1/00000000-0000-0000-0000-0000000000b1/repository"; got != want {
		t.Fatalf("filesystem repository = %q, want %q", got, want)
	}
}

// A prefix is names joined by '/', none of which can climb out of it or be empty, and only an S3
// destination has one.
func TestDestinationPrefixIsValidated(t *testing.T) {
	access := Access{AccessKey: "access", SecretKey: "secret"}
	s3 := func(prefix string) Destination {
		return Destination{ID: uuid.New(), Name: "Bucket", Kind: "s3", Endpoint: "https://storage.example.test", Bucket: "backups", Region: "test-1", Prefix: prefix}
	}
	for _, prefix := range []string{"", "web-1", "servers/web_1", "a/b.c/d-e", "A1"} {
		if err := ValidateDestination(s3(prefix), access); err != nil {
			t.Errorf("prefix %q refused: %v", prefix, err)
		}
	}
	long := ""
	for range 257 {
		long += "a"
	}
	for _, prefix := range []string{"/web", "web/", "web//db", "..", "web/../db", "./web", ".hidden", "web/-x", "web db", "web\x00", "web?x", "é", long} {
		if ValidateDestination(s3(prefix), access) == nil {
			t.Errorf("prefix %q accepted", prefix)
		}
	}
	fs := Destination{ID: uuid.New(), Name: "Local", Kind: "filesystem", Root: "/srv/backups", Prefix: "web"}
	if ValidateDestination(fs, Access{}) == nil {
		t.Error("a filesystem destination with a prefix was accepted")
	}
}
