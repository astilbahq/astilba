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
// of a bucket by spellings like that overlap, whatever case the host is in.
func TestDestinationsOverlapThroughAnEndpointThatNamesABucket(t *testing.T) {
	at := func(endpoint, bucket, prefix string) Destination {
		return Destination{Kind: "s3", Endpoint: endpoint, Bucket: bucket, Prefix: prefix}
	}
	aws := at("https://s3.eu-west-1.amazonaws.com", "victim", "servers/web-1")
	for _, c := range []struct {
		a, b    Destination
		overlap Overlap
	}{
		// Repositories under victim/servers/web-1/astilba, the same part as aws's.
		{aws, at("https://victim.s3.eu-west-1.amazonaws.com", "servers", "web-1"), ThroughEndpoint},
		{aws, at("https://VICTIM.S3.eu-west-1.amazonaws.com", "servers", "web-1"), ThroughEndpoint},
		{aws, at("https://victim.s3.dualstack.eu-west-1.amazonaws.com:443", "servers", "web-1"), ThroughEndpoint},
		// Inside aws's part, and around it.
		{aws, at("https://victim.s3.eu-west-1.amazonaws.com", "servers", "web-1/inner"), ThroughEndpoint},
		{aws, at("https://victim.s3.eu-west-1.amazonaws.com", "servers", ""), Apart},
		{at("https://s3.eu-west-1.amazonaws.com", "victim", "servers"), at("https://victim.s3.eu-west-1.amazonaws.com", "servers", ""), ThroughEndpoint},
		// A destination without a prefix keeps its repositories under astilba/.
		{at("https://s3.eu-west-1.amazonaws.com", "victim", ""), at("https://victim.s3.eu-west-1.amazonaws.com", "astilba", ""), ThroughEndpoint},
		// A bucket name with dots, and a custom domain named after its bucket.
		{at("https://s3.example.test", "backups.example.com", "servers"), at("https://backups.example.com", "servers", ""), ThroughEndpoint},
		{at("https://s3.example.test", "my.backups", "servers"), at("https://my.backups.s3.example.test", "servers", "web-1"), ThroughEndpoint},
		{at("https://s3.example.test", "victim", "servers/web-1"), at("https://victim.s3.eu-central-003.backblazeb2.com", "servers", "web-1"), ThroughEndpoint},
		// Other parts of the named bucket, other buckets, and hosts that name none.
		{aws, at("https://victim.s3.eu-west-1.amazonaws.com", "servers", "web-2"), Apart},
		{aws, at("https://other.s3.eu-west-1.amazonaws.com", "servers", "web-1"), Apart},
		{aws, at("https://192.0.2.10", "servers", "web-1"), Apart},
		// Management can't tell a service's own host from a bucket's, so a bucket named like the
		// start of another destination's host overlaps it, as the safe answer.
		{at("https://minio.internal:9000", "backups", ""), at("https://storage.example.test", "minio", "backups"), ThroughEndpoint},
	} {
		if c.a.Overlap(c.b) != c.overlap || c.b.Overlap(c.a) != c.overlap {
			t.Errorf("%+v and %+v overlap: %v, want %v", c.a, c.b, c.a.Overlap(c.b), c.overlap)
		}
	}
}

// An S3 destination's endpoint is an HTTPS origin in its one plain form, so each origin has one
// spelling to compare. A bucket can't be named through an AWS access point, whose bucket management
// can't know.
func TestDestinationEndpointIsAPlainOrigin(t *testing.T) {
	access := Access{AccessKey: "access", SecretKey: "secret"}
	s3 := func(endpoint, bucket string) Destination {
		return Destination{ID: uuid.New(), Name: "Bucket", Kind: "s3", Endpoint: endpoint, Bucket: bucket, Region: "test-1"}
	}
	for _, endpoint := range []string{"https://storage.example.test", "https://STORAGE.example.test", "https://minio:9000", "https://192.0.2.10", "https://[2001:db8::1]:9000", "https://s3.eu-west-1.amazonaws.com", "https://s3.amazonaws.com", "https://storage.example.test:65535"} {
		if err := ValidateDestination(s3(endpoint, "backups"), access); err != nil {
			t.Errorf("endpoint %q refused: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"", "http://storage.example.test", "HTTPS://storage.example.test", "https://storage.example.test.", "https://storage.example.test/", "https://storage.example.test/backups", "https://storage.example.test?", "https://storage.example.test#", "https://user@storage.example.test", "https://storage.example.test:", "https://storage.example.test:0443", "https://storage.example.test:0", "https://storage.example.test:65536", "https://storage..example.test", "https://-storage.example.test", "https://storage_1.example.test", "https://stor%61ge.example.test", "https://[2001:db8:0::1]", "https://[fe80::1%25en0]", "https://bücket.example.test", "https://ap-123456789012.s3-accesspoint.eu-west-1.amazonaws.com", "https://ap-123456789012.s3-accesspoint-fips.us-east-1.amazonaws.com", "https://ap-123456789012.s3-object-lambda.eu-west-1.amazonaws.com", "https://mfzwi23gnjvgw.mrap.accesspoint.s3-global.amazonaws.com", "https://ap-123456789012.op-01ac5d28a6a232904.s3-outposts.us-west-2.amazonaws.com", "https://ap-123456789012.s3-accesspoint.cn-north-1.amazonaws.com.cn"} {
		if ValidateDestination(s3(endpoint, "backups"), access) == nil {
			t.Errorf("endpoint %q accepted", endpoint)
		}
	}
	for _, bucket := range []string{"my-ap-hrzrlukc5m36ft7okagglf3gmwluquse1b-s3alias", "my-olap-cbmlzmhtvbmhgh9nmsqfrbbzw6ogn--ol-s3"} {
		if ValidateDestination(s3("https://s3.eu-west-1.amazonaws.com", bucket), access) == nil {
			t.Errorf("access point alias %q accepted", bucket)
		}
	}
}
