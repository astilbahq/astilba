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
// destination has one. It can't begin with astilba, where destinations without one keep theirs.
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
	for _, prefix := range []string{"/web", "web/", "web//db", "..", "web/../db", "./web", ".hidden", "web/-x", "web db", "web\x00", "web?x", "é", "astilba", "astilba/web", long} {
		if ValidateDestination(s3(prefix), access) == nil {
			t.Errorf("prefix %q accepted", prefix)
		}
	}
	fs := Destination{ID: uuid.New(), Name: "Local", Kind: "filesystem", Root: "/srv/backups", Prefix: "web"}
	if ValidateDestination(fs, Access{}) == nil {
		t.Error("a filesystem destination with a prefix was accepted")
	}
}

// Two destinations overlap when one's repositories are in, or around, the part of a bucket the
// other's are in, so keys limited to one's prefix would reach the other's.
func TestDestinationsOverlapWithinOneBucket(t *testing.T) {
	at := func(endpoint, bucket, prefix string) Destination {
		return Destination{Kind: "s3", Endpoint: endpoint, Bucket: bucket, Prefix: prefix}
	}
	origin := "https://storage.example.test"
	for _, c := range []struct {
		a, b    Destination
		overlap Overlap
	}{
		{at(origin, "backups", ""), at(origin, "backups", ""), InBucket},
		{at(origin, "backups", "servers"), at(origin, "backups", "servers"), InBucket},
		{at(origin, "backups", "servers"), at(origin, "backups", "servers/web-1"), InBucket},
		{at(origin, "backups", "servers/web-1"), at("https://STORAGE.example.test", "backups", "servers"), InBucket},
		{at(origin, "backups", "servers/web-1"), at(origin, "backups", "servers/web-2"), Apart},
		{at(origin, "backups", "servers/web"), at(origin, "backups", "servers/web-1"), Apart},
		{at(origin, "backups", ""), at(origin, "backups", "servers"), Apart},
		{at(origin, "backups", ""), at(origin, "other", ""), Apart},
		{at(origin, "backups", ""), at("https://storage.example.test:443", "backups", ""), InBucket},
		{at("https://s3.amazonaws.com", "backups", ""), at("https://s3.eu-west-1.amazonaws.com", "backups", "servers"), Apart},
		{at("https://s3.amazonaws.com", "backups", "servers"), at("https://s3.eu-west-1.amazonaws.com", "backups", "servers/web-1"), InBucket},
		{at("https://192.0.2.10", "backups", "servers"), at("https://s3.eu-west-1.amazonaws.com", "backups", "servers"), InBucket},
		{at(origin, "backups", ""), Destination{Kind: "filesystem", Root: "/srv/backups"}, Apart},
	} {
		if c.a.Overlap(c.b) != c.overlap || c.b.Overlap(c.a) != c.overlap {
			t.Errorf("%+v and %+v overlap: %v, want %v", c.a, c.b, c.a.Overlap(c.b), c.overlap)
		}
	}
}

// restic puts the bucket first in the path at an endpoint it doesn't recognise, so an endpoint
// whose host names a bucket, as AWS, Backblaze B2 and others read it, keeps the destination's
// repositories in that bucket under the destination's bucket. Two destinations reaching one part
// of a bucket by spellings like that overlap, whatever case the host is in. The one whose host names
// the bucket overlaps through its endpoint, and the other in its bucket.
func TestDestinationsOverlapThroughAnEndpointThatNamesABucket(t *testing.T) {
	at := func(endpoint, bucket, prefix string) Destination {
		return Destination{Kind: "s3", Endpoint: endpoint, Bucket: bucket, Prefix: prefix}
	}
	aws := at("https://s3.eu-west-1.amazonaws.com", "victim", "servers/web-1")
	for _, c := range []struct {
		named, hosted Destination
		overlap       bool
	}{
		// Repositories under victim/servers/web-1/astilba, the same part as aws's.
		{aws, at("https://victim.s3.eu-west-1.amazonaws.com", "servers", "web-1"), true},
		{aws, at("https://VICTIM.S3.eu-west-1.amazonaws.com", "servers", "web-1"), true},
		{aws, at("https://victim.s3.eu-west-1.amazonaws.com.", "servers", "web-1"), true},
		{aws, at("https://victim.s3.dualstack.eu-west-1.amazonaws.com:443", "servers", "web-1"), true},
		// Inside aws's part, and around it.
		{aws, at("https://victim.s3.eu-west-1.amazonaws.com", "servers", "web-1/inner"), true},
		{at("https://s3.eu-west-1.amazonaws.com", "victim", "servers"), at("https://victim.s3.eu-west-1.amazonaws.com", "servers", ""), true},
		// A destination without a prefix keeps its repositories under astilba/.
		{at("https://s3.eu-west-1.amazonaws.com", "victim", ""), at("https://victim.s3.eu-west-1.amazonaws.com", "astilba", ""), true},
		// A bucket name with dots, and a custom domain named after its bucket.
		{at("https://s3.example.test", "backups.example.com", "servers"), at("https://backups.example.com", "servers", ""), true},
		{at("https://s3.example.test", "my.backups", "servers"), at("https://my.backups.s3.example.test", "servers", "web-1"), true},
		{at("https://s3.example.test", "victim", "servers/web-1"), at("https://victim.s3.eu-central-003.backblazeb2.com", "servers", "web-1"), true},
		// Management can't tell a service's own host from a bucket's, so a bucket named like the
		// start of another destination's host overlaps it, as the safe answer.
		{at("https://storage.example.test", "minio", "backups"), at("https://minio.internal:9000", "backups", ""), true},
		// Other parts of the named bucket, other buckets, and hosts that name none.
		{aws, at("https://victim.s3.eu-west-1.amazonaws.com", "servers", ""), false},
		{aws, at("https://victim.s3.eu-west-1.amazonaws.com", "servers", "web-2"), false},
		{aws, at("https://other.s3.eu-west-1.amazonaws.com", "servers", "web-1"), false},
		{aws, at("https://192.0.2.10", "servers", "web-1"), false},
		{aws, at("https://192.0.2.10.", "servers", "web-1"), false},
	} {
		want := [2]Overlap{Apart, Apart}
		if c.overlap {
			want = [2]Overlap{InBucket, ThroughEndpoint}
		}
		if got := [2]Overlap{c.named.Overlap(c.hosted), c.hosted.Overlap(c.named)}; got != want {
			t.Errorf("%+v and %+v overlap: %v, want %v", c.named, c.hosted, got, want)
		}
	}
}

