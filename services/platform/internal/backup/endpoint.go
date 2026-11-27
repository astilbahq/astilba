package backup

import (
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// An S3 destination's endpoint is an HTTPS origin, and its bucket and prefix say where its
// repositories are. restic 0.19 asks minio-go to choose how a request names the bucket. minio-go
// puts it in the host only at the AWS, Google and Aliyun service endpoints it recognises, and
// first in the path everywhere else. A provider that reads a bucket from the host, as AWS does at
// victim.s3.eu-west-1.amazonaws.com, then keeps the destination's repositories in that bucket,
// with the destination's own bucket at the start of their keys. Management never contacts an
// endpoint, so it can't ask which kind an origin is. Instead it counts every bucket an endpoint's
// host could name, so two destinations that reach one part of a bucket under different names
// still overlap.

// hostPattern is a host name of ASCII letters, digits, '.', '-' and '_'. Go's HTTP client maps
// other characters, such as full-width letters, onto ASCII ones, so a host written with them could
// name a bucket that hostBuckets doesn't see.
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Unknowable names the field of new S3 destination d that hides which bucket its repositories are
// in, or is "" when management can tell. Destinations stored before these rules still run, so
// ValidateDestination, which agents apply too, doesn't include them.
//   - "endpoint": a scheme other than "https://" as written, which restic doesn't parse; a host
//     that isn't ASCII; or an AWS access point's host or alias.
//   - "bucket": an AWS access point's alias.
//
// An access point reaches whichever bucket it was created for at AWS, which its name doesn't say.
// AWS reserves the alias suffixes for access points, so no bucket of its own ends in them. Its
// host labels are AWS's, and are refused at any domain, since AWS's partitions use several.
func (d Destination) Unknowable() string {
	u, err := url.Parse(d.Endpoint)
	if err != nil || !strings.HasPrefix(d.Endpoint, "https://") {
		return "endpoint"
	}
	host := u.Hostname()
	if _, err := netip.ParseAddr(host); err != nil && !hostPattern.MatchString(host) {
		return "endpoint"
	}
	for label := range strings.SplitSeq(strings.ToLower(host), ".") {
		if accessPointAlias(label) || label == "accesspoint" || strings.HasPrefix(label, "s3-accesspoint") || strings.HasPrefix(label, "s3-object-lambda") || strings.HasPrefix(label, "s3-outposts") {
			return "endpoint"
		}
	}
	if accessPointAlias(d.Bucket) {
		return "bucket"
	}
	return ""
}

// accessPointAlias says whether name is an AWS access point's or Object Lambda access point's
// alias.
func accessPointAlias(name string) bool {
	return strings.HasSuffix(name, "-s3alias") || strings.HasSuffix(name, "--ol-s3")
}

// hostBuckets are the buckets d's endpoint could name in its host: every run of the host's
// leading labels that is a bucket name, the whole host included, as a custom domain named after
// its bucket is. An IP address names none.
func (d Destination) hostBuckets() []string {
	u, err := url.Parse(d.Endpoint)
	if err != nil {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	labels := strings.Split(host, ".")
	var out []string
	for i := range labels {
		if b := strings.Join(labels[:i+1], "."); bucketPattern.MatchString(b) {
			out = append(out, b)
		}
	}
	return out
}

// Overlap is whether, and how, S3 destination d keeps its repositories in one part of a bucket
// with another destination o, or one inside the other's, so keys limited to one would reach the
// other's. It is told from d's side, naming what d would change.
type Overlap int

const (
	// Apart destinations keep their repositories in parts of buckets that don't meet.
	Apart Overlap = iota
	// InBucket destinations meet in a bucket d names: o names it too, with a prefix equal to or
	// nested with d's, or o's endpoint names it in its host.
	InBucket
	// ThroughEndpoint destinations meet only because d's endpoint could name, in its host, the
	// bucket o names, and keep d's repositories in it under d's bucket.
	ThroughEndpoint
)

// nested says whether parts a and b of a bucket are equal or one is inside the other.
func nested(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// Overlap says whether S3 destinations d and o keep their repositories in the same part of a
// bucket, or one inside the other's, and how, from d's side. Endpoints aren't compared, since one
// bucket has many, such as a provider's regional and global origins, or an origin with its port. A
// bucket of one name at two providers overlaps too, as the safe answer. An endpoint whose host
// could name a bucket is also counted as keeping its destination's repositories there, under the
// destination's bucket.
func (d Destination) Overlap(o Destination) Overlap {
	switch {
	case d.Kind != "s3" || o.Kind != "s3":
		return Apart
	case d.Bucket == o.Bucket && nested(d.scope(), o.scope()):
		return InBucket
	case d.hostReaches(o):
		return ThroughEndpoint
	case o.hostReaches(d):
		return InBucket
	}
	// Two hosts naming one bucket needn't be compared: the parts they hold start with each
	// destination's own bucket, so they meet only when those buckets and prefixes already do.
	return Apart
}

// hostReaches says whether d's endpoint could name, in its host, o's bucket, and so keep d's
// repositories under d's bucket within it, in or around o's.
func (d Destination) hostReaches(o Destination) bool {
	return slices.Contains(d.hostBuckets(), o.Bucket) && nested(d.Bucket+"/"+d.scope(), o.scope())
}
