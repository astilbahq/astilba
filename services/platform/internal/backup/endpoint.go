package backup

import (
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
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

// hostnamePattern is a DNS name of letters, digits and hyphens, without a trailing dot.
var hostnamePattern = regexp.MustCompile(`^(?i)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)

// validEndpoint says whether s is an HTTPS origin written in its one plain form: a DNS name
// without a trailing dot, or an IP address in its shortest form, then a port without leading
// zeros if it has one, and nothing after it.
func validEndpoint(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || "https://"+u.Host != s {
		return false
	}
	host, port := u.Hostname(), u.Port()
	origin := host
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" || ip.String() != host {
			return false
		}
		if ip.Is6() {
			origin = "[" + host + "]"
		}
	} else if len(host) > 253 || !hostnamePattern.MatchString(host) {
		return false
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return false
		}
		origin += ":" + port
	}
	return u.Host == origin
}

// awsAlias says whether S3 destination d names a bucket through an AWS access point: an access
// point's alias in place of the bucket, or an access point's own host as the endpoint. An access
// point reaches whichever bucket it was created for at AWS, which its name doesn't say, so
// management couldn't tell which other destinations share that bucket. AWS reserves these
// suffixes and labels, so no bucket of its own uses them.
func awsAlias(d Destination) bool {
	if strings.HasSuffix(d.Bucket, "-s3alias") || strings.HasSuffix(d.Bucket, "--ol-s3") {
		return true
	}
	u, err := url.Parse(d.Endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".amazonaws.com") && !strings.HasSuffix(host, ".amazonaws.com.cn") {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if label == "accesspoint" || strings.HasPrefix(label, "s3-accesspoint") || strings.HasPrefix(label, "s3-object-lambda") || strings.HasPrefix(label, "s3-outposts") {
			return true
		}
	}
	return false
}

// hostBuckets are the buckets d's endpoint could name in its host: every run of the host's
// leading labels that is a bucket name, the whole host included, as a custom domain named after
// its bucket is. An IP address names none.
func (d Destination) hostBuckets() []string {
	u, err := url.Parse(d.Endpoint)
	if err != nil {
		return nil
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
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

// Overlap is whether, and how, two S3 destinations keep their repositories in one part of a
// bucket, or one inside the other's, so keys limited to one would reach the other's.
type Overlap int

const (
	// Apart destinations keep their repositories in parts of buckets that don't meet.
	Apart Overlap = iota
	// InBucket destinations name the same bucket, with prefixes that are equal or nested.
	InBucket
	// ThroughEndpoint destinations meet only because one's endpoint could name, in its host, the
	// bucket the other names, and keep the first's repositories in it under the first's bucket.
	ThroughEndpoint
)

// nested says whether parts a and b of a bucket are equal or one is inside the other.
func nested(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// Overlap says whether S3 destinations d and o keep their repositories in the same part of a
// bucket, or one inside the other's. Endpoints aren't compared, since one bucket has many, such as
// a provider's regional and global origins, or an origin with its port. A bucket of one name at
// two providers overlaps too, as the safe answer. An endpoint whose host could name a bucket is
// also counted as keeping its destination's repositories there, under the destination's bucket.
func (d Destination) Overlap(o Destination) Overlap {
	switch {
	case d.Kind != "s3" || o.Kind != "s3":
		return Apart
	case d.Bucket == o.Bucket && nested(d.scope(), o.scope()):
		return InBucket
	case d.hostReaches(o) || o.hostReaches(d):
		return ThroughEndpoint
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