// Management refuses a new S3 destination that hides which bucket its repositories are in, naming
// the field. Agents keep running destinations stored before the rule, whose endpoints restic reaches.
func TestDestinationBucketMustBeKnowable(t *testing.T) {
	s3 := func(endpoint, bucket string) Destination {
		return Destination{ID: uuid.New(), Name: "Bucket", Kind: "s3", Endpoint: endpoint, Bucket: bucket, Region: "test-1"}
	}
	for _, d := range []Destination{
		s3("https://storage.example.test", "backups"),
		s3("https://STORAGE.example.test.", "backups"),
		s3("https://minio_store:9000", "backups"),
		s3("https://192.0.2.10", "backups"),
		s3("https://[2001:db8::1]:9000", "backups"),
		s3("https://s3.eu-west-1.amazonaws.com", "backups"),
		s3("https://victim.s3.eu-west-1.amazonaws.com", "servers"),
		s3("https://s3.eu-west-1.amazonaws.com", "backups--usw2-az1--x-s3"),
	} {
		if field := d.Unknowable(); field != "" {
			t.Errorf("%s %s refused on %q", d.Endpoint, d.Bucket, field)
		}
	}
	for _, c := range []struct {
		d     Destination
		field string
	}{
		{s3("HTTPS://storage.example.test", "backups"), "endpoint"},
		{s3("https://ｓｔｏｒａｇｅ.example.test", "backups"), "endpoint"},
		{s3("https://bücket.example.test", "backups"), "endpoint"},
		{s3("https://ap-123456789012.s3-accesspoint.eu-west-1.amazonaws.com", "servers"), "endpoint"},
		{s3("https://ap-123456789012.s3-accesspoint-fips.us-east-1.amazonaws.com", "servers"), "endpoint"},
		{s3("https://ap-123456789012.s3-accesspoint.eusc-de-east-1.amazonaws.eu", "servers"), "endpoint"},
		{s3("https://ap-123456789012.s3-accesspoint.cn-north-1.amazonaws.com.cn.", "servers"), "endpoint"},
		{s3("https://ap-123456789012.s3-object-lambda.eu-west-1.amazonaws.com", "servers"), "endpoint"},
		{s3("https://mfzwi23gnjvgw.mrap.accesspoint.s3-global.amazonaws.com", "servers"), "endpoint"},
		{s3("https://accesspoint.vpce-1a2b3c4d-5e6f.s3.us-east-1.vpce.amazonaws.com", "servers"), "endpoint"},
		{s3("https://ap-123456789012.op-01ac5d28a6a232904.s3-outposts.us-west-2.amazonaws.com", "servers"), "endpoint"},
		{s3("https://my-ap-hrzrlukc5m36ft7okagglf3gmwluquse1b-s3alias.s3.eu-west-1.amazonaws.com", "servers"), "endpoint"},
		{s3("https://my-olap-cbmlzmhtvbmhgh9nmsqfrbbzw6ogn--ol-s3.s3.eu-west-1.amazonaws.com", "servers"), "endpoint"},
		{s3("https://s3.eu-west-1.amazonaws.com", "my-ap-hrzrlukc5m36ft7okagglf3gmwluquse1b-s3alias"), "bucket"},
		{s3("https://s3.eu-west-1.amazonaws.com", "my-olap-cbmlzmhtvbmhgh9nmsqfrbbzw6ogn--ol-s3"), "bucket"},
	} {
		if field := c.d.Unknowable(); field != c.field {
			t.Errorf("%s %s refused on %q, want %q", c.d.Endpoint, c.d.Bucket, field, c.field)
		}
	}
	// Agents apply only the structural rules, so a destination stored before these still runs.
	stored := s3("https://ap-123456789012.s3-accesspoint.eu-west-1.amazonaws.com.", "backups")
	if err := ValidateDestination(stored, Access{AccessKey: "access", SecretKey: "secret"}); err != nil {
		t.Errorf("stored destination refused: %v", err)
	}
}
